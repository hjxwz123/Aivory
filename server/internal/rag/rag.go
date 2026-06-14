// Package rag implements the simplified document parse/chunk/embed/retrieve
// pipeline described in design.md §4.11. It uses the embedded SQLite store
// and a hash-bag local embedding so the system is fully functional without
// external services. The Embedder interface (and the *Service abstraction)
// make a drop-in replacement trivial — pass a real OpenAI/Voyage embedder
// and nothing else changes.
package rag

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"aurelia/server/internal/queue"
	"aurelia/server/internal/storage"
	"aurelia/server/internal/store"
	"aurelia/server/internal/vector"
)

// Service is the public façade.
type Service struct {
	db     *sql.DB
	queue  queue.Queue
	logger *log.Logger
	task   TaskRouter
	vec    vector.Store
	// External integration config (§4.11-C/D): embedding HTTP backend + MinerU.
	// All values are env-fallbacks — runtime resolution prefers the admin
	// settings table so the live admin UI controls them without a restart.
	embBaseURL string
	embAPIKey  string
	embModel   string
	embDim     int
	mineruURL  string
	mineruKey  string
	// Sandbox sidecar URL/key — needed to talk to /storage/put + /storage/delete
	// for MinerU uploads. Fallback values from env; runtime reads `sandbox_*`
	// settings each ingest.
	sandboxURL string
	sandboxKey string
}

// SetExternalConfig wires the optional embedding HTTP backend + MinerU parser.
// Called by main() after construction. All values may be empty (dev fallback).
// The MinerU URL/token here are env-fallbacks; admin settings take precedence
// at ingest time so the live UI works without a restart.
func (s *Service) SetExternalConfig(embBaseURL, embAPIKey, embModel string, embDim int, mineruURL, mineruKey string) {
	s.embBaseURL, s.embAPIKey, s.embModel, s.embDim = embBaseURL, embAPIKey, embModel, embDim
	s.mineruURL, s.mineruKey = mineruURL, mineruKey
}

// SetSandboxFallback stashes the sandbox sidecar URL/key the env supplied at
// boot. Runtime reads the `sandbox_base_url` / `sandbox_api_key` settings
// first; these are the dev fallback so `MINERU` works in a freshly seeded
// install with no admin clicks.
func (s *Service) SetSandboxFallback(url, key string) {
	s.sandboxURL, s.sandboxKey = url, key
}

// TaskRouter is the subset of llm.TaskLLM the RAG service needs (kept as an
// interface to break the import cycle).
type TaskRouter interface {
	RunJSON(ctx context.Context, kind string, prompt string, out any, opts RouterOpts) error
}

// RouterOpts mirrors llm.RunOpts but in this package's vocabulary.
type RouterOpts struct {
	UserID         string
	ConversationID string
}

// New builds the service. The vector backend defaults to Disabled (brute-force
// over Postgres); call SetVectorStore to wire Qdrant.
func New(db *sql.DB, q queue.Queue, logger *log.Logger) *Service {
	return &Service{db: db, queue: q, logger: logger, vec: vector.NewDisabled()}
}

// SetVectorStore wires the similarity-search backend (Qdrant in production).
// Called by main() after construction.
func (s *Service) SetVectorStore(v vector.Store) {
	if v != nil {
		s.vec = v
	}
}

// SetTaskLLM is called by main() after the task helper exists. We accept any
// implementation of TaskRouter to avoid an import cycle.
func (s *Service) SetTaskLLM(t TaskRouter) { s.task = t }

// Embedder is the pluggable embedding backend. The local hash-bag embedder
// satisfies it for development and ensures search is always available; admins
// who configure a real channel of `kind=embedding` can wire in an HTTP-based
// embedder via the orchestrator.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Ingest enqueues the parse/chunk/embed pipeline for a freshly created
// document. Failures retry up to 3 times before the document is marked
// RequeueIncomplete re-enqueues documents left in a non-terminal state
// (pending/parsing/embedding) by a crash or restart — the in-memory queue
// doesn't survive a restart, so without this a doc would poll "indexing…"
// forever. Best-effort; call once at boot.
func (s *Service) RequeueIncomplete(ctx context.Context) {
	docs, err := store.ListIncompleteDocuments(ctx, s.db)
	if err != nil {
		s.logger.Printf("rag: requeue scan failed: %v", err)
		return
	}
	for _, d := range docs {
		s.logger.Printf("rag: requeueing stuck document %s (was %s)", d.ID, d.Status)
		s.Ingest(d.ID)
	}
}

