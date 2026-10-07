package llm

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func TestBackgroundProviderHealthRecordsChannelAndModelOutcomes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "provider-health.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}

	primary, err := store.CreateChannel(ctx, db, "Health primary", "openai", "chat", "https://primary.invalid", "primary-key")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CreateChannel(ctx, db, "Health fallback", "openai", "chat", "https://fallback.invalid", "fallback-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []*store.Channel{primary, fallback} {
		if _, err := db.Exec(`UPDATE channels SET auto_disable_errors=1, auto_disable_timeouts=1, auto_disable_minutes=5 WHERE id=?`, channel.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO channel_models(id,channel_id,request_id,label,kind,enabled) VALUES('health-fallback-model',?,'health-model','Health model','chat',1)`, fallback.ID); err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: primary.ID, RequestID: "health-model", Label: "Health model", Enabled: true,
		FallbackTTFTSec: 1, AutoDisableErrors: 1, AutoDisableTimeouts: 1, AutoDisableMinutes: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceModelChannelBindings(ctx, db, model,
		[]store.ModelChannelBinding{{ChannelID: primary.ID, Priority: 1, Weight: 100}},
		[]store.ModelChannelBinding{{ChannelID: fallback.ID, Priority: 1, Weight: 100}}); err != nil {
		t.Fatal(err)
	}

	recordBackgroundProviderHealth(ctx, db, model, []providerRequestSnapshot{
		{ChannelID: primary.ID, Error: "upstream failed"},
		{ChannelID: fallback.ID, Fallback: true},
	}, fallback.ID, false)
	primaryAfterError, err := store.GetChannel(ctx, db, primary.ID)
	if err != nil || primaryAfterError.AutoDisabledUntil <= time.Now().Unix() {
		t.Fatalf("primary channel was not quarantined: %+v, %v", primaryAfterError, err)
	}
	blockedUntil := primaryAfterError.AutoDisabledUntil
	if err := store.ResetChannelCounters(ctx, db, primary.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordChannelFailure(ctx, db, primary.ID, "error"); err != nil {
		t.Fatal(err)
	}
	primaryAfterInflightResult, err := store.GetChannel(ctx, db, primary.ID)
	if err != nil || primaryAfterInflightResult.AutoDisabledUntil != blockedUntil {
		t.Fatalf("in-flight result changed channel quarantine: %+v, %v", primaryAfterInflightResult, err)
	}
	bindings, err := store.ListModelChannelBindings(ctx, db, model.ID, "")
	if err != nil || len(bindings) != 2 {
		t.Fatalf("bindings=%+v, err=%v", bindings, err)
	}
	for _, binding := range bindings {
		if binding.ChannelID == primary.ID && binding.DisabledUntil <= time.Now().Unix() {
			t.Fatalf("primary model binding was not quarantined: %+v", binding)
		}
		if binding.ChannelID == fallback.ID && binding.DisabledUntil != 0 {
			t.Fatalf("successful fallback binding was quarantined: %+v", binding)
		}
	}

	if err := store.ResetChannelResult(ctx, db, primary.ID); err != nil {
		t.Fatal(err)
	}
	bindings, err = store.ListModelChannelBindings(ctx, db, model.ID, "regular")
	if err != nil || len(bindings) != 1 || bindings[0].DisabledUntil <= time.Now().Unix() {
		t.Fatalf("channel recovery also recovered model binding: %+v, %v", bindings, err)
	}
	if err := store.ResetModelChannelResult(ctx, db, model.ID, primary.ID, "regular"); err != nil {
		t.Fatal(err)
	}

	deadline := errors.Join(context.DeadlineExceeded, errors.New("upstream timed out"))
	if !shouldRecordProviderHealth(deadline, true) || shouldRecordProviderHealth(deadline, false) || shouldRecordProviderHealth(context.Canceled, true) {
		t.Fatal("provider-health gate did not distinguish TTFT timeout from cancellation and unrelated deadlines")
	}
	recordBackgroundProviderHealth(ctx, db, model, []providerRequestSnapshot{{ChannelID: primary.ID}}, fallback.ID, true)
	primaryAfterTimeout, err := store.GetChannel(ctx, db, primary.ID)
	if err != nil || primaryAfterTimeout.AutoDisabledUntil <= time.Now().Unix() {
		t.Fatalf("TTFT did not quarantine primary channel: %+v, %v", primaryAfterTimeout, err)
	}
	bindings, err = store.ListModelChannelBindings(ctx, db, model.ID, "regular")
	if err != nil || len(bindings) != 1 || bindings[0].DisabledUntil <= time.Now().Unix() {
		t.Fatalf("TTFT did not quarantine model binding: %+v, %v", bindings, err)
	}
}
