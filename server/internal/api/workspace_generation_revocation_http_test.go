package api

import (
	"bytes"
	"context"
	"errors"
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
	"aivory/server/internal/genstream"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
	"aivory/server/internal/tools"
)

const workspaceRevocationLateSecret = "late provider output after workspace revocation"

// workspaceRevocationIgnoringProvider intentionally observes cancellation only
// for the test assertion. Its generation path waits for an explicit release,
// emits a late secret, and returns success even when ctx is already canceled.
type workspaceRevocationIgnoringProvider struct {
	started         chan struct{}
	contextCanceled chan struct{}
	release         chan struct{}
	releaseOnce     sync.Once
}

func newWorkspaceRevocationIgnoringProvider() *workspaceRevocationIgnoringProvider {
	return &workspaceRevocationIgnoringProvider{
		started: make(chan struct{}), contextCanceled: make(chan struct{}), release: make(chan struct{}),
	}
}

func (provider *workspaceRevocationIgnoringProvider) ID() string { return "openai" }

func (provider *workspaceRevocationIgnoringProvider) Stream(
	ctx context.Context,
	_ llm.UnifiedChatRequest,
	_ llm.ToolRunner,
	onEvent func(llm.SseEvent),
) (*llm.UnifiedResult, error) {
	close(provider.started)
	go func() {
		<-ctx.Done()
		close(provider.contextCanceled)
	}()
	<-provider.release
	onEvent(llm.SseEvent{Type: "text_delta", Text: workspaceRevocationLateSecret})
	return &llm.UnifiedResult{
		Blocks:     []llm.UnifiedBlock{{Kind: "text", Text: workspaceRevocationLateSecret}},
		StopReason: "stop",
		Usage:      llm.Usage{InputTokens: 2, OutputTokens: 5},
	}, nil
}

func (provider *workspaceRevocationIgnoringProvider) releaseProvider() {
	provider.releaseOnce.Do(func() { close(provider.release) })
}

