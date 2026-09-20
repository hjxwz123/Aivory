package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"aivory/server/internal/store"
)

func normalizeDecisionModel(ctx context.Context, db *sql.DB, m *store.Model) error {
	channel, err := store.GetChannel(ctx, db, m.ChannelID)
	if err != nil {
		return err
	}
	if channel.Type != "typesafe" && m.Kind != "decision" {
		return nil
	}
	if channel.Type != "typesafe" {
		return errors.New("decision models require a typesafe channel")
	}
	if m.Kind != "decision" && m.Kind != "chat" && m.Kind != "" {
		return errors.New("typesafe channels support decision models only")
	}
	m.Kind = "decision"
	m.ToolMode = "none"
	m.Stream = false
	m.Vision = false
	m.Fast = false
	m.ResearchEnabled = false
	m.ResearchEnabledSet = true
	m.FallbackChannelID = ""
	m.ExtraParams = json.RawMessage("{}")
	m.OfficialTools = json.RawMessage("[]")
	m.PriceOutput = 0
	m.PriceCacheRead = 0
	m.PriceCacheWrite = 0
	m.PricePerImage = 0
	return nil
}

func normalizeDecisionPolicySetting(ctx context.Context, d Deps, raw json.RawMessage) (json.RawMessage, error) {
	var id string
	if json.Unmarshal(raw, &id) != nil {
		return nil, errInvalidInput
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return json.RawMessage(`""`), nil
	}
	m, err := store.GetModel(ctx, d.DB, id)
	if err != nil || !m.Enabled {
		return nil, errModelPolicyModelUnavailable
	}
	if m.Kind != "decision" {
		return normalizeAvailableChatModelSetting(ctx, d, raw)
	}
	c, err := store.GetChannel(ctx, d.DB, m.ChannelID)
	if err != nil || !c.Enabled || c.Type != "typesafe" || strings.TrimSpace(c.APIKey) == "" {
		return nil, errModelPolicyModelUnavailable
	}
	return json.Marshal(id)
}
