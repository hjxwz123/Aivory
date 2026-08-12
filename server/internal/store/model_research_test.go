package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestModelResearchEnabledDefaultsAndUpdates(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "model-research.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO channels(id, name, type) VALUES('ch1', 'Main', 'openai')`); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	created, err := CreateModel(ctx, db, Model{
		ChannelID:                "ch1",
		Kind:                     "chat",
		RequestID:                "gpt-research",
		Label:                    "GPT Research",
		Enabled:                  true,
		Vision:                   true,
		Stream:                   true,
		CompactionTokenThreshold: 64000,
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if !created.ResearchEnabled {
		t.Fatal("expected new chat models to expose Deep Research by default")
	}
	if created.CompactionTokenThreshold != 64000 {
		t.Fatalf("compaction threshold = %d, want 64000", created.CompactionTokenThreshold)
	}

	disabledOnCreate, err := CreateModel(ctx, db, Model{
		ChannelID:          "ch1",
		Kind:               "chat",
		RequestID:          "gpt-no-research",
		Label:              "GPT No Research",
		Enabled:            true,
		ResearchEnabled:    false,
		ResearchEnabledSet: true,
	})
	if err != nil {
		t.Fatalf("create disabled model: %v", err)
	}
	if disabledOnCreate.ResearchEnabled {
		t.Fatal("expected explicit create-time Deep Research disable to be preserved")
	}

	created.ResearchEnabled = false
	created.CompactionTokenThreshold = 48000
	updated, err := UpdateModel(ctx, db, created.ID, *created)
	if err != nil {
		t.Fatalf("update model: %v", err)
	}
	if updated.ResearchEnabled {
		t.Fatal("expected update to persist disabled Deep Research exposure")
	}
	if updated.CompactionTokenThreshold != 48000 {
		t.Fatalf("updated compaction threshold = %d, want 48000", updated.CompactionTokenThreshold)
	}

	got, err := GetModel(ctx, db, created.ID)
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	if got.ResearchEnabled {
		t.Fatal("expected disabled Deep Research exposure after reload")
	}
	if got.CompactionTokenThreshold != 48000 {
		t.Fatalf("reloaded compaction threshold = %d, want 48000", got.CompactionTokenThreshold)
	}
}
