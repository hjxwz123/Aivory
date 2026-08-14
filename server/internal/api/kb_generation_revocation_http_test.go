package api

import (
	"bytes"
	"context"
	"encoding/json"
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

const (
	knowledgeBaseRevocationLateSecret = "late provider output after knowledge-base revocation"
	knowledgeBaseResharedAnswer       = "fresh answer after knowledge-base re-share"
)

// knowledgeBaseRevocationProvider ignores cancellation for its first request
// and returns normally after release, reproducing the narrow persistence race.
// Later requests complete immediately so the same test can prove that a fresh
// share epoch remains usable.
type knowledgeBaseRevocationProvider struct {
	mu               sync.Mutex
	requests         int
	firstStarted     chan struct{}
	firstCanceled    chan struct{}
	firstRelease     chan struct{}
	firstReleaseOnce sync.Once
}

func newKnowledgeBaseRevocationProvider() *knowledgeBaseRevocationProvider {
	return &knowledgeBaseRevocationProvider{
		firstStarted: make(chan struct{}), firstCanceled: make(chan struct{}), firstRelease: make(chan struct{}),
	}
}

func (provider *knowledgeBaseRevocationProvider) ID() string { return "openai" }

func (provider *knowledgeBaseRevocationProvider) Stream(
	ctx context.Context,
	_ llm.UnifiedChatRequest,
	_ llm.ToolRunner,
	onEvent func(llm.SseEvent),
) (*llm.UnifiedResult, error) {
	provider.mu.Lock()
	provider.requests++
	requestNumber := provider.requests
	provider.mu.Unlock()
	if requestNumber == 1 {
		close(provider.firstStarted)
		go func() {
			<-ctx.Done()
			close(provider.firstCanceled)
		}()
		<-provider.firstRelease
		onEvent(llm.SseEvent{Type: "text_delta", Text: knowledgeBaseRevocationLateSecret})
		return &llm.UnifiedResult{
			Blocks:     []llm.UnifiedBlock{{Kind: "text", Text: knowledgeBaseRevocationLateSecret}},
			Raw:        json.RawMessage(`{"provider_secret":true}`),
			Citations:  []llm.Citation{{URL: "https://secret.example", Title: "revoked source"}},
			StopReason: "stop",
			Usage:      llm.Usage{InputTokens: 3, OutputTokens: 7, CacheReadTokens: 2, CacheWriteTokens: 1},
		}, nil
	}

	onEvent(llm.SseEvent{Type: "text_delta", Text: knowledgeBaseResharedAnswer})
	return &llm.UnifiedResult{
		Blocks:     []llm.UnifiedBlock{{Kind: "text", Text: knowledgeBaseResharedAnswer}},
		StopReason: "stop",
		Usage:      llm.Usage{InputTokens: 2, OutputTokens: 5},
	}, nil
}

func (provider *knowledgeBaseRevocationProvider) releaseFirst() {
	provider.firstReleaseOnce.Do(func() { close(provider.firstRelease) })
}

func TestHTTPKnowledgeBaseShareRevocationScrubsLateProviderOutputAndAllowsReshare(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "knowledge-base-generation-revocation-http.db"))
	t.Cleanup(func() { _ = db.Close() })
	for _, user := range []struct {
		id    string
		email string
	}{
		{"kb-revocation-owner", "kb-revocation-owner@example.test"},
		{"kb-revocation-member", "kb-revocation-member@example.test"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role,status) VALUES(?,?,?,?,'admin','active')`,
			user.id, user.email, "h", user.id)
	}
	owner, err := store.FindUserByID(ctx, db, "kb-revocation-owner")
	if err != nil {
		t.Fatalf("load owner: %v", err)
	}
	member, err := store.FindUserByID(ctx, db, "kb-revocation-member")
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Knowledge base revocation", "openai", "chat", "https://example.invalid/v1", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "kb-revocation-chat",
		Label: "Knowledge base revocation chat", Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatalf("create chat model: %v", err)
	}
	if err := store.SetFastModel(ctx, db, model.ID); err != nil {
		t.Fatalf("set fast model: %v", err)
	}
	embedding, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "embedding", RequestID: "kb-revocation-embedding",
		Label: "Knowledge base revocation embedding", Enabled: true, Dim: 3,
	})
	if err != nil {
		t.Fatalf("create embedding model: %v", err)
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
	kb, err := store.CreateKB(ctx, db, store.KnowledgeBase{
		ID: "kb-revocation-library", UserID: owner.ID, Name: "Revocation library",
		EmbeddingModelID: embedding.ID, EmbeddingDim: embedding.Dim,
	})
	if err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	if _, err := store.UpsertKnowledgeBaseShare(ctx, db, kb.ID, owner.ID, member.ID, "read"); err != nil {
		t.Fatalf("share knowledge base: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "kb-revocation-conversation", UserID: member.ID,
		Title: "Knowledge base revocation", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	memoryCache := cache.NewMemory()
	secret := "knowledge-base-generation-revocation-secret-32"
	cfg := config.Config{
		JWTSecret: secret, AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour,
		UploadDir: t.TempDir(), ArtifactDir: t.TempDir(),
	}
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	providers := llm.NewRegistry(logger)
	provider := newKnowledgeBaseRevocationProvider()
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
		provider.releaseFirst()
		memoryCache.Publish("user:"+owner.ID+":kill", "1")
		memoryCache.Publish("user:"+member.ID+":kill", "1")
		server.Close()
	})

	stream := branchHTTPPostMessage(t, client, server.URL, memberToken, conversation.ID,
		`{"text":"use the shared library","tool_mode":"disabled","fast":true,"kb_ids":["`+kb.ID+`"]}`)
	waitGenerationStopProviderStarted(t, provider.firstStarted, "knowledge-base generation")
	var assistantID string
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM messages
		  WHERE conversation_id=? AND role='assistant' AND author_id=? AND status='streaming'
		  ORDER BY created_at DESC LIMIT 1`, conversation.ID, member.ID,
	).Scan(&assistantID); err != nil {
		t.Fatalf("load active assistant id: %v", err)
	}
	followerReq, err := http.NewRequest(http.MethodGet,
		server.URL+"/api/conversations/"+conversation.ID+"/messages/"+assistantID+"/stream", nil)
	if err != nil {
		t.Fatalf("build follower request: %v", err)
	}
	followerReq.Header.Set("Authorization", "Bearer "+memberToken)
	followerResp, err := client.Do(followerReq)
	if err != nil {
		t.Fatalf("open follower stream: %v", err)
	}
	defer followerResp.Body.Close()

	revoke := branchHTTPDoJSON(t, client, http.MethodDelete,
		server.URL+"/api/kbs/"+kb.ID+"/shares/"+member.ID, ownerToken, ``)
	revokeBody, readErr := io.ReadAll(revoke.Body)
	revoke.Body.Close()
	if readErr != nil {
		t.Fatalf("read revoke response: %v", readErr)
	}
	if revoke.StatusCode != http.StatusOK || !strings.Contains(string(revokeBody), `"ok":true`) {
		t.Fatalf("revoke status=%d body=%s", revoke.StatusCode, revokeBody)
	}
	select {
	case <-provider.firstCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("share revocation returned but provider context was not canceled")
	}
	provider.releaseFirst()

	frames := branchHTTPReadSSE(t, stream, nil)
	branchHTTPAssertTerminal(t, "revoked knowledge-base generation", frames, "stopped")
	for _, frame := range frames {
		if strings.Contains(frame.Data, knowledgeBaseRevocationLateSecret) {
			t.Fatalf("late provider content reached original SSE: %+v", frame)
		}
	}
	followerBody, err := io.ReadAll(followerResp.Body)
	if err != nil {
		t.Fatalf("read follower stream: %v", err)
	}
	if strings.Contains(string(followerBody), knowledgeBaseRevocationLateSecret) {
		t.Fatalf("late provider content reached follower stream: %s", followerBody)
	}
	persisted, err := store.GetMessage(ctx, db, assistantID)
	if err != nil {
		t.Fatalf("load scrubbed assistant: %v", err)
	}
	if persisted.Status != "stopped" || persisted.StopReason != "stopped" || string(persisted.Blocks) != "[]" ||
		len(persisted.Raw) != 0 || string(persisted.Citations) != "[]" || persisted.OutputTokens != 0 || persisted.Cost != 0 {
		t.Fatalf("revoked output persisted: %+v", persisted)
	}
	if !genstream.IsRevoked(memoryCache, assistantID) {
		t.Fatal("knowledge-base revoke did not tombstone the assistant stream")
	}

	reshare := branchHTTPDoJSON(t, client, http.MethodPut,
		server.URL+"/api/kbs/"+kb.ID+"/shares", ownerToken,
		`{"user_id":"`+member.ID+`","role":"read"}`)
	reshareBody, readErr := io.ReadAll(reshare.Body)
	reshare.Body.Close()
	if readErr != nil {
		t.Fatalf("read re-share response: %v", readErr)
	}
	if reshare.StatusCode != http.StatusOK {
		t.Fatalf("re-share status=%d body=%s", reshare.StatusCode, reshareBody)
	}
	replayReq, err := http.NewRequest(http.MethodGet,
		server.URL+"/api/conversations/"+conversation.ID+"/messages/"+assistantID+"/stream", nil)
	if err != nil {
		t.Fatalf("build replay request: %v", err)
	}
	replayReq.Header.Set("Authorization", "Bearer "+memberToken)
	replayResp, err := client.Do(replayReq)
	if err != nil {
		t.Fatalf("replay revoked stream: %v", err)
	}
	replayBody, err := io.ReadAll(replayResp.Body)
	replayResp.Body.Close()
	if err != nil {
		t.Fatalf("read replay response: %v", err)
	}
	if replayResp.StatusCode != http.StatusOK || strings.Contains(string(replayBody), knowledgeBaseRevocationLateSecret) {
		t.Fatalf("revoked replay status=%d body=%s", replayResp.StatusCode, replayBody)
	}

	freshStream := branchHTTPPostMessage(t, client, server.URL, memberToken, conversation.ID,
		`{"text":"use the re-shared library","tool_mode":"disabled","fast":true,"kb_ids":["`+kb.ID+`"]}`)
	freshFrames := branchHTTPReadSSE(t, freshStream, nil)
	branchHTTPAssertTerminal(t, "re-shared knowledge-base generation", freshFrames, "stop")
	freshAssistantID := generationStopMessageID(t, freshFrames)
	fresh, err := store.GetMessage(ctx, db, freshAssistantID)
	if err != nil {
		t.Fatalf("load fresh assistant: %v", err)
	}
	if fresh.Status != "complete" || !strings.Contains(generationStopMessageText(t, *fresh), knowledgeBaseResharedAnswer) {
		t.Fatalf("re-shared knowledge base did not produce a fresh answer: %+v", fresh)
	}
	if strings.Contains(logs.String(), knowledgeBaseRevocationLateSecret) {
		t.Fatalf("revoked provider output reached logs: %s", logs.String())
	}
}
