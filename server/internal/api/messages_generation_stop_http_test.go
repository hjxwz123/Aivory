package api

import (
	"bufio"
	"bytes"
	"context"
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

const (
	generationStopFirstID           = "11111111-1111-4111-8111-111111111111"
	generationStopSecondID          = "22222222-2222-4222-8222-222222222222"
	generationStopRegenerateID      = "33333333-3333-4333-8333-333333333333"
	generationStopMessageSelectorID = "44444444-4444-4444-8444-444444444444"
	generationStopPreSubscribeID    = "55555555-5555-4555-8555-555555555555"
)

// generationStopHTTPProvider keeps controlled HTTP generations alive until the
// test either releases or stops each one. Calls 0 and 1 overlap in the same
// conversation; later calls exercise both regenerate stop selectors.
type generationStopHTTPProvider struct {
	mu       sync.Mutex
	requests []llm.UnifiedChatRequest
	started  [5]chan struct{}
	canceled [5]chan struct{}
	release  [5]chan struct{}
	once     [5]sync.Once
}

func newGenerationStopHTTPProvider() *generationStopHTTPProvider {
	provider := &generationStopHTTPProvider{}
	for index := range provider.started {
		provider.started[index] = make(chan struct{})
		provider.canceled[index] = make(chan struct{})
		provider.release[index] = make(chan struct{})
	}
	return provider
}

func (p *generationStopHTTPProvider) ID() string { return "openai" }

func (p *generationStopHTTPProvider) Stream(
	ctx context.Context,
	req llm.UnifiedChatRequest,
	_ llm.ToolRunner,
	onEvent func(llm.SseEvent),
) (*llm.UnifiedResult, error) {
	p.mu.Lock()
	call := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if call >= len(p.started) {
		return nil, fmt.Errorf("unexpected provider call %d", call+1)
	}
	close(p.started[call])

	select {
	case <-ctx.Done():
		close(p.canceled[call])
		return &llm.UnifiedResult{
			Blocks:     []llm.UnifiedBlock{},
			StopReason: "stopped",
		}, ctx.Err()
	case <-p.release[call]:
		// Prefer cancellation if both signals became ready while the provider was
		// descheduled. This makes a conversation-wide stop fail deterministically.
		if err := ctx.Err(); err != nil {
			close(p.canceled[call])
			return &llm.UnifiedResult{
				Blocks:     []llm.UnifiedBlock{},
				StopReason: "stopped",
			}, err
		}
		answer := "first branch completed after sibling stop"
		if call == 1 {
			answer = "second branch was released instead of stopped"
		}
		onEvent(llm.SseEvent{Type: "text_delta", Text: answer})
		return &llm.UnifiedResult{
			Blocks:     []llm.UnifiedBlock{{Kind: "text", Text: answer}},
			StopReason: "stop",
			Usage:      llm.Usage{InputTokens: 4, OutputTokens: 3},
		}, nil
	}
}

func (p *generationStopHTTPProvider) releaseCall(call int) {
	if call < 0 || call >= len(p.release) {
		return
	}
	p.once[call].Do(func() { close(p.release[call]) })
}

func TestHTTPGenerationScopedStopDoesNotCancelConcurrentEditedBranch(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "generation-scoped-stop-http.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','generation-stop@example.com','h','Generation Stop','admin')`)
	user, err := store.FindUserByID(ctx, db, "u1")
	if err != nil {
		t.Fatalf("load test user: %v", err)
	}

	channel, err := store.CreateChannel(ctx, db, "Generation Stop HTTP", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "generation-stop-model",
		Label:     "Generation stop model",
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
		ID: "c_generation_stop", UserID: user.ID, Title: "Generation scoped stop", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	memoryCache := cache.NewMemory()
	secret := "generation-stop-http-test-secret-32"
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
	provider := newGenerationStopHTTPProvider()
	providers.Register(provider)
	toolRegistry := tools.NewRegistry(db, cfg, logger)
	orchestrator := llm.NewOrchestrator(db, providers, toolRegistry, nil, memoryCache, nil, nil, nil, logger)
	authService := authsvc.New(secret, cfg.AccessTTL, cfg.RefreshTTL, memoryCache)
	token := issueBoundTestAccessToken(t, db, authService, user)

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
	client := server.Client()
	client.Timeout = 5 * time.Second
	t.Cleanup(func() {
		// Always unblock a broken implementation before httptest waits for active
		// handlers, so a failing regression test reports promptly instead of hanging.
		provider.releaseCall(0)
		provider.releaseCall(1)
		provider.releaseCall(2)
		provider.releaseCall(3)
		provider.releaseCall(4)
		memoryCache.Publish("user:"+user.ID+":kill", "1")
		server.Close()
	})

	firstResponse := branchHTTPPostMessage(t, client, server.URL, token, conversation.ID,
		`{"text":"original question","generation_id":"`+generationStopFirstID+`","tool_mode":"enabled","fast":true}`)
	waitGenerationStopProviderStarted(t, provider.started[0], "first generation")

	// A normal follow-up on the same active branch must not be accepted while its
	// assistant is still streaming. This is enforced by the server, so another
	// device/tab cannot bypass the current tab's Stop-only composer state.
	blockedResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/messages", token,
		`{"text":"must wait for the first answer","generation_id":"66666666-6666-4666-8666-666666666666","tool_mode":"enabled","fast":true}`)
	blockedBody, blockedReadErr := io.ReadAll(blockedResponse.Body)
	blockedResponse.Body.Close()
	if blockedReadErr != nil {
		t.Fatalf("read blocked normal append response: %v", blockedReadErr)
	}
	if blockedResponse.StatusCode != http.StatusConflict || !strings.Contains(string(blockedBody), `"error":"generation_in_progress"`) {
		t.Fatalf("blocked normal append status=%d body=%s, want 409 generation_in_progress", blockedResponse.StatusCode, blockedBody)
	}

	// Editing the root while the first answer is still running creates a sibling
	// user branch in the same conversation. Both provider calls must remain live.
	secondResponse := branchHTTPPostMessage(t, client, server.URL, token, conversation.ID,
		`{"text":"edited root question","parent_id":"","branch":true,"generation_id":"`+generationStopSecondID+`","tool_mode":"enabled","fast":true}`)
	waitGenerationStopProviderStarted(t, provider.started[1], "edited branch generation")

	stopResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/stop", token,
		`{"generation_id":"`+generationStopSecondID+`"}`)
	stopBody, readErr := io.ReadAll(stopResponse.Body)
	stopResponse.Body.Close()
	if readErr != nil {
		t.Fatalf("read targeted stop response: %v", readErr)
	}
	if stopResponse.StatusCode != http.StatusOK || !strings.Contains(string(stopBody), `"ok":true`) {
		t.Fatalf("targeted stop status=%d body=%s", stopResponse.StatusCode, stopBody)
	}
	mappedSecondID, mapped := memoryCache.Get(generationMessageKey(user.ID, conversation.ID, generationStopSecondID))
	if !mapped || mappedSecondID == "" {
		t.Fatal("targeted generation was not mapped to its persisted assistant message")
	}
	stoppingSecond, err := store.GetMessage(ctx, db, mappedSecondID)
	if err != nil {
		t.Fatalf("load assistant immediately after stop response: %v", err)
	}
	if stoppingSecond.Status == "streaming" {
		t.Fatal("stop response left assistant resumable as status=streaming")
	}

	secondFrames := branchHTTPReadSSE(t, secondResponse, nil)
	branchHTTPAssertTerminal(t, "targeted edited branch", secondFrames, "stopped")
	secondAssistantID := generationStopMessageID(t, secondFrames)

	// Completion of the targeted stream proves the stop was processed. Give any
	// accidental conversation-wide cancellation a scheduling window, then assert
	// that the first provider context is still alive.
	select {
	case <-provider.canceled[0]:
		t.Fatal("stopping the edited branch canceled the first branch")
	case <-time.After(200 * time.Millisecond):
	}

	provider.releaseCall(0)
	firstFrames := branchHTTPReadSSE(t, firstResponse, nil)
	branchHTTPAssertTerminal(t, "unrelated first branch", firstFrames, "stop")
	firstAssistantID := generationStopMessageID(t, firstFrames)

	select {
	case <-provider.canceled[1]:
	default:
		t.Fatal("targeted edited branch provider context was not canceled")
	}
	select {
	case <-provider.canceled[0]:
		t.Fatal("first branch was canceled before its successful completion")
	default:
	}

	firstAssistant, err := store.GetMessage(ctx, db, firstAssistantID)
	if err != nil {
		t.Fatalf("load first assistant: %v", err)
	}
	if firstAssistant.Status != "complete" || firstAssistant.StopReason != "stop" {
		t.Fatalf("first assistant status=%q stop_reason=%q, want complete/stop",
			firstAssistant.Status, firstAssistant.StopReason)
	}
	if got := generationStopMessageText(t, *firstAssistant); got != "first branch completed after sibling stop" {
		t.Fatalf("first assistant text = %q", got)
	}

	secondAssistant, err := store.GetMessage(ctx, db, secondAssistantID)
	if err != nil {
		t.Fatalf("load second assistant: %v", err)
	}
	if secondAssistant.Status != "stopped" || secondAssistant.StopReason != "stopped" {
		t.Fatalf("second assistant status=%q stop_reason=%q, want stopped/stopped",
			secondAssistant.Status, secondAssistant.StopReason)
	}
	if got := generationStopMessageText(t, *secondAssistant); got != "" {
		t.Fatalf("second assistant text = %q, want empty stopped reply", got)
	}

	updatedConversation, err := store.GetConversation(ctx, db, conversation.ID, user.ID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if updatedConversation.ActiveLeafID != secondAssistantID {
		t.Fatalf("active leaf = %q, want stopped edited branch %q",
			updatedConversation.ActiveLeafID, secondAssistantID)
	}

	// Regeneration has its own request decoder and stream setup. Exercise it over
	// HTTP as well so generation_id cannot accidentally work only for /messages.
	regenerateResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/regenerate", token,
		`{"assistant_id":"`+firstAssistantID+`","generation_id":"`+generationStopRegenerateID+`","tool_mode":"enabled","fast":true}`)
	if regenerateResponse.StatusCode != http.StatusOK {
		defer regenerateResponse.Body.Close()
		payload, _ := io.ReadAll(regenerateResponse.Body)
		t.Fatalf("regenerate status=%d body=%s", regenerateResponse.StatusCode, payload)
	}
	if contentType := regenerateResponse.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		regenerateResponse.Body.Close()
		t.Fatalf("regenerate content-type = %q, want text/event-stream", contentType)
	}
	waitGenerationStopProviderStarted(t, provider.started[2], "regeneration")

	regenerateStopResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/stop", token,
		`{"generation_id":"`+generationStopRegenerateID+`"}`)
	regenerateStopBody, readErr := io.ReadAll(regenerateStopResponse.Body)
	regenerateStopResponse.Body.Close()
	if readErr != nil {
		t.Fatalf("read regeneration stop response: %v", readErr)
	}
	if regenerateStopResponse.StatusCode != http.StatusOK || !strings.Contains(string(regenerateStopBody), `"ok":true`) {
		t.Fatalf("regeneration stop status=%d body=%s", regenerateStopResponse.StatusCode, regenerateStopBody)
	}

	regenerateFrames := branchHTTPReadSSE(t, regenerateResponse, nil)
	branchHTTPAssertTerminal(t, "targeted regeneration", regenerateFrames, "stopped")
	regeneratedAssistantID := generationStopMessageID(t, regenerateFrames)
	select {
	case <-provider.canceled[2]:
	default:
		t.Fatal("regeneration provider context was not canceled")
	}
	regeneratedAssistant, err := store.GetMessage(ctx, db, regeneratedAssistantID)
	if err != nil {
		t.Fatalf("load regenerated assistant: %v", err)
	}
	if regeneratedAssistant.Status != "stopped" || regeneratedAssistant.StopReason != "stopped" {
		t.Fatalf("regenerated assistant status=%q stop_reason=%q, want stopped/stopped",
			regeneratedAssistant.Status, regeneratedAssistant.StopReason)
	}

	// Also cover the server-message selector. Read message_start without draining
	// the active SSE response, then stop that exact persisted assistant row.
	messageScopedResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/regenerate", token,
		`{"assistant_id":"`+firstAssistantID+`","generation_id":"`+generationStopMessageSelectorID+`","tool_mode":"enabled","fast":true}`)
	if messageScopedResponse.StatusCode != http.StatusOK {
		defer messageScopedResponse.Body.Close()
		payload, _ := io.ReadAll(messageScopedResponse.Body)
		t.Fatalf("message-scoped regenerate status=%d body=%s", messageScopedResponse.StatusCode, payload)
	}
	waitGenerationStopProviderStarted(t, provider.started[3], "message-scoped regeneration")
	messageScanner := bufio.NewScanner(messageScopedResponse.Body)
	messageScanner.Buffer(make([]byte, 1024), 1<<20)
	startFrame, frameErr := branchHTTPNextSSE(messageScanner)
	if frameErr != nil {
		messageScopedResponse.Body.Close()
		t.Fatalf("read message-scoped message_start: %v", frameErr)
	}
	if startFrame.Value.Type != "message_start" || startFrame.Value.MessageID == "" {
		messageScopedResponse.Body.Close()
		t.Fatalf("first message-scoped frame = %+v, want message_start with id", startFrame)
	}
	messageScopedAssistantID := startFrame.Value.MessageID

	messageStopResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/stop", token,
		`{"message_id":"`+messageScopedAssistantID+`"}`)
	messageStopBody, readErr := io.ReadAll(messageStopResponse.Body)
	messageStopResponse.Body.Close()
	if readErr != nil {
		t.Fatalf("read message-scoped stop response: %v", readErr)
	}
	if messageStopResponse.StatusCode != http.StatusOK || !strings.Contains(string(messageStopBody), `"ok":true`) {
		t.Fatalf("message-scoped stop status=%d body=%s", messageStopResponse.StatusCode, messageStopBody)
	}

	messageScopedFrames := generationStopDrainSSE(t, messageScopedResponse, messageScanner, startFrame)
	branchHTTPAssertTerminal(t, "message-scoped regeneration", messageScopedFrames, "stopped")
	select {
	case <-provider.canceled[3]:
	default:
		t.Fatal("message-scoped regeneration provider context was not canceled")
	}
	messageScopedAssistant, err := store.GetMessage(ctx, db, messageScopedAssistantID)
	if err != nil {
		t.Fatalf("load message-scoped assistant: %v", err)
	}
	if messageScopedAssistant.Status != "stopped" || messageScopedAssistant.StopReason != "stopped" {
		t.Fatalf("message-scoped assistant status=%q stop_reason=%q, want stopped/stopped",
			messageScopedAssistant.Status, messageScopedAssistant.StopReason)
	}

	// A Stop can beat the generation request to its subscription. The durable
	// marker must be remembered without canceling message persistence: once the
	// assistant placeholder exists, activate the pending stop and settle it.
	preStopResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/stop", token,
		`{"generation_id":"`+generationStopPreSubscribeID+`"}`)
	preStopBody, readErr := io.ReadAll(preStopResponse.Body)
	preStopResponse.Body.Close()
	if readErr != nil {
		t.Fatalf("read pre-subscribe stop response: %v", readErr)
	}
	if preStopResponse.StatusCode != http.StatusOK || !strings.Contains(string(preStopBody), `"ok":true`) {
		t.Fatalf("pre-subscribe stop status=%d body=%s", preStopResponse.StatusCode, preStopBody)
	}

	preStoppedResponse := branchHTTPPostMessage(t, client, server.URL, token, conversation.ID,
		`{"text":"stop before subscription","generation_id":"`+generationStopPreSubscribeID+`","tool_mode":"enabled","fast":true}`)
	preStoppedFrames := branchHTTPReadSSE(t, preStoppedResponse, nil)
	branchHTTPAssertTerminal(t, "pre-subscribe generation", preStoppedFrames, "stopped")
	preStoppedAssistantID := generationStopMessageID(t, preStoppedFrames)
	preStoppedAssistant, err := store.GetMessage(ctx, db, preStoppedAssistantID)
	if err != nil {
		t.Fatalf("load pre-subscribe stopped assistant: %v", err)
	}
	if preStoppedAssistant.Status != "stopped" || preStoppedAssistant.StopReason != "stopped" {
		t.Fatalf("pre-subscribe assistant status=%q stop_reason=%q, want stopped/stopped",
			preStoppedAssistant.Status, preStoppedAssistant.StopReason)
	}
	if preStoppedAssistant.ParentID == "" {
		t.Fatal("pre-subscribe stopped assistant was persisted without its user parent")
	}
	if _, err := store.GetMessage(ctx, db, preStoppedAssistant.ParentID); err != nil {
		t.Fatalf("load pre-subscribe stopped user message: %v", err)
	}

	if active, ok := memoryCache.Get("gen:active:" + user.ID); !ok || active != "0" {
		t.Fatalf("active generation count = %q (present=%v), want 0", active, ok)
	}
	if strings.Contains(strings.ToLower(logs.String()), "foreign key") {
		t.Fatalf("server logged a foreign-key error: %s", logs.String())
	}
}

