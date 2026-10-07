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

// recordBackgroundProviderHealth tracks channel and model-binding health for
// provider calls made outside the main chat turn (tasks and private chat).
func recordBackgroundProviderHealth(ctx context.Context, db *sql.DB, model *store.Model, snapshots []providerRequestSnapshot, fallbackChannelID string, ttftTimedOut bool) {
	if model == nil || db == nil || ctx == nil {
		return
	}
	for index, snapshot := range snapshots {
		if ttftTimedOut && index == 0 {
			continue
		}
		channelID := snapshot.ChannelID
		if channelID == "" {
			channelID = model.ChannelID
		}
		role := "regular"
		if snapshot.Fallback {
			role = "fallback"
			if snapshot.ChannelID == "" && fallbackChannelID != "" {
				channelID = fallbackChannelID
			}
		}
		if strings.TrimSpace(snapshot.Error) != "" {
			if model.AutoDisableErrors > 0 {
				_ = store.RecordModelChannelResult(ctx, db, model.ID, channelID, role, "error", model.AutoDisableErrors, model.AutoDisableMinutes)
			}
			_ = store.RecordChannelFailure(ctx, db, channelID, "error")
			continue
		}
		_ = store.ResetChannelCounters(ctx, db, channelID)
		_ = store.ResetModelChannelCounters(ctx, db, model.ID, channelID, role)
	}
	if !ttftTimedOut {
		return
	}
	timeoutChannelID, timeoutRole := model.ChannelID, "regular"
	if len(snapshots) > 0 {
		first := snapshots[0]
		if first.ChannelID != "" {
			timeoutChannelID = first.ChannelID
		}
		if first.Fallback {
			timeoutRole = "fallback"
			if first.ChannelID == "" && fallbackChannelID != "" {
				timeoutChannelID = fallbackChannelID
			}
		}
	}
	if model.FallbackTTFTSec > 0 && model.AutoDisableTimeouts > 0 {
		_ = store.RecordModelChannelResult(ctx, db, model.ID, timeoutChannelID, timeoutRole, "timeout", model.AutoDisableTimeouts, model.AutoDisableMinutes)
	}
	_ = store.RecordChannelFailure(ctx, db, timeoutChannelID, "timeout")
}
