package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/genstream"
	"aivory/server/internal/llm"
	"aivory/server/internal/msgcache"
	"aivory/server/internal/sse"
	"aivory/server/internal/store"
)

// maxGenDuration caps a detached generation. Generation is deliberately NOT
// tied to the HTTP request anymore (so closing the page doesn't lose the reply),
// so this is the backstop that prevents a stuck turn from running forever and
// holding a concurrency slot. Reasoning/tool-heavy turns can run well past ten
// minutes, so keep this wide and let per-tool/admin TTFT limits handle the
// narrower failure modes.
var maxGenDuration = envcfg.Dur("AIVORY_API_MAX_GEN_DURATION", 90*time.Minute)

// SSE heartbeat and stream-replay tunables (env-overridable; defaults preserve
// prior hardcoded behavior).
var (
	ssePingHeartbeatPost        = envcfg.Dur("AIVORY_API_SSE_PING_HEARTBEAT_POST", 15*time.Second)
	ssePingHeartbeatRegenerate  = envcfg.Dur("AIVORY_API_SSE_PING_HEARTBEAT_REGENERATE", 15*time.Second)
	ssePingHeartbeatStream      = envcfg.Dur("AIVORY_API_SSE_PING_HEARTBEAT_STREAM", 15*time.Second)
	streamStatusRecheckInterval = envcfg.Dur("AIVORY_API_STREAM_STATUS_RECHECK_INTERVAL", 5*time.Second)
	streamReplayBatchSize       = envcfg.Int("AIVORY_API_STREAM_REPLAY_BATCH_SIZE", 200)
)

const chatRunErrorMessage = "The message could not be processed. Please try again."

type chatRunErrorMetadata struct {
	Operation       string
	UserID          string
	ConversationID  string
	Fast            bool
	Branch          bool
	ParentID        string
	ReferenceID     string
	AttachmentCount int
}

// logChatRunError records only request identifiers and turn-shape metadata.
// User text, attachment names/content, and assembled prompts must never be
// added here; the underlying error is enough for server-side diagnosis.
func logChatRunError(logger *log.Logger, meta chatRunErrorMetadata, err error) {
	if logger == nil || err == nil {
		return
	}
	logger.Printf(
		"chat run error: operation=%q user_id=%q conversation_id=%q fast=%t branch=%t parent_id=%q reference_id=%q attachment_count=%d error=%q",
		meta.Operation,
		meta.UserID,
		meta.ConversationID,
		meta.Fast,
		meta.Branch,
		meta.ParentID,
		meta.ReferenceID,
		meta.AttachmentCount,
		err.Error(),
	)
}

type postMessageReq struct {
	Text     string `json:"text"`
	ModelID  string `json:"model_id"`
	ParentID string `json:"parent_id"`
	Branch   bool   `json:"branch"`
	Mode     string `json:"mode"`
	// Verify enables Verify mode (§verify) — a secondary auditor model checks the
	// answer. No-op unless an admin configured `verify_model_id`.
	Verify bool `json:"verify"`
	// ToolMode is the per-turn tool policy: auto asks the configured task model,
	// disabled exposes no tools, and enabled preserves the model's configured tool
	// support. RawMessage distinguishes an omitted legacy request from explicit
	// invalid values such as null, an empty string, or a non-string. NoTools
	// remains a backwards-compatible alias; an explicit ToolMode always wins.
	ToolMode json.RawMessage `json:"tool_mode"`
	NoTools  bool            `json:"no_tools"`
	// WebSearch forces a server-side non-tool web search and is only meaningful
	// when tools are explicitly disabled.
	WebSearch bool `json:"web_search"`
	// Fast marks a fast-mode turn (§fast-mode): the model is resolved server-side
	// from the admin's fast model and masked from the user; Verify / Deep Research
	// / no-tools are all forced off; tools run on a quartered budget without
	// python_execute. Overrides ModelID (which the client omits on a fast turn).
	Fast           bool             `json:"fast"`
	Attachments    []llm.Attachment `json:"attachments"`
	ParamOverrides map[string]any   `json:"params"`
	// ImageStyleID selects an admin image style for an image-mode turn (§4.20).
	ImageStyleID string `json:"image_style_id"`
	// Locale is the user's current UI language (i18next code, e.g. "en", "zh");
	// drives the reply-language instruction (§ reply language).
	Locale string `json:"locale"`
}

