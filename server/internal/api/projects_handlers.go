package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

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

// groupCapFor returns the user's effective per-group resource caps (§ user
// groups). 0 = unlimited. Failures fail OPEN (0/unlimited) so a transient DB
// error never blocks a legitimate create.
func groupCapFor(d Deps, r *http.Request, userID, groupID string) (maxProjects, maxKBs int) {
	gid := groupID
	if gid == "" {
		gid = store.DefaultGroupID
	}
	g, err := store.GetUserGroup(r.Context(), d.DB, gid)
	if err != nil || g == nil {
		return 0, 0
	}
	return g.MaxProjects, g.MaxKBs
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
	if existing, err := store.GetProjectByName(r.Context(), d.DB, u.ID, req.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrProjectNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	// Enforce the group's project cap (§ user groups). 0 = unlimited.
	if maxProjects, _ := groupCapFor(d, r, u.ID, u.GroupID); maxProjects > 0 {
		if n, err := store.CountProjectsByUser(r.Context(), d.DB, u.ID); err == nil && n >= maxProjects {
			writeError(w, 403, errProjectLimit)
			return
		}
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
			if errors.Is(err, store.ErrProjectNameExists) {
				writeError(w, 409, err)
				return
			}
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
		if errors.Is(err, store.ErrKBNameExists) {
			writeError(w, 409, store.ErrProjectNameExists)
			return
		}
		writeError(w, 500, err)
		return
	}
	p, err := store.CreateProject(r.Context(), d.DB, store.Project{
		UserID: u.ID, Name: req.Name, Description: req.Description, Instructions: req.Instructions,
		Accent: req.Accent, Emoji: req.Emoji, KBID: kb.ID,
	})
	if err != nil {
		// Keep the two-step create from leaving an unattached project library if
		// the project insert loses a concurrent unique-name race.
		_ = store.DeleteKB(r.Context(), d.DB, kb.ID, u.ID)
		if errors.Is(err, store.ErrProjectNameExists) {
			writeError(w, 409, err)
			return
		}
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
	convs, _ := store.ListConversations(r.Context(), d.DB, u.ID, p.ID, "active", 200, 0)
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
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		p.Name = &name
		if name == "" {
			writeError(w, 400, errors.New("name required"))
			return
		}
		if existing, err := store.GetProjectByName(r.Context(), d.DB, u.ID, name); err == nil && existing != nil && existing.ID != id {
			writeError(w, 409, store.ErrProjectNameExists)
			return
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, 500, err)
			return
		}
	}
	upd, err := store.UpdateProject(r.Context(), d.DB, id, u.ID, p)
	if err != nil {
		if errors.Is(err, store.ErrProjectNameExists) {
			writeError(w, 409, err)
			return
		}
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
	if !rateLimitUser(d, u.ID, "upload", 20, time.Minute) { // §C4
		writeError(w, 429, errUploadRateLimited)
		return
	}
	id := pathParam(r, "id")
	p, err := store.GetProject(r.Context(), d.DB, id, u.ID)
	if err != nil || p.KBID == "" {
		writeError(w, 404, errNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, d.Config.MaxUploadBytes+1<<20) // §C3
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

// renameProjectDocHandler renames a document in the project KB.
func renameProjectDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		Filename string `json:"filename"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Filename == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	if err := store.RenameDocument(r.Context(), d.DB, docID, body.Filename); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
