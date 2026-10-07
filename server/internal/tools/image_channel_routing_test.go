package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestImageModelUsesAvailableRegularAndFallbackBindings(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	channels := map[string]*store.Channel{}
	for _, id := range []string{"image-primary", "image-alt", "image-fallback-1", "image-fallback-2"} {
		channel, err := store.CreateChannel(ctx, db, id, "openai", "chat", "https://"+id+".example.test", "key")
		if err != nil {
			t.Fatal(err)
		}
		channels[id] = channel
	}
	for _, id := range []string{"image-alt", "image-fallback-1", "image-fallback-2"} {
		if _, err := db.Exec(`INSERT INTO channel_models(id,channel_id,request_id,label,kind,enabled) VALUES(?,?, 'gpt-image-test','Image test','image',1)`, "cm-"+id, channels[id].ID); err != nil {
			t.Fatal(err)
		}
	}
	model, err := store.CreateModel(ctx, db, store.Model{ChannelID: channels["image-primary"].ID, Kind: "image", RequestID: "gpt-image-test", Label: "Image test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceModelChannelBindings(ctx, db, model,
		[]store.ModelChannelBinding{
			{ChannelID: channels["image-primary"].ID, Priority: 1, Weight: 100},
			{ChannelID: channels["image-alt"].ID, Priority: 2, Weight: 100},
		},
		[]store.ModelChannelBinding{
			{ChannelID: channels["image-fallback-1"].ID, Priority: 1, Weight: 100},
			{ChannelID: channels["image-fallback-2"].ID, Priority: 2, Weight: 100},
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE model_channel_bindings SET disabled_until=? WHERE model_id=? AND channel_id=? AND role='regular'`, time.Now().Unix()+300, model.ID, channels["image-primary"].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE channels SET auto_disabled_until=? WHERE id=?`, time.Now().Unix()+300, channels["image-fallback-1"].ID); err != nil {
		t.Fatal(err)
	}
	tool := &imageGenerateTool{db: db}
	selected, err := tool.resolveImageModel(ctx, &llm.ToolContext{ImageModelID: model.ID})
	if err != nil || selected.ChannelID != channels["image-alt"].ID {
		t.Fatalf("selected image channel=%v err=%v, want available alternate", selected, err)
	}
	fallbacks := tool.resolveImageFallbackChannels(ctx, selected, channels["image-alt"])
	if len(fallbacks) != 1 || fallbacks[0].ID != channels["image-fallback-2"].ID {
		t.Fatalf("fallbacks=%v, want next active priority binding", fallbacks)
	}
	if _, err := db.Exec(`UPDATE channels SET enabled=0 WHERE id=?`, channels["image-alt"].ID); err != nil {
		t.Fatal(err)
	}
	selected, err = tool.resolveImageModel(ctx, &llm.ToolContext{ImageModelID: model.ID})
	if err != nil || selected.ChannelID != channels["image-fallback-2"].ID {
		t.Fatalf("next available image channel=%v err=%v", selected, err)
	}
	if _, err := db.Exec(`UPDATE channels SET enabled=0 WHERE id=?`, channels["image-fallback-2"].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.resolveImageModel(ctx, &llm.ToolContext{ImageModelID: model.ID}); err == nil {
		t.Fatal("model with no schedulable image channel was accepted")
	}
}

func TestImageProviderHealthKeepsQuarantineUntilManualRecovery(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	channel, err := store.CreateChannel(ctx, db, "Image health", "openai", "chat", "https://image-health.example.test", "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE channels SET auto_disable_errors=1, auto_disable_timeouts=1, auto_disable_minutes=5 WHERE id=?`, channel.ID); err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "image", RequestID: "gpt-image-health", Label: "Image health", Enabled: true,
		FallbackTTFTSec: 1, AutoDisableErrors: 1, AutoDisableTimeouts: 1, AutoDisableMinutes: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	recordImageProviderHealth(ctx, db, model, channel.ID, "regular", errors.New("provider failed"), false)
	quarantined, err := store.GetChannel(ctx, db, channel.ID)
	if err != nil || quarantined.AutoDisabledUntil <= time.Now().Unix() {
		t.Fatalf("channel health=%+v err=%v", quarantined, err)
	}
	blockedUntil := quarantined.AutoDisabledUntil
	recordImageProviderHealth(ctx, db, model, channel.ID, "regular", nil, false)
	quarantined, err = store.GetChannel(ctx, db, channel.ID)
	if err != nil || quarantined.AutoDisabledUntil != blockedUntil {
		t.Fatalf("late success cleared channel quarantine: %+v err=%v", quarantined, err)
	}
	if err := store.ResetChannelResult(ctx, db, channel.ID); err != nil {
		t.Fatal(err)
	}
	bindings, err := store.ListModelChannelBindings(ctx, db, model.ID, "regular")
	if err != nil || len(bindings) != 1 || bindings[0].DisabledUntil <= time.Now().Unix() {
		t.Fatalf("channel recovery also recovered model binding: %+v err=%v", bindings, err)
	}
}

func TestImageTTFTTrackerMeasuresTimeToResponse(t *testing.T) {
	late := &imageTTFTTracker{threshold: time.Millisecond}
	late.start()
	time.Sleep(5 * time.Millisecond)
	late.responseReceived()
	if !late.timedOutResult() {
		t.Fatal("late first response byte did not trigger TTFT")
	}
	fast := &imageTTFTTracker{threshold: time.Second}
	fast.start()
	fast.responseReceived()
	if fast.timedOutResult() {
		t.Fatal("fast first response byte was marked as a timeout")
	}
}
