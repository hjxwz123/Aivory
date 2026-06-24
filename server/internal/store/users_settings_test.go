package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

func TestUpdateUserSettingsConcurrentMerges(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,settings) VALUES('u1','a@b.c','h','user','{}')`)

	patches := []map[string]any{
		{"onboarded": true},
		{"memory_enabled": false},
		{"response_length": "detailed"},
		{"accent_color": "moss"},
		{"persona_nickname": "Rae"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(patches))
	for _, patch := range patches {
		patch := patch
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := UpdateUserSettings(ctx, db, "u1", patch)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("update: %v", err)
		}
	}

	u, err := FindUserByID(ctx, db, "u1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	got := map[string]any{}
	if err := json.Unmarshal(u.Settings, &got); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	for _, key := range []string{"onboarded", "memory_enabled", "response_length", "accent_color", "persona_nickname"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("settings missing %q after concurrent updates: %s", key, string(u.Settings))
		}
	}
	if got["onboarded"] != true {
		t.Fatalf("onboarded = %v, want true", got["onboarded"])
	}
	if got["memory_enabled"] != false {
		t.Fatalf("memory_enabled = %v, want false", got["memory_enabled"])
	}
}

func TestBackfillUserOnboardedOnlyWhenMissing(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "onboarded.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,settings) VALUES('u_missing','m@b.c','h','user','{"accent_color":"moss"}')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,settings) VALUES('u_false','f@b.c','h','user','{"onboarded":false}')`)

	backfillUserOnboarded(db)

	var raw string
	if err := db.QueryRow(`SELECT settings FROM users WHERE id='u_missing'`).Scan(&raw); err != nil {
		t.Fatalf("read missing user: %v", err)
	}
	got := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal missing user: %v", err)
	}
	if got["onboarded"] != true || got["accent_color"] != "moss" {
		t.Fatalf("missing user settings = %s, want onboarded true with existing keys", raw)
	}

	if err := db.QueryRow(`SELECT settings FROM users WHERE id='u_false'`).Scan(&raw); err != nil {
		t.Fatalf("read false user: %v", err)
	}
	got = map[string]any{}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal false user: %v", err)
	}
	if got["onboarded"] != false {
		t.Fatalf("explicit onboarded false was overwritten: %s", raw)
	}
}
