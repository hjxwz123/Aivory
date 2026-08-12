package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// Project handler tunables (env-overridable; defaults preserve prior behavior).
var (
	projectDetailConversationsPageSize = envcfg.Int("AIVORY_API_PROJECT_DETAIL_CONVERSATIONS_PAGE_SIZE", 200)
	projectDocUploadRateLimit          = envcfg.Int("AIVORY_API_RATE_LIMIT_USER_2", 20)
)

// projectResponse omits the project library's retrieval implementation. The
// knowledge-base id remains public because it is the stable attachment scope;
// its index model and dimensions never need to leave the server.
type projectResponse struct {
	ID             string `json:"id"`
	UserID         string `json:"user_id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Instructions   string `json:"instructions"`
	Accent         string `json:"accent"`
	Emoji          string `json:"emoji"`
	Pinned         bool   `json:"pinned"`
	KBID           string `json:"kb_id"`
	AutoAddUploads bool   `json:"auto_add_uploads"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
}

func userProject(p store.Project) projectResponse {
	return projectResponse{
		ID: p.ID, UserID: p.UserID, Name: p.Name, Description: p.Description,
		Instructions: p.Instructions, Accent: p.Accent, Emoji: p.Emoji, Pinned: p.Pinned,
		KBID: p.KBID, AutoAddUploads: p.AutoAddUploads, CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt, WorkspaceID: p.WorkspaceID,
	}
}

func userProjects(rows []store.Project) []projectResponse {
	items := make([]projectResponse, 0, len(rows))
	for _, project := range rows {
		items = append(items, userProject(project))
	}
	return items
}

// listProjectsHandler returns the user's projects.
func listProjectsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	// Workspace scope (§workspaces): a member lists the space's shared projects;
	// otherwise the personal ones.
	var rows []store.Project
	var err error
	if wsID := strings.TrimSpace(r.URL.Query().Get("workspace_id")); wsID != "" {
		if role, merr := store.IsWorkspaceMember(r.Context(), d.DB, wsID, u.ID); merr != nil || role == "" {
			writeError(w, 404, errNotFound)
			return
		}
		rows, err = store.ListWorkspaceProjectsForUser(r.Context(), d.DB, wsID, u.ID)
	} else {
		rows, err = store.ListProjects(r.Context(), d.DB, u.ID)
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, userProjects(rows))
}

type createProjectReq struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	Accent       string `json:"accent"`
	Emoji        string `json:"emoji"`
	// '' = personal; set = create INSIDE that workspace (§workspaces, membership
	// validated server-side). Both members and the owner may create.
	WorkspaceID string `json:"workspace_id"`
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
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	if req.WorkspaceID != "" {
		if role, merr := store.IsWorkspaceMember(r.Context(), d.DB, req.WorkspaceID, u.ID); merr != nil || role == "" {
			writeError(w, 404, errNotFound)
			return
		}
	}
	if existing, err := store.GetProjectByName(r.Context(), d.DB, u.ID, req.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrProjectNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	// The store re-evaluates this cap while holding the creator row lock and
	// inserts in the same transaction. 0 remains unlimited.
	maxProjects, _ := groupCapFor(d, r, u.ID, u.GroupID)
	// Project libraries use the same administrator-selected embedding model as
	// standalone knowledge bases. The model identity remains server-side; a
	// user cannot choose or override it through the project request.
	embed, err := configuredEmbeddingModel(r.Context(), d)
	if err != nil {
		// Allow project without KB if no embedding model.
		p, err := store.CreateProjectWithLimit(r.Context(), d.DB, store.Project{
			UserID: u.ID, Name: req.Name, Description: req.Description, Instructions: req.Instructions,
			Accent: req.Accent, Emoji: req.Emoji, WorkspaceID: req.WorkspaceID,
		}, maxProjects)
		if err != nil {
			if errors.Is(err, store.ErrProjectLimitExceeded) {
				writeError(w, http.StatusForbidden, errProjectLimit)
				return
			}
			if errors.Is(err, store.ErrProjectNameExists) {
				writeError(w, 409, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, userProject(*p))
		return
	}
	p, err := store.CreateProjectWithLibraryAndLimit(r.Context(), d.DB, store.Project{
		UserID: u.ID, Name: req.Name, Description: req.Description, Instructions: req.Instructions,
		Accent: req.Accent, Emoji: req.Emoji, WorkspaceID: req.WorkspaceID,
	}, store.KnowledgeBase{
		UserID: u.ID, Name: req.Name + " — project library",
		EmbeddingModelID: embed.ID, EmbeddingDim: embed.Dim,
		WorkspaceID: req.WorkspaceID,
	}, maxProjects)
	if err != nil {
		if errors.Is(err, store.ErrProjectLimitExceeded) {
			writeError(w, http.StatusForbidden, errProjectLimit)
			return
		}
		if errors.Is(err, store.ErrKBNameExists) {
			writeError(w, http.StatusConflict, store.ErrProjectNameExists)
			return
		}
		if errors.Is(err, store.ErrProjectNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, userProject(*p))
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
		docs, _ = store.ListDocumentsForUser(r.Context(), d.DB, "kb", p.KBID, u.ID)
	}
	convs, _ := store.ListConversations(r.Context(), d.DB, u.ID, p.ID, "active", projectDetailConversationsPageSize, 0)
	writeJSON(w, 200, map[string]any{
		"project":       userProject(*p),
		"documents":     userDocuments(docs),
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
	writeJSON(w, 200, userProject(*upd))
}

// deleteProjectHandler removes the project.
func deleteProjectHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	deletion, err := store.DeleteProjectWithState(r.Context(), d.DB, id, u.ID, d.Config.UploadDir, d.Config.ArtifactDir)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	// DeleteProject atomically removes the project's dedicated KB and its DB
	// chunks. Keep the external vector/object stores in sync after that commit.
	for _, kbID := range deletion.KnowledgeBaseIDs {
		cleanupRAGKB(r.Context(), d, kbID, "delete project "+id)
	}
	cleanupStoragePaths(r.Context(), d, deletion.StoragePaths, "delete project "+id)
	// §23: conversations silently lost their project grouping — a generic
	// (id-less) event makes other devices re-sync their sidebar list.
	publishUserEvent(d, r, u.ID, "conversation.updated", "")
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
	docs, _ := store.ListDocumentsForUser(r.Context(), d.DB, "kb", p.KBID, u.ID)
	writeJSON(w, 200, userDocuments(docs))
}

// uploadProjectDocHandler ingests a new document into the project KB.
func uploadProjectDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !rateLimitUser(d, u.ID, "upload", projectDocUploadRateLimit, time.Minute) { // §C4
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
	doc, err := store.GetDocumentForUser(r.Context(), d.DB, docID, u.ID)
	if err != nil || doc.KBID != p.KBID {
		writeError(w, 404, errNotFound)
		return
	}
	if err := store.DeleteDocumentForUser(r.Context(), d.DB, docID, "kb", p.KBID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	cleanupRAGDocument(r.Context(), d, docID, "delete project document "+docID)
	cleanupStoragePaths(r.Context(), d, []string{doc.StoragePath}, "delete project document "+docID)
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
	doc, err := store.GetDocumentForUser(r.Context(), d.DB, docID, u.ID)
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
	if err := store.RenameDocumentForUser(r.Context(), d.DB, docID, "kb", p.KBID, u.ID, body.Filename); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