func TestHTTPConversationStopIsScopedToWorkspaceMember(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-member-stop-http.db"))
	t.Cleanup(func() { _ = db.Close() })
	for _, user := range []struct{ id, email string }{
		{"stop-member-a", "stop-member-a@example.test"},
		{"stop-member-b", "stop-member-b@example.test"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role) VALUES(?,?,?,?,'admin')`,
			user.id, user.email, "h", user.id)
	}
	memberA, err := store.FindUserByID(ctx, db, "stop-member-a")
	if err != nil {
		t.Fatalf("load member A: %v", err)
	}
	memberB, err := store.FindUserByID(ctx, db, "stop-member-b")
	if err != nil {
		t.Fatalf("load member B: %v", err)
	}
	workspace, err := store.CreateWorkspace(ctx, db, memberA.ID, "Stop isolation")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.JoinWorkspace(ctx, db, workspace.ID, memberB.ID); err != nil {
		t.Fatalf("join member B: %v", err)
	}

	channel, err := store.CreateChannel(ctx, db, "Workspace Stop HTTP", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "workspace-stop-model",
		Label:     "Workspace stop model",
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
		ID:          "workspace-member-stop-conversation",
		UserID:      memberA.ID,
		WorkspaceID: workspace.ID,
		IsPublic:    true,
		Title:       "Workspace member stop isolation",
		ModelID:     model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	memoryCache := cache.NewMemory()
	secret := "workspace-member-stop-http-secret-32"
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
	provider := newGenerationStopHTTPProvider()
	providers.Register(provider)
	toolRegistry := tools.NewRegistry(db, cfg, logger)
	orchestrator := llm.NewOrchestrator(db, providers, toolRegistry, nil, memoryCache, nil, nil, nil, logger)
	authService := authsvc.New(secret, cfg.AccessTTL, cfg.RefreshTTL, memoryCache)
	tokenA := issueBoundTestAccessToken(t, db, authService, memberA)
	tokenB := issueBoundTestAccessToken(t, db, authService, memberB)

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
	client := server.Client()
	client.Timeout = 5 * time.Second
	t.Cleanup(func() {
		for call := 0; call < len(provider.release); call++ {
			provider.releaseCall(call)
		}
		memoryCache.Publish("user:"+memberA.ID+":kill", "1")
		memoryCache.Publish("user:"+memberB.ID+":kill", "1")
		server.Close()
	})

	responseA := branchHTTPPostMessage(t, client, server.URL, tokenA, conversation.ID,
		`{"text":"member A stays live","tool_mode":"enabled","fast":true}`)
	waitGenerationStopProviderStarted(t, provider.started[0], "member A generation")
	responseB := branchHTTPPostMessage(t, client, server.URL, tokenB, conversation.ID,
		`{"text":"member B stops own generation","tool_mode":"enabled","fast":true}`)
	waitGenerationStopProviderStarted(t, provider.started[1], "member B generation")

	stopResponse := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/conversations/"+conversation.ID+"/stop", tokenB, ``)
	stopBody, readErr := io.ReadAll(stopResponse.Body)
	stopResponse.Body.Close()
	if readErr != nil {
		t.Fatalf("read member B stop response: %v", readErr)
	}
	if stopResponse.StatusCode != http.StatusOK || !strings.Contains(string(stopBody), `"ok":true`) {
		t.Fatalf("member B stop status=%d body=%s", stopResponse.StatusCode, stopBody)
	}

	framesB := branchHTTPReadSSE(t, responseB, nil)
	branchHTTPAssertTerminal(t, "member B stopped generation", framesB, "stopped")
	select {
	case <-provider.canceled[1]:
	default:
		t.Fatal("member B's provider context was not canceled")
	}
	select {
	case <-provider.canceled[0]:
		t.Fatal("member B's conversation stop canceled member A")
	case <-time.After(200 * time.Millisecond):
	}

	provider.releaseCall(0)
	framesA := branchHTTPReadSSE(t, responseA, nil)
	branchHTTPAssertTerminal(t, "member A unaffected generation", framesA, "stop")
	select {
	case <-provider.canceled[0]:
		t.Fatal("member A was canceled before successful completion")
	default:
	}

	// A selector-less conversation stop is a live broadcast, not a durable intent.
	// A later generation from member B must not inherit the previous cancellation.
	laterB := branchHTTPPostMessage(t, client, server.URL, tokenB, conversation.ID,
		`{"text":"member B later generation","tool_mode":"enabled","fast":true}`)
	waitGenerationStopProviderStarted(t, provider.started[2], "member B later generation")
	select {
	case <-provider.canceled[2]:
		t.Fatal("member B's earlier conversation stop canceled a later generation")
	case <-time.After(200 * time.Millisecond):
	}
	provider.releaseCall(2)
	laterFramesB := branchHTTPReadSSE(t, laterB, nil)
	branchHTTPAssertTerminal(t, "member B later generation", laterFramesB, "stop")

	for _, userID := range []string{memberA.ID, memberB.ID} {
		if active, ok := memoryCache.Get("gen:active:" + userID); !ok || active != "0" {
			t.Fatalf("active generation count for %s = %q (present=%v), want 0", userID, active, ok)
		}
	}
	if strings.Contains(strings.ToLower(logs.String()), "foreign key") {
		t.Fatalf("server logged a foreign-key error: %s", logs.String())
	}
}

func waitGenerationStopProviderStarted(t *testing.T, started <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not reach the provider", label)
	}
}

func generationStopMessageID(t *testing.T, frames []branchHTTPSSEFrame) string {
	t.Helper()
	for _, frame := range frames {
		if frame.Value.Type == "message_start" && frame.Value.MessageID != "" {
			return frame.Value.MessageID
		}
	}
	t.Fatalf("SSE stream has no message_start id: %+v", frames)
	return ""
}

func generationStopMessageText(t *testing.T, message store.Message) string {
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

func generationStopDrainSSE(
	t *testing.T,
	response *http.Response,
	scanner *bufio.Scanner,
	initial branchHTTPSSEFrame,
) []branchHTTPSSEFrame {
	t.Helper()
	defer response.Body.Close()
	frames := []branchHTTPSSEFrame{initial}
	for {
		frame, err := branchHTTPNextSSE(scanner)
		if err == io.EOF {
			return frames
		}
		if err != nil {
			t.Fatalf("parse remaining SSE frame: %v", err)
		}
		if frame.Value.Type == "error" || frame.Event == "error" {
			t.Fatalf("SSE emitted an error event: %s", frame.Data)
		}
		frames = append(frames, frame)
	}
}
