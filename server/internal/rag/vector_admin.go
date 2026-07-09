package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"aurelia/server/internal/store"
	"aurelia/server/internal/vector"
)

const vectorIssueSampleLimit = 100

// VectorIssue is one DB chunk whose expected vector is not usable.
type VectorIssue struct {
	ChunkID        string `json:"chunk_id"`
	DocumentID     string `json:"document_id"`
	KBID           string `json:"kb_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Filename       string `json:"filename"`
	EmbeddingModel string `json:"embedding_model"`
	Dim            int    `json:"dim,omitempty"`
	Reason         string `json:"reason"`
}

// VectorAuditModel is the per embedding-model/dimension breakdown returned to
// admins. A model can appear under more than one dimension when legacy data was
// produced before a dim correction.
type VectorAuditModel struct {
	EmbeddingModel string `json:"embedding_model"`
	Dim            int    `json:"dim"`
	Total          int    `json:"total"`
	Present        int    `json:"present"`
	Missing        int    `json:"missing"`
	Empty          int    `json:"empty"`
	Skipped        int    `json:"skipped"`
}

// VectorAuditReport summarizes whether every embedded DB child chunk has a
// corresponding non-empty vector in Qdrant.
type VectorAuditReport struct {
	Total   int                `json:"total"`
	Present int                `json:"present"`
	Missing int                `json:"missing"`
	Empty   int                `json:"empty"`
	Skipped int                `json:"skipped"`
	Models  []VectorAuditModel `json:"models"`
	Issues  []VectorIssue      `json:"issues"`
}

// VectorRebuildProgress is emitted between embedding/upsert batches.
type VectorRebuildProgress struct {
	Total   int
	Rebuilt int
	Failed  int
}

// VectorRebuildReport reports the pre-rebuild audit plus rebuild outcome.
type VectorRebuildReport struct {
	Before  VectorAuditReport  `json:"before"`
	After   *VectorAuditReport `json:"after,omitempty"`
	Rebuilt int                `json:"rebuilt"`
	Failed  int                `json:"failed"`
	Issues  []VectorIssue      `json:"issues"`
}

type vectorIssueChunk struct {
	issue VectorIssue
	chunk store.EmbeddedChunk
	dim   int
}

type vectorModelBreakdownKey struct {
	model string
	dim   int
}

func (s *Service) VectorStoreEnabled() bool {
	return s != nil && s.vec != nil && s.vec.Enabled()
}

func (s *Service) AuditVectorIndex(ctx context.Context) (VectorAuditReport, error) {
	report, _, err := s.collectVectorIssues(ctx)
	return report, err
}

func (s *Service) RebuildMissingVectors(ctx context.Context, progress func(VectorRebuildProgress)) (VectorRebuildReport, error) {
	report, issues, err := s.collectVectorIssues(ctx)
	if err != nil {
		return VectorRebuildReport{}, err
	}
	out := VectorRebuildReport{Before: report}
	if len(issues) == 0 {
		return out, nil
	}
	groups := map[string][]vectorIssueChunk{}
	for _, item := range issues {
		if item.issue.Reason != "missing" && item.issue.Reason != "empty_vector" {
			continue
		}
		key := item.chunk.EmbeddingModel + "\x00" + fmt.Sprint(item.chunk.EmbeddingDim)
		groups[key] = append(groups[key], item)
	}
	total := 0
	for _, rows := range groups {
		total += len(rows)
	}
	emit := func() {
		if progress != nil {
			progress(VectorRebuildProgress{Total: total, Rebuilt: out.Rebuilt, Failed: out.Failed})
		}
	}
	emit()

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rows := groups[key]
		if len(rows) == 0 {
			continue
		}
		em, emName, _, err := s.resolveEmbedderForVectorChunk(ctx, rows[0].chunk)
		if err != nil {
			for _, row := range rows {
				out.Failed++
				out.Issues = appendLimitedVectorIssue(out.Issues, vectorIssueFromChunk(row.chunk, row.dim, "embedding_model_unavailable"))
			}
			emit()
			continue
		}
		const batchSize = 64
		for start := 0; start < len(rows); start += batchSize {
			end := start + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			batch := rows[start:end]
			texts := make([]string, 0, len(batch))
			for _, row := range batch {
				texts = append(texts, row.chunk.Content)
			}
			vecs, err := em.Embed(ctx, texts)
			if err != nil {
				for _, row := range batch {
					out.Failed++
					out.Issues = appendLimitedVectorIssue(out.Issues, vectorIssueFromChunk(row.chunk, row.dim, "embed_failed"))
				}
				emit()
				continue
			}
			pointsByDim := map[int][]vector.Point{}
			pointChunks := map[int][]store.EmbeddedChunk{}
			for i, row := range batch {
				if i >= len(vecs) || len(vecs[i]) == 0 {
					out.Failed++
					out.Issues = appendLimitedVectorIssue(out.Issues, vectorIssueFromChunk(row.chunk, row.dim, "empty_embedding"))
					continue
				}
				actualDim := len(vecs[i])
				if row.chunk.KBID != "" && row.chunk.EmbeddingDim != actualDim {
					if err := store.SetKBEmbeddingDim(ctx, s.db, row.chunk.KBID, actualDim); err != nil && s.logger != nil {
						s.logger.Printf("rag: persist rebuilt embedding_dim for kb %s: %v", row.chunk.KBID, err)
					}
				}
				pointsByDim[actualDim] = append(pointsByDim[actualDim], vectorPointFromEmbeddedChunk(row.chunk, vecs[i]))
				pointChunks[actualDim] = append(pointChunks[actualDim], row.chunk)
			}
			dims := make([]int, 0, len(pointsByDim))
			for dim := range pointsByDim {
				dims = append(dims, dim)
			}
			sort.Ints(dims)
			for _, dim := range dims {
				points := pointsByDim[dim]
				if len(points) == 0 {
					continue
				}
				if err := s.vec.Upsert(ctx, dim, points); err != nil {
					out.Failed += len(points)
					for _, ch := range pointChunks[dim] {
						out.Issues = appendLimitedVectorIssue(out.Issues, vectorIssueFromChunk(ch, dim, "upsert_failed"))
					}
					if s.logger != nil {
						s.logger.Printf("rag: admin vector rebuild upsert failed for %s (%dd, %d points): %v", emName, dim, len(points), err)
					}
					continue
				}
				out.Rebuilt += len(points)
			}
			emit()
		}
	}
	if after, _, err := s.collectVectorIssues(ctx); err == nil {
		out.After = &after
	} else if s.logger != nil {
		s.logger.Printf("rag: audit after vector rebuild failed: %v", err)
	}
	return out, nil
}

func (s *Service) collectVectorIssues(ctx context.Context) (VectorAuditReport, []vectorIssueChunk, error) {
	report := VectorAuditReport{}
	if !s.VectorStoreEnabled() {
		return report, nil, errVectorBackendUnavailable
	}
	rows, err := store.ListEmbeddedChildChunks(ctx, s.db)
	if err != nil {
		return report, nil, err
	}
	report.Total = len(rows)
	breakdowns := map[vectorModelBreakdownKey]*VectorAuditModel{}
	byDim := map[int][]store.EmbeddedChunk{}
	for _, ch := range rows {
		dim, err := s.vectorDimForEmbeddedChunk(ctx, ch)
		key := vectorModelBreakdownKey{model: ch.EmbeddingModel, dim: dim}
		if err != nil {
			key.dim = 0
			b := vectorBreakdown(breakdowns, key)
			b.Total++
			b.Skipped++
			report.Skipped++
			report.Issues = appendLimitedVectorIssue(report.Issues, vectorIssueFromChunk(ch, 0, "embedding_model_unavailable"))
			continue
		}
		b := vectorBreakdown(breakdowns, key)
		b.Total++
		byDim[dim] = append(byDim[dim], ch)
	}

	issues := []vectorIssueChunk{}
	dims := make([]int, 0, len(byDim))
	for dim := range byDim {
		dims = append(dims, dim)
	}
	sort.Ints(dims)
	for _, dim := range dims {
		status, err := s.vec.VectorChunkStatuses(ctx, dim, vector.Scope{})
		if err != nil {
			return report, issues, err
		}
		for _, ch := range byDim[dim] {
			key := vectorModelBreakdownKey{model: ch.EmbeddingModel, dim: dim}
			b := vectorBreakdown(breakdowns, key)
			st, ok := status[ch.ID]
			switch {
			case !ok || !st.Exists:
				report.Missing++
				b.Missing++
				issue := vectorIssueFromChunk(ch, dim, "missing")
				report.Issues = appendLimitedVectorIssue(report.Issues, issue)
				issues = append(issues, vectorIssueChunk{issue: issue, chunk: ch, dim: dim})
			case !st.HasVector:
				report.Empty++
				b.Empty++
				issue := vectorIssueFromChunk(ch, dim, "empty_vector")
				report.Issues = appendLimitedVectorIssue(report.Issues, issue)
				issues = append(issues, vectorIssueChunk{issue: issue, chunk: ch, dim: dim})
			default:
				report.Present++
				b.Present++
			}
		}
	}
	keys := make([]vectorModelBreakdownKey, 0, len(breakdowns))
	for key := range breakdowns {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		return keys[i].dim < keys[j].dim
	})
	for _, key := range keys {
		report.Models = append(report.Models, *breakdowns[key])
	}
	return report, issues, nil
}

func vectorBreakdown(m map[vectorModelBreakdownKey]*VectorAuditModel, key vectorModelBreakdownKey) *VectorAuditModel {
	if b := m[key]; b != nil {
		return b
	}
	b := &VectorAuditModel{EmbeddingModel: key.model, Dim: key.dim}
	m[key] = b
	return b
}

func appendLimitedVectorIssue(issues []VectorIssue, issue VectorIssue) []VectorIssue {
	if len(issues) >= vectorIssueSampleLimit {
		return issues
	}
	return append(issues, issue)
}

func vectorIssueFromChunk(ch store.EmbeddedChunk, dim int, reason string) VectorIssue {
	return VectorIssue{
		ChunkID:        ch.ID,
		DocumentID:     ch.DocumentID,
		KBID:           ch.KBID,
		ConversationID: ch.ConversationID,
		Filename:       ch.Filename,
		EmbeddingModel: ch.EmbeddingModel,
		Dim:            dim,
		Reason:         reason,
	}
}

func vectorPointFromEmbeddedChunk(ch store.EmbeddedChunk, vec []float32) vector.Point {
	return vector.Point{
		ChunkID: ch.ID,
		Vector:  vec,
		Payload: vector.Payload{
			DocumentID:     ch.DocumentID,
			KBID:           ch.KBID,
			ConversationID: ch.ConversationID,
			ParentID:       ch.ParentID,
			ChunkType:      ch.ChunkType,
			Seq:            ch.Seq,
			Content:        ch.Content,
			Filename:       ch.Filename,
		},
	}
}

func (s *Service) vectorDimForEmbeddedChunk(ctx context.Context, ch store.EmbeddedChunk) (int, error) {
	_, _, dim, err := s.resolveEmbedderForVectorChunk(ctx, ch)
	if err != nil {
		return 0, err
	}
	return dim, nil
}

func (s *Service) resolveEmbedderForVectorChunk(ctx context.Context, ch store.EmbeddedChunk) (Embedder, string, int, error) {
	name := strings.TrimSpace(ch.EmbeddingModel)
	switch {
	case name == "":
		return nil, "", 0, fmt.Errorf("empty embedding model")
	case name == "aurelia-local-embed":
		return NewLocalEmbedder(localEmbedDim), name, localEmbedDim, nil
	case name == "emb:env":
		if s.embAPIKey == "" {
			return nil, name, 0, fmt.Errorf("env embedding backend is not configured")
		}
		dim := ch.EmbeddingDim
		if dim <= 0 {
			dim = s.embDim
		}
		if dim <= 0 {
			dim = 1536
		}
		return &httpEmbedder{baseURL: s.embBaseURL, apiKey: s.embAPIKey, model: s.embModel, dim: dim}, name, dim, nil
	case strings.HasPrefix(name, "emb:"):
		modelID := strings.TrimPrefix(name, "emb:")
		m, err := store.GetModel(ctx, s.db, modelID)
		if err != nil || !m.Enabled || m.Kind != "embedding" {
			return nil, name, 0, fmt.Errorf("embedding model %s missing/disabled", modelID)
		}
		chRow, err := store.GetChannel(ctx, s.db, m.ChannelID)
		if err != nil || chRow.APIKey == "" {
			return nil, name, 0, fmt.Errorf("embedding model %s channel missing api key", modelID)
		}
		dim := ch.EmbeddingDim
		if dim <= 0 {
			dim = m.Dim
		}
		if dim <= 0 {
			dim = 1536
		}
		return &httpEmbedder{baseURL: chRow.BaseURL, apiKey: chRow.APIKey, model: m.RequestID, dim: dim}, name, dim, nil
	default:
		return nil, name, 0, fmt.Errorf("unknown embedding model %q", name)
	}
}
