package api

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
	"aivory/server/internal/tools"
)

type branchHTTPProvider struct {
	mu           sync.Mutex
	requests     []llm.UnifiedChatRequest
	firstStarted chan struct{}
}

func (p *branchHTTPProvider) ID() string { return "openai" }

func (p *branchHTTPProvider) Stream(
	ctx context.Context,
	req llm.UnifiedChatRequest,
	_ llm.ToolRunner,
	onEvent func(llm.SseEvent),
) (*llm.UnifiedResult, error) {
	p.mu.Lock()
	call := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	switch call {
	case 0:
		close(p.firstStarted)
		onEvent(llm.SseEvent{Type: "text_delta", Text: "partial first branch"})
		<-ctx.Done()
		return &llm.UnifiedResult{
			Blocks:     []llm.UnifiedBlock{{Kind: "text", Text: "partial first branch"}},
			StopReason: "stopped",
			Usage:      llm.Usage{InputTokens: 3, OutputTokens: 2},
		}, ctx.Err()
	case 1:
		onEvent(llm.SseEvent{Type: "text_delta", Text: "edited branch answer"})
		return &llm.UnifiedResult{
			Blocks:     []llm.UnifiedBlock{{Kind: "text", Text: "edited branch answer"}},
			StopReason: "stop",
			Usage:      llm.Usage{InputTokens: 4, OutputTokens: 2},
		}, nil
	case 2:
		onEvent(llm.SseEvent{Type: "text_delta", Text: "follow-up answer"})
		return &llm.UnifiedResult{
			Blocks:     []llm.UnifiedBlock{{Kind: "text", Text: "follow-up answer"}},
			StopReason: "stop",
			Usage:      llm.Usage{InputTokens: 5, OutputTokens: 2},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected provider call %d", call+1)
	}
}

func (p *branchHTTPProvider) capturedRequests() []llm.UnifiedChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.UnifiedChatRequest(nil), p.requests...)
}

type branchHTTPSSEFrame struct {
	Event string
	ID    string
	Data  string
	Value llm.SseEvent
}

