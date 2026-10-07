package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openModelChannelTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "model-channels.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate: %v", err)
	}
	return db, context.Background()
}

func TestModelChannelBindingsEnforceCapabilitiesAndPriority(t *testing.T) {
	db, ctx := openModelChannelTestDB(t)
	defer db.Close()
	for _, channel := range []string{"primary", "higher", "fallback", "unsupported"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO channels(id, name, type, api_key, enabled) VALUES(?, ?, 'openai', 'key', 1)`, channel, channel); err != nil {
			t.Fatalf("seed channel %s: %v", channel, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO channel_models(id, channel_id, request_id, label, kind, enabled) VALUES
		('cm-primary', 'primary', 'gpt-test', 'GPT test', 'chat', 1),
		('cm-higher', 'higher', 'gpt-test', 'GPT test', 'chat', 1),
		('cm-fallback', 'fallback', 'gpt-test', 'GPT test', 'chat', 1)`); err != nil {
		t.Fatalf("seed capabilities: %v", err)
	}
	model, err := CreateModel(ctx, db, Model{ChannelID: "primary", RequestID: "gpt-test", Label: "GPT test", Enabled: true})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if _, err := ReplaceModelChannelBindings(ctx, db, model,
		[]ModelChannelBinding{{ChannelID: "primary", Priority: 2, Weight: 100}, {ChannelID: "higher", Priority: 1, Weight: 100}},
		[]ModelChannelBinding{{ChannelID: "fallback", Priority: 1, Weight: 100}}); err != nil {
		t.Fatalf("replace bindings: %v", err)
	}
	selected, err := SelectModelChannelID(ctx, db, model.ID, model.RequestID, "regular", "")
	if err != nil {
		t.Fatalf("select regular: %v", err)
	}
	if selected != "higher" {
		t.Fatalf("selected regular channel = %q, want higher priority channel", selected)
	}
	_, err = ReplaceModelChannelBindings(ctx, db, model,
		[]ModelChannelBinding{{ChannelID: "primary", Priority: 1, Weight: 100}},
		[]ModelChannelBinding{{ChannelID: "unsupported", Priority: 1, Weight: 100}})
	if !errors.Is(err, ErrUnsupportedChannelModel) {
		t.Fatalf("unsupported fallback error = %v, want %v", err, ErrUnsupportedChannelModel)
	}
}

func TestRecordModelChannelResultQuarantinesOnlyBinding(t *testing.T) {
	db, ctx := openModelChannelTestDB(t)
	defer db.Close()
	for _, channel := range []string{"primary", "other"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO channels(id, name, type, api_key, enabled) VALUES(?, ?, 'openai', 'key', 1)`, channel, channel); err != nil {
			t.Fatalf("seed channel: %v", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO channel_models(id, channel_id, request_id, label, kind, enabled) VALUES(?, ?, 'gpt-test', 'GPT test', 'chat', 1)`, "cm-"+channel, channel); err != nil {
			t.Fatalf("seed capability: %v", err)
		}
	}
	model, err := CreateModel(ctx, db, Model{ChannelID: "primary", RequestID: "gpt-test", Label: "GPT test", Enabled: true})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if _, err := ReplaceModelChannelBindings(ctx, db, model,
		[]ModelChannelBinding{{ChannelID: "primary", Priority: 1, Weight: 100}}, nil); err != nil {
		t.Fatalf("replace binding: %v", err)
	}
	if err := RecordModelChannelResult(ctx, db, model.ID, "primary", "regular", "error", 2, 5); err != nil {
		t.Fatalf("record first error: %v", err)
	}
	if err := RecordModelChannelResult(ctx, db, model.ID, "primary", "regular", "error", 2, 5); err != nil {
		t.Fatalf("record second error: %v", err)
	}
	bindings, err := ListModelChannelBindings(ctx, db, model.ID, "regular")
	if err != nil {
		t.Fatalf("list binding: %v", err)
	}
	if len(bindings) != 1 || bindings[0].DisabledUntil < time.Now().Unix()+299 || bindings[0].ConsecutiveErrors != 0 {
		t.Fatalf("binding health = %+v, want five-minute quarantine with reset counter", bindings)
	}
	blockedUntil := bindings[0].DisabledUntil
	if err := ResetModelChannelCounters(ctx, db, model.ID, "primary", "regular"); err != nil {
		t.Fatalf("reset healthy counters: %v", err)
	}
	if err := RecordModelChannelResult(ctx, db, model.ID, "primary", "regular", "error", 1, 5); err != nil {
		t.Fatalf("record in-flight failure: %v", err)
	}
	bindings, err = ListModelChannelBindings(ctx, db, model.ID, "regular")
	if err != nil || len(bindings) != 1 || bindings[0].DisabledUntil != blockedUntil {
		t.Fatalf("in-flight result changed quarantine: %+v, %v", bindings, err)
	}
	if err := ResetModelChannelResult(ctx, db, model.ID, "primary", "regular"); err != nil {
		t.Fatalf("manual recovery: %v", err)
	}
	bindings, err = ListModelChannelBindings(ctx, db, model.ID, "regular")
	if err != nil || len(bindings) != 1 || bindings[0].DisabledUntil != 0 {
		t.Fatalf("manual recovery did not clear quarantine: %+v, %v", bindings, err)
	}
}

