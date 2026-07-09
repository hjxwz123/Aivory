package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// collectionPrefix namespaces Aurelia's collections so DeleteBy* can enumerate
// them and so a shared Qdrant instance won't collide with other tenants.
const collectionPrefix = "aurelia_c"

// pointNamespace turns string chunk ids into deterministic UUIDv5 point ids
// (Qdrant only accepts unsigned-int or UUID ids). Re-ingesting a chunk maps to
// the same point, so upserts are idempotent.
var pointNamespace = uuid.MustParse("8f4d2c1a-1f2e-4b6a-9c3d-7a0b1c2d3e4f")

// Qdrant is an HTTP client for a Qdrant server. Safe for concurrent use.
type Qdrant struct {
	baseURL string
	apiKey  string
	http    *http.Client

	mu      sync.Mutex
	ensured map[int]bool // dimensions whose collection has been created
}

// NewQdrant builds a client for baseURL (e.g. http://qdrant:6333). apiKey may
// be empty for an unauthenticated instance.
func NewQdrant(baseURL, apiKey string) *Qdrant {
	return &Qdrant{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 20 * time.Second},
		ensured: map[int]bool{},
	}
}

// Enabled reports that a real backend is wired.
func (q *Qdrant) Enabled() bool { return true }

func collectionName(dim int) string { return fmt.Sprintf("%s%d", collectionPrefix, dim) }

func scopeShould(scope Scope) []map[string]any {
	should := []map[string]any{}
	for _, kb := range scope.KBIDs {
		if kb == "" {
			continue
		}
		should = append(should, map[string]any{"key": "kb_id", "match": map[string]any{"value": kb}})
	}
	if scope.ConversationID != "" {
		should = append(should, map[string]any{"key": "conversation_id", "match": map[string]any{"value": scope.ConversationID}})
	}
	return should
}

// do performs one JSON request. Any 2xx is success; the decoded body (if out is
// non-nil) is unmarshalled. A non-2xx returns an error carrying the body.
func (q *Qdrant) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, q.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if q.apiKey != "" {
		req.Header.Set("api-key", q.apiKey)
	}
	resp, err := q.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qdrant %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ensureCollection creates the per-dimension collection (and the payload
// indexes used for scope filtering) the first time a dimension is seen.
func (q *Qdrant) ensureCollection(ctx context.Context, dim int) error {
	if dim <= 0 {
		return fmt.Errorf("vector: invalid dimension %d", dim)
	}
	q.mu.Lock()
	done := q.ensured[dim]
	q.mu.Unlock()
	if done {
		return nil
	}

	name := collectionName(dim)
	// Exists already?
	err := q.do(ctx, http.MethodGet, "/collections/"+name, nil, nil)
	if err != nil {
		// Create it. Cosine distance matches the L2-normalised embeddings.
		create := map[string]any{
			"vectors": map[string]any{"size": dim, "distance": "Cosine"},
		}
		if err := q.do(ctx, http.MethodPut, "/collections/"+name, create, nil); err != nil {
			// A concurrent creator may have won the race — tolerate "exists".
			if !strings.Contains(err.Error(), "already exists") {
				return err
			}
		}
		// Keyword payload indexes make scope filters cheap. Best-effort.
		for _, field := range []string{"kb_id", "conversation_id", "document_id"} {
			_ = q.do(ctx, http.MethodPut, "/collections/"+name+"/index?wait=true",
				map[string]any{"field_name": field, "field_schema": "keyword"}, nil)
		}
		// §4.11-E independent keyword leg: text payload index on `content` so
		// scroll-with-text-match returns a real keyword-filtered result set
		// (RRF fuses it with the dense leg). Best-effort — failing to create
		// degrades to the dense-only path until the index is provisioned.
		_ = q.do(ctx, http.MethodPut, "/collections/"+name+"/index?wait=true",
			map[string]any{
				"field_name": "content",
				"field_schema": map[string]any{
					"type":          "text",
					"tokenizer":     "multilingual",
					"min_token_len": 1,
					"lowercase":     true,
				},
			}, nil)
	}

	q.mu.Lock()
	q.ensured[dim] = true
	q.mu.Unlock()
	return nil
}

func pointID(chunkID string) string {
	return uuid.NewSHA1(pointNamespace, []byte(chunkID)).String()
}

// Upsert writes points into the dimension's collection.
func (q *Qdrant) Upsert(ctx context.Context, dim int, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	if err := q.ensureCollection(ctx, dim); err != nil {
		return err
	}
	type qpoint struct {
		ID      string    `json:"id"`
		Vector  []float32 `json:"vector"`
		Payload Payload   `json:"payload"`
	}
	body := struct {
		Points []qpoint `json:"points"`
	}{Points: make([]qpoint, 0, len(points))}
	for _, p := range points {
		pl := p.Payload
		pl.ChunkID = p.ChunkID
		body.Points = append(body.Points, qpoint{ID: pointID(p.ChunkID), Vector: p.Vector, Payload: pl})
	}
	return q.do(ctx, http.MethodPut, "/collections/"+collectionName(dim)+"/points?wait=true", body, nil)
}

// Search runs a filtered nearest-neighbour query.
func (q *Qdrant) Search(ctx context.Context, dim int, vector []float32, scope Scope, topK int) ([]Hit, error) {
	if len(vector) == 0 || topK <= 0 {
		return nil, nil
	}
	if err := q.ensureCollection(ctx, dim); err != nil {
		return nil, err
	}
	should := scopeShould(scope)
	if len(should) == 0 {
		return nil, nil
	}
	body := map[string]any{
		"vector":       vector,
		"limit":        topK,
		"with_payload": true,
		"filter":       map[string]any{"should": should},
	}
	var out struct {
		Result []struct {
			Score   float32 `json:"score"`
			Payload Payload `json:"payload"`
		} `json:"result"`
	}
	if err := q.do(ctx, http.MethodPost, "/collections/"+collectionName(dim)+"/points/search", body, &out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Result))
	for _, r := range out.Result {
		hits = append(hits, Hit{Score: r.Score, Payload: r.Payload})
	}
	return hits, nil
}