func validTurnToolMode(mode string) bool {
	switch mode {
	case llm.ToolModeAuto, llm.ToolModeDisabled, llm.ToolModeEnabled:
		return true
	}
	return false
}

// resolveTurnToolMode maps the legacy no_tools boolean onto the tri-state
// protocol. An omitted/false legacy flag means enabled so old clients retain
// their previous behavior; new clients send tool_mode explicitly (normally
// auto). Invalid explicit values are rejected instead of silently changing how
// a turn is executed.
func resolveTurnToolMode(explicit json.RawMessage, legacyNoTools bool) (string, error) {
	if len(explicit) == 0 {
		if legacyNoTools {
			return llm.ToolModeDisabled, nil
		}
		return llm.ToolModeEnabled, nil
	}
	var mode string
	if err := json.Unmarshal(explicit, &mode); err != nil || !validTurnToolMode(mode) {
		return "", errors.New("tool_mode must be one of: auto, disabled, enabled")
	}
	return mode, nil
}

// normalizeTurnFlags enforces feature mutual exclusion server-side. Deep
// Research always needs tools; forced web search is the explicit-disabled
// fallback and cannot be combined with auto/enabled policies.
func normalizeTurnFlags(mode, toolMode string, webSearch bool) (string, bool) {
	if mode == "deep-research" {
		return llm.ToolModeEnabled, false
	}
	if toolMode != llm.ToolModeDisabled {
		return toolMode, false
	}
	return toolMode, webSearch
}

// postMessageHandler is the SSE-streaming endpoint. The orchestrator owns the
// full lifecycle; this handler simply opens the stream, runs the orchestrator
// and writes events to the wire.
func postMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	conv, err := store.GetConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
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
	// A branch edit names the exact persisted message it forks from. Reject a
	// stale optimistic id (or a message from another conversation) before opening
	// the SSE response, so the client receives a clear conflict instead of a 200
	// stream that later contains a database foreign-key error.
	if req.Branch && req.ParentID != "" {
		parent, parentErr := store.GetMessage(r.Context(), d.DB, req.ParentID)
		if parentErr != nil && !errors.Is(parentErr, store.ErrNotFound) {
			writeError(w, 500, parentErr)
			return
		}
		if parentErr != nil || parent.ConversationID != id {
			writeError(w, http.StatusConflict, llm.ErrInvalidMessageParent)
			return
		}
	}
	if err := ensureAttachedDocumentsReady(r.Context(), d.DB, id, req.Attachments); err != nil {
		writeError(w, 409, err)
		return
	}
	toolMode, err := resolveTurnToolMode(req.ToolMode, req.NoTools)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	// Deep Research is a per-group capability (§ user groups). If the user's
	// group isn't entitled, silently downgrade to a normal turn (the client also
	// hides the button, so this is defense-in-depth, not the primary UX).
	// §fast-mode overrides every other turn flag: a fast turn never runs Verify,
	// Deep Research, no-tools, or forced web search (the orchestrator re-enforces
	// this, but keep the request self-consistent here too).
	if req.Fast {
		req.Mode = ""
		req.Verify = false
		toolMode = llm.ToolModeEnabled
		req.WebSearch = false
	}
	if req.Mode == "deep-research" && u.Role != "admin" && !userGroupHasFeature(r.Context(), d, u.GroupID, "research") {
		req.Mode = ""
	}
	toolMode, req.WebSearch = normalizeTurnFlags(req.Mode, toolMode, req.WebSearch)
	// §8 hard rule: per-user concurrent generation cap. Reserve the slot FIRST,
	// before the daily-message counter is incremented — otherwise a request that
	// is rejected here (slot full) would still burn a daily count for a turn that
	// never ran. Released when the SSE handler returns.
	release, ok := reserveConcurrentGen(d, u.ID)
	if !ok {
		writeError(w, 429, errors.New("too many concurrent generations — wait for the current one to finish or stop it"))
		return
	}
	defer release()
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
		t := time.NewTicker(ssePingHeartbeatPost)
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

	streamMessageID := ""
	terminalSent := false
	sendEvent := func(ev llm.SseEvent) {
		if ev.Type == "message_start" && ev.MessageID != "" {
			streamMessageID = ev.MessageID
		}
		if streamMessageID != "" && ev.MessageID == "" {
			ev.MessageID = streamMessageID
		}
		if genstream.Terminal(ev) {
			terminalSent = true
		}
		if streamMessageID != "" {
			if eventID, ok := genstream.Append(d.Cache, streamMessageID, ev); ok {
				_ = writer.SendID(ev, ev.Type, eventID)
				return
			}
		}
		_ = writer.Send(ev, ev.Type)
	}

	_, err = d.Orchestrator.Run(ctx, llm.RunRequest{
		UserID:         u.ID,
		ConversationID: id,
		ModelID:        req.ModelID,
		UserText:       req.Text,
		Attachments:    req.Attachments,
		ParentID:       req.ParentID,
		Branch:         req.Branch,
		Mode:           req.Mode,
		Verify:         req.Verify,
		ToolMode:       toolMode,
		ForceWebSearch: req.WebSearch,
		Fast:           req.Fast,
		ParamOverrides: req.ParamOverrides,
		ImageStyleID:   req.ImageStyleID,
		Locale:         req.Locale,
	}, sendEvent)
	if err != nil && !terminalSent {
		parentID := req.ParentID
		if parentID == "" && !req.Branch {
			parentID = conv.ActiveLeafID
		}
		logChatRunError(d.Logger, chatRunErrorMetadata{
			Operation:       "post_message",
			UserID:          u.ID,
			ConversationID:  id,
			Fast:            req.Fast,
			Branch:          req.Branch,
			ParentID:        parentID,
			AttachmentCount: len(req.Attachments),
		}, err)
		sendEvent(llm.SseEvent{Type: "error", Message: chatRunErrorMessage, MessageID: streamMessageID})
	}
	// §23: the turn is over (success, stop, or error — the user message and any
	// partial answer are persisted either way); nudge the user's other devices.
	publishUserEvent(d, r, u.ID, "conversation.updated", id)
}