// `failed` with the last error (§4.11-C-3). The pipeline is idempotent —
// repeat calls re-write existing chunks.
func (s *Service) Ingest(docID string) {
	s.queue.Enqueue("rag.ingest", func(ctx context.Context) error {
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			if err = s.runPipeline(ctx, docID); err == nil {
				return nil
			}
			s.logger.Printf("rag: ingest %s attempt %d/3 failed: %v", docID, attempt, err)
			// Back off between whole-pipeline retries so a transient upstream
			// outage (e.g. embeddings TLS timeout) gets a chance to recover
			// instead of being hammered three times in a row.
			if attempt < 3 {
				timer := time.NewTimer(time.Duration(attempt) * 3 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		_ = store.UpdateDocumentStatus(ctx, s.db, docID, "failed", err.Error(), 0)
		return err
	})
}

// sanitizeIngestText removes NUL bytes and invalid UTF-8 from parsed document
// text. Postgres TEXT columns reject these (SQLSTATE 22021 "invalid byte
// sequence for encoding UTF8: 0x00"), which otherwise fails the whole ingest.
func sanitizeIngestText(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "")
}

func (s *Service) runPipeline(ctx context.Context, docID string) error {
	d, err := store.GetDocument(ctx, s.db, docID)
	if err != nil {
		return err
	}
	_ = store.UpdateDocumentStatus(ctx, s.db, docID, "parsing", "", 0)

	// Resolve MinerU + sandbox + storage from admin settings (live), falling
	// back to env values supplied at boot. The MinerU URL/token come from
	// the settings keys `mineru_api_url` / `mineru_api_token`; the sandbox
	// sidecar (which hosts /storage/put for the upload-then-fetch flow)
	// comes from `sandbox_base_url` / `sandbox_api_key`; the bucket comes
	// from the `storage_*` block. Any of these can be blank — the parser
	// degrades gracefully (binary docs become a one-line placeholder).
	mineruURL := readSettingString(s.db, "mineru_api_url", s.mineruURL)
	mineruKey := readSettingString(s.db, "mineru_api_token", s.mineruKey)
	// Blank admin setting → fall back to the env/boot default (bundled sandbox).
	sbURL := readSettingString(s.db, "sandbox_base_url", s.sandboxURL)
	if strings.TrimSpace(sbURL) == "" {
		sbURL = s.sandboxURL
	}
	sbKey := readSettingString(s.db, "sandbox_api_key", s.sandboxKey)
	if strings.TrimSpace(sbKey) == "" {
		sbKey = s.sandboxKey
	}
	storageCfg := storageBlockFromSettings(s.db)
	storageClient := storage.New(sbURL, sbKey, storageCfg)

	// Spreadsheets are data, not prose: never parse or embed them. They stay as
	// conversation files and are analysed in the code sandbox (python_execute
	// stages them to /workspace/uploads). Mark ready with zero chunks so the
	// ingest pipeline completes cleanly instead of vectorising rows of numbers.
	if isSpreadsheetData(d.Filename, d.MimeType) {
		return store.UpdateDocumentStatus(ctx, s.db, docID, "ready", "", 0)
	}

	// Parse: text docs + text-native PDF/DOC(X)/PPT(X) locally; only scanned or
	// image-bearing documents go to MinerU OCR (§4.11-C). parseDocument makes the
	// per-document call from the file's content.
	content, err := parseDocument(ctx, d.StoragePath, d.MimeType, d.Filename, mineruURL, mineruKey, storageClient)
	if err != nil {
		return err
	}
	// Strip NUL / invalid UTF-8 at the source: parsed binary docs (docx/pdf/ppt)
	// carry bytes Postgres TEXT columns reject (SQLSTATE 22021). This guarantees
	// every downstream write (chunks, parents) is clean regardless of insert path.
	content = sanitizeIngestText(content)

	// Chunk hierarchically (§4.11-C-2 small-to-big): parents carry section
	// context (not embedded), children carry the vectors. The new chunker also
	// returns image_caption rows for MinerU-extracted images (§4.11-C-1).
	parents := chunkHierarchical(content)
	_ = store.UpdateDocumentStatus(ctx, s.db, docID, "embedding", "", 0)

	// §4.11-B2 lock: ingest into a KB MUST use the KB's locked embedding model;
	// global setting changes never re-route an existing KB's vectors. For pure
	// conversation-scoped docs (no KB), fall through to the global resolver.
	var (
		em     Embedder
		emName string
		dim    int
	)
	if d.KBID != "" {
		em, emName, dim, err = s.resolveEmbedderForKB(ctx, d.KBID)
		if err != nil {
			return err
		}
	} else {
		em, emName, dim = s.resolveEmbedder(ctx)
	}
	// Re-ingest is idempotent at the row level (chunks are rewritten); clear any
	// previous vectors for this document so stale points don't linger in Qdrant.
	if s.vec.Enabled() {
		if err := s.vec.DeleteByDocument(ctx, docID); err != nil {
			s.logger.Printf("rag: clear old vectors for %s: %v", docID, err)
		}
	}
	written := 0
	totalTokens := 0
	seq := 0
	points := []vector.Point{}
	for _, p := range parents {
		parentID := ""
		if len(parents) > 1 || len(p.Children) > 1 {
			parentID, err = store.CreateChunkFull(ctx, s.db, store.ChunkInsert{
				DocumentID: docID, KBID: d.KBID, ConversationID: d.ConversationID,
				Seq: seq, ChunkType: "parent", Content: p.Content, EmbeddingModel: emName,
			})
			if err != nil {
				return err
			}
			seq++
		}
		vecs, err := em.Embed(ctx, p.Children)
		if err != nil {
			return err
		}
		// Reconcile the collection dimension with what the model ACTUALLY
		// returned. The configured dim is only a hint — some endpoints ignore
		// it and emit their native width (e.g. configured 1536 but the model
		// returns 1024). Writing wrong-dim vectors makes Qdrant reject the whole
		// upsert ("Vector dimension error"). Trust the vectors over the config so
		// ingest just works regardless of misconfiguration.
		if len(vecs) > 0 && len(vecs[0]) > 0 && len(vecs[0]) != dim {
			actual := len(vecs[0])
			s.logger.Printf("rag: embedding model returned dim %d but KB/config expected %d — using %d (doc %s)", actual, dim, actual, docID)
			dim = actual
			if d.KBID != "" {
				if err := store.SetKBEmbeddingDim(ctx, s.db, d.KBID, actual); err != nil {
					s.logger.Printf("rag: persist corrected embedding_dim for kb %s: %v", d.KBID, err)
				}
			}
		}
		for i, child := range p.Children {
			// Classify image_caption strictly: a child chunk must be EXACTLY one
			// `![…](mineru://…)` marker (optionally preceded by the page-number
			// HTML comment we emit in parser.go). Anything mixed with prose stays
			// `chunkType=text` so we don't collapse the prose under an image_ref,
			// and so a child that happens to embed two image markers isn't tagged
			// against just the first one's filename.
			chunkType := "text"
			imageRef := ""
			if ref, ok := soleMineruImageMarker(child); ok {
				chunkType = "image_caption"
				imageRef = ref
			}
			// Keep the vector in Postgres too: it's the brute-force fallback when
			// Qdrant is disabled, and cheap insurance otherwise.
			chunkID, err := store.CreateChunkFull(ctx, s.db, store.ChunkInsert{
				DocumentID: docID, KBID: d.KBID, ConversationID: d.ConversationID,
				Seq: seq, ParentID: parentID, ChunkType: chunkType, Content: child,
				ImageRef:  imageRef,
				Embedding: packFloats(vecs[i]), EmbeddingModel: emName,
			})
			if err != nil {
				s.logger.Printf("rag: insert chunk: %v", err)
			} else if s.vec.Enabled() {
				points = append(points, vector.Point{
					ChunkID: chunkID,
					Vector:  vecs[i],
					Payload: vector.Payload{
						DocumentID: docID, KBID: d.KBID, ConversationID: d.ConversationID,
						ParentID: parentID, ChunkType: chunkType, Seq: seq,
						Content: child, Filename: d.Filename,
					},
				})
			}
			seq++
			written++
			totalTokens += len(child) / 4
		}
	}
	if s.vec.Enabled() && len(points) > 0 {
		if err := s.vec.Upsert(ctx, dim, points); err != nil {
			// Don't fail the whole ingest on a vector-store problem (e.g. Qdrant
			// down / mis-keyed): the same vectors are written to Postgres, so
			// retrieval degrades to brute-force. Log loudly and mark the doc ready.
			s.logger.Printf("rag: vector upsert for %s failed (%v) — falling back to Postgres brute-force", docID, err)
		}
	}
	// Record embedding spend (§8.3, purpose=embedding) — best-effort.
	s.logEmbeddingUsage(ctx, d.KBID, d.ConversationID, emName, totalTokens)
	return store.UpdateDocumentStatus(ctx, s.db, docID, "ready", "", written)
}

// logEmbeddingUsage writes one usage_logs row for an embedding batch. The
// owning user is resolved through the KB or conversation.
func (s *Service) logEmbeddingUsage(ctx context.Context, kbID, convID, embedder string, tokens int) {
	if tokens == 0 || strings.HasPrefix(embedder, "aurelia-local") {
		return // local hash embedder is free — don't pollute the report
	}
	userID := ""
	if kbID != "" {
		_ = s.db.QueryRowContext(ctx, `SELECT user_id FROM knowledge_bases WHERE id=?`, kbID).Scan(&userID)
	}
	if userID == "" && convID != "" {
		_ = s.db.QueryRowContext(ctx, `SELECT user_id FROM conversations WHERE id=?`, convID).Scan(&userID)
	}
	if userID == "" {
		return
	}
	modelID := strings.TrimPrefix(embedder, "emb:")
	_ = store.LogUsage(ctx, s.db, store.UsageLog{
		UserID:         userID,
		ConversationID: convID,
		ModelID:        modelID,
		Purpose:        "embedding",
		InputTokens:    tokens,
	})
}

// Snippet is the slim search hit returned by Retrieve. The orchestrator
// and the `search_knowledge_base` tool convert it to llm.Citation for the
// downstream message + SSE pipeline.
type Snippet struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"`
}

