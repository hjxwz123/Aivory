package genstream

import (
	"encoding/json"
	"time"

	"aurelia/server/internal/cache"
	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/llm"
)

// TTL is env-overridable (see docs/config-reference.md); it falls back to the
// original 2h default when AURELIA_GENSTREAM_TTL is unset.
var TTL = envcfg.Dur("AURELIA_GENSTREAM_TTL", 2*time.Hour)

type Event struct {
	ID    string
	Value llm.SseEvent
}

func Key(messageID string) string {
	return "gen:" + messageID
}

func Topic(messageID string) string {
	return "gen:" + messageID + ":notify"
}

func Append(c cache.Cache, messageID string, ev llm.SseEvent) (string, bool) {
	if c == nil || messageID == "" {
		return "", false
	}
	if ev.MessageID == "" {
		ev.MessageID = messageID
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return "", false
	}
	id, ok := c.StreamAppend(Key(messageID), string(b), TTL)
	if !ok {
		return "", false
	}
	c.Publish(Topic(messageID), id)
	return id, true
}

func Read(c cache.Cache, messageID, afterID string, limit int) ([]Event, bool) {
	if c == nil || messageID == "" {
		return nil, false
	}
	rows, ok := c.StreamRead(Key(messageID), afterID, limit)
	if !ok {
		return nil, false
	}
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		var ev llm.SseEvent
		if json.Unmarshal([]byte(row.Value), &ev) != nil {
			continue
		}
		if ev.MessageID == "" {
			ev.MessageID = messageID
		}
		out = append(out, Event{ID: row.ID, Value: ev})
	}
	return out, true
}

func Terminal(ev llm.SseEvent) bool {
	switch ev.Type {
	case "done", "error":
		return true
	default:
		return false
	}
}
