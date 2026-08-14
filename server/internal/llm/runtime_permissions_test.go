package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/store"
)

type queuedMemoryJob struct {
	name string
	job  func(context.Context) error
}

type blockingMemoryExtractProvider struct {
	started chan struct{}
	release chan struct{}
	calls   int
}

func (p *blockingMemoryExtractProvider) ID() string { return "openai" }

func (p *blockingMemoryExtractProvider) Stream(
	ctx context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	p.calls++
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &UnifiedResult{
		Blocks: []UnifiedBlock{{Kind: "text", Text: `[{"memory_text":"The user prefers tea.","slot":"drink","value":"tea","memory_type":"preference","confidence":0.9,"status":"ACTIVE"}]`}},
		Usage:  Usage{InputTokens: 2, OutputTokens: 1},
	}, nil
}

func (q *queuedMemoryJob) Enqueue(name string, job func(context.Context) error) {
	q.name = name
	q.job = job
}

func TestOrchestratorRejectsDirectImageTurnWithoutDrawingPermission(t *testing.T) {
	orchestrator, provider, _, conversation, _, db := setupToolRouteTest(t)
	permissions := store.DefaultUserGroupPermissions()
	permissions.AllowDrawing = false
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_groups(id,name,permissions) VALUES('ug_no_drawing','No drawing',?)`, string(raw)); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET role='user',group_id='ug_no_drawing' WHERE id='u1'`); err != nil {
		t.Fatalf("assign group: %v", err)
	}
	channel, err := store.CreateChannel(context.Background(), db, "Image permission", "openai", "image", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create image channel: %v", err)
	}
	imageModel, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: channel.ID, Kind: "image", RequestID: "image-permission-test",
		Label: "Image permission test", Enabled: true, Stream: true,
	})
	if err != nil {
		t.Fatalf("create image model: %v", err)
	}

	_, runErr := orchestrator.Run(context.Background(), RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: imageModel.ID,
		UserText: "draw a lighthouse",
		// A forged permissive snapshot must not override the current database row.
		ToolAccessPolicy: &ToolAccessPolicy{
			Mode: "all", AllowDrawing: true, AllowMemory: true, AllowSkills: true,
		},
	}, func(SseEvent) {})
	if !errors.Is(runErr, ErrDrawingPermission) {
		t.Fatalf("Run error = %v, want ErrDrawingPermission", runErr)
	}
	if len(provider.mainRequests) != 0 {
		t.Fatalf("drawing-denied turn reached provider: %+v", provider.mainRequests)
	}
	var messages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=?`, conversation.ID).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messages != 0 {
		t.Fatalf("drawing-denied turn persisted %d messages, want 0", messages)
	}
}

func TestGroupMemoryPermissionBlocksInjectionAndSaveTool(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	permissions := store.DefaultUserGroupPermissions()
	permissions.AllowMemory = false
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_groups(id,name,permissions) VALUES('ug_no_memory','No memory',?)`, string(raw)); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET role='user',group_id='ug_no_memory' WHERE id='u1'`); err != nil {
		t.Fatalf("assign group: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES(?, 'ug_no_memory', 604800, 'count', 10)`, model.ID); err != nil {
		t.Fatalf("grant model quota: %v", err)
	}
	const marker = "MEMORY_PERMISSION_MARKER_DO_NOT_INJECT"
	if _, err := store.CreateMemory(context.Background(), db, store.Memory{
		UserID: "u1", MemoryText: marker, Slot: "permission-test",
		Value: marker, MemoryType: "preference", Status: "ACTIVE", Confidence: 1,
	}); err != nil {
		t.Fatalf("create memory: %v", err)
	}
	orchestrator.tools = memoryToolRegistry{}
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{
		ToolMode: ToolModeEnabled,
		ToolAccessPolicy: &ToolAccessPolicy{
			Mode: "all", AllowDrawing: true, AllowMemory: false, AllowSkills: true,
		},
	})
	if len(provider.mainRequests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(provider.mainRequests))
	}
	request := provider.mainRequests[0]
	if requestHasTool(request, "save_memory") {
		t.Fatalf("memory-denied request declared save_memory: %+v", request.Tools)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), marker) {
		t.Fatalf("memory-denied request injected existing memory: %s", encoded)
	}
}