func TestHTTPWorkspaceKickCancelsAndScrubsActiveMemberGeneration(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-generation-kick-http.db"))
	t.Cleanup(func() { _ = db.Close() })
	for _, user := range []struct {
		id    string
		email string
	}{
		{"generation-kick-owner", "generation-kick-owner@example.test"},
		{"generation-kick-member", "generation-kick-member@example.test"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role,status) VALUES(?,?,?,?,'admin','active')`,
			user.id, user.email, "h", user.id)
	}
	owner, err := store.FindUserByID(ctx, db, "generation-kick-owner")
	if err != nil {
		t.Fatalf("load owner: %v", err)
	}
	member, err := store.FindUserByID(ctx, db, "generation-kick-member")
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	workspace, err := store.CreateWorkspace(ctx, db, owner.ID, "Generation kick")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.JoinWorkspace(ctx, db, workspace.ID, member.ID); err != nil {
		t.Fatalf("join member: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Generation kick HTTP", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "generation-kick-model",
		Label: "Generation kick model", Enabled: true, Stream: true, ToolMode: "native",
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
		ID: "workspace-generation-kick-conversation", UserID: owner.ID,
		WorkspaceID: workspace.ID, IsPublic: true, Title: "Generation kick", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	memoryCache := cache.NewMemory()
	secret := "workspace-generation-kick-secret-32"
	cfg := config.Config{
		JWTSecret: secret, AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour,
		UploadDir: t.TempDir(), ArtifactDir: t.TempDir(),
	}
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	providers := llm.NewRegistry(logger)
	provider := newWorkspaceRevocationIgnoringProvider()
	providers.Register(provider)
	toolRegistry := tools.NewRegistry(db, cfg, logger)
	orchestrator := llm.NewOrchestrator(db, providers, toolRegistry, nil, memoryCache, nil, nil, nil, logger)
	authService := authsvc.New(secret, cfg.AccessTTL, cfg.RefreshTTL, memoryCache)
	ownerToken := issueBoundTestAccessToken(t, db, authService, owner)
	memberToken := issueBoundTestAccessToken(t, db, authService, member)
	d := Deps{
		Config: cfg, DB: db, Cache: memoryCache, Auth: authService,
		Providers: providers, Tools: toolRegistry, Orchestrator: orchestrator, Logger: logger,
	}
	server := httptest.NewServer(NewRouter(d))
	client := server.Client()
	client.Timeout = 5 * time.Second
	t.Cleanup(func() {
		provider.releaseProvider()
		memoryCache.Publish("user:"+owner.ID+":kill", "1")
		memoryCache.Publish("user:"+member.ID+":kill", "1")
		server.Close()
	})

	stream := branchHTTPPostMessage(t, client, server.URL, memberToken, conversation.ID,
		`{"text":"answer must stop when I am kicked","tool_mode":"enabled","fast":true}`)
	waitGenerationStopProviderStarted(t, provider.started, "workspace member generation")
	var activeAssistantID string
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM messages
		  WHERE conversation_id=? AND role='assistant' AND author_id=? AND status='streaming'
		  ORDER BY created_at DESC LIMIT 1`, conversation.ID, member.ID,
	).Scan(&activeAssistantID); err != nil {
		t.Fatalf("load active assistant id: %v", err)
	}
	followerReq, err := http.NewRequest(http.MethodGet,
		server.URL+"/api/conversations/"+conversation.ID+"/messages/"+activeAssistantID+"/stream", nil)
	if err != nil {
		t.Fatalf("build pre-kick follower request: %v", err)
	}
	followerReq.Header.Set("Authorization", "Bearer "+memberToken)
	followerResp, err := client.Do(followerReq)
	if err != nil {
		t.Fatalf("open pre-kick follower stream: %v", err)
	}
	defer followerResp.Body.Close()

	kick := branchHTTPDoJSON(t, client, http.MethodDelete,
		server.URL+"/api/workspaces/"+workspace.ID+"/members/"+member.ID, ownerToken, ``)
	kickBody, readErr := io.ReadAll(kick.Body)
	kick.Body.Close()
	if readErr != nil {
		t.Fatalf("read kick response: %v", readErr)
	}
	if kick.StatusCode != http.StatusOK || !strings.Contains(string(kickBody), `"ok":true`) {
		t.Fatalf("kick status=%d body=%s", kick.StatusCode, kickBody)
	}
	select {
	case <-provider.contextCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("kick returned but the member's provider context was not canceled")
	}
	provider.releaseProvider()

	frames := branchHTTPReadSSE(t, stream, nil)
	branchHTTPAssertTerminal(t, "kicked member generation", frames, "stopped")
	for _, frame := range frames {
		if strings.Contains(frame.Data, workspaceRevocationLateSecret) {
			t.Fatalf("late provider content reached kicked member SSE: %+v", frame)
		}
	}
	followerBody, err := io.ReadAll(followerResp.Body)
	if err != nil {
		t.Fatalf("read pre-kick follower after revocation: %v", err)
	}
	if strings.Contains(string(followerBody), workspaceRevocationLateSecret) {
		t.Fatalf("late provider content reached pre-opened follower stream: %s", followerBody)
	}
	assistantID := generationStopMessageID(t, frames)
	if assistantID != activeAssistantID {
		t.Fatalf("stream assistant id=%q, database active id=%q", assistantID, activeAssistantID)
	}
	persisted, err := store.GetMessage(ctx, db, assistantID)
	if err != nil {
		t.Fatalf("load kicked member placeholder: %v", err)
	}
	if persisted.AuthorID != member.ID {
		t.Fatalf("assistant generation owner=%q, want %q", persisted.AuthorID, member.ID)
	}
	if persisted.Status != "stopped" || persisted.StopReason != "stopped" || string(persisted.Blocks) != "[]" {
		t.Fatalf("kicked member result status=%q stop=%q blocks=%s", persisted.Status, persisted.StopReason, persisted.Blocks)
	}
	if !genstream.IsRevoked(memoryCache, assistantID) {
		t.Fatal("kick did not tombstone the revoked assistant message stream")
	}

	// A fresh governed re-invitation creates new message epochs; it must neither
	// clear nor inherit the old message tombstone.
	reinvite, err := store.CreateWorkspaceInvite(ctx, db, workspace.ID, owner.ID, "", store.WorkspaceRoleMember, 0, 1)
	if err != nil {
		t.Fatalf("create re-invite: %v", err)
	}
	join := branchHTTPDoJSON(t, client, http.MethodPost,
		server.URL+"/api/workspaces/join/"+reinvite.Token, memberToken, ``)
	joinBody, readErr := io.ReadAll(join.Body)
	join.Body.Close()
	if readErr != nil {
		t.Fatalf("read rejoin response: %v", readErr)
	}
	if join.StatusCode != http.StatusOK {
		t.Fatalf("rejoin status=%d body=%s", join.StatusCode, joinBody)
	}
	if !genstream.IsRevoked(memoryCache, assistantID) {
		t.Fatal("fresh rejoin cleared the old generation tombstone")
	}
	replayReq, err := http.NewRequest(http.MethodGet,
		server.URL+"/api/conversations/"+conversation.ID+"/messages/"+assistantID+"/stream", nil)
	if err != nil {
		t.Fatalf("build replay request: %v", err)
	}
	replayReq.Header.Set("Authorization", "Bearer "+memberToken)
	replayResp, err := client.Do(replayReq)
	if err != nil {
		t.Fatalf("replay old stream after rejoin: %v", err)
	}
	replayBody, err := io.ReadAll(replayResp.Body)
	replayResp.Body.Close()
	if err != nil {
		t.Fatalf("read replay response: %v", err)
	}
	if replayResp.StatusCode != http.StatusOK || strings.Contains(string(replayBody), workspaceRevocationLateSecret) {
		t.Fatalf("revoked replay status=%d body=%s", replayResp.StatusCode, replayBody)
	}

	freshAssistant, err := store.CreateMessageForUser(ctx, db, store.Message{
		ID: "workspace-generation-after-rejoin", ConversationID: conversation.ID,
		Role: "assistant", AuthorID: member.ID, Status: "streaming",
	}, member.ID)
	if err != nil {
		t.Fatalf("create generation after rejoin: %v", err)
	}
	if _, appended, revoked := genstream.Append(memoryCache, freshAssistant.ID,
		llm.SseEvent{Type: "message_start", MessageID: freshAssistant.ID},
		generationStreamDenyKeys(workspace.ID)...); !appended || revoked {
		t.Fatalf("fresh generation inherited old tombstone: appended=%v revoked=%v", appended, revoked)
	}
	if strings.Contains(strings.ToLower(logs.String()), "foreign key") {
		t.Fatalf("server logged a foreign-key error: %s", logs.String())
	}
}

