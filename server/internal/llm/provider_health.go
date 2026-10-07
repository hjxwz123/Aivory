package llm

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"aivory/server/internal/store"
)

func shouldRecordProviderHealth(err error, ttftTimedOut bool) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	return !errors.Is(err, context.DeadlineExceeded) || ttftTimedOut
}

// The final original-model attempt is the one still waiting when its watchdog
// fires. Later snapshots may belong to a transparent timeout fallback model.
func providerTimeoutSnapshotIndex(snapshots []providerRequestSnapshot, modelID string) int {
	index := -1
	for i, snapshot := range snapshots {
		if snapshot.ModelID == modelID && modelID != "" {
			index = i
		}
	}
	if index < 0 && len(snapshots) > 0 {
		return 0
	}
	return index
}

// recordBackgroundProviderHealth tracks channel and model-binding health for
// provider calls made outside the main chat turn (tasks and private chat).
func recordBackgroundProviderHealth(ctx context.Context, db *sql.DB, model *store.Model, snapshots []providerRequestSnapshot, fallbackChannelID string, ttftTimedOut bool) {
	if model == nil || db == nil || ctx == nil {
		return
	}
	timedOutIndex := -1
	if ttftTimedOut {
		timedOutIndex = providerTimeoutSnapshotIndex(snapshots, model.ID)
	}
	for index, snapshot := range snapshots {
		if index == timedOutIndex || (snapshot.ModelID != "" && snapshot.ModelID != model.ID) {
			continue
		}
		channelID := snapshot.ChannelID
		if channelID == "" {
			channelID = model.ChannelID
		}
		if snapshot.Fallback {
			if snapshot.ChannelID == "" && fallbackChannelID != "" {
				channelID = fallbackChannelID
			}
		}
		if strings.TrimSpace(snapshot.Error) != "" {
			if model.AutoDisableErrors > 0 {
				_ = store.RecordModelChannelResult(ctx, db, model.ID, channelID, "regular", "error", model.AutoDisableErrors, model.AutoDisableMinutes)
			}
			_ = store.RecordChannelFailure(ctx, db, channelID, "error")
			continue
		}
		_ = store.ResetChannelCounters(ctx, db, channelID)
		_ = store.ResetModelChannelCounters(ctx, db, model.ID, channelID, "regular")
	}
	if !ttftTimedOut {
		return
	}
	timeoutChannelID := model.ChannelID
	if timedOutIndex >= 0 {
		timedOut := snapshots[timedOutIndex]
		if timedOut.ChannelID != "" {
			timeoutChannelID = timedOut.ChannelID
		}
		if timedOut.Fallback && timedOut.ChannelID == "" && fallbackChannelID != "" {
			timeoutChannelID = fallbackChannelID
		}
	}
	if modelTTFTSeconds(db, model.ID) > 0 && model.AutoDisableTimeouts > 0 {
		_ = store.RecordModelChannelResult(ctx, db, model.ID, timeoutChannelID, "regular", "timeout", model.AutoDisableTimeouts, model.AutoDisableMinutes)
	}
	_ = store.RecordChannelFailure(ctx, db, timeoutChannelID, "timeout")
}
