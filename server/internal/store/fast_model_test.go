package store

import (
	"context"
	"path/filepath"
	"testing"
)

// §fast-mode: exactly one model is fast at a time; marking one clears the others,
// forces Deep Research off on it, and it must be an enabled chat model. The
// advanced-count guard backs the "keep ≥1 advanced model" rule.
func TestFastModelLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "fast-model.db"))
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
	mk := func(req string) *Model {
		m, err := CreateModel(ctx, db, Model{ChannelID: "ch1", Kind: "chat", RequestID: req, Label: req, Enabled: true})
		if err != nil {
			t.Fatalf("create %s: %v", req, err)
		}
		return m
	}
	a, b, c := mk("alpha"), mk("beta"), mk("gamma")

	// No fast model yet.
	if fm, err := GetFastModel(ctx, db); err != nil || fm != nil {
		t.Fatalf("expected no fast model, got %+v err=%v", fm, err)
	}

	// Mark A fast → GetFastModel returns A, and A's Deep Research is forced off.
	if err := SetFastModel(ctx, db, a.ID); err != nil {
		t.Fatalf("set fast A: %v", err)
	}
	fm, err := GetFastModel(ctx, db)
	if err != nil || fm == nil || fm.ID != a.ID {
		t.Fatalf("fast model should be A, got %+v err=%v", fm, err)
	}
	if fm.ResearchEnabled {
		t.Fatal("fast model must have Deep Research forced off")
	}

	// Marking B fast clears A (only one fast at a time).
	if err := SetFastModel(ctx, db, b.ID); err != nil {
		t.Fatalf("set fast B: %v", err)
	}
	fm, _ = GetFastModel(ctx, db)
	if fm == nil || fm.ID != b.ID {
		t.Fatalf("fast model should now be B, got %+v", fm)
	}
	if reA, _ := GetModel(ctx, db, a.ID); reA.Fast {
		t.Fatal("A should no longer be fast after B was marked")
	}

	// Advanced count excludes the fast model (B) and the excluded id.
	n, err := CountAdvancedChatModels(ctx, db, c.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 { // alpha is the only advanced model other than gamma
		t.Fatalf("advanced count (excluding gamma) = %d, want 1 (alpha; beta is fast)", n)
	}

	// A disabled model can't be the fast model (GetFastModel filters enabled).
	if _, err := db.ExecContext(ctx, `UPDATE models SET enabled=0 WHERE id=?`, b.ID); err != nil {
		t.Fatalf("disable B: %v", err)
	}
	if fm, _ := GetFastModel(ctx, db); fm != nil {
		t.Fatalf("a disabled fast model must not resolve, got %+v", fm)
	}

	// Clearing (id="") removes the fast designation entirely.
	if err := SetFastModel(ctx, db, ""); err != nil {
		t.Fatalf("clear fast: %v", err)
	}
	var cnt int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM models WHERE fast=1`).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("expected no fast models after clear, got %d", cnt)
	}
}