func ensureAttachedDocumentsReady(ctx context.Context, db *sql.DB, convID string, atts []llm.Attachment) error {
	docIDs := []string{}
	fileIDs := []string{}
	seen := map[string]bool{}
	attachedFiles := map[string]bool{}
	queuedFiles := map[string]bool{}
	for _, a := range atts {
		if fileID := strings.TrimSpace(a.ID); fileID != "" {
			attachedFiles[fileID] = true
		}
		id := strings.TrimSpace(a.DocumentID)
		if id != "" {
			if seen[id] {
				continue
			}
			seen[id] = true
			docIDs = append(docIDs, id)
			continue
		}
		fileID := strings.TrimSpace(a.ID)
		if fileID == "" || queuedFiles[fileID] || !isDocKind(a.Kind) {
			continue
		}
		queuedFiles[fileID] = true
		fileIDs = append(fileIDs, fileID)
	}
	// Never trust the refreshed client to remember local attachment state. Every
	// server-side composer draft must be present in this turn; otherwise the user
	// could refresh while parsing, send with attachments=[], and receive an answer
	// that silently ignored the file.
	drafts, err := store.ListDraftFilesForConversation(ctx, db, convID)
	if err != nil {
		return err
	}
	for _, draft := range drafts {
		if !attachedFiles[draft.ID] {
			return errors.New("conversation has unsent attachments; reload and try again")
		}
	}
	if len(docIDs) > 0 {
		statuses, err := store.ConversationDocumentStatuses(ctx, db, convID, docIDs)
		if err != nil {
			return err
		}
		for _, id := range docIDs {
			status, ok := statuses[id]
			if !ok {
				return errors.New("attached document not found")
			}
			if status != "ready" {
				return fmt.Errorf("attached document is still indexing (%s)", status)
			}
		}
	}
	if len(fileIDs) > 0 {
		statuses, err := store.ConversationDocumentStatusesForFiles(ctx, db, convID, fileIDs)
		if err != nil {
			return err
		}
		// The client's attachment.kind is untrusted and can drift from the server's
		// classification — most notably an .xlsx, whose OOXML MIME carries an
		// "officedocument" substring that trips browser-side /doc/ heuristics into
		// labelling it 'doc', while the backend files it as 'sheet' (sandbox data,
		// no RAG document row). Resolve the SERVER kind so "no document" is only an
		// error for files that were actually supposed to be ingested; spreadsheets
		// and images legitimately have none and must not block the send (the old
		// behaviour 409'd every xlsx upload with "attached document not found").
		serverKinds, err := store.ConversationFileKinds(ctx, db, convID, fileIDs)
		if err != nil {
			return err
		}
		for _, id := range fileIDs {
			fileStatuses := statuses[id]
			if len(fileStatuses) == 0 {
				// A file that genuinely doesn't exist (unknown id) or that IS a
				// document-kind but has no ingested document is a real problem —
				// keep rejecting it. A file the server filed as a non-document kind
				// (sheet → sandbox, image → vision) legitimately has no document and
				// must pass regardless of what the client called it.
				kind, known := serverKinds[id]
				if !known || isDocKind(kind) {
					return errors.New("attached document not found")
				}
				continue
			}
			for _, status := range fileStatuses {
				if status != "ready" {
					return fmt.Errorf("attached document is still indexing (%s)", status)
				}
			}
		}
	}
	return nil
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
		AssistantID    string          `json:"assistant_id"`
		ModelID        string          `json:"model_id"`
		Mode           string          `json:"mode"`
		Verify         bool            `json:"verify"`
		ToolMode       json.RawMessage `json:"tool_mode"`
		NoTools        bool            `json:"no_tools"`
		WebSearch      bool            `json:"web_search"`
		Fast           bool            `json:"fast"` // §fast-mode: honour the CURRENT picker (regenerate follows the live toggle)
		ParamOverrides map[string]any  `json:"params"`
		Locale         string          `json:"locale"`
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
	toolMode, err := resolveTurnToolMode(body.ToolMode, body.NoTools)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	// §fast-mode overrides all other turn flags (see postMessageHandler).
	if body.Fast {
		body.Mode = ""
		body.Verify = false
		toolMode = llm.ToolModeEnabled
		body.WebSearch = false
	}
	// Keep regenerate aligned with the normal send path: users without the
	// Deep Research group feature cannot force it by calling /regenerate.
	if body.Mode == "deep-research" && u.Role != "admin" && !userGroupHasFeature(r.Context(), d, u.GroupID, "research") {
		body.Mode = ""
	}
	toolMode, body.WebSearch = normalizeTurnFlags(body.Mode, toolMode, body.WebSearch)
	// §8/§C7 daily-message + token + concurrent-gen quotas apply to regenerate
	// too — otherwise repeated /regenerate bypasses the per-day message cap.
	// Reserve the concurrent-gen slot FIRST so a slot-full 429 doesn't burn a
	// daily-message count for a turn that never ran.
	release, ok := reserveConcurrentGen(d, u.ID)
	if !ok {
		writeError(w, 429, errors.New("too many concurrent generations"))
		return
	}
	defer release()
	if !checkDailyMessageLimit(d, u.ID) {
		writeError(w, 429, errors.New("daily message limit reached"))
		return
	}
	if !checkDailyTokenQuota(d, u.ID) {
		writeError(w, 429, errors.New("daily token quota reached"))
		return
	}
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
		t := time.NewTicker(ssePingHeartbeatRegenerate)
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
	streamMessageID := ""
	terminalSent := false
	sendEvent := func(ev llm.SseEvent) {
		if ev.Type == "message_start" && ev.MessageID != "" {
			streamMessageID = ev.MessageID
		}
		if streamMessageID != "" && ev.MessageID == "" {
			ev.MessageID = streamMessageID
		}
		if genstream.Terminal(ev) {
			terminalSent = true
		}
		if streamMessageID != "" {
			if eventID, ok := genstream.Append(d.Cache, streamMessageID, ev); ok {
				_ = writer.SendID(ev, ev.Type, eventID)
				return
			}
		}
		_ = writer.Send(ev, ev.Type)
	}

	_, err = d.Orchestrator.Run(ctx, llm.RunRequest{
		UserID:                   u.ID,
		ConversationID:           id,
		ModelID:                  body.ModelID,
		UserText:                 text,
		ParentID:                 user.ID, // assistant sibling under SAME user — §4.15
		ReuseExistingUserMessage: true,
		Mode:                     body.Mode,
		Verify:                   body.Verify,
		ToolMode:                 toolMode,
		ForceWebSearch:           body.WebSearch,
		Fast:                     body.Fast,
		ParamOverrides:           body.ParamOverrides,
		Locale:                   body.Locale,
	}, sendEvent)
	if err != nil && !terminalSent {
		logChatRunError(d.Logger, chatRunErrorMetadata{
			Operation:      "regenerate",
			UserID:         u.ID,
			ConversationID: id,
			Fast:           body.Fast,
			Branch:         true,
			ParentID:       user.ID,
			ReferenceID:    body.AssistantID,
		}, err)
		sendEvent(llm.SseEvent{Type: "error", Message: chatRunErrorMessage, MessageID: streamMessageID})
	}
	// §23: regeneration finished — nudge the user's other devices.
	publishUserEvent(d, r, u.ID, "conversation.updated", id)
}

