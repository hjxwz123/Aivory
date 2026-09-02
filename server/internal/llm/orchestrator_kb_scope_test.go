package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/rag"
	"aivory/server/internal/store"
)

func TestConversationKnowledgeBaseSelectionUsesPerTurnSnapshot(t *testing.T) {
	conv := &store.Conversation{KBIDs: json.RawMessage(`["kb-persisted"]`)}

	if got := conversationKnowledgeBaseSelection(conv, RunRequest{}); len(got) != 1 || got[0] != "kb-persisted" {
		t.Fatalf("legacy omitted snapshot = %v, want persisted selection", got)
	}

	got := conversationKnowledgeBaseSelection(conv, RunRequest{
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{},
	})
	if len(got) != 0 {
		t.Fatalf("explicit empty snapshot = %v, want no optional knowledge bases", got)
	}

	got = conversationKnowledgeBaseSelection(conv, RunRequest{
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{"kb-current"},
	})
	if len(got) != 1 || got[0] != "kb-current" {
		t.Fatalf("explicit snapshot = %v, want current turn selection", got)
	}
}

func TestResolveConversationKnowledgeBaseSelectionDoesNotCarryOptionalKBToNextExplicitEmptyTurn(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "orchestrator-kb-sequential.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`,
		`INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,dim)
			VALUES('emb-a','ch1','embedding','emb-a','Embedding A',3)`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
			('kb-a','u1','A','emb-a',3)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	conv := &store.Conversation{
		UserID: "u1",
		KBIDs:  json.RawMessage(`["kb-a"]`),
	}

	first, err := resolveConversationKnowledgeBaseSelection(ctx, db, conv, RunRequest{
		UserID:                           "u1",
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{"kb-a"},
	})
	if err != nil || len(first) != 1 || first[0] != "kb-a" {
		t.Fatalf("first explicit turn ids=%v err=%v, want [kb-a]", first, err)
	}

	second, err := resolveConversationKnowledgeBaseSelection(ctx, db, conv, RunRequest{
		UserID:                           "u1",
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{},
	})
	if err != nil || len(second) != 0 {
		t.Fatalf("second explicit empty turn ids=%v err=%v, want no optional KB", second, err)
	}

	legacy, err := resolveConversationKnowledgeBaseSelection(ctx, db, conv, RunRequest{UserID: "u1"})
	if err != nil || len(legacy) != 1 || legacy[0] != "kb-a" {
		t.Fatalf("omitted-field compatibility ids=%v err=%v, want persisted [kb-a]", legacy, err)
	}
}