func TestRecordChannelResultAggregatesAcrossModelsAndRecoveryIsScoped(t *testing.T) {
	db, ctx := openModelChannelTestDB(t)
	defer db.Close()
	for _, id := range []string{"shared", "other"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO channels(id, name, type, api_key, enabled, auto_disable_errors, auto_disable_minutes) VALUES(?, ?, 'openai', 'key', 1, 2, 5)`, id, id); err != nil {
			t.Fatalf("seed channel %s: %v", id, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO channel_models(id, channel_id, request_id, label, kind, enabled) VALUES(?, ?, 'gpt-test', 'GPT test', 'chat', 1)`, "cm-"+id, id); err != nil {
			t.Fatalf("seed capability %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO channel_models(id, channel_id, request_id, label, kind, enabled) VALUES('cm-shared-b', 'shared', 'gpt-test-b', 'GPT test B', 'chat', 1)`); err != nil {
		t.Fatal(err)
	}
	modelA, err := CreateModel(ctx, db, Model{ChannelID: "shared", RequestID: "gpt-test", Label: "A", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	modelB, err := CreateModel(ctx, db, Model{ChannelID: "shared", RequestID: "gpt-test-b", Label: "B", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []*Model{modelA, modelB} {
		if _, err := ReplaceModelChannelBindings(ctx, db, model, []ModelChannelBinding{{ChannelID: "shared", Priority: 1, Weight: 100}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecordChannelResult(ctx, db, "shared", "error", 2, 5); err != nil {
		t.Fatal(err)
	}
	if err := RecordChannelResult(ctx, db, "shared", "error", 2, 5); err != nil {
		t.Fatal(err)
	}
	channel, err := GetChannel(ctx, db, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if channel.AutoDisabledUntil < time.Now().Unix()+299 || channel.ConsecutiveErrors != 0 {
		t.Fatalf("channel health = %+v", channel)
	}
	if _, err := SelectModelChannelID(ctx, db, modelA.ID, modelA.RequestID, "regular", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("model A should be blocked globally, got %v", err)
	}
	if _, err := SelectModelChannelID(ctx, db, modelB.ID, modelB.RequestID, "regular", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("model B should be blocked globally, got %v", err)
	}
	if err := RecordModelChannelResult(ctx, db, modelA.ID, "shared", "regular", "error", 1, 5); err != nil {
		t.Fatal(err)
	}
	if err := ResetChannelResult(ctx, db, "shared"); err != nil {
		t.Fatal(err)
	}
	channel, _ = GetChannel(ctx, db, "shared")
	if channel.AutoDisabledUntil != 0 {
		t.Fatalf("channel recovery did not clear global quarantine: %+v", channel)
	}
	bindings, err := ListModelChannelBindings(ctx, db, modelA.ID, "regular")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].DisabledUntil == 0 {
		t.Fatalf("channel recovery cleared model quarantine: %+v", bindings)
	}
	if err := ResetModelChannelResult(ctx, db, modelA.ID, "shared", "regular"); err != nil {
		t.Fatal(err)
	}
	channel, _ = GetChannel(ctx, db, "shared")
	if channel.AutoDisabledUntil != 0 {
		t.Fatalf("model recovery changed channel state: %+v", channel)
	}
}

func TestRecordChannelTimeoutAndManualChannelCloseKeepBindings(t *testing.T) {
	db, ctx := openModelChannelTestDB(t)
	defer db.Close()
	if _, err := db.ExecContext(ctx, `INSERT INTO channels(id, name, type, api_key, enabled, auto_disable_timeouts, auto_disable_minutes) VALUES('timeout', 'timeout', 'openai', 'key', 1, 2, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO channel_models(id, channel_id, request_id, label, kind, enabled) VALUES('cm-timeout', 'timeout', 'gpt-test', 'GPT test', 'chat', 1)`); err != nil {
		t.Fatal(err)
	}
	model, err := CreateModel(ctx, db, Model{ChannelID: "timeout", RequestID: "gpt-test", Label: "Timeout", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceModelChannelBindings(ctx, db, model, []ModelChannelBinding{{ChannelID: "timeout", Priority: 1, Weight: 100}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := RecordChannelResult(ctx, db, "timeout", "timeout", 2, 1); err != nil {
		t.Fatal(err)
	}
	if err := RecordChannelResult(ctx, db, "timeout", "timeout", 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectModelChannelID(ctx, db, model.ID, model.RequestID, "regular", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("timeout quarantine should block selection, got %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE channels SET enabled=0 WHERE id='timeout'`); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectModelChannelID(ctx, db, model.ID, model.RequestID, "regular", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("manually closed channel remained schedulable: %v", err)
	}
	bindings, err := ListModelChannelBindings(ctx, db, model.ID, "regular")
	if err != nil || len(bindings) != 1 || bindings[0].ChannelEnabled {
		t.Fatalf("manual close should preserve visible binding: %+v, %v", bindings, err)
	}
}

func TestLegacyChannelSchemaGetsQuarantineDefaults(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-channel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE channels (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, api_format TEXT NOT NULL DEFAULT '',
		base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '', headers TEXT NOT NULL DEFAULT '{}',
		enabled INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0
	); INSERT INTO channels(id,name,type,api_key) VALUES('legacy','Legacy','openai','key')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	channel, err := GetChannel(context.Background(), db, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if channel.AutoDisableErrors != 0 || channel.AutoDisableTimeouts != 0 || channel.AutoDisableMinutes != 0 || channel.AutoDisabledUntil != 0 || channel.ConsecutiveErrors != 0 || channel.ConsecutiveTimeouts != 0 {
		t.Fatalf("legacy quarantine defaults = %+v", channel)
	}
}

func TestLegacyModelChannelsAreBackfilledOnUpgrade(t *testing.T) {
	db, ctx := openModelChannelTestDB(t)
	defer db.Close()
	for _, id := range []string{"legacy-primary", "legacy-fallback"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO channels(id,name,type,api_key,enabled) VALUES(?,?,'openai','key',1)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO models(id,channel_id,kind,request_id,label,fallback_channel_id,enabled) VALUES('legacy-model','legacy-primary','chat','gpt-legacy','Legacy model','legacy-fallback',1)`); err != nil {
		t.Fatal(err)
	}
	// These tables/configuration did not exist in the older version.
	if _, err := db.ExecContext(ctx, `DELETE FROM channel_models; DELETE FROM model_channel_bindings`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("upgrade legacy model routing: %v", err)
	}
	for _, expected := range []struct{ channelID, role string }{
		{"legacy-primary", "regular"},
		{"legacy-fallback", "fallback"},
	} {
		var requestID string
		if err := db.QueryRowContext(ctx, `SELECT cm.request_id FROM channel_models cm WHERE cm.channel_id=? AND cm.enabled=1`, expected.channelID).Scan(&requestID); err != nil || requestID != "gpt-legacy" {
			t.Fatalf("capability for %s = %q, err=%v", expected.channelID, requestID, err)
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM model_channel_bindings WHERE model_id='legacy-model' AND channel_id=? AND role=?`, expected.channelID, expected.role).Scan(&count); err != nil || count != 1 {
			t.Fatalf("legacy %s binding count = %d, err=%v", expected.role, count, err)
		}
	}
}