// streamMessageHandler replays and follows the generation stream for one
// assistant message. It is keyed by assistant message id (not conversation id),
// so two concurrent branches in the same conversation cannot interleave frames.
func streamMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	msgID := pathParam(r, "msgId")
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	msg, err := store.GetMessage(r.Context(), d.DB, msgID)
	if err != nil || msg.ConversationID != convID || msg.Role != "assistant" {
		writeError(w, 404, errNotFound)
		return
	}
	writer := sse.New(w)
	if writer == nil {
		writeError(w, 500, errors.New("streaming not supported"))
		return
	}

	lastID := r.Header.Get("Last-Event-ID")
	if lastID == "" {
		lastID = r.URL.Query().Get("last_id")
	}
	terminal := false
	flush := func() bool {
		events, ok := genstream.Read(d.Cache, msgID, lastID, streamReplayBatchSize)
		if !ok {
			_ = writer.Send(llm.SseEvent{Type: "error", MessageID: msgID, Message: "stream replay unavailable"}, "error")
			return true
		}
		for _, ev := range events {
			lastID = ev.ID
			if genstream.Terminal(ev.Value) {
				terminal = true
			}
			_ = writer.SendID(ev.Value, ev.Value.Type, ev.ID)
		}
		return terminal
	}
	if flush() {
		return
	}
	if msg.Status != "streaming" {
		if !terminal {
			_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: msg.StopReason, Credits: msg.Credits}, "done")
		}
		return
	}

	ch, unsub := d.Cache.Subscribe(genstream.Topic(msgID))
	defer unsub()
	if flush() {
		return
	}
	ping := time.NewTicker(ssePingHeartbeatStream)
	defer ping.Stop()
	statusCheck := time.NewTicker(streamStatusRecheckInterval)
	defer statusCheck.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if flush() {
				return
			}
		case <-ping.C:
			writer.Ping()
		case <-statusCheck.C:
			if flush() {
				return
			}
			fresh, ferr := store.GetMessage(r.Context(), d.DB, msgID)
			if ferr == nil && fresh.Status != "streaming" {
				if !terminal {
					_ = writer.Send(llm.SseEvent{Type: "done", MessageID: msgID, StopReason: fresh.StopReason, Credits: fresh.Credits}, "done")
				}
				return
			}
		}
	}
}

