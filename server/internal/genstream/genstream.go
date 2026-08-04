package genstream

import (
	"encoding/json"
	"time"

	"aivory/server/internal/cache"

	"aivory/server/internal/llm"
)

// TTL is how long a per-message SSE event stream (gen:<id>) is retained (2h).
var TTL = 2 * time.Hour

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

func RevocationKey(messageID string) string {
	return "gen:" + messageID + ":revoked"
}

func RevocationTopic(messageID string) string {
	return "gen:" + messageID + ":revoked:notify"
}

func RevocationTTL() time.Duration {
	if TTL > 0 {
		return TTL + time.Minute
	}
	return 2*time.Hour + time.Minute
}

// Append returns appended=false, revoked=true when a durable message or
// workspace tombstone denied the event. Callers must not fall back to writing
// that event directly to a response.
func Append(c cache.Cache, messageID string, ev llm.SseEvent, additionalDenyKeys ...string) (id string, appended, revoked bool) {
	if c == nil || messageID == "" {
		return "", false, false
	}
	if ev.MessageID == "" {
		ev.MessageID = messageID
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return "", false, false
	}
	denyKeys := make([]string, 0, 1+len(additionalDenyKeys))
	denyKeys = append(denyKeys, RevocationKey(messageID))
	denyKeys = append(denyKeys, additionalDenyKeys...)
	id, allowed, ok := c.StreamAppendIfAllowed(Key(messageID), denyKeys, string(b), TTL)
	if !ok {
		return "", false, false
	}
	if !allowed {
		return "", false, true
	}
	c.Publish(Topic(messageID), id)
	return id, true, false
}

func Read(c cache.Cache, messageID, afterID string, limit int, additionalDenyKeys ...string) (events []Event, available, revoked bool) {
	if c == nil || messageID == "" {
		return nil, false, false
	}
	denyKeys := make([]string, 0, 1+len(additionalDenyKeys))
	denyKeys = append(denyKeys, RevocationKey(messageID))
	denyKeys = append(denyKeys, additionalDenyKeys...)
	rows, allowed, ok := c.StreamReadIfAllowed(Key(messageID), denyKeys, afterID, limit)
	if !ok {
		return nil, false, false
	}
	if !allowed {
		return nil, true, true
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
	return out, true, false
}

// Revoke permanently closes one generation stream for at least the stream's
// entire replay lifetime. The cache operation atomically installs the tombstone
// and deletes frames that were buffered before membership revocation.
func Revoke(c cache.Cache, messageID string) bool {
	if c == nil || messageID == "" {
		return false
	}
	return c.StreamRevoke(Key(messageID), RevocationKey(messageID), RevocationTTL())
}

func IsRevoked(c cache.Cache, messageID string) bool {
	if c == nil || messageID == "" {
		return true
	}
	_, revoked := c.Get(RevocationKey(messageID))
	return revoked
}

func Terminal(ev llm.SseEvent) bool {
	switch ev.Type {
	case "done", "error":
		return true
	default:
		return false
	}
}