// Retrieve runs the hybrid search described in §4.11-E (vector + keyword
// fusion) over the chunks visible to the conversation: own conversation
// uploads ∪ attached KBs (project KBs included via the orchestrator).
//
// Returns up to topK Snippets carrying the snippet and the filename so the
// frontend can render them with the same component as web_search citations.
//
// §4.11-B2 embedding lock: when one or more KBs are in scope, the FIRST KB's
// locked embedding model is used. We refuse cross-model fan-out (multiple KBs
// at different dims) — the orchestrator should split the call instead. With
// no KBs in scope (pure conversation upload), we fall back to the global
// resolver since conversation uploads are ephemeral and not locked.
// §2.4 query-vector cache: identical RAG queries (retries, common questions,
// the same question across users on a shared KB) reuse the embedding instead of
// re-calling the embedding API. Keyed by embedder name + query so different
// models/dims never collide. Process-local, short TTL, bounded size.
const (
	queryEmbedTTL = 10 * time.Minute
	queryEmbedMax = 4096
)

type queryEmbedEntry struct {
	vec []float32
	exp int64
}

var (
	queryEmbedMu    sync.Mutex
	queryEmbedStore = map[string]queryEmbedEntry{}
)

func (s *Service) embedQueryCached(ctx context.Context, em Embedder, emName, query string) (vec []float32, cached bool, err error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(emName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(query))
	key := fmt.Sprintf("%x", h.Sum64())

	now := time.Now().UnixNano()
	queryEmbedMu.Lock()
	if e, ok := queryEmbedStore[key]; ok && now < e.exp {
		v := e.vec
		queryEmbedMu.Unlock()
		return v, true, nil
	}
	queryEmbedMu.Unlock()

	vecs, err := em.Embed(ctx, []string{query})
	if err != nil {
		return nil, false, err
	}
	if len(vecs) == 0 {
		return nil, false, fmt.Errorf("rag: embedder returned no vector")
	}
	v := vecs[0]
	queryEmbedMu.Lock()
	if len(queryEmbedStore) >= queryEmbedMax {
		queryEmbedStore = map[string]queryEmbedEntry{} // crude cap; cheap to rebuild
	}
	queryEmbedStore[key] = queryEmbedEntry{vec: v, exp: time.Now().Add(queryEmbedTTL).UnixNano()}
	queryEmbedMu.Unlock()
	return v, false, nil
}

