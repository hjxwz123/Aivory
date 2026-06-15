package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"aurelia/server/internal/llm"
	"aurelia/server/internal/sse"
	"aurelia/server/internal/store"
)

// maxGenDuration caps a detached generation. Generation is deliberately NOT
// tied to the HTTP request anymore (so closing the page doesn't lose the reply),
// so this is the backstop that prevents a stuck turn from running forever and
// holding a concurrency slot.
const maxGenDuration = 10 * time.Minute

type postMessageReq struct {
	Text           string           `json:"text"`
	ModelID        string           `json:"model_id"`
	ParentID       string           `json:"parent_id"`
	Branch         bool             `json:"branch"`
	Mode           string           `json:"mode"`
	Attachments    []llm.Attachment `json:"attachments"`
	ParamOverrides map[string]any   `json:"params"`
}

// postMessageHandler is the SSE-streaming endpoint. The orchestrator owns the
// full lifecycle; this handler simply opens the stream, runs the orchestrator
// and writes events to the wire.
func postMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if _, err := store.GetConversation(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var req postMessageReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, 400, errors.New("text required"))
		return
	}
	// Admins are exempt from all usage quotas (§ admin) — they can test freely.
	if u.Role != "admin" {
		// Limit per day.
		if !checkDailyMessageLimit(d, u.ID) {
			writeError(w, 429, errors.New("daily message limit reached"))
			return
		}
		// §8 hard rule: daily token ceiling. 0 = disabled.
		if !checkDailyTokenQuota(d, u.ID) {
			writeError(w, 429, errors.New("daily token quota reached"))
			return
		}
	}
	// §8 hard rule: per-user concurrent generation cap. Slot reserved here,
	// released when the SSE handler returns.
	release, ok := reserveConcurrentGen(d, u.ID)
	if !ok {
		writeError(w, 429, errors.New("too many concurrent generations — wait for the current one to finish or stop it"))
		return
	}
	defer release()

	writer := sse.New(w)
	if writer == nil {
		writeError(w, 500, errors.New("streaming not supported"))
		return
	}

	// Build the cancellable context: HTTP disconnect + per-conversation stop +
	// per-user kill (real-time ban, §8.1 — banUserAdmin publishes user:{id}:kill).
	// The reply must survive the user closing the page mid-stream: detach the
	// generation from the HTTP request so a browser disconnect no longer aborts
	// (and loses) the answer — it finishes server-side and is persisted, ready
	// when the user returns. Only an explicit stop/kill or the hard time cap can
	// cancel it now.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), maxGenDuration)
	defer cancel()
	stopCh, unsub := d.Cache.Subscribe("conv:" + id + ":stop")
	defer unsub()
	killCh, unsubKill := d.Cache.Subscribe("user:" + u.ID + ":kill")
	defer unsubKill()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-killCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Periodic ping.
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				writer.Ping()
			}
		}
	}()

	_, err := d.Orchestrator.Run(ctx, llm.RunRequest{
		UserID:         u.ID,
		ConversationID: id,
		ModelID:        req.ModelID,
		UserText:       req.Text,
		Attachments:    req.Attachments,
		ParentID:       req.ParentID,
		Branch:         req.Branch,
		Mode:           req.Mode,
		ParamOverrides: req.ParamOverrides,
	}, func(ev llm.SseEvent) {
		_ = writer.Send(ev, ev.Type)
	})
	if err != nil {
		_ = writer.Send(map[string]string{"type": "error", "message": err.Error()}, "error")
	}
}

