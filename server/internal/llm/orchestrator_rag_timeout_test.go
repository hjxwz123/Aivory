package llm

import (
	"bytes"
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aivory/server/internal/rag"
	"aivory/server/internal/store"
)

type blockingOnlineRAGRouter struct {
	calls atomic.Int32
}

func (router *blockingOnlineRAGRouter) RunJSON(ctx context.Context, _ string, _ string, _ any, _ rag.RouterOpts) error {
	router.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

type ragTimeoutProvider struct {
	calls atomic.Int32
}

func (*ragTimeoutProvider) ID() string { return "openai" }

func (provider *ragTimeoutProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	provider.calls.Add(1)
	onEvent(SseEvent{Type: "text_delta", Text: "main model answer"})
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "main model answer"}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 2, OutputTokens: 3},
	}, nil
}

func TestOrchestratorOnlineRAGTimeoutFailsOpenToMainProvider(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "online-rag-timeout.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','rag-timeout@example.test','hash','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "RAG timeout", "openai", "chat", "https://example.invalid", "test-key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "rag-timeout-model", Label: "RAG timeout model",
		Enabled: true, Stream: true, ToolMode: "none", Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c1", UserID: "u1", Title: "RAG timeout", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path)
		 VALUES('doc1',?,'large.txt','text/plain',10000,'ready','/tmp/large.txt')`, conversation.ID,
	); err != nil {
		t.Fatalf("insert document: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model)
		 VALUES('chunk1','doc1',?,0,'text',?,'')`, conversation.ID, strings.Repeat("large document context ", 200),
	); err != nil {
		t.Fatalf("insert chunk: %v", err)
	}
	if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
		t.Fatalf("set RAG threshold: %v", err)
	}

	var logs bytes.Buffer
	logger := log.New(io.MultiWriter(&logs), "", 0)
	ragService := rag.New(db, nil, logger)
	router := &blockingOnlineRAGRouter{}
	ragService.SetTaskLLM(router)
	registry := NewRegistry(logger)
	provider := &ragTimeoutProvider{}
	registry.Register(provider)
	orchestrator := NewOrchestrator(db, registry, generationInterruptedTools{}, ragService, nil, nil, nil, nil, logger)

	previousTimeout := ragQueryTimeout
	ragQueryTimeout = 25 * time.Millisecond
	t.Cleanup(func() { ragQueryTimeout = previousTimeout })
	started := time.Now()
	result, err := orchestrator.Run(ctx, RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "one word", ToolMode: ToolModeDisabled,
	}, func(SseEvent) {})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("run after RAG timeout: %v", err)
	}
	if result == nil || result.AssistantMessage == nil {
		t.Fatalf("missing run result: %+v", result)
	}
	if router.calls.Load() != 1 {
		t.Fatalf("router calls=%d, want 1", router.calls.Load())
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("main provider calls=%d, want 1", provider.calls.Load())
	}
	if elapsed > time.Second {
		t.Fatalf("RAG fail-open took %s, want under 1s", elapsed)
	}
	if !strings.Contains(logs.String(), "rag: online query timed out") {
		t.Fatalf("missing explicit RAG timeout log: %s", logs.String())
	}
	persisted, err := store.GetMessage(ctx, db, result.AssistantMessage.ID)
	if err != nil {
		t.Fatalf("load assistant: %v", err)
	}
	if persisted.Status != "complete" {
		t.Fatalf("assistant status=%q, want complete", persisted.Status)
	}
}