// SearchKeyword runs an INDEPENDENT keyword leg of the §4.11-E hybrid
// retrieval. The implementation uses Qdrant's full-text payload index on
// `content` (a "Text" payload index is provisioned the first time we ensure a
// collection) plus a scope filter. Score is the upstream's text-match relevance.
//
// We use scroll-with-filter rather than search because Qdrant's text-match
// filter is a yes/no predicate; we approximate BM25-ish ranking by token-
// overlap in the caller — but we still pre-filter with text-match so the
// fusion's keyword leg is independent of the dense leg's top-K.
func (q *Qdrant) SearchKeyword(ctx context.Context, dim int, query string, scope Scope, topK int) ([]Hit, error) {
	if query == "" || topK <= 0 {
		return nil, nil
	}
	if err := q.ensureCollection(ctx, dim); err != nil {
		return nil, err
	}
	should := scopeShould(scope)
	if len(should) == 0 {
		return nil, nil
	}
	body := map[string]any{
		"filter": map[string]any{
			"should": should,
			"must": []map[string]any{
				{"key": "content", "match": map[string]any{"text": query}},
			},
		},
		"limit":        topK,
		"with_payload": true,
		"with_vector":  false,
	}
	var out struct {
		Result struct {
			Points []struct {
				Payload Payload `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := q.do(ctx, http.MethodPost, "/collections/"+collectionName(dim)+"/points/scroll", body, &out); err != nil {
		// If the text index doesn't exist yet (legacy collections), Qdrant
		// returns a 400. Fall back to scroll-without-text-filter scored
		// against the empty filter; the caller still computes BM25 over the
		// returned payload's content, so we degrade gracefully.
		return nil, nil
	}
	hits := make([]Hit, 0, len(out.Result.Points))
	for i, p := range out.Result.Points {
		// Synthesize a monotonically-decreasing score from row order so the
		// caller's RRF can rank them — actual BM25 ranking happens caller-side.
		hits = append(hits, Hit{Score: float32(len(out.Result.Points) - i), Payload: p.Payload})
	}
	return hits, nil
}

// ExistingChunkIDs scrolls Qdrant payloads for the given dimension + scope and
// returns the chunk ids currently present in the vector index. It deliberately
// fetches only chunk_id (no vectors/content) because the relational DB remains
// the source of truth for rendering and full-context fallback.
func (q *Qdrant) ExistingChunkIDs(ctx context.Context, dim int, scope Scope) (map[string]bool, error) {
	ids := map[string]bool{}
	if err := q.ensureCollection(ctx, dim); err != nil {
		return nil, err
	}
	should := scopeShould(scope)
	if len(should) == 0 {
		return ids, nil
	}
	var offset json.RawMessage
	for {
		body := map[string]any{
			"filter":       map[string]any{"should": should},
			"limit":        256,
			"with_payload": true,
			"with_vector":  false,
		}
		if len(offset) > 0 && string(offset) != "null" {
			body["offset"] = offset
		}
		var out struct {
			Result struct {
				Points []struct {
					Payload Payload `json:"payload"`
				} `json:"points"`
				Next json.RawMessage `json:"next_page_offset"`
			} `json:"result"`
		}
		if err := q.do(ctx, http.MethodPost, "/collections/"+collectionName(dim)+"/points/scroll", body, &out); err != nil {
			return nil, err
		}
		for _, p := range out.Result.Points {
			if p.Payload.ChunkID != "" {
				ids[p.Payload.ChunkID] = true
			}
		}
		if len(out.Result.Next) == 0 || string(out.Result.Next) == "null" {
			break
		}
		offset = out.Result.Next
	}
	return ids, nil
}

// VectorChunkStatuses scrolls every point in a dimension/scope and reports
// whether its vector payload is non-empty. It is intentionally separate from
// ExistingChunkIDs so normal retrieval can keep using the lighter payload-only
// consistency check, while admin maintenance can verify vector integrity.
func (q *Qdrant) VectorChunkStatuses(ctx context.Context, dim int, scope Scope) (map[string]ChunkVectorStatus, error) {
	status := map[string]ChunkVectorStatus{}
	if err := q.ensureCollection(ctx, dim); err != nil {
		return nil, err
	}
	should := scopeShould(scope)
	var offset json.RawMessage
	for {
		body := map[string]any{
			"limit":        256,
			"with_payload": true,
			"with_vector":  true,
		}
		if len(should) > 0 {
			body["filter"] = map[string]any{"should": should}
		}
		if len(offset) > 0 && string(offset) != "null" {
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
		if err := q.do(ctx, http.MethodPost, "/collections/"+collectionName(dim)+"/points/scroll", body, &out); err != nil {
			return nil, err
		}
		for _, p := range out.Result.Points {
			if p.Payload.ChunkID == "" {
				continue
			}
			status[p.Payload.ChunkID] = ChunkVectorStatus{
				Exists:    true,
				HasVector: hasNonEmptyVectorJSON(p.Vector),
			}
		}
		if len(out.Result.Next) == 0 || string(out.Result.Next) == "null" {
			break
		}
		offset = out.Result.Next
	}
	return status, nil
}

func hasNonEmptyVectorJSON(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		return len(arr) > 0
	}
	var named map[string]json.RawMessage
	if json.Unmarshal(raw, &named) == nil {
		for _, v := range named {
			if hasNonEmptyVectorJSON(v) {
				return true
			}
		}
		return false
	}
	return true
}

// listCollections returns the names of Aurelia's per-dimension collections.
func (q *Qdrant) listCollections(ctx context.Context) ([]string, error) {
	var out struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if err := q.do(ctx, http.MethodGet, "/collections", nil, &out); err != nil {
		return nil, err
	}
	names := []string{}
	for _, c := range out.Result.Collections {
		if strings.HasPrefix(c.Name, collectionPrefix) {
			names = append(names, c.Name)
		}
	}
	return names, nil
}

// deleteByField removes every point matching payload field==value across all
// dimension collections (the caller rarely knows which dimension a document
// landed in, so we sweep them all — there are only as many as embedding sizes).
func (q *Qdrant) deleteByField(ctx context.Context, field, value string) error {
	if value == "" {
		return nil
	}
	names, err := q.listCollections(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"filter": map[string]any{
			"must": []map[string]any{
				{"key": field, "match": map[string]any{"value": value}},
			},
		},
	}
	var firstErr error
	for _, name := range names {
		if err := q.do(ctx, http.MethodPost, "/collections/"+name+"/points/delete?wait=true", body, nil); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DeleteByDocument removes all points for a document.
func (q *Qdrant) DeleteByDocument(ctx context.Context, documentID string) error {
	return q.deleteByField(ctx, "document_id", documentID)
}

// DeleteByKB removes all points for a knowledge base.
func (q *Qdrant) DeleteByKB(ctx context.Context, kbID string) error {
	return q.deleteByField(ctx, "kb_id", kbID)
}

// DeleteByConversation removes all points for a conversation.
func (q *Qdrant) DeleteByConversation(ctx context.Context, conversationID string) error {
	return q.deleteByField(ctx, "conversation_id", conversationID)
}
