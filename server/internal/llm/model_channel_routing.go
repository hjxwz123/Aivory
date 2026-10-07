package llm

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"aivory/server/internal/store"
)

func selectRegularModelChannelID(ctx context.Context, db *sql.DB, model *store.Model) (string, error) {
	if model == nil {
		return "", sql.ErrNoRows
	}
	channelID, err := store.SelectModelChannelID(ctx, db, model.ID, model.RequestID, "regular", "")
	if err == nil {
		return channelID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	bindings, err := store.ListModelChannelBindings(ctx, db, model.ID, "regular")
	if err != nil {
		return "", err
	}
	if len(bindings) > 0 {
		if primary, primaryErr := store.GetChannel(ctx, db, model.ChannelID); primaryErr == nil {
			if !primary.Enabled {
				return "", errors.New("channel is disabled")
			}
			if primary.AutoDisabledUntil > time.Now().Unix() {
				return "", errors.New("channel is temporarily auto-disabled")
			}
		}
		return "", sql.ErrNoRows
	}
	channel, err := store.GetChannel(ctx, db, model.ChannelID)
	if err != nil {
		return "", err
	}
	if !channel.Enabled || channel.AutoDisabledUntil > time.Now().Unix() {
		return "", sql.ErrNoRows
	}
	return channel.ID, nil
}