func TestResolveConversationKnowledgeBaseSelectionKeepsStrictSnapshotSemantics(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "orchestrator-kb-scope.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES
			('u1','u1@example.test','h','user'),
			('u2','u2@example.test','h','user')`,
		`INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,dim)
			VALUES('emb-a','ch1','embedding','emb-a','Embedding A',3)`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
			('kb-a','u1','A','emb-a',3),
			('kb-b','u1','B','emb-a',3),
			('kb-other','u2','Other','emb-a',3)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}

	conv := &store.Conversation{KBIDs: json.RawMessage(`["kb-a","kb-other"]`)}
	ids, err := resolveConversationKnowledgeBaseSelection(ctx, db, conv, RunRequest{UserID: "u1"})
	if err != nil || len(ids) != 1 || ids[0] != "kb-a" {
		t.Fatalf("persisted compatibility selection ids=%v err=%v", ids, err)
	}

	ids, err = resolveConversationKnowledgeBaseSelection(ctx, db, conv, RunRequest{
		UserID:                           "u1",
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{"kb-b", "kb-a", "kb-b"},
	})
	if err != nil || len(ids) != 2 || ids[0] != "kb-b" || ids[1] != "kb-a" {
		t.Fatalf("explicit ordered selection ids=%v err=%v", ids, err)
	}

	_, err = resolveConversationKnowledgeBaseSelection(ctx, db, conv, RunRequest{
		UserID:                           "u1",
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{"kb-a", "kb-other"},
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("explicit unauthorized selection err=%v, want ErrNotFound", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err = resolveConversationKnowledgeBaseSelection(ctx, db, conv, RunRequest{
		UserID:                           "u1",
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{"kb-a"},
	})
	if err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("explicit selection database err=%v, want surfaced database error", err)
	}
}

func TestOrchestratorKnowledgeBaseScopeIsPerTurn(t *testing.T) {
	ctx := context.Background()
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	logger := log.New(io.Discard, "", 0)
	orchestrator.rag = rag.New(db, nil, logger)

	embeddingChannel, err := store.CreateChannel(ctx, db, "KB embedding", "openai", "embedding", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create embedding channel: %v", err)
	}
	embeddingModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: embeddingChannel.ID,
		Kind:      "embedding",
		RequestID: "kb-scope-embedding",
		Label:     "KB Scope Embedding",
		Enabled:   true,
		Dim:       3,
	})
	if err != nil {
		t.Fatalf("create embedding model: %v", err)
	}

	const optionalMarker = "OPTIONAL_KB_SCOPE_MARKER_ALPHA"
	optionalKB, err := store.CreateKB(ctx, db, store.KnowledgeBase{
		ID:               "kb-optional-scope",
		UserID:           "u1",
		Name:             "Optional Scope",
		EmbeddingModelID: embeddingModel.ID,
		EmbeddingDim:     embeddingModel.Dim,
	})
	if err != nil {
		t.Fatalf("create optional KB: %v", err)
	}
	createScopeTestKBChunk(t, ctx, db, optionalKB.ID, embeddingModel.ID, optionalMarker)
	if _, err := db.ExecContext(ctx, `UPDATE conversations SET kb_ids=? WHERE id=?`, `["`+optionalKB.ID+`"]`, conv.ID); err != nil {
		t.Fatalf("persist conversation KB selection: %v", err)
	}

	firstEvents := []SseEvent{}
	first, err := orchestrator.Run(ctx, RunRequest{
		UserID:                           "u1",
		ConversationID:                   conv.ID,
		ModelID:                          model.ID,
		UserText:                         "Use the optional source.",
		ToolMode:                         ToolModeDisabled,
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{optionalKB.ID},
	}, func(event SseEvent) { firstEvents = append(firstEvents, event) })
	if err != nil {
		t.Fatalf("first explicit KB turn: %v", err)
	}
	assertRequestRAGMarker(t, provider.mainRequests, 0, optionalMarker, true)
	assertRAGEventPresence(t, firstEvents, true)

	secondEvents := []SseEvent{}
	if _, err := orchestrator.Run(ctx, RunRequest{
		UserID:                           "u1",
		ConversationID:                   conv.ID,
		ModelID:                          model.ID,
		UserText:                         "Do not use an optional source.",
		ToolMode:                         ToolModeDisabled,
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{},
	}, func(event SseEvent) { secondEvents = append(secondEvents, event) }); err != nil {
		t.Fatalf("second explicit empty KB turn: %v", err)
	}
	assertRequestRAGMarker(t, provider.mainRequests, 1, optionalMarker, false)
	assertRAGEventPresence(t, secondEvents, false)
	if got := provider.mainRequests[1].RAGSnippets; len(got) != 0 {
		t.Fatalf("second explicit empty turn RAG snippets=%+v, want none", got)
	}
	assertRequestContextExcludesMarker(t, provider.mainRequests[1], optionalMarker)

	regenerateEvents := []SseEvent{}
	if _, err := orchestrator.Run(ctx, RunRequest{
		UserID:                           "u1",
		ConversationID:                   conv.ID,
		ModelID:                          model.ID,
		ParentID:                         first.UserMessage.ID,
		ReuseExistingUserMessage:         true,
		ToolMode:                         ToolModeDisabled,
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{},
	}, func(event SseEvent) { regenerateEvents = append(regenerateEvents, event) }); err != nil {
		t.Fatalf("regenerate with explicit empty KB selection: %v", err)
	}
	assertRequestRAGMarker(t, provider.mainRequests, 2, optionalMarker, false)
	assertRAGEventPresence(t, regenerateEvents, false)
	if got := provider.mainRequests[2].RAGSnippets; len(got) != 0 {
		t.Fatalf("regenerate explicit empty RAG snippets=%+v, want none", got)
	}
	assertRequestContextExcludesMarker(t, provider.mainRequests[2], optionalMarker)

	if _, err := orchestrator.Run(ctx, RunRequest{
		UserID:         "u1",
		ConversationID: conv.ID,
		ModelID:        model.ID,
		UserText:       "Use the persisted compatibility selection.",
		ToolMode:       ToolModeDisabled,
	}, func(SseEvent) {}); err != nil {
		t.Fatalf("omitted-field compatibility turn: %v", err)
	}
	assertRequestRAGMarker(t, provider.mainRequests, 3, optionalMarker, true)

	const projectMarker = "PROJECT_KB_SCOPE_MARKER_BETA"
	projectKB, err := store.CreateKB(ctx, db, store.KnowledgeBase{
		ID:               "kb-project-scope",
		UserID:           "u1",
		Name:             "Project Scope",
		EmbeddingModelID: embeddingModel.ID,
		EmbeddingDim:     embeddingModel.Dim,
	})
	if err != nil {
		t.Fatalf("create project KB: %v", err)
	}
	createScopeTestKBChunk(t, ctx, db, projectKB.ID, embeddingModel.ID, projectMarker)
	project, err := store.CreateProject(ctx, db, store.Project{
		ID:     "project-kb-scope",
		UserID: "u1",
		Name:   "Project Scope",
		KBID:   projectKB.ID,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE conversations SET project_id=? WHERE id=?`, project.ID, conv.ID); err != nil {
		t.Fatalf("attach project: %v", err)
	}

	if _, err := orchestrator.Run(ctx, RunRequest{
		UserID:                           "u1",
		ConversationID:                   conv.ID,
		ModelID:                          model.ID,
		UserText:                         "Use only the implicit project source.",
		ToolMode:                         ToolModeDisabled,
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{},
	}, func(SseEvent) {}); err != nil {
		t.Fatalf("explicit empty optional selection in project: %v", err)
	}
	assertRequestRAGMarker(t, provider.mainRequests, 4, projectMarker, true)
	assertRequestRAGMarker(t, provider.mainRequests, 4, optionalMarker, false)

	emptyProjectKB, err := store.CreateKB(ctx, db, store.KnowledgeBase{
		ID:               "kb-empty-project-scope",
		UserID:           "u1",
		Name:             "Empty Project Scope",
		EmbeddingModelID: embeddingModel.ID,
		EmbeddingDim:     embeddingModel.Dim,
	})
	if err != nil {
		t.Fatalf("create empty project KB: %v", err)
	}
	emptyProject, err := store.CreateProject(ctx, db, store.Project{
		ID: "empty-project-kb-scope", UserID: "u1", Name: "Empty Project", KBID: emptyProjectKB.ID,
	})
	if err != nil {
		t.Fatalf("create empty project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE conversations SET project_id=? WHERE id=?`, emptyProject.ID, conv.ID); err != nil {
		t.Fatalf("attach empty project: %v", err)
	}
	emptyEvents := []SseEvent{}
	if _, err := orchestrator.Run(ctx, RunRequest{
		UserID: "u1", ConversationID: conv.ID, ModelID: model.ID,
		UserText: "Answer without project files.", ToolMode: ToolModeDisabled,
		KnowledgeBaseSelectionConfigured: true, KnowledgeBaseIDs: []string{},
	}, func(event SseEvent) { emptyEvents = append(emptyEvents, event) }); err != nil {
		t.Fatalf("empty project turn: %v", err)
	}
	assertRAGEventPresence(t, emptyEvents, false)
	if got := provider.mainRequests[len(provider.mainRequests)-1].RAGSnippets; len(got) != 0 {
		t.Fatalf("empty project RAG snippets=%+v, want none", got)
	}
}

