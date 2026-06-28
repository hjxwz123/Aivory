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
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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
		// Cache the parsed content across retries so a transient embed/DB failure
		// re-runs only the cheap embed step — never the paid MinerU OCR again.
		cache := &parseCache{}
		for attempt := 1; attempt <= 3; attempt++ {
			if err = s.runPipeline(ctx, docID, cache); err == nil {
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

// parseCache memoises a document's parsed content across pipeline retry
// attempts so a later-stage (embed/DB) failure never re-runs paid MinerU OCR.
type parseCache struct {
	ok      bool
	content string
}

func (s *Service) runPipeline(ctx context.Context, docID string, cache *parseCache) error {
	d, err := store.GetDocument(ctx, s.db, docID)
	if err != nil {
		return err
	}
	// Idempotent re-ingest: drop any chunks AND vectors from a previous, partial,
	// or retried run BEFORE doing anything else. Doing it FIRST (not after parse /
	// embedder-resolve) means a failure later in THIS run — parse error, MinerU
	// outage, KB embedder-resolve error — can't leave stale rows behind; and
	// repeats (RequeueIncomplete, the 3× retry loop, a manual re-Ingest) never
	// duplicate. Unconditional: skip-embed docs write chunk rows too, and every
	// insert below mints a new id. (§4.11-C-3)
	if err := store.DeleteChunksByDocument(ctx, s.db, docID); err != nil {
		return err
	}
	if s.vec.Enabled() {
		if derr := s.vec.DeleteByDocument(ctx, docID); derr != nil {
			s.logger.Printf("rag: clear old vectors for %s: %v", docID, derr)
		}
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
	// per-document call from the file's content. Reuse the cached parse on a retry
	// so we never pay for MinerU OCR twice.
	var content string
	if cache != nil && cache.ok {
		content = cache.content
	} else {
		raw, extracted, perr := parseDocument(ctx, d.StoragePath, d.MimeType, d.Filename, mineruURL, mineruKey, storageClient, s.logger)
		if perr != nil {
			return perr
		}
		// Strip NUL / invalid UTF-8 at the source: parsed binary docs (docx/pdf/ppt)
		// carry bytes Postgres TEXT columns reject (SQLSTATE 22021). This guarantees
		// every downstream write (chunks, parents) is clean regardless of insert path.
		content = sanitizeIngestText(raw)

		// A KB upload whose text couldn't be extracted (e.g. a scan with MinerU
		// unavailable) must NOT be embedded — a junk placeholder vector silently
		// pollutes search. Fail it loudly with the reason instead, and return nil
		// so it isn't retried (re-running MinerU/parse would just fail again).
		if !extracted && d.KBID != "" {
			reason := strings.TrimSpace(content)
			if len(reason) > 500 {
				reason = reason[:500]
			}
			return store.UpdateDocumentStatus(ctx, s.db, docID, "failed", reason, 0)
		}
		// Only cache real extractions — a placeholder isn't worth reusing, and
		// caching it would skip a (cheap) re-parse that might succeed next time.
		if cache != nil && extracted {
			cache.ok = true
			cache.content = content
		}
	}

	// Chunk hierarchically (§4.11-C-2 small-to-big): parents carry section
	// context (not embedded), children carry the vectors. The new chunker also
	// returns image_caption rows for MinerU-extracted images (§4.11-C-1).
	parents := chunkHierarchical(content)

	// A conversation-scoped doc that fits the full-text window is ALWAYS injected
	// whole (the §4.11-B router skips retrieval for small scopes), so its vectors
	// would never be queried — skip embedding entirely: instant ingest, no wasted
	// embedding calls (this is why a small .c/.txt upload no longer "indexes").
	//
	// Source code & structured config (json/xml/yaml/…) are skipped regardless of
	// size: chunking breaks their structure and dense-vector similarity retrieves
	// code/config badly. In a conversation they're injected whole (when small) and
	// always readable via the sandbox, so embedding adds cost without value. Prose
	// (md/txt/log) still embeds. KB uploads always embed — a KB is an explicit
	// cross-document search index, so skipping there would silently break search.
	skipEmbed := d.KBID == "" && d.ConversationID != "" &&
		(isCodeOrConfigText(d.Filename) || estimateTokens(content) <= s.ragSettings().FullTextThreshold)

	// §4.11-B2 lock: ingest into a KB MUST use the KB's locked embedding model;
	// global setting changes never re-route an existing KB's vectors. For pure
	// conversation-scoped docs (no KB), fall through to the global resolver.
	var (
		em     Embedder
		emName string
		dim    int
	)
	if !skipEmbed {
		_ = store.UpdateDocumentStatus(ctx, s.db, docID, "embedding", "", 0)
		if d.KBID != "" {
			em, emName, dim, err = s.resolveEmbedderForKB(ctx, d.KBID)
			if err != nil {
				return err
			}
		} else {
			em, emName, dim = s.resolveEmbedder(ctx)
		}
	}
	// (Old chunks/vectors were already cleared at the top of runPipeline, so a
	// failure between there and here never leaves stale rows.)
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
		var vecs [][]float32
		if !skipEmbed {
			vecs, err = em.Embed(ctx, p.Children)
			if err != nil {
				return err
			}
		}
		// Reconcile the collection dimension with what the model ACTUALLY
		// returned. The configured dim is only a hint — some endpoints ignore
		// it and emit their native width (e.g. configured 1536 but the model
		// returns 1024). Writing wrong-dim vectors makes Qdrant reject the whole
		// upsert ("Vector dimension error"). Trust the vectors over the config so
		// ingest just works regardless of misconfiguration.
		if len(vecs) > 0 && len(vecs[0]) > 0 && len(vecs[0]) != dim {
			actual := len(vecs[0])
			// Not an error: the model's native width differs from the configured
			// one (e.g. DashScope text-embedding-v3 only emits 1024, can't do
			// 1536). We already asked for the configured width via `dimensions`;
			// the model ignored/capped it, so we adapt the collection to the real
			// width and move on. To get 1536 you need a model that supports it.
			s.logger.Printf("rag: embedding model emits %d-dim vectors (config requested %d, unsupported) — adapting to %d (doc %s)", actual, dim, actual, docID)
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
			var emb []byte
			if !skipEmbed {
				emb = packFloats(vecs[i])
			}
			chunkID, err := store.CreateChunkFull(ctx, s.db, store.ChunkInsert{
				DocumentID: docID, KBID: d.KBID, ConversationID: d.ConversationID,
				Seq: seq, ParentID: parentID, ChunkType: chunkType, Content: child,
				ImageRef:  imageRef,
				Embedding: emb, EmbeddingModel: emName,
			})
			if err != nil {
				s.logger.Printf("rag: insert chunk: %v", err)
			} else if !skipEmbed && s.vec.Enabled() {
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
			totalTokens += estimateTokens(child)
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
	// Record embedding spend (§8.3, purpose=embedding) — best-effort. Skipped
	// when we didn't embed (small conversation doc), so the report stays honest.
	if !skipEmbed {
		s.logEmbeddingUsage(ctx, d.KBID, d.ConversationID, emName, totalTokens)
	}
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
	terms := tokenize(strings.ToLower(query))
	if topK <= 0 {
		topK = 5
	}

	// Resolve the embedding model(s) covering the scope and search each model's
	// chunks with ITS OWN query vector. KB docs use the KB's locked model; a
	// conversation's own (large, embedded) uploads use the GLOBAL model. A single
	// query vector can't meaningfully score — or even reach, since Qdrant
	// collections are per-dimension — vectors from a different model, so when a
	// bound KB and the conversation disagree on the model we search them as two
	// groups and merge. (§4.11 model split)
	var cands []retrievalCandidate
	if len(kbIDs) > 0 {
		kbEm, kbName, kbDim, err := s.resolveEmbedderForKB(ctx, kbIDs[0])
		if err != nil {
			return nil, err
		}
		// Cross-KB consistency: all bound KBs must share ONE embedding model (same
		// dim + same model). A mismatch would score one model's query against
		// another's vectors; erroring surfaces it instead of returning garbage.
		for _, other := range kbIDs[1:] {
			_, otherName, otherDim, oerr := s.resolveEmbedderForKB(ctx, other)
			if oerr != nil {
				return nil, oerr
			}
			if otherDim != kbDim || otherName != kbName {
				return nil, fmt.Errorf("rag: cross-KB query mixes embedding models (%s/%dd vs %s/%dd) — search these KBs separately or re-embed to align", kbName, kbDim, otherName, otherDim)
			}
		}
		gEm, gName, gDim := s.resolveEmbedder(ctx)
		if convID != "" && (gName != kbName || gDim != kbDim) {
			// Two model groups: KBs under the KB model, conversation docs under the
			// global model — each with its own query embedding + per-dim collection.
			kbCands, err := s.searchScope(ctx, userID, convID, kbEm, kbName, kbDim, vector.Scope{KBIDs: kbIDs}, query, terms)
			if err != nil {
				return nil, err
			}
			cands = kbCands
			if convCands, cerr := s.searchScope(ctx, userID, convID, gEm, gName, gDim, vector.Scope{ConversationID: convID}, query, terms); cerr == nil {
				cands = appendUniqueCandidates(cands, convCands)
			} else {
				s.logger.Printf("rag: conversation-scope retrieval failed for %s: %v", convID, cerr)
			}
		} else {
			// One model across KBs (+ the conversation when its model matches): a
			// single combined-scope search — exactly the prior behaviour.
			cands, err = s.searchScope(ctx, userID, convID, kbEm, kbName, kbDim, vector.Scope{KBIDs: kbIDs, ConversationID: convID}, query, terms)
			if err != nil {
				return nil, err
			}
		}
	} else {
		gEm, gName, gDim := s.resolveEmbedder(ctx)
		var err error
		cands, err = s.searchScope(ctx, userID, convID, gEm, gName, gDim, vector.Scope{ConversationID: convID}, query, terms)
		if err != nil {
			return nil, err
		}
	}
	// Surface in-scope chunks that were intentionally left UNEMBEDDED (small
	// conversation docs, code/config — runPipeline's skipEmbed). They live in
	// neither Qdrant nor the dense brute-force set, so without this the
	// search_knowledge_base tool and inject-mode retrieval couldn't find a
	// freshly-uploaded small/code file at all — only auto-mode's pinned injection
	// covered them. Conversation-scoped only (KB docs always embed). (§4.11 skip-embed)
	if convID != "" {
		seen := make(map[string]bool, len(cands))
		for _, c := range cands {
			seen[c.chunkID] = true
		}
		for _, c := range s.keywordOnlyUnembedded(ctx, kbIDs, convID, terms) {
			if !seen[c.chunkID] {
				cands = append(cands, c)
			}
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}

	cfg := s.ragSettings()
	ranked := fuseReciprocalRank(cands)
	if cfg.DynamicTopK {
		// Inject EVERY hit whose cosine similarity clears the cutoff — no fixed K,
		// no cap (§admin RAG). Keyword-only hits (sim 0) don't qualify here.
		cut := float32(cfg.SimThreshold)
		kept := make([]retrievalCandidate, 0, len(ranked))
		for _, c := range ranked {
			if c.sim >= cut {
				kept = append(kept, c)
			}
		}
		ranked = kept
	} else {
		k := cfg.TopK
		if k <= 0 {
			k = topK
		}
		if k > 0 && len(ranked) > k {
			ranked = ranked[:k]
		}
	}

	result := []Snippet{}
	seenParent := map[string]bool{}
	for _, c := range ranked {
		// Small-to-big: inject the FULL parent section for a retrieved hit (no
		// per-snippet truncation — §admin RAG "检索到的全量注入"); one section per
		// parent (deduped on its top-ranked child) so distinct sections aren't
		// crowded out. Falls back to the child's own text when there's no parent.
		snippet := c.content
		if c.parentID != "" {
			if seenParent[c.parentID] {
				continue
			}
			seenParent[c.parentID] = true
			if parent, _ := store.GetChunkContent(ctx, s.db, c.parentID); strings.TrimSpace(parent) != "" {
				snippet = parent
			}
		}
		result = append(result, Snippet{
			ID:      c.chunkID,
			Index:   len(result) + 1,
			Title:   c.filename,
			URL:     "doc://" + c.documentID,
			Snippet: snippet,
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

// keywordOnlyUnembedded returns in-scope CHILD chunks that were intentionally not
// embedded (runPipeline's skipEmbed: small conversation docs, code/config),
// scored by keyword overlap only. Such chunks have no vector in Qdrant and are
// skipped by the dense brute-force leg, so this is the ONLY way the
// search_knowledge_base tool / inject-mode retrieval can reach them. Only chunks
// that actually match the query (bm > 0) are surfaced — a query-driven search
// shouldn't dump every unembedded chunk into context.
func (s *Service) keywordOnlyUnembedded(ctx context.Context, kbIDs []string, convID string, terms []string) []retrievalCandidate {
	if len(terms) == 0 {
		return nil
	}
	rows, err := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
	if err != nil {
		return nil
	}
	out := []retrievalCandidate{}
	for _, r := range rows {
		// Parents carry no own text vector; embedded children are already covered
		// by the dense/keyword legs above.
		if r.ChunkType == "parent" || len(r.Embedding) != 0 {
			continue
		}
		bm := keywordScore(terms, r.Content)
		if bm <= 0 {
			continue
		}
		out = append(out, retrievalCandidate{
			chunkID:    r.ID,
			documentID: r.DocumentID,
			parentID:   r.ParentID,
			filename:   r.Filename,
			content:    r.Content,
			sim:        0,
			bm:         bm,
		})
	}
	return out
}

// searchScope runs the dense + keyword retrieval legs for ONE embedding model over
// the given vector scope, returning fusion-input candidates. Factored out of
// Retrieve so a query whose scope spans sources embedded by DIFFERENT models (a
// KB's locked model vs the global model used for conversation uploads) searches
// each with its OWN query vector — never scoring one model's vectors against
// another's, nor missing a doc whose vectors sit in a different per-dim
// collection. (§4.11 model split)
func (s *Service) searchScope(ctx context.Context, userID, convID string, em Embedder, emName string, dim int, scope vector.Scope, query string, terms []string) ([]retrievalCandidate, error) {
	qVec, cached, err := s.embedQueryCached(ctx, em, emName, query)
	if err != nil {
		return nil, err
	}
	// Trust the actual query-vector width over the configured dim (a model that
	// emits 1024 despite a 1536 config still hits the right collection).
	if len(qVec) > 0 && len(qVec) != dim {
		dim = len(qVec)
	}
	// Query embedding is billable (§8.3) — but only when we actually called the API
	// (no call on a query-vector cache hit, or for the local embedder).
	if !cached && !strings.HasPrefix(emName, "aurelia-local") && userID != "" {
		_ = store.LogUsage(ctx, s.db, store.UsageLog{
			UserID: userID, ConversationID: convID,
			ModelID: strings.TrimPrefix(emName, "emb:"),
			Purpose: "embedding", InputTokens: estimateTokens(query),
		})
	}
	if !s.vec.Enabled() {
		// Dev / no-Qdrant: brute-force cosine over the relational vector copy.
		return s.bruteForceCandidates(ctx, scope.KBIDs, scope.ConversationID, qVec, terms)
	}
	// §4.11-E independent legs: 30 dense ∥ 30 keyword, fused later; the same scope
	// so a chunk that hits in only one leg survives.
	hits, err := s.vec.Search(ctx, dim, qVec, scope, 30)
	if err != nil {
		s.logger.Printf("rag: qdrant search failed (%v) — falling back to Postgres brute-force", err)
		return s.bruteForceCandidates(ctx, scope.KBIDs, scope.ConversationID, qVec, terms)
	}
	kwHits, _ := s.vec.SearchKeyword(ctx, dim, query, scope, 30)
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
	out := make([]retrievalCandidate, 0, len(merged))
	for _, c := range merged {
		out = append(out, c)
	}
	return out, nil
}

// appendUniqueCandidates appends candidates from src not already in dst (by chunk
// id), used to merge two model-group searches without double-counting.
func appendUniqueCandidates(dst, src []retrievalCandidate) []retrievalCandidate {
	if len(src) == 0 {
		return dst
	}
	seen := make(map[string]bool, len(dst))
	for _, c := range dst {
		seen[c.chunkID] = true
	}
	for _, c := range src {
		if !seen[c.chunkID] {
			dst = append(dst, c)
			seen[c.chunkID] = true
		}
	}
	return dst
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
		// Rune-safe cut — a byte slice mid-rune produces invalid UTF-8 (mojibake /
		// a Postgres-rejected trailing byte) for CJK content.
		return s[:clampRune(s, max-1)] + "…"
	}
	return s
}

// clampRune clamps a byte offset into [0,len(s)] and snaps it forward to a UTF-8
// rune boundary so substring slices never split a multi-byte (e.g. CJK) rune.
func clampRune(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// Budget (bytes) for a retrieved chunk's injected snippet. Sized to hold a full
// child chunk (childTargetChars) plus a little surrounding section context, so a
// hit deep in a long section is shown in full rather than truncated to the
// section head. (§4.11 retrieval fidelity)
const retrievedSnippetChars = 2000

// expandHit returns a snippet that is GUARANTEED to contain the matched child
// chunk, with surrounding section context when available. The old small-to-big
// path returned snippetOf(parent, …) — i.e. always the SECTION HEAD — so a hit
// located deep in a long section, or past the parent's truncation, was dropped
// from what the model saw (it "matched 案例98 but couldn't answer"). Here we find
// the child inside its parent section and center a budget-sized window on it;
// when the child lies beyond the parent's truncation we return the child itself.
func expandHit(parent, child string, budget int) string {
	child = strings.TrimSpace(child)
	if parent != "" {
		needle := stripBreadcrumb(child)
		if needle != "" {
			if idx := strings.Index(parent, needle); idx >= 0 {
				win := windowAround(parent, idx, idx+len(needle), budget)
				if bc := breadcrumbOf(child); bc != "" {
					win = bc + " " + win
				}
				return win
			}
		}
	}
	// No parent, or the child sits past the parent's truncation → the child IS
	// the hit; return it directly.
	return snippetOf(child, budget)
}

// windowAround returns a rune-safe, budget-sized window of s spanning the byte
// range [start,end], centered on it, with ellipses where trimmed and newlines
// collapsed.
func windowAround(s string, start, end, budget int) string {
	if end-start >= budget {
		return snippetOf(s[clampRune(s, start):], budget)
	}
	pad := (budget - (end - start)) / 2
	ws := clampRune(s, start-pad)
	we := clampRune(s, end+pad)
	out := strings.TrimSpace(strings.ReplaceAll(s[ws:we], "\n", " "))
	if ws > 0 {
		out = "…" + out
	}
	if we < len(s) {
		out = out + "…"
	}
	return out
}

// stripBreadcrumb removes the leading "[breadcrumb]\n" prefix added to a child at
// ingest, so the child text can be located inside its (un-prefixed) parent.
func stripBreadcrumb(s string) string {
	if strings.HasPrefix(s, "[") {
		if nl := strings.IndexByte(s, '\n'); nl > 0 && strings.IndexByte(s[:nl], ']') > 0 {
			return strings.TrimSpace(s[nl+1:])
		}
	}
	return s
}

// breadcrumbOf returns the "[breadcrumb]" prefix of a child chunk (heading path),
// or "" when absent.
func breadcrumbOf(s string) string {
	if strings.HasPrefix(s, "[") {
		if nl := strings.IndexByte(s, '\n'); nl > 0 && strings.IndexByte(s[:nl], ']') > 0 {
			return strings.TrimSpace(s[:nl])
		}
	}
	return ""
}

// tokenize splits text into lexical terms for keyword scoring AND the hashed
// local embedder. ASCII/Latin words are kept whole (so existing Latin indexes +
// embeddings are byte-for-byte unchanged), but spaceless CJK is segmented into
// overlapping bigrams (plus any embedded digits/Latin).
//
// Why: the old `[\p{L}\p{N}_]+` regex collapsed an entire Chinese phrase into ONE
// token (Han chars are \p{L} with no spaces between them). `keywordScore` then did
// `strings.Count(doc, term)` with that whole-phrase term, which never matched, and
// the FNV-hashed embedder put the whole phrase in one bucket — so a reference
// buried inside the phrase, e.g. "案例98" inside "讲解案例98及相关知识点", was
// unretrievable. Bigram segmentation makes "案例"/"98"/"知识"… matchable units that
// a query and a document share. (§4.11 CJK retrieval)
func tokenize(s string) []string {
	re := regexp.MustCompile(`[\p{L}\p{N}_]+`)
	runs := re.FindAllString(s, -1)
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		if !hasCJK(run) {
			out = append(out, run) // Latin/alphanumeric — unchanged
			continue
		}
		out = append(out, cjkGrams(run)...)
	}
	return out
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func hasCJK(s string) bool {
	for _, r := range s {
		if isCJKRune(r) {
			return true
		}
	}
	return false
}

// estimateTokens approximates a string's token count. The plain byte-len/4
// heuristic is tuned for English and badly UNDER-counts CJK: each Han/Kana/Hangul
// char is ~3 UTF-8 bytes but ~1 token, so len/4 scores it ~0.75 tokens — which let
// long Chinese documents be misjudged as "fits the full-text window" and silently
// overflow the prompt. We count CJK runes as ~1 token each and apply byte/4 to the
// rest. (§4.11 token budgeting)
func estimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if isCJKRune(r) {
			cjk++
		} else {
			other += utf8.RuneLen(r)
		}
	}
	return cjk + other/4
}

// cjkGrams segments a mixed CJK run: maximal CJK spans become overlapping bigrams
// (a lone CJK char → itself), and ASCII/Latin/digit spans are kept whole (so an
// id like "98" inside "案例98" survives as its own matchable token).
func cjkGrams(run string) []string {
	runes := []rune(run)
	out := []string{}
	for i := 0; i < len(runes); {
		if isCJKRune(runes[i]) {
			j := i
			for j < len(runes) && isCJKRune(runes[j]) {
				j++
			}
			seg := runes[i:j]
			if len(seg) == 1 {
				out = append(out, string(seg))
			} else {
				for k := 0; k+1 < len(seg); k++ {
					out = append(out, string(seg[k:k+2]))
				}
			}
			i = j
		} else {
			j := i
			for j < len(runes) && !isCJKRune(runes[j]) {
				j++
			}
			out = append(out, string(runes[i:j]))
			i = j
		}
	}
	return out
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
	for _, m := range matches {
		// Body BEFORE this heading line. For the first heading this is the document
		// preamble (frontmatter / abstract / intro / title blurb) — keep it with an
		// empty breadcrumb (stack is empty here) instead of dropping it; for later
		// headings it's the previous section's body under the current stack.
		headingStart := m[0]
		body := content[cursor:headingStart]
		if strings.TrimSpace(body) != "" {
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
	return s[:clampRune(s, max)]
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

// RAG knobs (§4.11-B) are admin-tunable from the Documents settings page and read
// live from the settings table (no restart). Defaults inject only genuinely small
// docs in full and send everything larger to RETRIEVAL — a whole medium file
// injected every turn is what blew the prompt to 70k+.
const (
	// defaultRAGFullTextThreshold: a conversation doc whose estimated tokens are
	// at/below this is injected in FULL (and not vectorised); above it the doc is
	// vectorised and only relevant chunks are retrieved.
	defaultRAGFullTextThreshold = 8000
	defaultRAGTopK              = 8
	defaultRAGSimThreshold      = 0.5
)

// ragSettings holds the live RAG configuration.
type ragSettings struct {
	FullTextThreshold int     // ≤ this (est. tokens) → inject whole; above → retrieve
	TopK              int     // chunks retrieved when DynamicTopK is off
	DynamicTopK       bool    // inject ALL hits with cosine sim ≥ SimThreshold instead of a fixed K
	SimThreshold      float64 // dynamic-topK cutoff (cosine similarity)
}

// ragSettings reads the admin-tunable RAG knobs (with safe defaults).
func (s *Service) ragSettings() ragSettings {
	c := ragSettings{FullTextThreshold: defaultRAGFullTextThreshold, TopK: defaultRAGTopK, SimThreshold: defaultRAGSimThreshold}
	if raw, err := store.GetSetting(s.db, "rag_full_text_threshold"); err == nil && len(raw) > 0 {
		var v int
		if json.Unmarshal(raw, &v) == nil && v > 0 {
			c.FullTextThreshold = v
		}
	}
	if raw, err := store.GetSetting(s.db, "rag_top_k"); err == nil && len(raw) > 0 {
		var v int
		if json.Unmarshal(raw, &v) == nil && v > 0 {
			c.TopK = v
		}
	}
	if raw, err := store.GetSetting(s.db, "rag_dynamic_topk"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.DynamicTopK)
	}
	if raw, err := store.GetSetting(s.db, "rag_similarity_threshold"); err == nil && len(raw) > 0 {
		var v float64
		if json.Unmarshal(raw, &v) == nil && v > 0 {
			c.SimThreshold = v
		}
	}
	return c
}

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
	cfg := s.ragSettings()

	// Step 0: size check + pinned docs. "Pinned" = in-scope child chunks with no
	// embedding — small conversation docs we intentionally did NOT vectorise (see
	// runPipeline). They can't be semantically retrieved, so they are ALWAYS
	// injected in full whenever we don't take the whole-scope full-text path.
	scope, _ := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
	pinned := []store.Chunk{}
	embeddedTokens := 0
	pinnedTokens := 0
	for _, c := range scope {
		if c.ChunkType == "parent" {
			continue // parents duplicate child text
		}
		if len(c.Embedding) == 0 {
			pinned = append(pinned, c)
			pinnedTokens += estimateTokens(c.Content)
		} else {
			embeddedTokens += estimateTokens(c.Content)
		}
	}
	// Whole scope fits (or nothing is embedded) → inject everything in full.
	if len(scope) > 0 && (embeddedTokens == 0 || pinnedTokens+embeddedTokens <= cfg.FullTextThreshold) {
		decision.Strategy = "full_text"
		return fullTextSnippets(scope), decision, nil
	}
	// Otherwise retrieve over the embedded chunks, but ALWAYS prepend the pinned
	// (unembedded) docs in full so they're never dropped from a large, mixed-size
	// conversation. No injection cap (§admin RAG) — cost is guarded by the
	// pre-flight credit check, not by truncation.
	pinnedSnips := fullTextSnippets(pinned)
	withPinned := func(out []Snippet) []Snippet {
		if len(pinnedSnips) == 0 {
			return out
		}
		merged := append(append([]Snippet{}, pinnedSnips...), out...)
		for i := range merged {
			merged[i].Index = i + 1
		}
		return merged
	}

	// Build a list of (filename, ~first sentence) so the router can resolve
	// pronouns like "this report" / "the second doc" (§4.11-B router prompt).
	docHints := s.collectDocHints(ctx, kbIDs, convID)

	if s.task == nil {
		out, err := s.Retrieve(ctx, userID, convID, kbIDs, userText, cfg.TopK)
		return withPinned(out), decision, err
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
		// Even when the router sees no need to retrieve, the pinned (small,
		// always-on) docs are still injected — the user uploaded them on purpose.
		return withPinned(nil), decision, nil
	case "full_doc":
		// Whole-document question → inject the entire document in order. No cap
		// (§admin RAG); the pre-flight credit check governs cost.
		return fullTextSnippets(scope), decision, nil
	default:
		// retrieve: run each rewritten query, merge + dedupe. With dynamic top-K
		// the per-query result is already similarity-bounded, so don't cap the
		// merge; with fixed K, cap the merged set at K.
		seen := map[string]struct{}{}
		merged := []Snippet{}
		queries := decision.Queries
		if len(queries) == 0 {
			queries = []string{userText}
		}
		for _, q := range queries {
			subset, err := s.Retrieve(ctx, userID, convID, kbIDs, q, cfg.TopK)
			if err != nil {
				continue
			}
			for _, sn := range subset {
				if _, ok := seen[sn.ID]; ok {
					continue
				}
				seen[sn.ID] = struct{}{}
				merged = append(merged, sn)
			}
			if !cfg.DynamicTopK && len(merged) >= cfg.TopK {
				break
			}
		}
		if !cfg.DynamicTopK && len(merged) > cfg.TopK {
			merged = merged[:cfg.TopK]
		}
		return withPinned(merged), decision, nil
	}
}

// fullTextSnippets returns the scope's child chunks in document order as
// snippets, capped at budget tokens (≈4 chars/token).
// fullTextSnippets returns the scope's child chunks in document order, each in
// FULL — no token budget / truncation (§admin RAG: "删除封顶"). Cost on a credit
// turn is bounded by the pre-flight estimate, not here.
func fullTextSnippets(scope []store.Chunk) []Snippet {
	out := []Snippet{}
	idx := 1
	for _, c := range scope {
		if c.ChunkType == "parent" {
			continue
		}
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
		t := estimateTokens(c.Content)
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
