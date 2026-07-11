// Package vector is the pluggable similarity-search backend for RAG. In
// production it is backed by Qdrant (one collection per embedding dimension).
// When no QDRANT_URL is configured a Disabled store is used and the rag layer
// injects full in-scope document text instead of doing vector retrieval.
//
// The store is intentionally thin: it knows nothing about chunking, routing or
// fusion — it upserts vectors with a small payload, runs a filtered nearest-
// neighbour search, and deletes by document/kb/conversation when content is
// removed.
package vector

import "context"

// Payload is the metadata stored alongside each vector so a search hit can be
// rendered as a citation without a second database round-trip.
type Payload struct {
	ChunkID        string `json:"chunk_id"`
	DocumentID     string `json:"document_id"`
	KBID           string `json:"kb_id"`
	ConversationID string `json:"conversation_id"`
	ParentID       string `json:"parent_id"`
	ChunkType      string `json:"chunk_type"`
	Seq            int    `json:"seq"`
	Content        string `json:"content"`
	Filename       string `json:"filename"`
}

// Point is a vector plus its payload, keyed by the originating chunk id.
type Point struct {
	ChunkID string
	Vector  []float32
	Payload Payload
}

// Hit is one search result.
type Hit struct {
	Score   float32
	Payload Payload
}

// ChunkVectorStatus reports whether a chunk id exists in the vector backend and
// whether the stored point carries a non-empty vector. Admin maintenance uses
// this to detect partial/empty Qdrant restores without trusting search top-K.
type ChunkVectorStatus struct {
	Exists    bool
	HasVector bool
}

// Scope restricts a search to a conversation's visible chunks: any chunk whose
// kb_id is in KBIDs, OR whose conversation_id equals ConversationID.
type Scope struct {
	KBIDs          []string
	ConversationID string
}

// Store is the backend surface. dim selects the per-dimension collection.
type Store interface {
	// Enabled reports whether a real vector backend is wired. When false the
	// rag layer uses full-context injection instead.
	Enabled() bool
	// Upsert writes (or overwrites) the points for the given dimension.
	Upsert(ctx context.Context, dim int, points []Point) error
	// Search returns up to topK nearest neighbours within scope.
	Search(ctx context.Context, dim int, vector []float32, scope Scope, topK int) ([]Hit, error)
	// SearchKeyword returns up to topK keyword/text matches within scope. The
	// query is full-text scored against the chunks' content payload field —
	// this is the independent keyword leg of the §4.11-E hybrid retrieval.
	SearchKeyword(ctx context.Context, dim int, query string, scope Scope, topK int) ([]Hit, error)
	// ExistingChunkIDs returns the chunk ids present in the vector backend for
	// this dimension + scope. Retrieval uses it as a consistency guard because
	// chunks.content is the source of truth while Qdrant is only the search index.
	ExistingChunkIDs(ctx context.Context, dim int, scope Scope) (map[string]bool, error)
	// VectorChunkStatuses returns vector presence within a required scope. An
	// empty scope returns no rows, matching the other scoped query methods.
	VectorChunkStatuses(ctx context.Context, dim int, scope Scope) (map[string]ChunkVectorStatus, error)
	// AllVectorChunkStatuses returns vector presence for every Aivory point in a
	// dimension. This deliberately explicit operation is reserved for global
	// administrative maintenance.
	AllVectorChunkStatuses(ctx context.Context, dim int) (map[string]ChunkVectorStatus, error)
	// DeleteByDocument removes every point belonging to a document.
	DeleteByDocument(ctx context.Context, documentID string) error
	// DeleteByKB removes every point belonging to a knowledge base.
	DeleteByKB(ctx context.Context, kbID string) error
	// DeleteByConversation removes every point belonging to a conversation.
	DeleteByConversation(ctx context.Context, conversationID string) error
}

// Disabled is the no-op store used when no vector backend is configured.
type Disabled struct{}

// NewDisabled returns a store that reports Enabled()==false.
func NewDisabled() Store { return Disabled{} }

func (Disabled) Enabled() bool                              { return false }
func (Disabled) Upsert(context.Context, int, []Point) error { return nil }
func (Disabled) Search(context.Context, int, []float32, Scope, int) ([]Hit, error) {
	return nil, nil
}
func (Disabled) SearchKeyword(context.Context, int, string, Scope, int) ([]Hit, error) {
	return nil, nil
}
func (Disabled) ExistingChunkIDs(context.Context, int, Scope) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (Disabled) VectorChunkStatuses(context.Context, int, Scope) (map[string]ChunkVectorStatus, error) {
	return map[string]ChunkVectorStatus{}, nil
}
func (Disabled) AllVectorChunkStatuses(context.Context, int) (map[string]ChunkVectorStatus, error) {
	return map[string]ChunkVectorStatus{}, nil
}
func (Disabled) DeleteByDocument(context.Context, string) error     { return nil }
func (Disabled) DeleteByKB(context.Context, string) error           { return nil }
func (Disabled) DeleteByConversation(context.Context, string) error { return nil }
