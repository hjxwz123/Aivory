package llm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"reflect"
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

func setupDocumentRAGProgress(t *testing.T, router rag.TaskRouter) (*Orchestrator, RunRequest, *ragTimeoutProvider, *bytes.Buffer, *sql.DB) {
	t.Helper()
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
	ragService.SetTaskLLM(router)
	registry := NewRegistry(logger)
	provider := &ragTimeoutProvider{}
	registry.Register(provider)
	orchestrator := NewOrchestrator(db, registry, generationInterruptedTools{}, ragService, nil, nil, nil, nil, logger)

	return orchestrator, RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "one word", ToolMode: ToolModeDisabled,
	}, provider, &logs, db
}

func TestOrchestratorOnlineRAGTimeoutFailsOpenToMainProvider(t *testing.T) {
	ctx := context.Background()
	router := &blockingOnlineRAGRouter{}
	orchestrator, request, provider, logs, db := setupDocumentRAGProgress(t, router)

	previousTimeout := ragQueryTimeout
	ragQueryTimeout = 25 * time.Millisecond
	t.Cleanup(func() { ragQueryTimeout = previousTimeout })
	started := time.Now()
	var statuses []string
	result, err := orchestrator.Run(ctx, request, func(event SseEvent) {
		if event.Type == "rag" {
			if event.Status == "document_searching" && router.calls.Load() != 0 {
				t.Error("searching event arrived after the router started")
			}
			statuses = append(statuses, event.Status)
		}
		if event.Type == "text_delta" && !reflect.DeepEqual(statuses, []string{"document_searching", "document_error"}) {
			t.Errorf("text arrived before retrieval failure notice: %v", statuses)
		}
	})
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

type documentProgressRouter struct {
	response string
	onCall   func()
}

func (r *documentProgressRouter) RunJSON(ctx context.Context, _ string, _ string, out any, _ rag.RouterOpts) error {
	if r.onCall != nil {
		r.onCall()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return json.Unmarshal([]byte(r.response), out)
}

func TestOrchestratorDocumentRAGProgressBeforeAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, response, terminal string
	}{
		{"found", `{"strategy":"retrieve","queries":["large document"]}`, "document_found"},
		{"skipped", `{"strategy":"none","queries":[]}`, "document_skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var statuses []string
			router := &documentProgressRouter{response: tc.response, onCall: func() {
				if !reflect.DeepEqual(statuses, []string{"document_searching"}) {
					t.Errorf("router started without progress: %v", statuses)
				}
			}}
			o, request, provider, _, _ := setupDocumentRAGProgress(t, router)
			_, err := o.Run(context.Background(), request, func(event SseEvent) {
				if event.Type == "rag" {
					statuses = append(statuses, event.Status)
					if event.Status == "document_found" && (event.SourceCount == nil || *event.SourceCount < 1) {
						t.Error("reported found without evidence")
					}
				}
				if event.Type == "text_delta" && !reflect.DeepEqual(statuses, []string{"document_searching", tc.terminal}) {
					t.Errorf("text arrived before final retrieval status: %v", statuses)
				}
			})
			if err != nil || provider.calls.Load() != 1 || !reflect.DeepEqual(statuses, []string{"document_searching", tc.terminal}) {
				t.Fatalf("err=%v provider=%d statuses=%v", err, provider.calls.Load(), statuses)
			}
		})
	}
}

func TestOrchestratorDocumentRAGStopDoesNotStartMainModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router := &documentProgressRouter{onCall: cancel}
	o, request, provider, _, _ := setupDocumentRAGProgress(t, router)
	var statuses []string
	_, err := o.Run(ctx, request, func(event SseEvent) {
		if event.Type == "rag" {
			statuses = append(statuses, event.Status)
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if provider.calls.Load() != 0 || !reflect.DeepEqual(statuses, []string{"document_searching"}) {
		t.Fatalf("provider=%d statuses=%v, stopped retrieval must not start generation or report success/error", provider.calls.Load(), statuses)
	}
}
