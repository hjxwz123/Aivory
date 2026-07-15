package llm

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

type captureRequestProvider struct {
	req UnifiedChatRequest
}

func (p *captureRequestProvider) ID() string { return "openai" }

func (p *captureRequestProvider) Stream(_ context.Context, req UnifiedChatRequest, _ ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	p.req = req
	onEvent(SseEvent{Type: "text_delta", Text: "ok"})
	return &UnifiedResult{Blocks: []UnifiedBlock{{Kind: "text", Text: "ok"}}}, nil
}

func TestModelExtraParamsFlowToTaskAndFallback(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "extra-params.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(ctx, db, "Extra params", "openai", "chat", "https://api.example", "key")
	if err != nil {
		t.Fatal(err)
	}
	taskModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID:   channel.ID,
		Kind:        "chat",
		RequestID:   "task-model",
		Label:       "Task model",
		Enabled:     true,
		ExtraParams: json.RawMessage(`{"temperature":0.2,"reasoning":{"effort":"low"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	fallbackModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID:   channel.ID,
		Kind:        "chat",
		RequestID:   "fallback-model",
		Label:       "Fallback model",
		Enabled:     true,
		ExtraParams: json.RawMessage(`{"temperature":0.8,"reasoning":{"effort":"high"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := &captureRequestProvider{}
	reg := NewRegistry(log.New(io.Discard, "", 0))
	reg.Register(provider)
	task := NewTaskLLM(db, reg, log.New(io.Discard, "", 0))
	if _, err := task.Run(ctx, TaskTitle, "hello", RunOpts{ModelID: taskModel.ID}); err != nil {
		t.Fatalf("task run: %v", err)
	}
	assertJSONObjectsEqual(t, provider.req.ExtraParams, taskModel.ExtraParams)

	o := &Orchestrator{db: db, reg: reg, logger: log.New(io.Discard, "", 0)}
	base := UnifiedChatRequest{ExtraParams: taskModel.ExtraParams}
	fallbackReq, _, _, err := o.buildFallbackRequest(ctx, base, fallbackModel.ID)
	if err != nil {
		t.Fatalf("build fallback request: %v", err)
	}
	assertJSONObjectsEqual(t, fallbackReq.ExtraParams, fallbackModel.ExtraParams)
}

func assertJSONObjectsEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotObj, wantObj map[string]any
	if err := json.Unmarshal(got, &gotObj); err != nil {
		t.Fatalf("decode got JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(want, &wantObj); err != nil {
		t.Fatalf("decode want JSON: %v (%s)", err, want)
	}
	gotJSON, _ := json.Marshal(gotObj)
	wantJSON, _ := json.Marshal(wantObj)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("extra params = %s, want %s", gotJSON, wantJSON)
	}
}