func (s *Service) Retrieve(ctx context.Context, userID, convID string, kbIDs []string, query string, topK int) ([]Snippet, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	var (
		em     Embedder
		emName string
		dim    int
		err    error
	)
	if len(kbIDs) > 0 {
		em, emName, dim, err = s.resolveEmbedderForKB(ctx, kbIDs[0])
		if err != nil {
			return nil, err
		}
		// Best-effort dimension sanity check: refuse to fan out to KBs with a
		// different dim than the first one. Caller should split the call.
		for _, other := range kbIDs[1:] {
			_, _, otherDim, oerr := s.resolveEmbedderForKB(ctx, other)
			if oerr != nil {
				return nil, oerr
			}
			if otherDim != dim {
				return nil, fmt.Errorf("rag: cross-KB query has mixed embedding dims (%d vs %d) — re-create the KB to align", dim, otherDim)
			}
		}
	} else {
		em, emName, dim = s.resolveEmbedder(ctx)
	}
	qVec, cached, err := s.embedQueryCached(ctx, em, emName, query)
	if err != nil {
		return nil, err
	}
	// Mirror the ingest-side reconciliation: trust the actual query-vector width
	// over the configured dim so we search the same Qdrant collection the vectors
	// were written into (a model that emits 1024 despite a 1536 config still
	// retrieves correctly).
	if len(qVec) > 0 && len(qVec) != dim {
		dim = len(qVec)
	}
	// Query embedding is billable (§8.3 purpose=embedding) — but only when we
	// actually called the API. On a §2.4 query-vector cache hit, no call was made.
	if !cached && !strings.HasPrefix(emName, "aurelia-local") && userID != "" {
		_ = store.LogUsage(ctx, s.db, store.UsageLog{
			UserID: userID, ConversationID: convID,
			ModelID: strings.TrimPrefix(emName, "emb:"),
			Purpose: "embedding", InputTokens: len(query) / 4,
		})
	}
	terms := tokenize(strings.ToLower(query))
	if topK <= 0 {
		topK = 5
	}

	var cands []retrievalCandidate
	if s.vec.Enabled() {
		// §4.11-E independent legs: 30 dense ∥ 30 keyword, RRF k=60 → top-K.
		// Both are run with the SAME scope filter so a chunk that hits in only
		// one leg still survives fusion. We dedupe by chunk id post-merge.
		denseN := 30
		keywordN := 30
		hits, err := s.vec.Search(ctx, dim, qVec, vector.Scope{KBIDs: kbIDs, ConversationID: convID}, denseN)
		if err != nil {
			// Qdrant down / mis-keyed: degrade to the Postgres brute-force copy
			// instead of failing retrieval (the vectors are dual-written there).
			s.logger.Printf("rag: qdrant search failed (%v) — falling back to Postgres brute-force", err)
			bf, bfErr := s.bruteForceCandidates(ctx, kbIDs, convID, qVec, terms)
			if bfErr != nil {
				return nil, bfErr
			}
			cands = bf
		} else {
			kwHits, _ := s.vec.SearchKeyword(ctx, dim, query, vector.Scope{KBIDs: kbIDs, ConversationID: convID}, keywordN)
			merged := map[string]retrievalCandidate{}
			for _, h := range hits {
				merged[h.Payload.ChunkID] = retrievalCandidate{
					chunkID:    h.Payload.ChunkID,
					documentID: h.Payload.DocumentID,
					parentID:   h.Payload.ParentID,
					filename:   h.Payload.Filename,
					content:    h.Payload.Content,
					sim:        h.Score,
					bm:         keywordScore(terms, h.Payload.Content),
				}
			}
			for _, h := range kwHits {
				if cur, ok := merged[h.Payload.ChunkID]; ok {
					// Combine: keep dense sim, refresh bm from independent ranking.
					cur.bm += keywordScore(terms, h.Payload.Content)
					merged[h.Payload.ChunkID] = cur
					continue
				}
				merged[h.Payload.ChunkID] = retrievalCandidate{
					chunkID:    h.Payload.ChunkID,
					documentID: h.Payload.DocumentID,
					parentID:   h.Payload.ParentID,
					filename:   h.Payload.Filename,
					content:    h.Payload.Content,
					sim:        0,
					bm:         keywordScore(terms, h.Payload.Content),
				}
			}
			cands = make([]retrievalCandidate, 0, len(merged))
			for _, c := range merged {
				cands = append(cands, c)
			}
		}
	} else {
		// Dev / no-Qdrant path: brute-force cosine over the vectors kept in the
		// relational store (the dual-write insurance copy).
		bf, err := s.bruteForceCandidates(ctx, kbIDs, convID, qVec, terms)
		if err != nil {
			return nil, err
		}
		cands = bf
	}
	if len(cands) == 0 {
		return nil, nil
	}

	ranked := fuseReciprocalRank(cands)
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}

	result := []Snippet{}
	seenParent := map[string]bool{}
	for _, c := range ranked {
		// Small-to-big expansion: a child hit returns its PARENT section so the
		// model gets surrounding context, deduped per parent (§4.11-C-2).
		content := c.content
		if c.parentID != "" {
			if seenParent[c.parentID] {
				continue
			}
			if parent, err := store.GetChunkContent(ctx, s.db, c.parentID); err == nil && parent != "" {
				content = parent
				seenParent[c.parentID] = true
			}
		}
		result = append(result, Snippet{
			ID:      c.chunkID,
			Index:   len(result) + 1,
			Title:   c.filename,
			URL:     "doc://" + c.documentID,
			Snippet: snippetOf(content, 1200),
			Source:  "kb",
		})
	}
	return result, nil
}

// bruteForceCandidates scores every in-scope chunk by cosine over the vectors
// kept in the relational store (the dual-write insurance copy). Used when Qdrant
// is disabled OR unavailable, so retrieval keeps working without it.
func (s *Service) bruteForceCandidates(ctx context.Context, kbIDs []string, convID string, qVec []float32, terms []string) ([]retrievalCandidate, error) {
	rows, err := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
	if err != nil {
		return nil, err
	}
	cands := make([]retrievalCandidate, 0, len(rows))
	for _, r := range rows {
		// Parent rows carry section context but no vector — they're returned via
		// expansion, never scored directly (§4.11-C-2).
		if r.ChunkType == "parent" || len(r.Embedding) == 0 {
			continue
		}
		v := unpackFloats(r.Embedding)
		sim := float32(0)
		if len(v) > 0 {
			sim = cosine(qVec, v)
		}
		cands = append(cands, retrievalCandidate{
			chunkID:    r.ID,
			documentID: r.DocumentID,
			parentID:   r.ParentID,
			filename:   r.Filename,
			content:    r.Content,
			sim:        sim,
			bm:         keywordScore(terms, r.Content),
		})
	}
	return cands, nil
}

// retrievalCandidate is one scored chunk feeding the reciprocal-rank fusion in
// Retrieve. Both retrieval paths (Qdrant search and Postgres brute force)
// produce these so the fusion + small-to-big expansion runs identically.
type retrievalCandidate struct {
	chunkID    string
	documentID string
	parentID   string
	filename   string
	content    string
	sim        float32 // raw vector similarity (higher = closer)
	bm         float32 // keyword overlap score
}

