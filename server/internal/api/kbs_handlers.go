package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"aurelia/server/internal/store"
)

// listKBsHandler returns the user's knowledge bases.
func listKBsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	rows, err := store.ListKBs(r.Context(), d.DB, u.ID)
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
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, kb)
}

// deleteKBHandler removes the KB and cascades to docs and chunks.
func deleteKBHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if err := store.DeleteKB(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	// Keep the vector backend in sync with the cascaded chunk deletes.
	if err := d.RAG.OnKBDeleted(r.Context(), id); err != nil {
		d.Logger.Printf("rag: drop vectors for kb %s: %v", id, err)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// uploadKBDocHandler accepts a document into the KB and enqueues parsing.
func uploadKBDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !rateLimitUser(d, u.ID, "upload", 20, time.Minute) { // §C4
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
	if err := d.RAG.OnDocumentDeleted(r.Context(), docID); err != nil {
		d.Logger.Printf("rag: drop vectors for doc %s: %v", docID, err)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
