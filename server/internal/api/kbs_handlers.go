package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

var kbDocUploadRateLimit = envcfg.Int("AIVORY_API_RATE_LIMIT_USER", 20)

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
		rows, err = store.ListWorkspaceKBs(r.Context(), d.DB, wsID)
	} else {
		rows, err = store.ListKBs(r.Context(), d.DB, u.ID)
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

type createKBReq struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	EmbeddingModelID string `json:"embedding_model_id"`
	// '' = personal; set = shared workspace KB (§workspaces).
	WorkspaceID string `json:"workspace_id"`
}

// createKBHandler creates a new KB pinned to one embedding model.
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
	// Enforce the group's knowledge-base cap (§ user groups). 0 = unlimited.
	// Only standalone KBs count — project libraries are governed by the project
	// cap.
	if _, maxKBs := groupCapFor(d, r, u.ID, u.GroupID); maxKBs > 0 {
		if n, err := store.CountStandaloneKBsByUser(r.Context(), d.DB, u.ID); err == nil && n >= maxKBs {
			writeError(w, 403, errKBLimit)
			return
		}
	}
	if req.EmbeddingModelID == "" {
		embeds, _ := store.ListModels(r.Context(), d.DB, "embedding", true)
		if len(embeds) == 0 {
			writeError(w, 400, errors.New("no embedding model configured"))
			return
		}
		req.EmbeddingModelID = embeds[0].ID
	}
	m, err := store.GetModel(r.Context(), d.DB, req.EmbeddingModelID)
	if err != nil {
		writeError(w, 400, errors.New("unknown embedding model"))
		return
	}
	kb, err := store.CreateKB(r.Context(), d.DB, store.KnowledgeBase{
		UserID:           u.ID,
		Name:             req.Name,
		Description:      req.Description,
		EmbeddingModelID: m.ID,
		EmbeddingDim:     m.Dim,
		WorkspaceID:      req.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, store.ErrKBNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, kb)
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
	if err := store.DeleteKB(r.Context(), d.DB, id, u.ID); err != nil {
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
		writeError(w, 400, err)
		return
	}
	d.RAG.Ingest(doc.ID)
	writeJSON(w, 201, doc)
}

// listKBDocsHandler returns documents within a KB.
func listKBDocsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if _, err := store.GetKB(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	docs, err := store.ListDocuments(r.Context(), d.DB, "kb", id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, docs)
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
	doc, err := store.GetDocument(r.Context(), d.DB, docID)
	if err != nil || doc.KBID != id {
		writeError(w, 404, errNotFound)
		return
	}
	_ = store.DeleteDocument(r.Context(), d.DB, docID)
	cleanupRAGDocument(r.Context(), d, docID, "delete kb document "+docID)
	cleanupStoragePaths(r.Context(), d, []string{doc.StoragePath}, "delete kb document "+docID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
