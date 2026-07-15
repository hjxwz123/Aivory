package llm

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"aivory/server/internal/store"
)

type parentValidationProvider struct {
	calls atomic.Int32
}

func (p *parentValidationProvider) ID() string { return "openai" }

func (p *parentValidationProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	p.calls.Add(1)
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "ok"}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

type parentValidationTools struct{}

func (parentValidationTools) List(string) []ToolDef { return nil }

func (parentValidationTools) Run(context.Context, string, []byte, *ToolContext) (string, []Citation, error) {
	return "", nil, nil
}

type parentValidationFixture struct {
	db           *sql.DB
	orchestrator *Orchestrator
	provider     *parentValidationProvider
	logs         *bytes.Buffer
	modelID      string
	rootID       string
	leafID       string
	foreignID    string
}

func newParentValidationFixture(t *testing.T) parentValidationFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "parent-validation.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','parent@example.com','h','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Parent validation", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "parent-validation-model",
		Label:     "Parent validation model",
		Enabled:   true,
		Stream:    true,
		ToolMode:  "native",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	for _, conversation := range []store.Conversation{
		{ID: "c1", UserID: "u1", Title: "Primary", ModelID: model.ID},
		{ID: "c2", UserID: "u1", Title: "Foreign", ModelID: model.ID},
	} {
		if _, err := store.CreateConversation(ctx, db, conversation); err != nil {
			t.Fatalf("create conversation %s: %v", conversation.ID, err)
		}
	}
	root, err := store.CreateMessage(ctx, db, store.Message{ID: "root", ConversationID: "c1", Role: "user"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	leaf, err := store.CreateMessage(ctx, db, store.Message{ID: "leaf", ConversationID: "c1", ParentID: root.ID, Role: "assistant"})
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	foreign, err := store.CreateMessage(ctx, db, store.Message{ID: "foreign", ConversationID: "c2", Role: "assistant"})
	if err != nil {
		t.Fatalf("create foreign message: %v", err)
	}

	logs := &bytes.Buffer{}
	logger := log.New(logs, "", 0)
	provider := &parentValidationProvider{}
	registry := NewRegistry(logger)
	registry.Register(provider)
	return parentValidationFixture{
		db:           db,
		orchestrator: NewOrchestrator(db, registry, parentValidationTools{}, nil, nil, nil, nil, nil, logger),
		provider:     provider,
		logs:         logs,
		modelID:      model.ID,
		rootID:       root.ID,
		leafID:       leaf.ID,
		foreignID:    foreign.ID,
	}
}

func TestValidExplicitParentsPreserveBranchAndRegenerateSemantics(t *testing.T) {
	t.Run("branch edit", func(t *testing.T) {
		fixture := newParentValidationFixture(t)
		result, err := fixture.orchestrator.Run(context.Background(), RunRequest{
			UserID:         "u1",
			ConversationID: "c1",
			ModelID:        fixture.modelID,
			UserText:       "fork after the existing answer",
			ParentID:       fixture.leafID,
			Branch:         true,
			ToolMode:       ToolModeEnabled,
		}, func(SseEvent) {})
		if err != nil {
			t.Fatalf("run valid branch edit: %v", err)
		}
		if result == nil || result.UserMessage == nil || result.UserMessage.ParentID != fixture.leafID {
			t.Fatalf("branch result = %+v, want user parent %q", result, fixture.leafID)
		}
	})

	t.Run("regenerate reuses user", func(t *testing.T) {
		fixture := newParentValidationFixture(t)
		var before int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id='c1'`).Scan(&before); err != nil {
			t.Fatalf("count messages before: %v", err)
		}
		result, err := fixture.orchestrator.Run(context.Background(), RunRequest{
			UserID:                   "u1",
			ConversationID:           "c1",
			ModelID:                  fixture.modelID,
			UserText:                 "original question",
			ParentID:                 fixture.rootID,
			ReuseExistingUserMessage: true,
			ToolMode:                 ToolModeEnabled,
		}, func(SseEvent) {})
		if err != nil {
			t.Fatalf("run valid regenerate: %v", err)
		}
		if result == nil || result.UserMessage == nil || result.AssistantMessage == nil {
			t.Fatalf("incomplete regenerate result: %+v", result)
		}
		if result.UserMessage.ID != fixture.rootID || result.AssistantMessage.ParentID != fixture.rootID {
			t.Fatalf("regenerate result = %+v, want reused user %q", result, fixture.rootID)
		}
		var after int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id='c1'`).Scan(&after); err != nil {
			t.Fatalf("count messages after: %v", err)
		}
		if after != before+1 {
			t.Fatalf("regenerate inserted %d messages, want exactly one assistant", after-before)
		}
	})
}

