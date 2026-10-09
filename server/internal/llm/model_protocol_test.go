package llm

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestModelProtocolFlowsToTasksAndTTFTFallback(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "model-protocol.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(ctx, db, "Generic", "claude", "", "https://proxy.example/v1", "key")
	if err != nil {
		t.Fatal(err)
	}
	m, err := store.CreateModel(ctx, db, store.Model{ChannelID: channel.ID, Kind: "chat", Protocol: "openai.responses", RequestID: "test", Label: "Test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	provider := &captureRequestProvider{}
	logger := log.New(io.Discard, "", 0)
	reg := NewRegistry(logger)
	reg.Register(provider)
	task := NewTaskLLM(db, reg, logger)
	if _, err := task.Run(ctx, TaskTitle, "hello", RunOpts{ModelID: m.ID}); err != nil {
		t.Fatal(err)
	}
	if provider.req.Model.Provider != "openai" || provider.req.Model.APIFormat != "responses" {
		t.Fatalf("task used channel protocol: %+v", provider.req.Model)
	}
	o := &Orchestrator{db: db, reg: reg, logger: logger}
	request, _, _, err := o.buildFallbackRequest(ctx, UnifiedChatRequest{}, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.Model.Provider != "openai" || request.Model.APIFormat != "responses" {
		t.Fatalf("TTFT fallback used channel protocol: %+v", request.Model)
	}
	stored, err := store.GetChannel(ctx, db, channel.ID)
	if err != nil || stored.Type != "claude" {
		t.Fatalf("request mutated stored channel: %+v, %v", stored, err)
	}
}

func TestModelProtocolVendorVersionRoots(t *testing.T) {
	for _, test := range []struct{ root, version, want string }{
		{"https://proxy.example", "v1", "https://proxy.example/v1"},
		{"https://proxy.example/v1/", "v1", "https://proxy.example/v1"},
		{"https://proxy.example/custom/v3", "v1", "https://proxy.example/custom/v3"},
		{"https://proxy.example/v1beta", "v1beta", "https://proxy.example/v1beta"},
	} {
		if got := VendorAPIBaseURL(test.root, "", test.version); got != test.want {
			t.Errorf("root %s = %s, want %s", test.root, got, test.want)
		}
	}
}