func TestHTTPWorkspaceDeleteRevokesPreopenedGenerationStream(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-generation-delete-http.db"))
	t.Cleanup(func() { _ = db.Close() })
	for _, user := range []struct {
		id    string
		email string
	}{
		{"generation-delete-owner", "generation-delete-owner@example.test"},
		{"generation-delete-member", "generation-delete-member@example.test"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role,status) VALUES(?,?,?,?,'admin','active')`,
			user.id, user.email, "h", user.id)
	}
	owner, err := store.FindUserByID(ctx, db, "generation-delete-owner")
	if err != nil {
		t.Fatalf("load owner: %v", err)
	}
	member, err := store.FindUserByID(ctx, db, "generation-delete-member")
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	workspace, err := store.CreateWorkspace(ctx, db, owner.ID, "Delete generation")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.JoinWorkspace(ctx, db, workspace.ID, member.ID); err != nil {
		t.Fatalf("join member: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "workspace-generation-delete-conversation", UserID: owner.ID,
		WorkspaceID: workspace.ID, IsPublic: true, Title: "Delete generation",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	assistant, err := store.CreateMessageForUser(ctx, db, store.Message{
		ID: "workspace-generation-delete-assistant", ConversationID: conversation.ID,
		Role: "assistant", AuthorID: member.ID, Status: "streaming",
	}, member.ID)
	if err != nil {
		t.Fatalf("create streaming assistant: %v", err)
	}

	memoryCache := cache.NewMemory()
	if _, appended, revoked := genstream.Append(memoryCache, assistant.ID,
		llm.SseEvent{Type: "message_start", MessageID: assistant.ID},
		generationStreamDenyKeys(workspace.ID)...); !appended || revoked {
		t.Fatalf("seed generation stream appended=%v revoked=%v", appended, revoked)
	}
	secret := "workspace-generation-delete-secret-32"
	cfg := config.Config{
		JWTSecret: secret, AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour,
		UploadDir: t.TempDir(), ArtifactDir: t.TempDir(),
	}
	authService := authsvc.New(secret, cfg.AccessTTL, cfg.RefreshTTL, memoryCache)
	ownerToken := issueBoundTestAccessToken(t, db, authService, owner)
	memberToken := issueBoundTestAccessToken(t, db, authService, member)
	d := Deps{
		Config: cfg, DB: db, Cache: memoryCache, Auth: authService,
		Logger: log.New(io.Discard, "", 0),
	}
	server := httptest.NewServer(NewRouter(d))
	client := server.Client()
	client.Timeout = 5 * time.Second
	t.Cleanup(server.Close)

	followerReq, err := http.NewRequest(http.MethodGet,
		server.URL+"/api/conversations/"+conversation.ID+"/messages/"+assistant.ID+"/stream", nil)
	if err != nil {
		t.Fatalf("build follower request: %v", err)
	}
	followerReq.Header.Set("Authorization", "Bearer "+memberToken)
	followerResp, err := client.Do(followerReq)
	if err != nil {
		t.Fatalf("open follower stream: %v", err)
	}
	defer followerResp.Body.Close()

	deleteResp := branchHTTPDoJSON(t, client, http.MethodDelete,
		server.URL+"/api/workspaces/"+workspace.ID, ownerToken, ``)
	deleteBody, err := io.ReadAll(deleteResp.Body)
	deleteResp.Body.Close()
	if err != nil {
		t.Fatalf("read delete response: %v", err)
	}
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("delete workspace status=%d body=%s", deleteResp.StatusCode, deleteBody)
	}

	const lateDeleteSecret = "late output after workspace deletion"
	if _, appended, revoked := genstream.Append(memoryCache, assistant.ID,
		llm.SseEvent{Type: "text_delta", MessageID: assistant.ID, Text: lateDeleteSecret},
		generationStreamDenyKeys(workspace.ID)...); appended || !revoked {
		t.Fatalf("append after workspace delete appended=%v revoked=%v", appended, revoked)
	}
	followerBody, err := io.ReadAll(followerResp.Body)
	if err != nil {
		t.Fatalf("read deleted workspace follower: %v", err)
	}
	if strings.Contains(string(followerBody), lateDeleteSecret) {
		t.Fatalf("deleted workspace follower received late output: %s", followerBody)
	}
	if !genstream.IsRevoked(memoryCache, assistant.ID) {
		t.Fatal("workspace delete did not tombstone active assistant stream")
	}
	if _, revoked := memoryCache.Get(workspaceGenerationRevocationKey(workspace.ID)); !revoked {
		t.Fatal("workspace delete did not retain workspace generation tombstone")
	}
	if _, err := store.GetWorkspace(ctx, db, workspace.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted workspace lookup error=%v, want ErrNotFound", err)
	}
}
