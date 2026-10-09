package llm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"aivory/server/internal/store"
)

func selectRegularModelChannelID(ctx context.Context, db *sql.DB, model *store.Model) (string, error) {
	if model == nil {
		return "", sql.ErrNoRows
	}
	candidates, err := store.ModelChannelCandidateIDs(ctx, db, model.ID, model.RequestID)
	if err == nil && len(candidates) > 0 {
		return candidates[0], nil
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

func resolveModelChannelCandidates(ctx context.Context, db *sql.DB, model *store.Model, preferredID string) ([]ChannelCreds, error) {
	if model == nil {
		return nil, sql.ErrNoRows
	}
	ids, err := store.ModelChannelCandidateIDs(ctx, db, model.ID, model.RequestID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	legacyRouting := false
	legacyFallback := strings.TrimSpace(model.FallbackChannelID)
	if errors.Is(err, sql.ErrNoRows) || legacyFallback != "" {
		bindings, bindingErr := store.ListModelChannelBindings(ctx, db, model.ID, "")
		if bindingErr != nil {
			return nil, bindingErr
		}
		legacyRouting = len(bindings) == 0
		if errors.Is(err, sql.ErrNoRows) {
			if !legacyRouting {
				for _, binding := range bindings {
					if binding.ChannelID == model.ChannelID && !binding.ChannelEnabled {
						return nil, errors.New("channel is disabled")
					}
					if binding.ChannelID == model.ChannelID && binding.ChannelAutoDisabledUntil > time.Now().Unix() {
						return nil, errors.New("channel is temporarily auto-disabled")
					}
				}
				return nil, sql.ErrNoRows
			}
			ids = []string{model.ChannelID}
		}
		// A legacy field must never bypass an existing binding's quarantine or
		// removed capability. It only supplements a channel without a binding.
		bound := false
		for _, binding := range bindings {
			if binding.ChannelID == legacyFallback {
				bound = true
				break
			}
		}
		if legacyFallback != "" && legacyFallback != model.ChannelID && !bound {
			supported, supportErr := store.ChannelSupportsRequestID(ctx, db, legacyFallback, model.RequestID)
			if supportErr != nil {
				return nil, supportErr
			}
			if legacyRouting || supported {
				ids = append(ids, legacyFallback)
			}
		}
	}
	preferredID = strings.TrimSpace(preferredID)
	for i, id := range ids {
		if id == preferredID {
			ids[0], ids[i] = ids[i], ids[0]
			break
		}
	}
	out := make([]ChannelCreds, 0, len(ids))
	for _, id := range ids {
		channel, err := store.GetChannel(ctx, db, id)
		if err != nil || !channel.Enabled || channel.AutoDisabledUntil > time.Now().Unix() ||
			(strings.TrimSpace(channel.APIKey) == "" && !(legacyRouting && id == model.ChannelID)) {
			continue
		}
		out = append(out, ChannelCreds{ID: channel.ID, BaseURL: channel.BaseURL, APIKey: channel.APIKey, Headers: channel.Headers})
	}
	if len(out) == 0 {
		return nil, sql.ErrNoRows
	}
	return out, nil
}
