package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func TestProviderPriorityQueueRetriesAllChannelsAndStaysOnSuccessfulChannel(t *testing.T) {
	var hits [3]atomic.Int32
	candidates := make([]ChannelCreds, 0, 3)
	for index, channelID := range []string{"first", "second", "third"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[index].Add(1)
			if r.Header.Get("X-Channel") != channelID {
				t.Errorf("channel %s headers = %v", channelID, r.Header)
			}
			if index == 0 {
				http.Error(w, "first channel failed", http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, channelID)
		}))
		t.Cleanup(server.Close)
		candidates = append(candidates, ChannelCreds{ID: channelID, BaseURL: server.URL, APIKey: "key", Headers: map[string]string{"X-Channel": channelID}})
	}
	recorder := newProviderRequestRecorder()
	visible := new(atomic.Bool)
	ctx := contextWithProviderRequestRecorder(context.Background(), recorder)
	ctx = contextWithProviderVisibleOutput(ctx, visible)
	flag := new(atomic.Bool)
	index := new(atomic.Int64)
	model := ModelInfo{ID: "queue-model", ChannelCandidates: candidates, ChannelIndex: index}
	var events []SseEvent
	consume := func(resp *http.Response, emit func(SseEvent)) error {
		body, err := providerParsedTestBody(resp)
		if err != nil {
			return err
		}
		if body == "second" {
			return errors.New("second channel protocol failed")
		}
		emit(SseEvent{Type: "text_delta", Text: body})
		// Delivery must happen while this response is being consumed.
		if len(events) == 0 {
			t.Error("visible output was buffered until the response completed")
		}
		return nil
	}
	for range 2 {
		if err := doProviderParsedRequest(ctx, model, flag, providerParsedTestBuild(ctx), consume,
			observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible)); err != nil {
			t.Fatal(err)
		}
	}
	if got := []int32{hits[0].Load(), hits[1].Load(), hits[2].Load()}; !reflect.DeepEqual(got, []int32{1, 1, 2}) {
		t.Fatalf("attempts = %v, want first, second, third, third", got)
	}
	if index.Load() != 2 || !flag.Load() {
		t.Fatalf("sticky index=%d fallback=%v", index.Load(), flag.Load())
	}
	snapshots := recorder.snapshots()
	var channelIDs []string
	for _, snapshot := range snapshots {
		channelIDs = append(channelIDs, snapshot.ChannelID)
		if snapshot.ModelID != model.ID {
			t.Fatalf("request model attribution=%q, want %q", snapshot.ModelID, model.ID)
		}
	}
	if !reflect.DeepEqual(channelIDs, []string{"first", "second", "third", "third"}) || snapshots[0].Error == "" || snapshots[1].Error == "" || snapshots[2].Error != "" {
		t.Fatalf("request attribution = %+v", snapshots)
	}
}

func TestProviderPriorityQueueStopsAfterVisibleOutput(t *testing.T) {
	var hits [3]atomic.Int32
	candidates := make([]ChannelCreds, 0, 3)
	for index, channelID := range []string{"first", "second", "third"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits[index].Add(1)
			_, _ = io.WriteString(w, channelID)
		}))
		t.Cleanup(server.Close)
		candidates = append(candidates, ChannelCreds{ID: channelID, BaseURL: server.URL, APIKey: "key"})
	}
	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
	partialError := errors.New("stream broke after visible text")
	var text string
	err := doProviderParsedRequest(ctx, ModelInfo{ChannelCandidates: candidates}, new(atomic.Bool), providerParsedTestBuild(ctx),
		func(resp *http.Response, emit func(SseEvent)) error {
			body, err := providerParsedTestBody(resp)
			if err != nil {
				return err
			}
			if body == "first" {
				return errors.New("failed before output")
			}
			emit(SseEvent{Type: "text_delta", Text: "partial answer"})
			return partialError
		}, observeProviderVisibleOutput(func(ev SseEvent) { text += ev.Text }, visible))
	if !errors.Is(err, partialError) || text != "partial answer" || hits[2].Load() != 0 {
		t.Fatalf("err=%v text=%q third channel hits=%d", err, text, hits[2].Load())
	}
}

func TestGlobalTTFTOverridesModelValuesAndDisabledBindingsStayUnavailable(t *testing.T) {
	ctx := context.Background()
	db := openTaskChannelFallbackTestDB(t)
	primary, err := store.CreateChannel(ctx, db, "Queue primary", "openai", "chat", "https://primary.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CreateChannel(ctx, db, "Queue fallback", "openai", "chat", "https://fallback.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{ChannelID: primary.ID, FallbackChannelID: fallback.ID,
		RequestID: "queue-test", Label: "Queue test", Enabled: true, FallbackTTFTSec: 99, AutoDisableTimeouts: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, seconds := range []int{7, 0, 12} {
		if err := store.SetSetting(db, "fallback_ttft_sec", seconds); err != nil {
			t.Fatal(err)
		}
		got, threshold := modelTTFTPolicy(db, model.ID)
		if got != seconds || (seconds == 0 && threshold != 0) || (seconds > 0 && threshold != 2) {
			t.Fatalf("global=%d policy=%d/%d", seconds, got, threshold)
		}
	}
	if err := store.RecordModelChannelResult(ctx, db, model.ID, fallback.ID, "regular", "error", 1, 5); err != nil {
		t.Fatal(err)
	}
	queue, err := resolveModelChannelCandidates(ctx, db, model, "")
	if err != nil || len(queue) != 1 || queue[0].ID != primary.ID {
		t.Fatalf("legacy field bypassed binding quarantine: %v err=%v", queue, err)
	}
	if err := store.RecordModelChannelResult(ctx, db, model.ID, primary.ID, "regular", "error", 1, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveModelChannelCandidates(ctx, db, model, ""); err == nil {
		t.Fatal("all quarantined channels were scheduled through legacy fields")
	}
	if err := store.ResetModelChannelResult(ctx, db, model.ID, fallback.ID, "regular"); err != nil {
		t.Fatal(err)
	}
	queue, err = resolveModelChannelCandidates(ctx, db, model, "")
	if err != nil || len(queue) != 1 || queue[0].ID != fallback.ID {
		t.Fatalf("recovered lower-priority channel unavailable: %v err=%v", queue, err)
	}
	model.AutoDisableErrors, model.AutoDisableTimeouts, model.AutoDisableMinutes = 1, 1, 5
	recordBackgroundProviderHealth(ctx, db, model, []providerRequestSnapshot{
		{ModelID: model.ID, ChannelID: primary.ID, Error: "primary failed"},
		{ModelID: model.ID, ChannelID: fallback.ID, Fallback: true, Error: "context deadline exceeded"},
	}, fallback.ID, true)
	bindings, err := store.ListModelChannelBindings(ctx, db, model.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range bindings {
		if binding.DisabledUntil <= time.Now().Unix() {
			t.Fatalf("actual failed/timeout channel was not quarantined: %+v", binding)
		}
	}
}