// fuseReciprocalRank re-orders candidates by reciprocal-rank fusion of the
// vector-similarity ranking and the keyword ranking (§4.11-E). It returns a new
// slice sorted best-first; the input is left untouched.
func fuseReciprocalRank(cands []retrievalCandidate) []retrievalCandidate {
	const k = 60
	n := len(cands)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	fused := make([]float32, n)
	// Vector leg: rank by similarity, accumulate 1/(rank+k).
	sort.SliceStable(idx, func(a, b int) bool { return cands[idx[a]].sim > cands[idx[b]].sim })
	for rank, i := range idx {
		fused[i] += 1 / float32(rank+k)
	}
	// Keyword leg: rank by BM-ish score, accumulate 1/(rank+k).
	sort.SliceStable(idx, func(a, b int) bool { return cands[idx[a]].bm > cands[idx[b]].bm })
	for rank, i := range idx {
		fused[i] += 1 / float32(rank+k)
	}
	sort.SliceStable(idx, func(a, b int) bool { return fused[idx[a]] > fused[idx[b]] })
	out := make([]retrievalCandidate, n)
	for pos, i := range idx {
		out[pos] = cands[i]
	}
	return out
}

// OnDocumentDeleted removes a document's vectors from the search backend so it
// stays in sync with the relational chunk rows the store deletes. No-op when
// the vector backend is disabled.
func (s *Service) OnDocumentDeleted(ctx context.Context, documentID string) error {
	return s.vec.DeleteByDocument(ctx, documentID)
}

// PromoteDocument moves a conversation-scoped document into a KB and RE-EMBEDS
// it with that KB's locked embedder (§C5). The old chunks/vectors were embedded
// with the conversation's (possibly different model/dim) embedder; keeping them
// would silently break retrieval, so we drop them and re-run the pipeline, which
// resolves the destination KB's embedder. Re-ingest is async (Ingest enqueues).
func (s *Service) PromoteDocument(ctx context.Context, docID, kbID string) error {
	if err := store.PromoteDocumentToKB(ctx, s.db, docID, kbID); err != nil {
		return err
	}
	_ = s.vec.DeleteByDocument(ctx, docID)
	if err := store.DeleteChunksByDocument(ctx, s.db, docID); err != nil {
		return err
	}
	s.Ingest(docID) // re-parse + re-embed with the KB's locked model
	return nil
}

// OnKBDeleted removes every vector belonging to a knowledge base.
func (s *Service) OnKBDeleted(ctx context.Context, kbID string) error {
	return s.vec.DeleteByKB(ctx, kbID)
}

// OnConversationDeleted removes every vector belonging to a conversation's
// uploads.
func (s *Service) OnConversationDeleted(ctx context.Context, conversationID string) error {
	return s.vec.DeleteByConversation(ctx, conversationID)
}

func snippetOf(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if max <= 0 {
		max = 240
	}
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

func tokenize(s string) []string {
	re := regexp.MustCompile(`[\p{L}\p{N}_]+`)
	return re.FindAllString(s, -1)
}

func keywordScore(terms []string, doc string) float32 {
	if len(terms) == 0 || doc == "" {
		return 0
	}
	low := strings.ToLower(doc)
	score := float32(0)
	for _, t := range terms {
		count := strings.Count(low, t)
		if count > 0 {
			score += float32(math.Log(float64(1 + count)))
		}
	}
	return score
}

// LocalEmbedder hashes tokens into a fixed-dimension feature vector. The
// result is deterministic, fast and good enough to make local search work
// without external services.
type LocalEmbedder struct{ Dim int }

// NewLocalEmbedder returns a LocalEmbedder at the given dimension.
func NewLocalEmbedder(dim int) *LocalEmbedder { return &LocalEmbedder{Dim: dim} }

// Embed returns one vector per text.
func (l *LocalEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = featureVector(t, l.Dim)
	}
	return out, nil
}

func featureVector(s string, dim int) []float32 {
	v := make([]float32, dim)
	terms := tokenize(strings.ToLower(s))
	for _, t := range terms {
		h := fnv.New32a()
		_, _ = h.Write([]byte(t))
		idx := int(h.Sum32() % uint32(dim))
		v[idx] += 1
	}
	// L2 normalise.
	var norm float32
	for _, x := range v {
		norm += x * x
	}
	if norm > 0 {
		inv := 1 / float32(math.Sqrt(float64(norm)))
		for i := range v {
			v[i] *= inv
		}
	}
	return v
}

func cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