func createScopeTestKBChunk(t *testing.T, ctx context.Context, db *sql.DB, kbID, embeddingModelID, marker string) {
	t.Helper()
	document, err := store.CreateDocument(ctx, db, store.Document{
		ID:         "doc-" + kbID,
		KBID:       kbID,
		Filename:   kbID + ".txt",
		MimeType:   "text/plain",
		SizeBytes:  int64(len(marker)),
		Status:     "ready",
		ChunkCount: 1,
	})
	if err != nil {
		t.Fatalf("create KB document: %v", err)
	}
	if _, err := store.CreateChunkFull(ctx, db, store.ChunkInsert{
		DocumentID:     document.ID,
		KBID:           kbID,
		ChunkType:      "child",
		Content:        marker,
		EmbeddingModel: embeddingModelID,
	}); err != nil {
		t.Fatalf("create KB chunk: %v", err)
	}
}

func assertRequestRAGMarker(t *testing.T, requests []UnifiedChatRequest, index int, marker string, want bool) {
	t.Helper()
	if len(requests) <= index {
		t.Fatalf("main requests=%d, want request index %d", len(requests), index)
	}
	found := false
	for _, snippet := range requests[index].RAGSnippets {
		if strings.Contains(snippet.Snippet, marker) {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("request %d marker %q found=%v, want %v; snippets=%+v", index, marker, found, want, requests[index].RAGSnippets)
	}
}

func assertRequestContextExcludesMarker(t *testing.T, request UnifiedChatRequest, marker string) {
	t.Helper()
	history, err := json.Marshal(request.History)
	if err != nil {
		t.Fatalf("marshal provider history: %v", err)
	}
	for label, value := range map[string]string{
		"system prompt":   request.SystemPrompt,
		"message history": string(history),
	} {
		if strings.Contains(value, marker) {
			t.Fatalf("%s unexpectedly contains detached KB marker %q: %s", label, marker, value)
		}
	}
}

func assertRAGEventPresence(t *testing.T, events []SseEvent, want bool) {
	t.Helper()
	found := false
	for _, event := range events {
		if event.Type == "rag" {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("rag event found=%v, want %v; events=%+v", found, want, events)
	}
}
