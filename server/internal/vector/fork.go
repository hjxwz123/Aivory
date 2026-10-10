package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// CopyConversationPoints copies existing vectors without calling an embedding
// model or charging again. Only remapped chunks from the selected branch move.
func (q *Qdrant) CopyConversationPoints(ctx context.Context, sourceID string, remap map[string]string) error {
	qdrantArchiveGate.RLock()
	defer qdrantArchiveGate.RUnlock()
	return q.copyConversationPoints(ctx, sourceID, remap)
}

// Acquire the archive gate before the caller opens a SQL transaction. Backup
// restoration uses archive -> SQL order, so acquiring it inside SQL can deadlock.
func (q *Qdrant) PrepareConversationFork() (func(context.Context, string, map[string]string) error, func()) {
	qdrantArchiveGate.RLock()
	var once sync.Once
	return q.copyConversationPoints, func() { once.Do(qdrantArchiveGate.RUnlock) }
}

func (q *Qdrant) copyConversationPoints(ctx context.Context, sourceID string, remap map[string]string) error {
	names, err := q.listCollections(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		var offset json.RawMessage
		for {
			body := map[string]any{"limit": 128, "with_payload": true, "with_vector": true,
				"filter": map[string]any{"must": []map[string]any{{"key": "conversation_id", "match": map[string]any{"value": sourceID}}}}}
			if len(offset) > 0 {
				body["offset"] = offset
			}
			var out struct {
				Result struct {
					Points []struct {
						Payload Payload         `json:"payload"`
						Vector  json.RawMessage `json:"vector"`
					} `json:"points"`
					Next json.RawMessage `json:"next_page_offset"`
				} `json:"result"`
			}
			if err := q.do(ctx, http.MethodPost, "/collections/"+name+"/points/scroll", body, &out); err != nil {
				return err
			}
			points := []map[string]any{}
			for _, point := range out.Result.Points {
				newID, ok := remap[point.Payload.ChunkID]
				if !ok {
					continue
				}
				payload := point.Payload
				payload.ChunkID = newID
				payload.DocumentID = remap[payload.DocumentID]
				payload.ConversationID = remap[sourceID]
				payload.ParentID = remap[payload.ParentID]
				points = append(points, map[string]any{"id": pointID(newID), "vector": point.Vector, "payload": payload})
			}
			if len(points) > 0 {
				if err := q.do(ctx, http.MethodPut, "/collections/"+name+"/points?wait=true", map[string]any{"points": points}, nil); err != nil {
					return err
				}
			}
			if len(out.Result.Next) == 0 || string(out.Result.Next) == "null" {
				break
			}
			offset = out.Result.Next
		}
	}
	return nil
}

// SQLite vectors were already copied under the resource transaction.
func (*SQLite) CopyConversationPoints(context.Context, string, map[string]string) error { return nil }