func TestMessagesHTTPStoppedRootEditBranchAllowsFollowUp(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "messages-branch-flow-http.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','branch-http@example.com','h','Branch HTTP','admin')`)
	user, err := store.FindUserByID(ctx, db, "u1")
	if err != nil {
		t.Fatalf("load test user: %v", err)
	}

	channel, err := store.CreateChannel(ctx, db, "Branch HTTP", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "branch-http-model",
		Label:     "Branch HTTP model",
		Enabled:   true,
		Stream:    true,
		ToolMode:  "native",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := store.SetFastModel(ctx, db, model.ID); err != nil {
		t.Fatalf("set fast model: %v", err)
	}
	for key, value := range map[string]any{
		"fallback_ttft_sec":          0,
		"fallback_model_id":          "",
		"disabled_tools":             []string{},
		"max_concurrent_generations": 3,
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c1", UserID: user.ID, Title: "HTTP stopped edit branch regression", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	memoryCache := cache.NewMemory()
	secret := "branch-http-test-secret-at-least-32-chars"
	cfg := config.Config{
		JWTSecret:   secret,
		AccessTTL:   time.Hour,
		RefreshTTL:  24 * time.Hour,
		UploadDir:   t.TempDir(),
		ArtifactDir: t.TempDir(),
	}
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	providers := llm.NewRegistry(logger)
	provider := &branchHTTPProvider{firstStarted: make(chan struct{})}
	providers.Register(provider)
	toolRegistry := tools.NewRegistry(db, nil, cfg, logger)
	orchestrator := llm.NewOrchestrator(db, providers, toolRegistry, nil, memoryCache, nil, nil, nil, logger)
	authService := authsvc.New(secret, cfg.AccessTTL, cfg.RefreshTTL, memoryCache)
	token, _, err := authService.IssueAccess(user.ID, user.Role, user.TokenVer)
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	d := Deps{
		Config:       cfg,
		DB:           db,
		Cache:        memoryCache,
		Auth:         authService,
		Providers:    providers,
		Tools:        toolRegistry,
		Orchestrator: orchestrator,
		Logger:       logger,
	}
	server := httptest.NewServer(NewRouter(d))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 10 * time.Second

	firstResponse := branchHTTPPostMessage(t, client, server.URL, token, conversation.ID,
		`{"text":"original question","tool_mode":"enabled","fast":true}`)
	stopSent := false
	firstFrames := branchHTTPReadSSE(t, firstResponse, func(frame branchHTTPSSEFrame) {
		if frame.Value.Type != "message_start" || stopSent {
			return
		}
		select {
		case <-provider.firstStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("fake provider did not start the first generation")
		}
		stopResponse := branchHTTPDoJSON(t, client, http.MethodPost,
			server.URL+"/api/conversations/"+conversation.ID+"/stop", token, ``)
		defer stopResponse.Body.Close()
		body, readErr := io.ReadAll(stopResponse.Body)
		if readErr != nil {
			t.Fatalf("read stop response: %v", readErr)
		}
		if stopResponse.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
			t.Fatalf("stop status=%d body=%s", stopResponse.StatusCode, body)
		}
		stopSent = true
	})
	if !stopSent {
		t.Fatal("first SSE stream never reached message_start, so /stop was not sent")
	}
	branchHTTPAssertTerminal(t, "stopped first turn", firstFrames, "stopped")

	secondResponse := branchHTTPPostMessage(t, client, server.URL, token, conversation.ID,
		`{"text":"edited root question","parent_id":"","branch":true,"tool_mode":"enabled","fast":true}`)
	secondFrames := branchHTTPReadSSE(t, secondResponse, nil)
	branchHTTPAssertTerminal(t, "edited root branch", secondFrames, "stop")

	followUpResponse := branchHTTPPostMessage(t, client, server.URL, token, conversation.ID,
		`{"text":"follow up on the edited branch","tool_mode":"enabled","fast":true}`)
	followUpFrames := branchHTTPReadSSE(t, followUpResponse, nil)
	branchHTTPAssertTerminal(t, "edited branch follow-up", followUpFrames, "stop")

	activeKey := "gen:active:" + user.ID
	if active, ok := memoryCache.Get(activeKey); !ok || active != "0" {
		t.Fatalf("active generation slot %q = %q (present=%v), want 0", activeKey, active, ok)
	}
	logText := strings.ToLower(logs.String())
	for _, forbidden := range []string{"save user message", "foreign key", "messages_parent_id_fkey"} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("server log contains %q after successful branch flow: %s", forbidden, logs.String())
		}
	}

	all, err := store.ListAllMessages(ctx, db, conversation.ID)
	if err != nil {
		t.Fatalf("list full message tree: %v", err)
	}
	if len(all) != 6 {
		t.Fatalf("message count = %d, want 6: %+v", len(all), all)
	}
	byText := make(map[string]store.Message, len(all))
	for _, message := range all {
		byText[branchHTTPMessageText(t, message)] = message
	}
	firstUser := branchHTTPRequireMessage(t, byText, "original question")
	firstAssistant := branchHTTPRequireMessage(t, byText, "partial first branch")
	secondUser := branchHTTPRequireMessage(t, byText, "edited root question")
	secondAssistant := branchHTTPRequireMessage(t, byText, "edited branch answer")
	followUpUser := branchHTTPRequireMessage(t, byText, "follow up on the edited branch")
	followUpAssistant := branchHTTPRequireMessage(t, byText, "follow-up answer")

	if firstUser.ParentID != "" || secondUser.ParentID != "" {
		t.Fatalf("edited question did not form a sibling root: first parent=%q edited parent=%q",
			firstUser.ParentID, secondUser.ParentID)
	}
	if firstAssistant.ParentID != firstUser.ID || firstAssistant.Status != "stopped" || firstAssistant.StopReason != "stopped" {
		t.Fatalf("stopped first branch is invalid: user=%+v assistant=%+v", firstUser, firstAssistant)
	}
	if secondAssistant.ParentID != secondUser.ID {
		t.Fatalf("edited assistant parent = %q, want edited user %q", secondAssistant.ParentID, secondUser.ID)
	}
	if followUpUser.ParentID != secondAssistant.ID || followUpAssistant.ParentID != followUpUser.ID {
		t.Fatalf("follow-up chain is invalid: edited assistant=%q follow-up user=%+v assistant=%+v",
			secondAssistant.ID, followUpUser, followUpAssistant)
	}

	updated, err := store.GetConversation(ctx, db, conversation.ID, user.ID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if updated.ActiveLeafID != followUpAssistant.ID {
		t.Fatalf("active leaf = %q, want second branch tail %q", updated.ActiveLeafID, followUpAssistant.ID)
	}
	firstPath, err := store.ListMessages(ctx, db, conversation.ID, firstAssistant.ID)
	if err != nil {
		t.Fatalf("reload stopped first branch: %v", err)
	}
	branchHTTPAssertPath(t, "stopped first branch", firstPath, firstUser.ID, firstAssistant.ID)
	activePath, err := store.ListMessages(ctx, db, conversation.ID, "")
	if err != nil {
		t.Fatalf("reload active edited branch: %v", err)
	}
	branchHTTPAssertPath(t, "active edited branch", activePath,
		secondUser.ID, secondAssistant.ID, followUpUser.ID, followUpAssistant.ID)
	branchHTTPAssertValidParents(t, db, conversation.ID)

	requests := provider.capturedRequests()
	if len(requests) != 3 {
		t.Fatalf("provider request count = %d, want 3", len(requests))
	}
	branchHTTPAssertHistory(t, "stopped first request", requests[0].History, "original question")
	branchHTTPAssertHistory(t, "edited root request", requests[1].History, "edited root question")
	branchHTTPAssertHistory(t, "edited branch follow-up request", requests[2].History,
		"edited root question", "edited branch answer", "follow up on the edited branch")

	// A client can briefly retain an optimistic local parent id when stop and
	// edit overlap. Reject that stale explicit branch parent before opening SSE
	// or inserting either half of a turn.
	invalidResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/messages", token,
		`{"text":"must not be saved","parent_id":"m_local-only-parent","branch":true,"tool_mode":"enabled","fast":true}`)
	invalidBody, err := io.ReadAll(invalidResponse.Body)
	invalidResponse.Body.Close()
	if err != nil {
		t.Fatalf("read invalid-parent response: %v", err)
	}
	if invalidResponse.StatusCode != http.StatusConflict {
		t.Fatalf("invalid branch parent status=%d, want 409; body=%s", invalidResponse.StatusCode, invalidBody)
	}
	if contentType := invalidResponse.Header.Get("Content-Type"); strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("invalid branch parent opened SSE instead of returning JSON: content-type=%q body=%s", contentType, invalidBody)
	}
	invalidText := strings.ToLower(string(invalidBody))
	for _, forbidden := range []string{"save user message", "foreign key", "messages_parent_id_fkey", `"type":"error"`} {
		if strings.Contains(invalidText, forbidden) {
			t.Fatalf("invalid-parent response contains %q: %s", forbidden, invalidBody)
		}
	}

	allAfterReject, err := store.ListAllMessages(ctx, db, conversation.ID)
	if err != nil {
		t.Fatalf("list messages after invalid parent rejection: %v", err)
	}
	if len(allAfterReject) != len(all) {
		t.Fatalf("invalid parent changed message count from %d to %d", len(all), len(allAfterReject))
	}
	afterReject, err := store.GetConversation(ctx, db, conversation.ID, user.ID)
	if err != nil {
		t.Fatalf("reload conversation after invalid parent rejection: %v", err)
	}
	if afterReject.ActiveLeafID != followUpAssistant.ID {
		t.Fatalf("invalid parent changed active leaf to %q, want %q", afterReject.ActiveLeafID, followUpAssistant.ID)
	}
	if gotRequests := len(provider.capturedRequests()); gotRequests != 3 {
		t.Fatalf("invalid parent reached provider: request count=%d, want 3", gotRequests)
	}
	if active, ok := memoryCache.Get(activeKey); !ok || active != "0" {
		t.Fatalf("invalid parent leaked active generation slot %q = %q (present=%v)", activeKey, active, ok)
	}
	branchHTTPAssertValidParents(t, db, conversation.ID)
	finalLogText := strings.ToLower(logs.String())
	for _, forbidden := range []string{"save user message", "foreign key", "messages_parent_id_fkey"} {
		if strings.Contains(finalLogText, forbidden) {
			t.Fatalf("server log contains %q after invalid parent rejection: %s", forbidden, logs.String())
		}
	}
}