func TestNormalAppendRecoversInvalidExplicitParent(t *testing.T) {
	fixture := newParentValidationFixture(t)
	result, err := fixture.orchestrator.Run(context.Background(), RunRequest{
		UserID:         "u1",
		ConversationID: "c1",
		ModelID:        fixture.modelID,
		UserText:       "continue on the active branch",
		ParentID:       "m_stale_optimistic_parent",
		ToolMode:       ToolModeEnabled,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("normal append with stale parent: %v", err)
	}
	if result == nil || result.UserMessage == nil || result.AssistantMessage == nil {
		t.Fatalf("incomplete result: %+v", result)
	}
	if result.UserMessage.ParentID != fixture.leafID {
		t.Fatalf("recovered parent = %q, want active leaf %q", result.UserMessage.ParentID, fixture.leafID)
	}
	if fixture.provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", fixture.provider.calls.Load())
	}
	if !strings.Contains(fixture.logs.String(), "recovered invalid explicit append parent") {
		t.Fatalf("parent recovery was not logged: %s", fixture.logs.String())
	}
}

func TestExplicitBranchRejectsInvalidParentBeforePersistence(t *testing.T) {
	tests := []struct {
		name   string
		parent func(parentValidationFixture) string
		reuse  bool
	}{
		{name: "missing branch parent", parent: func(parentValidationFixture) string { return "m_missing" }},
		{name: "cross-conversation branch parent", parent: func(f parentValidationFixture) string { return f.foreignID }},
		{name: "missing reused user parent", parent: func(parentValidationFixture) string { return "m_missing_reuse" }, reuse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newParentValidationFixture(t)
			var before int
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id='c1'`).Scan(&before); err != nil {
				t.Fatalf("count messages before: %v", err)
			}
			_, err := fixture.orchestrator.Run(context.Background(), RunRequest{
				UserID:                   "u1",
				ConversationID:           "c1",
				ModelID:                  fixture.modelID,
				UserText:                 "invalid branch",
				ParentID:                 tt.parent(fixture),
				Branch:                   !tt.reuse,
				ReuseExistingUserMessage: tt.reuse,
				ToolMode:                 ToolModeEnabled,
			}, func(SseEvent) {})
			if !errors.Is(err, ErrInvalidMessageParent) {
				t.Fatalf("error = %v, want ErrInvalidMessageParent", err)
			}
			low := strings.ToLower(err.Error())
			if strings.Contains(low, "save user message") || strings.Contains(low, "foreign key") || strings.Contains(low, "23503") {
				t.Fatalf("domain error leaked persistence details: %v", err)
			}
			var after int
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id='c1'`).Scan(&after); err != nil {
				t.Fatalf("count messages after: %v", err)
			}
			if after != before {
				t.Fatalf("message count changed from %d to %d for rejected parent", before, after)
			}
			if fixture.provider.calls.Load() != 0 {
				t.Fatalf("provider was called %d times for rejected parent", fixture.provider.calls.Load())
			}
			conversation, getErr := store.GetConversation(context.Background(), fixture.db, "c1", "u1")
			if getErr != nil {
				t.Fatalf("reload conversation: %v", getErr)
			}
			if conversation.ActiveLeafID != fixture.leafID {
				t.Fatalf("active leaf changed to %q, want %q", conversation.ActiveLeafID, fixture.leafID)
			}
		})
	}
}

func TestNormalizeMessageCreateErrorHidesParentForeignKeyDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "sqlite", err: errors.New("FOREIGN KEY constraint failed")},
		{name: "postgres", err: errors.New(`ERROR: insert or update on table "messages" violates foreign key constraint "messages_parent_id_fkey" (SQLSTATE 23503)`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, operation := range []string{"save user message", "save assistant placeholder"} {
				got := normalizeMessageCreateError(operation, "stale-parent", tt.err)
				if !errors.Is(got, ErrInvalidMessageParent) {
					t.Fatalf("error = %v, want ErrInvalidMessageParent", got)
				}
				low := strings.ToLower(got.Error())
				if strings.Contains(low, "foreign key") || strings.Contains(low, "23503") || strings.Contains(low, operation) {
					t.Fatalf("normalized error leaked persistence details: %v", got)
				}
			}
		})
	}

	underlying := errors.New("database unavailable")
	got := normalizeMessageCreateError("save assistant placeholder", "parent", underlying)
	if !errors.Is(got, underlying) || !strings.Contains(got.Error(), "save assistant placeholder") {
		t.Fatalf("non-FK error lost operation/cause: %v", got)
	}
}