func TestQueuedMemoryJobRechecksPermissionAfterRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "memory-revocation.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	permissions := store.DefaultUserGroupPermissions()
	raw, _ := json.Marshal(permissions)
	if _, err := db.ExecContext(ctx, `INSERT INTO user_groups(id,name,permissions) VALUES('ug_memory','Memory',?)`, string(raw)); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role,group_id) VALUES('memory-user','memory@example.test','h','user','ug_memory')`,
		`INSERT INTO channels(id,name,type) VALUES('memory-channel','Memory channel','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,enabled) VALUES('memory-task','memory-channel','chat','memory-task','Memory task',1)`,
		`INSERT INTO conversations(id,user_id,title,model_id) VALUES('memory-conversation','memory-user','Memory conversation','memory-task')`,
		`INSERT INTO messages(id,conversation_id,role,blocks,status,author_id) VALUES('memory-message','memory-conversation','user','[{"kind":"text","text":"I prefer tea"}]','complete','memory-user')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("fixture %q: %v", statement, err)
		}
	}
	if err := store.SetSetting(db, "task_model_id", "memory-task"); err != nil {
		t.Fatalf("set task model: %v", err)
	}
	provider := &toolRouteCaptureProvider{}
	registry := NewRegistry(log.New(io.Discard, "", 0))
	registry.Register(provider)
	worker := NewMemoryWorker(db, NewTaskLLM(db, registry, log.New(io.Discard, "", 0)), log.New(io.Discard, "", 0))
	queued := &queuedMemoryJob{}
	worker.EnqueueIfReady(queued, "memory-conversation")
	if queued.name != "memory.process" || queued.job == nil {
		t.Fatalf("memory job was not queued: %+v", queued)
	}

	permissions.AllowMemory = false
	raw, _ = json.Marshal(permissions)
	if _, err := db.ExecContext(ctx, `UPDATE user_groups SET permissions=? WHERE id='ug_memory'`, string(raw)); err != nil {
		t.Fatalf("revoke memory permission: %v", err)
	}
	if err := queued.job(ctx); err != nil {
		t.Fatalf("run queued job: %v", err)
	}
	if len(provider.mainRequests) != 0 || len(provider.taskRequests) != 0 {
		t.Fatalf("revoked queued job reached task model: main=%d task=%d", len(provider.mainRequests), len(provider.taskRequests))
	}
	memories, err := store.ListMemories(ctx, db, "memory-user", "")
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("revoked queued job saved memories: %+v", memories)
	}
}

func TestMemoryExtractionRechecksPermissionAfterInFlightRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "memory-inflight-revocation.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	permissions := store.DefaultUserGroupPermissions()
	raw, _ := json.Marshal(permissions)
	if _, err := db.ExecContext(ctx, `INSERT INTO user_groups(id,name,permissions) VALUES('ug_memory_inflight','Memory',?)`, string(raw)); err != nil {
		t.Fatalf("insert memory group: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role,group_id) VALUES('memory-inflight-user','memory-inflight@example.test','h','user','ug_memory_inflight')`,
		`INSERT INTO channels(id,name,type) VALUES('memory-inflight-channel','Memory channel','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,enabled) VALUES('memory-inflight-task','memory-inflight-channel','chat','memory-inflight-task','Memory task',1)`,
		`INSERT INTO conversations(id,user_id,title,model_id) VALUES('memory-inflight-conversation','memory-inflight-user','Memory conversation','memory-inflight-task')`,
		`INSERT INTO messages(id,conversation_id,role,blocks,status,author_id) VALUES('memory-inflight-message','memory-inflight-conversation','user','[{"kind":"text","text":"I prefer tea"}]','complete','memory-inflight-user')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("fixture %q: %v", statement, err)
		}
	}
	if err := store.SetSetting(db, "task_model_id", "memory-inflight-task"); err != nil {
		t.Fatalf("set task model: %v", err)
	}
	if err := store.SetSetting(db, "memory_enabled", true); err != nil {
		t.Fatalf("enable memory: %v", err)
	}

	provider := &blockingMemoryExtractProvider{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	registry := NewRegistry(log.New(io.Discard, "", 0))
	registry.Register(provider)
	worker := NewMemoryWorker(db, NewTaskLLM(db, registry, log.New(io.Discard, "", 0)), log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- worker.Process(ctx, "memory-inflight-conversation") }()

	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("memory extraction did not reach the task model")
	}
	permissions.AllowMemory = false
	raw, _ = json.Marshal(permissions)
	if _, err := db.ExecContext(ctx, `UPDATE user_groups SET permissions=? WHERE id='ug_memory_inflight'`, string(raw)); err != nil {
		t.Fatalf("revoke memory permission: %v", err)
	}
	close(provider.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process memory after revocation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory extraction did not finish after release")
	}
	if provider.calls != 1 {
		t.Fatalf("task model calls=%d, want only the in-flight extraction", provider.calls)
	}
	memories, err := store.ListMemories(ctx, db, "memory-inflight-user", "")
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("in-flight extraction saved memories after revocation: %+v", memories)
	}
}