func branchHTTPPostMessage(
	t *testing.T,
	client *http.Client,
	baseURL, token, conversationID, body string,
) *http.Response {
	t.Helper()
	response := branchHTTPDoJSON(t, client, http.MethodPost,
		baseURL+"/api/conversations/"+conversationID+"/messages", token, body)
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("post message status=%d body=%s", response.StatusCode, payload)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		response.Body.Close()
		t.Fatalf("post message content-type = %q, want text/event-stream", contentType)
	}
	return response
}

func branchHTTPDoJSON(
	t *testing.T,
	client *http.Client,
	method, target, token, body string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, target, err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("execute %s %s: %v", method, target, err)
	}
	return response
}

func branchHTTPReadSSE(
	t *testing.T,
	response *http.Response,
	onFrame func(branchHTTPSSEFrame),
) []branchHTTPSSEFrame {
	t.Helper()
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1024), 1<<20)
	frames := []branchHTTPSSEFrame{}
	for {
		frame, err := branchHTTPNextSSE(scanner)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse SSE frame: %v", err)
		}
		if frame.Event != "" && frame.Event != frame.Value.Type {
			t.Fatalf("SSE event line %q does not match payload type %q: %s", frame.Event, frame.Value.Type, frame.Data)
		}
		lowerData := strings.ToLower(frame.Data)
		for _, forbidden := range []string{"save user message", "foreign key", "messages_parent_id_fkey"} {
			if strings.Contains(lowerData, forbidden) {
				t.Fatalf("SSE contains database failure text %q: %s", forbidden, frame.Data)
			}
		}
		if frame.Value.Type == "error" || frame.Event == "error" {
			t.Fatalf("SSE emitted an error event: %s", frame.Data)
		}
		frames = append(frames, frame)
		if onFrame != nil {
			onFrame(frame)
		}
	}
	if len(frames) == 0 {
		t.Fatal("SSE stream ended without events")
	}
	return frames
}