// regenerateHandler creates a sibling assistant message under the SAME user
// parent — the §4.15 design: "regenerate forks at the assistant, never at the
// user". We do NOT copy the user turn into a new sibling; we simply run the
// orchestrator with the user message id as the parent so a new assistant
// child is produced. The branch picker on the assistant message then shows
// "1/2" / "2/2" between the previous reply and the new one.
//
// Implementation detail: the orchestrator's Run signature requires a UserText
// because it always inserts a user turn first. We sidestep that by injecting a
// flag in the request — when reusing an existing user message, the
// orchestrator must not create a new one.
func regenerateHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	var body struct {
		AssistantID    string         `json:"assistant_id"`
		ModelID        string         `json:"model_id"`
		Mode           string         `json:"mode"`
		ParamOverrides map[string]any `json:"params"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	conv, err := store.GetConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	// §8/§C7 daily-message + token + concurrent-gen quotas apply to regenerate
	// too — otherwise repeated /regenerate bypasses the per-day message cap.
	if !checkDailyMessageLimit(d, u.ID) {
		writeError(w, 429, errors.New("daily message limit reached"))
		return
	}
	if !checkDailyTokenQuota(d, u.ID) {
		writeError(w, 429, errors.New("daily token quota reached"))
		return
	}
	release, ok := reserveConcurrentGen(d, u.ID)
	if !ok {
		writeError(w, 429, errors.New("too many concurrent generations"))
		return
	}
	defer release()
	if body.AssistantID == "" {
		body.AssistantID = conv.ActiveLeafID
	}
	if body.AssistantID == "" {
		writeError(w, 400, errors.New("assistant_id required"))
		return
	}
	assistant, err := store.GetMessage(r.Context(), d.DB, body.AssistantID)
	if err != nil || assistant.ConversationID != id || assistant.Role != "assistant" {
		writeError(w, 404, errNotFound)
		return
	}
	user, err := store.GetMessage(r.Context(), d.DB, assistant.ParentID)
	if err != nil || user.Role != "user" {
		writeError(w, 404, errNotFound)
		return
	}
	// Extract text from the parent user message — purely so the orchestrator's
	// existing prompt path has a UserText to reference. The new assistant
	// message will be parented to `user.ID`, NOT to a new sibling.
	var blocks []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(user.Blocks, &blocks)
	text := ""
	for _, b := range blocks {
		if b.Kind == "text" {
			text += b.Text + "\n"
		}
	}
	text = strings.TrimSpace(text)

	writer := sse.New(w)
	if writer == nil {
		writeError(w, 500, errors.New("streaming not supported"))
		return
	}
	// The reply must survive the user closing the page mid-stream: detach the
	// generation from the HTTP request so a browser disconnect no longer aborts
	// (and loses) the answer — it finishes server-side and is persisted, ready
	// when the user returns. Only an explicit stop/kill or the hard time cap can
	// cancel it now.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), maxGenDuration)
	defer cancel()
	stopCh, unsub := d.Cache.Subscribe("conv:" + id + ":stop")
	defer unsub()
	killCh, unsubKill := d.Cache.Subscribe("user:" + u.ID + ":kill")
	defer unsubKill()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-killCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	// §6.2: 15s heartbeat to keep proxies from closing the SSE channel.
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				writer.Ping()
			}
		}
	}()
	_, err = d.Orchestrator.Run(ctx, llm.RunRequest{
		UserID:         u.ID,
		ConversationID: id,
		ModelID:        body.ModelID,
		UserText:       text,
		ParentID:       user.ID, // assistant sibling under SAME user — §4.15
		ReuseExistingUserMessage: true,
		Mode:           body.Mode,
		ParamOverrides: body.ParamOverrides,
	}, func(ev llm.SseEvent) {
		_ = writer.Send(ev, ev.Type)
	})
	if err != nil {
		_ = writer.Send(map[string]string{"type": "error", "message": err.Error()}, "error")
	}
}

func checkDailyMessageLimit(d Deps, userID string) bool {
	limit := 200
	if raw, err := store.GetSetting(d.DB, "daily_message_limit"); err == nil {
		_ = json.Unmarshal(raw, &limit)
	}
	key := "quota:" + userID + ":" + time.Now().Format("2006-01-02")
	n := d.Cache.Incr(key, 24*time.Hour)
	return int(n) <= limit
}

// editMessageHandler edits a user message's text IN PLACE (no new branch, no
// regeneration) — the "save edit" action. Only the conversation owner may edit,
// and only their own `user` messages.
func editMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	msgID := pathParam(r, "msgId")
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeError(w, 400, errors.New("text required"))
		return
	}
	msg, err := store.GetMessage(r.Context(), d.DB, msgID)
	if err != nil || msg.ConversationID != convID || msg.Role != "user" {
		writeError(w, 404, errNotFound)
		return
	}
	blocks, _ := json.Marshal([]llm.UnifiedBlock{{Kind: "text", Text: body.Text}})
	if err := store.UpdateMessageContent(r.Context(), d.DB, msgID, blocks); err != nil {
		writeError(w, 500, err)
		return
	}
	updated, _ := store.GetMessage(r.Context(), d.DB, msgID)
	writeJSON(w, 200, updated)
}

// feedbackMessageHandler stores a like/dislike on an assistant message.
func feedbackMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	msgID := pathParam(r, "msgId")
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var body struct {
		Feedback string `json:"feedback"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if body.Feedback != "" && body.Feedback != "like" && body.Feedback != "dislike" {
		writeError(w, 400, errors.New("feedback must be 'like', 'dislike', or empty"))
		return
	}
	msg, err := store.GetMessage(r.Context(), d.DB, msgID)
	if err != nil || msg.ConversationID != convID || msg.Role != "assistant" {
		writeError(w, 404, errNotFound)
		return
	}
	if err := store.SetMessageFeedback(r.Context(), d.DB, msgID, body.Feedback); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
