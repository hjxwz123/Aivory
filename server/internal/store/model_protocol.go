package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The model owns the wire protocol; channels only provide transport credentials.
const legacyModelProtocolSQL = `CASE
	WHEN kind='embedding' THEN CASE WHEN (SELECT rtrim(base_url,'/') FROM channels WHERE id=models.channel_id) LIKE '%/api/v1' OR (SELECT base_url FROM channels WHERE id=models.channel_id) LIKE '%/services/embeddings/%' THEN 'dashscope.embeddings' ELSE 'openai.embeddings' END
	WHEN kind='decision' THEN 'typesafe.decisions'
	WHEN kind='image' THEN CASE WHEN (SELECT lower(trim(type)) FROM channels WHERE id=models.channel_id) IN ('google','gemini') THEN 'gemini.generateContent' ELSE 'openai.images' END
	WHEN (SELECT lower(trim(type)) FROM channels WHERE id=models.channel_id) IN ('anthropic','claude') THEN 'anthropic.messages'
	WHEN (SELECT lower(trim(type)) FROM channels WHERE id=models.channel_id) IN ('google','gemini') THEN 'gemini.generateContent'
	WHEN (SELECT lower(trim(type)) FROM channels WHERE id=models.channel_id)='typesafe' THEN 'typesafe.decisions'
	WHEN (SELECT lower(trim(api_format)) FROM channels WHERE id=models.channel_id)='responses' THEN 'openai.responses'
	ELSE 'openai.chat' END`

const modelProtocolSelect = `COALESCE(NULLIF(protocol,''), ` + legacyModelProtocolSQL + `)`

func BackfillModelProtocols(ctx context.Context, db RowExecer) error {
	_, err := db.ExecContext(ctx, `UPDATE models SET protocol=`+legacyModelProtocolSQL+` WHERE protocol=''`)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE models SET kind='decision', tool_mode='none', stream=0, vision=0, fast=0, research_enabled=0, extra_params='{}', official_tools='[]' WHERE protocol='typesafe.decisions' AND kind='chat'`)
	return err
}

func LegacyModelProtocol(kind string, channel *Channel) string {
	if kind == "embedding" {
		if channel != nil {
			base := strings.TrimRight(channel.BaseURL, "/")
			if strings.HasSuffix(base, "/api/v1") || strings.Contains(base, "/services/embeddings/") {
				return "dashscope.embeddings"
			}
		}
		return "openai.embeddings"
	}
	if kind == "decision" {
		return "typesafe.decisions"
	}
	if kind == "image" {
		if channel != nil && (channel.Type == "google" || channel.Type == "gemini") {
			return "gemini.generateContent"
		}
		return "openai.images"
	}
	if channel != nil {
		switch strings.ToLower(strings.TrimSpace(channel.Type)) {
		case "typesafe":
			return "typesafe.decisions"
		case "google", "gemini":
			return "gemini.generateContent"
		case "claude", "anthropic":
			return "anthropic.messages"
		}
	}
	if channel != nil && channel.APIFormat == "responses" {
		return "openai.responses"
	}
	return "openai.chat"
}

func ValidateModelProtocol(m *Model) error {
	m.Protocol = strings.TrimSpace(m.Protocol)
	valid := false
	switch m.Protocol {
	case "openai.chat", "openai.responses", "anthropic.messages":
		valid = m.Kind == "chat"
	case "gemini.generateContent":
		valid = m.Kind == "chat" || m.Kind == "image"
	case "openai.images":
		valid = m.Kind == "image"
	case "openai.embeddings", "dashscope.embeddings":
		valid = m.Kind == "embedding"
	case "typesafe.decisions":
		valid = m.Kind == "decision"
	}
	if !valid {
		return fmt.Errorf("invalid model protocol %q for kind %q", m.Protocol, m.Kind)
	}
	return nil
}

func NormalizeModelProtocol(ctx context.Context, db *sql.DB, m *Model) error {
	m.Kind = strings.ToLower(strings.TrimSpace(m.Kind))
	if m.Kind == "" {
		m.Kind = "chat"
	}
	if strings.TrimSpace(m.Protocol) == "" {
		channel, err := GetChannel(ctx, db, m.ChannelID)
		if err != nil {
			return err
		}
		m.Protocol = LegacyModelProtocol(m.Kind, channel)
		if m.Protocol == "typesafe.decisions" {
			m.Kind = "decision"
		}
	}
	return ValidateModelProtocol(m)
}

// ChannelForModel returns a request-local copy. Persisted legacy channel types
// remain readable by older installations, but never override an explicit model protocol.
func ChannelForModel(m *Model, channel *Channel) *Channel {
	if channel == nil || m == nil || m.Protocol == "" {
		return channel
	}
	resolved := *channel
	resolved.APIFormat = ""
	switch m.Protocol {
	case "openai.chat":
		resolved.Type, resolved.APIFormat = "openai", "chat"
	case "openai.responses":
		resolved.Type, resolved.APIFormat = "openai", "responses"
	case "openai.images", "openai.embeddings":
		resolved.Type = "openai"
	case "dashscope.embeddings":
		resolved.Type = "dashscope"
	case "anthropic.messages":
		resolved.Type = "anthropic"
	case "gemini.generateContent":
		resolved.Type = "google"
	case "typesafe.decisions":
		resolved.Type = "typesafe"
	default:
		resolved.Type = ""
	}
	return &resolved
}

func GetModelChannel(ctx context.Context, db *sql.DB, model *Model, channelID string) (*Channel, error) {
	channel, err := GetChannel(ctx, db, channelID)
	if err != nil {
		return nil, err
	}
	return ChannelForModel(model, channel), nil
}