func branchHTTPNextSSE(scanner *bufio.Scanner) (branchHTTPSSEFrame, error) {
	for {
		frame := branchHTTPSSEFrame{}
		data := []string{}
		endedByBlankLine := false
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				endedByBlankLine = true
				break
			}
			switch {
			case strings.HasPrefix(line, ":"):
				continue
			case strings.HasPrefix(line, "event:"):
				frame.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "id:"):
				frame.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if err := scanner.Err(); err != nil {
			return branchHTTPSSEFrame{}, err
		}
		if len(data) == 0 {
			if endedByBlankLine {
				continue // comment-only heartbeat frame
			}
			return branchHTTPSSEFrame{}, io.EOF
		}
		frame.Data = strings.Join(data, "\n")
		if err := json.Unmarshal([]byte(frame.Data), &frame.Value); err != nil {
			return branchHTTPSSEFrame{}, fmt.Errorf("decode %q: %w", frame.Data, err)
		}
		return frame, nil
	}
}

func branchHTTPAssertTerminal(t *testing.T, label string, frames []branchHTTPSSEFrame, wantReason string) {
	t.Helper()
	starts := 0
	dones := 0
	for _, frame := range frames {
		switch frame.Value.Type {
		case "message_start":
			starts++
		case "done":
			dones++
			if frame.Value.StopReason != wantReason {
				t.Fatalf("%s stop reason = %q, want %q", label, frame.Value.StopReason, wantReason)
			}
		}
	}
	if starts != 1 || dones != 1 {
		t.Fatalf("%s SSE starts=%d dones=%d, want 1/1: %+v", label, starts, dones, frames)
	}
}

func branchHTTPMessageText(t *testing.T, message store.Message) string {
	t.Helper()
	var blocks []llm.UnifiedBlock
	if err := json.Unmarshal(message.Blocks, &blocks); err != nil {
		t.Fatalf("decode message %s blocks: %v", message.ID, err)
	}
	var text strings.Builder
	for _, block := range blocks {
		if block.Kind == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func branchHTTPRequireMessage(t *testing.T, byText map[string]store.Message, text string) store.Message {
	t.Helper()
	message, ok := byText[text]
	if !ok {
		t.Fatalf("message with text %q not found; got %+v", text, byText)
	}
	return message
}

func branchHTTPAssertPath(t *testing.T, label string, got []store.Message, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s path length = %d, want %d: %+v", label, len(got), len(want), got)
	}
	for index, id := range want {
		if got[index].ID != id {
			t.Fatalf("%s path[%d] = %q, want %q", label, index, got[index].ID, id)
		}
	}
}

func branchHTTPAssertValidParents(t *testing.T, db *sql.DB, conversationID string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT child.id, child.parent_id
		FROM messages child
		LEFT JOIN messages parent ON parent.id=child.parent_id
		WHERE child.conversation_id=?
		  AND child.parent_id IS NOT NULL
		  AND (parent.id IS NULL OR parent.conversation_id<>child.conversation_id)`, conversationID)
	if err != nil {
		t.Fatalf("query invalid message parents: %v", err)
	}
	if rows.Next() {
		var childID, parentID string
		if err := rows.Scan(&childID, &parentID); err != nil {
			rows.Close()
			t.Fatalf("scan invalid message parent: %v", err)
		}
		rows.Close()
		t.Fatalf("message %q has invalid parent %q", childID, parentID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate invalid message parents: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close invalid-parent rows: %v", err)
	}

	fkRows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run foreign_key_check: %v", err)
	}
	defer fkRows.Close()
	if fkRows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var fkID int
		if err := fkRows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			t.Fatalf("scan foreign_key_check: %v", err)
		}
		t.Fatalf("foreign key violation: table=%s rowid=%v parent=%s fk=%d", table, rowID, parent, fkID)
	}
	if err := fkRows.Err(); err != nil {
		t.Fatalf("iterate foreign_key_check: %v", err)
	}
}

func branchHTTPAssertHistory(t *testing.T, label string, history []llm.UnifiedMessage, want ...string) {
	t.Helper()
	got := make([]string, 0, len(history))
	for _, message := range history {
		var text strings.Builder
		for _, block := range message.Blocks {
			if block.Kind == "text" {
				text.WriteString(block.Text)
			}
		}
		got = append(got, text.String())
	}
	if len(got) != len(want) {
		t.Fatalf("%s history = %v, want %v", label, got, want)
	}
	for index, text := range want {
		if got[index] != text {
			t.Fatalf("%s history[%d] = %q, want %q (full=%v)", label, index, got[index], text, got)
		}
	}
}