// userGroupHasFeature reports whether the user's group carries a capability
// flag (e.g. "research"). Missing group / parse error → not entitled.
func userGroupHasFeature(ctx context.Context, d Deps, groupID, feature string) bool {
	if groupID == "" {
		groupID = store.DefaultGroupID
	}
	g, err := store.GetUserGroup(ctx, d.DB, groupID)
	if err != nil || g == nil {
		return false
	}
	var feats []string
	if json.Unmarshal(g.Features, &feats) != nil {
		return false
	}
	for _, f := range feats {
		if f == feature {
			return true
		}
	}
	return false
}

// nextMidnightUTC returns the next UTC midnight, used to set quota key TTLs so
// they expire at the start of the next calendar day rather than "24 hours from
// first use" (H-13).
func nextMidnightUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

func checkDailyMessageLimit(d Deps, userID string) bool {
	// H-13: read the limit BEFORE incrementing the counter so a limit of 0
	// (disabled) never burns a count, and so the check reflects the true intent.
	limit := 200
	if raw, err := store.GetSetting(d.DB, "daily_message_limit"); err == nil {
		_ = json.Unmarshal(raw, &limit)
	}
	if limit <= 0 {
		return true // 0 = unlimited; don't touch the counter at all
	}
	key := "quota:" + userID + ":" + time.Now().UTC().Format("2006-01-02")
	ttl := time.Until(nextMidnightUTC())
	n := d.Cache.Incr(key, ttl)
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
	// §workspaces: in shared conversations only the AUTHOR may edit their own
	// question (legacy rows with no author fall back to the conversation gate).
	if msg.AuthorID != "" && msg.AuthorID != u.ID {
		writeError(w, 404, errNotFound)
		return
	}
	blocks, _ := json.Marshal([]llm.UnifiedBlock{{Kind: "text", Text: body.Text}})
	if err := store.UpdateMessageContent(r.Context(), d.DB, msgID, blocks); err != nil {
		writeError(w, 500, err)
		return
	}
	msgcache.Bump(d.Cache, convID)
	updated, _ := store.GetMessage(r.Context(), d.DB, msgID)
	publishUserEvent(d, r, u.ID, "conversation.updated", convID)
	// This endpoint bypasses the redactCost chokepoint, so apply the same
	// user-boundary redaction here: strip admin-only cost/raw, and §fast-mode blank
	// the real model identity on a fast turn (a fast user row is stamped with the
	// hidden fast model's id/label/provider — never return them).
	if updated != nil {
		updated.Cost, updated.Currency, updated.Raw = 0, "", nil
		if updated.Fast {
			updated.ModelID, updated.ModelLabel, updated.Provider = "", "", ""
		}
	}
	writeJSON(w, 200, updated)
}