func packFloats(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(x))
	}
	return out
}
func unpackFloats(b []byte) []float32 {
	if len(b)%4 != 0 || len(b) == 0 {
		return nil
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// parentChunk groups one large section with its embedded child chunks
// (§4.11-C-2 small-to-big: search the small, return the big).
type parentChunk struct {
	Content  string
	Children []string
	// Breadcrumb is the heading path leading to this section (e.g.
	// "Manual > Setup > Networking"). Prepended to each child so embeddings
	// carry context the bare body would otherwise lose (§4.11-C-1).
	Breadcrumb string
	// ChunkType marks atomic units we MUST NOT split mid-stream (table, code).
	// "text" = default; "table" = preserve whole row block; "code" = preserve
	// fenced block.
	ChunkType string
}

// Target child chunk size in characters. The design specifies 400-800 tokens;
// at ~4 chars/token the safe range is ~1600-3200 chars. Defaulting at 2000
// gives the embedder enough context per vector without diluting precision.
const (
	childTargetChars  = 2000
	parentTargetChars = 4800
	// Overlap between consecutive children (~12%) keeps boundary information
	// retrievable from either side (§4.11-C-1 "10-15% overlap").
	chunkOverlapChars = 250
)

// chunkHierarchical splits content into parent sections, each subdivided into
// overlapping child chunks. Children are embedded; the parent is returned at
// retrieval time for fuller context. The structural splitter respects
// headings → paragraphs → sentences as natural break points and protects
// tables / fenced code blocks as atomic units.
func chunkHierarchical(content string) []parentChunk {
	sections := splitByHeadings(content)
	out := []parentChunk{}
	for _, sec := range sections {
		atoms := splitProtectedAtoms(sec.body)
		merged := mergeAtomsIntoChildren(atoms, childTargetChars)
		if len(merged) == 0 {
			continue
		}
		// Sliding-window overlap between adjacent children.
		children := withOverlap(merged, chunkOverlapChars)
		// Prefix each child with the breadcrumb so its embedding captures the
		// heading path (§4.11-C-1).
		breadcrumb := sec.breadcrumb
		labeled := make([]string, len(children))
		for i, c := range children {
			if breadcrumb != "" {
				labeled[i] = "[" + breadcrumb + "]\n" + c
			} else {
				labeled[i] = c
			}
		}
		parent := truncateAt(sec.body, parentTargetChars)
		out = append(out, parentChunk{
			Content: parent, Children: labeled,
			Breadcrumb: breadcrumb, ChunkType: "text",
		})
	}
	return out
}

// section represents a body of text under a heading path.
type section struct {
	breadcrumb string
	body       string
}

// headingRe matches ATX-style markdown headings (#, ##, … up to ######) at the
// start of a line. We also accept "Section N:" style headings as a fallback.
var headingRe = regexp.MustCompile(`(?m)^(\s{0,3})(#{1,6})\s+(.+)$`)

// splitByHeadings cuts content at heading lines, building a heading-path
// breadcrumb (e.g. "Manual > Setup > Networking") for each resulting body.
func splitByHeadings(content string) []section {
	matches := headingRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return []section{{breadcrumb: "", body: content}}
	}
	out := []section{}
	stack := []string{}
	prevDepth := 0
	cursor := 0
	for i, m := range matches {
		// Body of the previous section: from cursor up to the start of this
		// heading line.
		headingStart := m[0]
		body := content[cursor:headingStart]
		if i > 0 && strings.TrimSpace(body) != "" {
			out = append(out, section{breadcrumb: strings.Join(stack, " > "), body: body})
		}
		depth := m[5] - m[4] // length of the # run (group 2)
		title := strings.TrimSpace(content[m[6]:m[7]])
		// Maintain heading-depth stack: pop until our depth fits, then push.
		if depth <= prevDepth {
			pops := prevDepth - depth + 1
			if pops > len(stack) {
				pops = len(stack)
			}
			stack = stack[:len(stack)-pops]
		}
		stack = append(stack, title)
		prevDepth = depth
		cursor = m[1]
	}
	// Tail body.
	if cursor < len(content) {
		tail := content[cursor:]
		if strings.TrimSpace(tail) != "" {
			out = append(out, section{breadcrumb: strings.Join(stack, " > "), body: tail})
		}
	}
	if len(out) == 0 {
		out = append(out, section{breadcrumb: "", body: content})
	}
	return out
}

// atom is one chunkable unit: a paragraph, a table block, or a fenced code
// block. Tables and code are marked atomic so mergeAtomsIntoChildren never
// splits them across two children (§4.11-C-1 "保护表格/代码").
type atom struct {
	text   string
	atomic bool
}

// fenced code fence ``` … ``` plus pipe-table runs are recognised as atomics.
var (
	fenceRe = regexp.MustCompile("(?ms)^(\\s{0,3}```[^\n]*\\n.*?```)")
	tableRe = regexp.MustCompile(`(?m)^\|.*\|\s*$`)
	mathRe  = regexp.MustCompile(`(?ms)^\\\$\\\$.*?\\\$\\\$`)
	imageRe = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
)

// splitProtectedAtoms returns paragraph + table + code-block atoms in document
// order. Anything inside a fenced code block or a contiguous table run is kept
// in a single atom so a child chunk can never end mid-row or mid-statement.
func splitProtectedAtoms(body string) []atom {
	if body == "" {
		return nil
	}
	out := []atom{}
	rest := body
	// First, peel off fenced code blocks (they win over paragraphs).
	for {
		loc := fenceRe.FindStringIndex(rest)
		if loc == nil {
			break
		}
		before := rest[:loc[0]]
		code := rest[loc[0]:loc[1]]
		if strings.TrimSpace(before) != "" {
			out = append(out, splitParagraphsAndTables(before)...)
		}
		out = append(out, atom{text: code, atomic: true})
		rest = rest[loc[1]:]
	}
	if strings.TrimSpace(rest) != "" {
		out = append(out, splitParagraphsAndTables(rest)...)
	}
	return out
}

// splitParagraphsAndTables splits a non-code chunk by blank lines and groups
// consecutive pipe-table lines into one atomic atom.
func splitParagraphsAndTables(s string) []atom {
	out := []atom{}
	for _, para := range regexp.MustCompile(`\n{2,}`).Split(s, -1) {
		p := strings.TrimSpace(para)
		if p == "" {
			continue
		}
		// If every line of this paragraph looks like a table row, treat the
		// whole paragraph as atomic.
		lines := strings.Split(p, "\n")
		allTable := true
		for _, l := range lines {
			if !tableRe.MatchString(strings.TrimRight(l, " \t")) {
				allTable = false
				break
			}
		}
		if allTable && len(lines) >= 2 {
			out = append(out, atom{text: p, atomic: true})
			continue
		}
		// math/image blocks are kept whole too.
		if mathRe.MatchString(p) || (imageRe.MatchString(p) && len(p) < 800) {
			out = append(out, atom{text: p, atomic: true})
			continue
		}
		// Otherwise long paragraphs are sub-split on sentence boundaries inside
		// mergeAtomsIntoChildren so we never split mid-sentence.
		out = append(out, atom{text: p, atomic: false})
	}
	return out
}

// mergeAtomsIntoChildren accumulates atoms into children of ~target chars,
// splitting long non-atomic paragraphs on sentence boundaries when necessary.
func mergeAtomsIntoChildren(atoms []atom, target int) []string {
	out := []string{}
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	for _, a := range atoms {
		if a.atomic {
			if cur.Len() > 0 {
				flush()
			}
			out = append(out, a.text)
			continue
		}
		if cur.Len()+len(a.text) > target && cur.Len() > 0 {
			flush()
		}
		// If a paragraph alone exceeds target, sentence-split it.
		if len(a.text) > target {
			sentences := splitSentences(a.text)
			for _, s := range sentences {
				if cur.Len()+len(s) > target && cur.Len() > 0 {
					flush()
				}
				if cur.Len() > 0 {
					cur.WriteString(" ")
				}
				cur.WriteString(s)
			}
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(a.text)
	}
	flush()
	return out
}

// withOverlap re-emits children so each (except the first) starts with the
// tail of the previous one. Skip atomics: tables / code don't need overlap.
func withOverlap(children []string, overlap int) []string {
	if overlap <= 0 || len(children) <= 1 {
		return children
	}
	out := make([]string, 0, len(children))
	for i, c := range children {
		if i == 0 {
			out = append(out, c)
			continue
		}
		prev := children[i-1]
		// Don't overlap into a code/table block (it would be a fragment).
		if strings.Contains(prev, "```") || strings.HasPrefix(strings.TrimSpace(prev), "|") {
			out = append(out, c)
			continue
		}
		tail := prev
		if len(tail) > overlap {
			tail = tail[len(tail)-overlap:]
			// Pull back to a word boundary.
			if i := strings.IndexAny(tail, " \n。.；;！!？?"); i > 0 && i < len(tail)-1 {
				tail = tail[i+1:]
			}
		}
		out = append(out, strings.TrimSpace(tail)+"\n\n"+c)
	}
	return out
}

// splitSentences breaks a paragraph on common sentence-ending punctuation,
// supporting CJK (。 ！ ？ ；) plus ASCII.
func splitSentences(p string) []string {
	// Insert a NUL after each end-of-sentence punctuation then split on NUL.
	endRunes := map[rune]bool{'.': true, '!': true, '?': true, ';': true, '。': true, '！': true, '？': true, '；': true}
	var b strings.Builder
	for _, r := range p {
		b.WriteRune(r)
		if endRunes[r] {
			b.WriteByte(0)
		}
	}
	raw := strings.Split(b.String(), "\x00")
	out := []string{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{p}
	}
	return out
}

func truncateAt(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// mineruImageMarker matches the markdown image syntax parser.go emits for
// MinerU image extractions: `![caption](mineru://filename)`. The capture
// groups are (1) caption, (2) filename.
var mineruImageMarker = regexp.MustCompile(`!\[([^\]]*)\]\(mineru://([^)]+)\)`)

// soleMineruImageMarker reports whether `chunk` consists of exactly one
// MinerU image marker (after trimming whitespace and an optional leading
// `<!-- mineru-image … -->` page-number comment that parser.go appends).
// Returns the image filename on hit.
//
// Rationale: a child chunk that mixes prose with an image marker should
// stay `text` — we don't want to collapse the prose under an image_ref or
// classify the chunk against just one of multiple image filenames it may
// contain. This is the strict check the chunker uses for classification;
// the broader `mineruImageMarker` regex is still used elsewhere.
func soleMineruImageMarker(chunk string) (string, bool) {
	s := strings.TrimSpace(chunk)
	// Strip an optional leading page-number comment we emit when we append
	// fallback image markers in parser.go.
	if strings.HasPrefix(s, "<!--") {
		if end := strings.Index(s, "-->"); end > 0 {
			s = strings.TrimSpace(s[end+3:])
		}
	}
	m := mineruImageMarker.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	// The whole match must cover the trimmed chunk — any trailing prose or
	// a second marker would leave residue.
	if m[0] != s {
		return "", false
	}
	return m[2], true
}

// RouteDecision is the structured output of the query router (§4.11-B).
type RouteDecision struct {
	Strategy string   `json:"strategy"` // retrieve | full_doc | none
	Queries  []string `json:"queries"`
}

// FullTextThreshold (§4.11-B): when the bound documents fit in ~32K tokens we
// skip the router entirely and inject the full text — cheapest and highest
// fidelity for small docs.
const fullTextThresholdTokens = 32_000

// ContextBudget caps how much full-document text we inject before degrading to
// map-reduce summarisation (§4.11-B). Set to match the full-text threshold so
// the small-document path never silently truncates between threshold and budget.
const contextBudgetTokens = 32_000

// RouteAndRetrieve runs the §4.11-B router pipeline:
//
//  1. Small scope (≤ FullTextThreshold) → inject the full text directly, no
//     router call at all.
//  2. Otherwise ask the task model: none | retrieve (with rewritten queries) |
//     full_doc.
//  3. full_doc over ContextBudget → map-reduce: summarise chunk groups via the
//     task model, then merge (§4.11-B).
//
// When the task model is unavailable it falls back to "retrieve" using the
// user's text as the query (safest).
func (s *Service) RouteAndRetrieve(ctx context.Context, userID, convID string, kbIDs []string, userText string, history []string, topK int) ([]Snippet, RouteDecision, error) {
	decision := RouteDecision{Strategy: "retrieve", Queries: []string{userText}}

	// Step 0: size check — small corpora skip routing entirely.
	scope, _ := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
	if len(scope) > 0 {
		estTokens := 0
		for _, c := range scope {
			if c.ChunkType != "parent" { // parents duplicate child text
				estTokens += len(c.Content) / 4
			}
		}
		if estTokens <= fullTextThresholdTokens {
			decision.Strategy = "full_text"
			return fullTextSnippets(scope, contextBudgetTokens), decision, nil
		}
	}

	// Build a list of (filename, ~first sentence) so the router can resolve
	// pronouns like "this report" / "the second doc" (§4.11-B router prompt).
	docHints := s.collectDocHints(ctx, kbIDs, convID)

	if s.task == nil {
		out, err := s.Retrieve(ctx, userID, convID, kbIDs, userText, topK)
		return out, decision, err
	}
	prompt := buildRouterPrompt(userText, history, docHints)
	var d RouteDecision
	if err := s.task.RunJSON(ctx, "task.router", prompt, &d, RouterOpts{UserID: userID, ConversationID: convID}); err == nil {
		if d.Strategy != "" {
			decision = d
		}
	}
	switch decision.Strategy {
	case "none":
		return nil, decision, nil
	case "full_doc":
		// Whole-document question. Within budget → inject everything in document
		// order; over budget → map-reduce summarisation via the task model.
		estTokens := 0
		for _, c := range scope {
			if c.ChunkType != "parent" {
				estTokens += len(c.Content) / 4
			}
		}
		if estTokens <= contextBudgetTokens {
			return fullTextSnippets(scope, contextBudgetTokens), decision, nil
		}
		out, err := s.mapReduceSummarise(ctx, userID, convID, scope, userText)
		if err != nil || len(out) == 0 {
			// Fall back to wide retrieval if summarisation fails.
			out, err = s.Retrieve(ctx, userID, convID, kbIDs, userText, topK*2)
		}
		return out, decision, err
	default:
		// retrieve: run each rewritten query, merge and dedupe.
		seen := map[string]struct{}{}
		merged := []Snippet{}
		queries := decision.Queries
		if len(queries) == 0 {
			queries = []string{userText}
		}
		for _, q := range queries {
			subset, err := s.Retrieve(ctx, userID, convID, kbIDs, q, topK)
			if err != nil {
				continue
			}
			for _, sn := range subset {
				if _, ok := seen[sn.ID]; ok {
					continue
				}
				seen[sn.ID] = struct{}{}
				merged = append(merged, sn)
				if len(merged) >= topK {
					break
				}
			}
			if len(merged) >= topK {
				break
			}
		}
		return merged, decision, nil
	}
}

// fullTextSnippets returns the scope's child chunks in document order as
// snippets, capped at budget tokens (≈4 chars/token).
func fullTextSnippets(scope []store.Chunk, budgetTokens int) []Snippet {
	out := []Snippet{}
	used := 0
	idx := 1
	for _, c := range scope {
		if c.ChunkType == "parent" {
			continue
		}
		t := len(c.Content) / 4
		if used+t > budgetTokens {
			break
		}
		used += t
		out = append(out, Snippet{
			ID:      c.ID,
			Index:   idx,
			Title:   c.Filename,
			URL:     "doc://" + c.DocumentID,
			Snippet: c.Content,
			Source:  "kb",
		})
		idx++
	}
	return out
}

// mapReduceSummarise condenses an over-budget corpus (§4.11-B): chunk groups
// are summarised by the task model with the user's question as focus (map),
// and the partial summaries are returned as snippets (reduce happens in the
// answer model's context).
func (s *Service) mapReduceSummarise(ctx context.Context, userID, convID string, scope []store.Chunk, userText string) ([]Snippet, error) {
	if s.task == nil {
		return nil, nil
	}
	const groupTokens = 6000
	const maxGroups = 8
	groups := [][]store.Chunk{}
	cur := []store.Chunk{}
	used := 0
	for _, c := range scope {
		if c.ChunkType == "parent" {
			continue
		}
		t := len(c.Content) / 4
		if used+t > groupTokens && len(cur) > 0 {
			groups = append(groups, cur)
			cur, used = nil, 0
			if len(groups) >= maxGroups {
				break
			}
		}
		cur = append(cur, c)
		used += t
	}
	if len(cur) > 0 && len(groups) < maxGroups {
		groups = append(groups, cur)
	}

	out := []Snippet{}
	for gi, g := range groups {
		var b strings.Builder
		fmt.Fprintf(&b, "针对问题「%s」，提炼下面文档片段中相关的事实与数据，≤200字。无关内容忽略。\n\n", userText)
		for _, c := range g {
			b.WriteString(c.Content)
			b.WriteString("\n\n")
		}
		var summary struct {
			Summary string `json:"summary"`
		}
		text := ""
		if err := s.task.RunJSON(ctx, "task.router", b.String()+`\n以 JSON 回复: {"summary":"..."}`, &summary, RouterOpts{UserID: userID, ConversationID: convID}); err == nil {
			text = summary.Summary
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, Snippet{
			ID:      g[0].ID,
			Index:   gi + 1,
			Title:   g[0].Filename + " (摘要)",
			URL:     "doc://" + g[0].DocumentID,
			Snippet: text,
			Source:  "kb",
		})
	}
	return out, nil
}

func buildRouterPrompt(userText string, history []string, docHints []string) string {
	b := strings.Builder{}
	b.WriteString("Decide whether the user's question needs retrieval from their uploaded documents.\n\n")
	if len(docHints) > 0 {
		b.WriteString("Documents in scope (filename — first words):\n")
		for _, d := range docHints {
			b.WriteString("- ")
			b.WriteString(d)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(history) > 0 {
		b.WriteString("Recent conversation:\n")
		for _, h := range history {
			b.WriteString("- ")
			b.WriteString(h)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Latest user message:\n")
	b.WriteString(userText)
	b.WriteString("\n\n")
	b.WriteString(`Rules:
- Use "none" when the question is unrelated to the documents (general chit-chat, math, code unrelated to files).
- Use "retrieve" for fact-finding inside the documents — return 2-4 rewritten queries that disambiguate pronouns ("this report" → use the filename) and split compound questions.
- Use "full_doc" when the user asks for a summary, an overview, or a high-level comparison spanning the WHOLE document(s).
Reply with strict JSON: {"strategy":"retrieve|full_doc|none","queries":["<rewritten query 1>","<rewritten query 2>"]}`)
	return b.String()
}

// collectDocHints returns up to ~12 "filename — first ~120 chars" lines so the
// router can resolve "this report", "the second one", etc. without a separate
// look-up. Chunks of type=parent are preferred since they carry the section
// heading.
func (s *Service) collectDocHints(ctx context.Context, kbIDs []string, convID string) []string {
	seen := map[string]bool{}
	hints := []string{}
	chunks, _ := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
	for _, c := range chunks {
		if seen[c.DocumentID] {
			continue
		}
		seen[c.DocumentID] = true
		first := c.Content
		if len(first) > 120 {
			first = first[:120]
		}
		first = strings.ReplaceAll(first, "\n", " ")
		hints = append(hints, c.Filename+" — "+strings.TrimSpace(first))
		if len(hints) >= 12 {
			break
		}
	}
	return hints
}
