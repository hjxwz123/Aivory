package api

import (
	"errors"
	"net/http"
	"strings"

	"aurelia/server/internal/store"
)

// listProjectsHandler returns the user's projects.
func listProjectsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	rows, err := store.ListProjects(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

type createProjectReq struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	Accent       string `json:"accent"`
	Emoji        string `json:"emoji"`
}

// createProjectHandler creates a project + its dedicated knowledge base.
func createProjectHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req createProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	// Find embedding model.
	embeds, err := store.ListModels(r.Context(), d.DB, "embedding", true)
	if err != nil || len(embeds) == 0 {
		// Allow project without KB if no embedding model.
		p, err := store.CreateProject(r.Context(), d.DB, store.Project{
			UserID: u.ID, Name: req.Name, Description: req.Description, Instructions: req.Instructions,
			Accent: req.Accent, Emoji: req.Emoji,
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, p)
		return
	}
	kb, err := store.CreateKB(r.Context(), d.DB, store.KnowledgeBase{
		UserID: u.ID, Name: req.Name + " — project library",
		EmbeddingModelID: embeds[0].ID, EmbeddingDim: embeds[0].Dim,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	p, err := store.CreateProject(r.Context(), d.DB, store.Project{
		UserID: u.ID, Name: req.Name, Description: req.Description, Instructions: req.Instructions,
		Accent: req.Accent, Emoji: req.Emoji, KBID: kb.ID,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, p)
}

// getProjectHandler returns one project + its docs and conversations.
func getProjectHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	p, err := store.GetProject(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	docs := []store.Document{}
	if p.KBID != "" {
		docs, _ = store.ListDocuments(r.Context(), d.DB, "kb", p.KBID)
	}
	convs, _ := store.ListConversations(r.Context(), d.DB, u.ID, p.ID, "active")
	writeJSON(w, 200, map[string]any{
		"project":       p,
		"documents":     docs,
		"conversations": convs,
	})
}

// updateProjectHandler edits selected fields.
func updateProjectHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	var p store.ProjectPatch
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	upd, err := store.UpdateProject(r.Context(), d.DB, id, u.ID, p)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, upd)
}

// deleteProjectHandler removes the project.
func deleteProjectHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if err := store.DeleteProject(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// listProjectDocsHandler returns documents in the project's KB.
func listProjectDocsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	p, err := store.GetProject(r.Context(), d.DB, id, u.ID)
	if err != nil || p.KBID == "" {
		writeError(w, 404, errNotFound)
		return
	}
	docs, _ := store.ListDocuments(r.Context(), d.DB, "kb", p.KBID)
	writeJSON(w, 200, docs)
}

// uploadProjectDocHandler ingests a new document into the project KB.
func uploadProjectDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	p, err := store.GetProject(r.Context(), d.DB, id, u.ID)
	if err != nil || p.KBID == "" {
		writeError(w, 404, errNotFound)
		return
	}
	doc, err := receiveDocument(d, r, p.KBID, "")
	if err != nil {
		writeError(w, 400, err)
		return
	}
	d.RAG.Ingest(doc.ID)
	writeJSON(w, 201, doc)
}

// deleteProjectDocHandler removes a document from the project KB.
func deleteProjectDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	docID := pathParam(r, "docId")
	p, err := store.GetProject(r.Context(), d.DB, id, u.ID)
	if err != nil || p.KBID == "" {
		writeError(w, 404, errNotFound)
		return
	}
	doc, err := store.GetDocument(r.Context(), d.DB, docID)
	if err != nil || doc.KBID != p.KBID {
		writeError(w, 404, errNotFound)
		return
	}
	_ = store.DeleteDocument(r.Context(), d.DB, docID)
	if err := d.RAG.OnDocumentDeleted(r.Context(), docID); err != nil {
		d.Logger.Printf("rag: drop vectors for doc %s: %v", docID, err)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