// deleteMessageHandler deletes ONE conversational round (the user question + all
// of its assistant answers) given any message id inside it. Branch-safe: earlier
// turns, later turns, and sibling branches are preserved (see store.DeleteRound).
// Returns the conversation's new active leaf + the refreshed active-path messages.
func deleteMessageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	msgID := pathParam(r, "msgId")
	conv, err := store.GetConversation(r.Context(), d.DB, convID, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	// §workspaces: deleting a round in a shared conversation is limited to the
	// round's author or the conversation creator. Resolve the round's USER turn
	// (clicking an answer implies its question) and check its author.
	if conv.WorkspaceID != "" && conv.UserID != u.ID {
		if m, merr := store.GetMessage(r.Context(), d.DB, msgID); merr == nil && m.ConversationID == convID {
			author := m.AuthorID
			if m.Role != "user" && m.ParentID != "" {
				if pu, perr := store.GetMessage(r.Context(), d.DB, m.ParentID); perr == nil && pu.Role == "user" {
					author = pu.AuthorID
				}
			}
			if author == "" || author != u.ID {
				writeError(w, 404, errNotFound)
				return
			}
		} else {
			writeError(w, 404, errNotFound)
			return
		}
	}
	newLeaf, err := store.DeleteRound(r.Context(), d.DB, convID, u.ID, msgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	msgcache.Bump(d.Cache, convID)
	msgs, err := msgcache.ListMessages(r.Context(), d.Cache, d.DB, convID, newLeaf)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Enrich with sibling/branch metadata + redact admin-only cost, exactly like
	// getConversationHandler — otherwise the swapped-in path loses its `< n/m >`
	// branch picker and leaks per-message cost to the user.
	publishUserEvent(d, r, u.ID, "conversation.updated", convID)
	writeJSON(w, 200, map[string]any{"ok": true, "active_leaf_id": newLeaf, "messages": redactCost(enrichWithAuthors(d, r, enrichWithSiblings(d, r, msgs)))})
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
