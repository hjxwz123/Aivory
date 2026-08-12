package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

var kbDocUploadRateLimit = envcfg.Int("AIVORY_API_RATE_LIMIT_USER", 20)

// knowledgeBaseResponse is the ordinary user-facing shape. Retrieval model
// identities and vector dimensions stay server-side and remain available to
// administrators through their dedicated endpoints.
type knowledgeBaseResponse struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ProjectID   string `json:"project_id"`
	CreatedAt   int64  `json:"created_at"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

func userKnowledgeBase(kb store.KnowledgeBase) knowledgeBaseResponse {
	return knowledgeBaseResponse{
		ID: kb.ID, UserID: kb.UserID, Name: kb.Name, Description: kb.Description,
		ProjectID: kb.ProjectID, CreatedAt: kb.CreatedAt, WorkspaceID: kb.WorkspaceID,
	}
}

func userKnowledgeBases(rows []store.KnowledgeBase) []knowledgeBaseResponse {
	items := make([]knowledgeBaseResponse, 0, len(rows))
	for _, kb := range rows {
		items = append(items, userKnowledgeBase(kb))
	}
	return items
}

// userDocuments removes internal ingest diagnostics from ordinary user
// responses. Administrators retain the original store.Document payload through
// their dedicated drill-down endpoints.
func userDocuments(rows []store.Document) []store.Document {
	items := make([]store.Document, len(rows))
	copy(items, rows)
	for i := range items {
		items[i].Error = ""
	}
	return items
}

func userDocument(doc *store.Document) *store.Document {
	if doc == nil {
		return nil
	}
	item := *doc
	item.Error = ""
	return &item
}

func configuredEmbeddingModel(ctx context.Context, d Deps) (*store.Model, error) {
	var modelID string
	raw, err := store.GetSetting(d.DB, "embedding_model_id")
	if err != nil || json.Unmarshal(raw, &modelID) != nil || strings.TrimSpace(modelID) == "" {
		return nil, errKnowledgeBaseUnavailable
	}
	model, err := store.GetModel(ctx, d.DB, strings.TrimSpace(modelID))
	if err != nil || model == nil || !model.Enabled || model.Kind != "embedding" {
		return nil, errKnowledgeBaseUnavailable
	}
	return model, nil
}

// listKBsHandler returns the user's knowledge bases.
func listKBsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	// Workspace scope (§workspaces): a member lists the space's shared KBs;
	// otherwise the personal ones. Inside a workspace, personal KBs are unusable.
	var rows []store.KnowledgeBase
	var err error
	if wsID := strings.TrimSpace(r.URL.Query().Get("workspace_id")); wsID != "" {
		if role, merr := store.IsWorkspaceMember(r.Context(), d.DB, wsID, u.ID); merr != nil || role == "" {
			writeError(w, 404, errNotFound)
			return
		}
		rows, err = store.ListWorkspaceKBsForUser(r.Context(), d.DB, wsID, u.ID)
	} else {
		rows, err = store.ListKBs(r.Context(), d.DB, u.ID)
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, userKnowledgeBases(rows))
}

type createKBReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// '' = personal; set = shared workspace KB (§workspaces).
	WorkspaceID string `json:"workspace_id"`
}

// createKBHandler creates a new KB pinned to the administrator-configured
// embedding model. Unknown request fields are ignored for backwards
// compatibility and cannot override the administrator setting.
func createKBHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req createKBReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if req.Name = strings.TrimSpace(req.Name); req.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	if req.WorkspaceID != "" {
		if role, merr := store.IsWorkspaceMember(r.Context(), d.DB, req.WorkspaceID, u.ID); merr != nil || role == "" {
			writeError(w, 404, errNotFound)
			return
		}
	}
	if existing, err := store.GetKBByName(r.Context(), d.DB, u.ID, req.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrKBNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	// The store re-evaluates this standalone-KB cap while holding the creator
	// row lock and inserts in the same transaction. 0 remains unlimited.
	_, maxKBs := groupCapFor(d, r, u.ID, u.GroupID)
	m, err := configuredEmbeddingModel(r.Context(), d)
	if err != nil {
		writeError(w, http.StatusConflict, errKnowledgeBaseUnavailable)
		return
	}
	kb, err := store.CreateKBWithLimit(r.Context(), d.DB, store.KnowledgeBase{
		UserID:           u.ID,
		Name:             req.Name,
		Description:      req.Description,
		EmbeddingModelID: m.ID,
		EmbeddingDim:     m.Dim,
		WorkspaceID:      req.WorkspaceID,
	}, maxKBs)
	if err != nil {
		if errors.Is(err, store.ErrKBLimitExceeded) {
			writeError(w, http.StatusForbidden, errKBLimit)
			return
		}
		if errors.Is(err, store.ErrKBNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, userKnowledgeBase(*kb))
}

// deleteKBHandler removes the KB and cascades to docs and chunks.
func deleteKBHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	docs, _ := store.ListDocuments(r.Context(), d.DB, "kb", id)
	storagePaths := make([]string, 0, len(docs))
	for _, doc := range docs {
		storagePaths = append(storagePaths, doc.StoragePath)
	}
	if err := store.DeleteKB(r.Context(), d.DB, id, u.ID, d.Config.UploadDir, d.Config.ArtifactDir); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	// Keep the vector backend in sync with the cascaded chunk deletes.
	cleanupRAGKB(r.Context(), d, id, "delete kb "+id)
	cleanupStoragePaths(r.Context(), d, storagePaths, "delete kb "+id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// uploadKBDocHandler accepts a document into the KB and enqueues parsing.
func uploadKBDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !rateLimitUser(d, u.ID, "upload", kbDocUploadRateLimit, time.Minute) { // §C4
		writeError(w, 429, errUploadRateLimited)
		return
	}
	id := pathParam(r, "id")
	if _, err := store.GetKB(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, d.Config.MaxUploadBytes+1<<20) // §C3
	doc, err := receiveDocument(d, r, id, "")
	if err != nil {
		status := 400
		if errors.Is(err, errStorageQuotaExceeded) {
			status = http.StatusInsufficientStorage
		}
		writeError(w, status, err)
		return
	}
	d.RAG.Ingest(doc.ID)
	writeJSON(w, 201, userDocument(doc))
}

// listKBDocsHandler returns documents within a KB.
func listKBDocsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if _, err := store.GetKB(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	docs, err := store.ListDocumentsForUser(r.Context(), d.DB, "kb", id, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, userDocuments(docs))
}

// retryKBDocHandler requeues a failed knowledge-base document in the existing
// ingest pipeline. It does not create a second parser or duplicate the upload.
func retryKBDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	kbID := pathParam(r, "id")
	docID := pathParam(r, "docId")
	if _, err := store.GetKB(r.Context(), d.DB, kbID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	doc, err := store.GetDocumentForUser(r.Context(), d.DB, docID, u.ID)
	if err != nil || doc.KBID != kbID {
		writeError(w, 404, errNotFound)
		return
	}
	if doc.Status != "failed" {
		writeError(w, 409, errors.New("document is not failed"))
		return
	}
	if d.RAG == nil {
		writeError(w, 503, errors.New("rag service is unavailable"))
		return
	}
	if err := store.RetryKBDocumentForUser(r.Context(), d.DB, docID, kbID, u.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	d.RAG.IngestNow(docID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// deleteKBDocHandler removes a single document.
func deleteKBDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	docID := pathParam(r, "docId")
	if _, err := store.GetKB(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	doc, err := store.GetDocumentForUser(r.Context(), d.DB, docID, u.ID)
	if err != nil || doc.KBID != id {
		writeError(w, 404, errNotFound)
		return
	}
	if err := store.DeleteDocumentForUser(r.Context(), d.DB, docID, "kb", id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	cleanupRAGDocument(r.Context(), d, docID, "delete kb document "+docID)
	cleanupStoragePaths(r.Context(), d, []string{doc.StoragePath}, "delete kb document "+docID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
