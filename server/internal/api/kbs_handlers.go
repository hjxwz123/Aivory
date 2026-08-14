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

// knowledgeBaseResponse is the user-facing knowledge-base shape. Retrieval
// implementation details stay server-side; callers only need the library
// identity and their effective capabilities.
type knowledgeBaseResponse struct {
	ID               string `json:"id"`
	UserID           string `json:"user_id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
	AccessRole       string `json:"access_role,omitempty"`
	OwnerName        string `json:"owner_name,omitempty"`
	CanShare         bool   `json:"can_share"`
	CanUpload        bool   `json:"can_upload"`
	CanDelete        bool   `json:"can_delete"`
	CanDeleteContent bool   `json:"can_delete_content"`
	CanManageMembers bool   `json:"can_manage_members"`
	ProjectID        string `json:"project_id,omitempty"`
	CreatedAt        int64  `json:"created_at"`
}

func userKnowledgeBase(kb store.KnowledgeBase) knowledgeBaseResponse {
	return knowledgeBaseResponse{
		ID: kb.ID, UserID: kb.UserID, Name: kb.Name, Description: kb.Description,
		WorkspaceID: kb.WorkspaceID, AccessRole: kb.AccessRole, OwnerName: kb.OwnerName,
		CanShare: kb.CanShare, CanUpload: kb.CanUpload, CanDelete: kb.CanDelete,
		CanDeleteContent: kb.CanDeleteContent, CanManageMembers: kb.CanManageMembers,
		ProjectID: kb.ProjectID, CreatedAt: kb.CreatedAt,
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

// getKBHandler resolves a library by id through its current resource-level
// authorization. It deliberately does not depend on the client's active
// workspace, so direct links keep working across sidebar scope changes.
func getKBHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	kb, err := store.GetStandaloneKB(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, userKnowledgeBase(*kb))
}

type createKBReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// '' = personal; set = shared workspace KB (§workspaces).
	WorkspaceID string `json:"workspace_id"`
}

// createKBHandler creates a new KB pinned to the administrator-configured
// embedding model. The user request has no model field; unknown fields are
// ignored for backwards compatibility and can never override this setting.
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
		workspace, workspaceErr := store.GetWorkspaceForMember(r.Context(), d.DB, req.WorkspaceID, u.ID)
		if workspaceErr != nil {
			writeError(w, 404, errNotFound)
			return
		}
		if !workspace.CanCreateKB {
			writeError(w, http.StatusForbidden, errWorkspaceKBCreationPermission)
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
		if errors.Is(err, store.ErrNotFound) && req.WorkspaceID != "" {
			writeError(w, http.StatusForbidden, errWorkspaceKBCreationPermission)
			return
		}
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
	kb, kbErr := store.GetStandaloneKB(r.Context(), d.DB, id, u.ID)
	if kbErr != nil || !kb.CanDelete {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	accessUserIDs, accessErr := store.ListKnowledgeBaseAccessUserIDs(r.Context(), d.DB, id)
	if accessErr != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	docs, _ := store.ListDocuments(r.Context(), d.DB, "kb", id)
	storagePaths := make([]string, 0, len(docs))
	for _, doc := range docs {
		storagePaths = append(storagePaths, doc.StoragePath)
	}
	// Install the permanent deny marker before the destructive transaction. New
	// turns cannot slip between the commit and the revocation broadcast. If the
	// delete loses an authorization race, remove the marker so the surviving KB
	// remains usable.
	revokeKnowledgeBaseGenerations(d, id)
	if err := store.DeleteKB(r.Context(), d.DB, id, u.ID, d.Config.UploadDir, d.Config.ArtifactDir); err != nil {
		if d.Cache != nil {
			d.Cache.Delete(knowledgeBaseGenerationRevocationKey(id))
		}
		writeError(w, 404, errNotFound)
		return
	}
	for _, userID := range accessUserIDs {
		publishUserEvent(d, nil, userID, "knowledge_base.access_updated", "")
	}
	// Keep the vector backend in sync with the cascaded chunk deletes.
	cleanupRAGKB(r.Context(), d, id, "delete kb "+id)
	cleanupStoragePaths(r.Context(), d, storagePaths, "delete kb "+id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// uploadKBDocHandler accepts a document into the KB and enqueues parsing.
func uploadKBDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	kb, err := store.GetStandaloneKB(r.Context(), d.DB, id, u.ID)
	if err != nil || !kb.CanUpload {
		writeError(w, 404, errNotFound)
		return
	}
	if !requireUserCapabilityError(d, w, r, errFileUploadGroupPermission, func(p store.UserGroupPermissions) bool { return p.AllowFileUpload }) {
		return
	}
	if !rateLimitUser(d, u.ID, "upload", kbDocUploadRateLimit, time.Minute) { // §C4
		writeError(w, 429, errUploadRateLimited)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, d.Config.MaxUploadBytes+1<<20) // §C3
	doc, err := receiveDocument(d, r, id, "")
	if err != nil {
		status := 400
		if errors.Is(err, errFileUploadGroupPermission) {
			status = http.StatusForbidden
		} else if errors.Is(err, errKnowledgeBaseGroupPermission) {
			status = http.StatusForbidden
		} else if errors.Is(err, errStorageQuotaExceeded) {
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
	if _, err := store.GetStandaloneKB(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	docs, err := store.ListKBDocumentsForUser(
		r.Context(), d.DB, id, u.ID,
		r.URL.Query().Get("search"), r.URL.Query().Get("uploaded_by_user_id"),
	)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, userDocuments(docs))
}

func listKBDocumentUploadersHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if _, err := store.GetStandaloneKB(r.Context(), d.DB, pathParam(r, "id"), u.ID); err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	rows, err := store.ListKBDocumentUploadersForUser(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func listKBShareCandidatesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !requireUserCapabilityError(d, w, r, errKnowledgeBaseSharingGroupPermission, func(p store.UserGroupPermissions) bool { return p.AllowKnowledgeBaseSharing }) {
		return
	}
	rows, err := store.SearchKnowledgeBaseShareCandidates(
		r.Context(), d.DB, pathParam(r, "id"), u.ID, r.URL.Query().Get("search"), 20,
	)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func listKBSharesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !requireUserCapabilityError(d, w, r, errKnowledgeBaseSharingGroupPermission, func(p store.UserGroupPermissions) bool { return p.AllowKnowledgeBaseSharing }) {
		return
	}
	rows, err := store.ListKnowledgeBaseShares(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// publishKnowledgeBaseAccessEvent refreshes both consumers and managers. A
// knowledge-base owner or workspace library creator may be editing access in a
// second tab; notifying only the changed user leaves that management surface
// stale. extraUserIDs covers a share that was just removed.
func publishKnowledgeBaseAccessEvent(d Deps, r *http.Request, kbID string, extraUserIDs ...string) {
	recipients := map[string]struct{}{}
	if userIDs, err := store.ListKnowledgeBaseAccessUserIDs(r.Context(), d.DB, kbID); err == nil {
		for _, userID := range userIDs {
			if id := strings.TrimSpace(userID); id != "" {
				recipients[id] = struct{}{}
			}
		}
	}
	for _, userID := range extraUserIDs {
		if id := strings.TrimSpace(userID); id != "" {
			recipients[id] = struct{}{}
		}
	}
	for userID := range recipients {
		publishUserEvent(d, r, userID, "knowledge_base.access_updated", "")
	}
}

func upsertKBShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !requireUserCapabilityError(d, w, r, errKnowledgeBaseSharingGroupPermission, func(p store.UserGroupPermissions) bool { return p.AllowKnowledgeBaseSharing }) {
		return
	}
	var body struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	share, err := store.UpsertKnowledgeBaseShare(r.Context(), d.DB, pathParam(r, "id"), u.ID, body.UserID, body.Role)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidKnowledgeBaseShare):
			writeError(w, http.StatusBadRequest, errInvalidInput)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	publishKnowledgeBaseAccessEvent(d, r, pathParam(r, "id"), u.ID, body.UserID)
	writeJSON(w, http.StatusOK, share)
}

func deleteKBShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !requireUserCapabilityError(d, w, r, errKnowledgeBaseSharingGroupPermission, func(p store.UserGroupPermissions) bool { return p.AllowKnowledgeBaseSharing }) {
		return
	}
	memberID := pathParam(r, "uid")
	canRevoke, err := store.CanRevokeKnowledgeBaseShare(
		r.Context(), d.DB, pathParam(r, "id"), u.ID, memberID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !canRevoke {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	// Two-phase epoch advance closes both sides of the mutation: generations
	// already active before this request see the first revoke; a generation that
	// starts while the DELETE is waiting on the database sees the second.
	revokeKnowledgeBaseUserGenerations(d, pathParam(r, "id"), memberID)
	if err := store.DeleteKnowledgeBaseShare(r.Context(), d.DB, pathParam(r, "id"), u.ID, memberID); err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	revokeKnowledgeBaseUserGenerations(d, pathParam(r, "id"), memberID)
	publishKnowledgeBaseAccessEvent(d, r, pathParam(r, "id"), u.ID, memberID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func listWorkspaceKBMembersHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	rows, err := store.ListWorkspaceKnowledgeBaseMemberPermissions(
		r.Context(), d.DB, pathParam(r, "id"), u.ID,
	)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func updateWorkspaceKBMemberHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var body struct {
		CanAddFiles      bool `json:"can_add_files"`
		CanDeleteContent bool `json:"can_delete_content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	member, err := store.UpdateWorkspaceKnowledgeBaseMemberPermission(
		r.Context(), d.DB, pathParam(r, "id"), u.ID, pathParam(r, "uid"),
		body.CanAddFiles, body.CanDeleteContent,
	)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	publishKnowledgeBaseAccessEvent(d, r, pathParam(r, "id"), u.ID, pathParam(r, "uid"))
	writeJSON(w, http.StatusOK, member)
}

// retryKBDocHandler requeues a failed knowledge-base document in the existing
// ingest pipeline. It does not create a second parser or duplicate the upload.
func retryKBDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	kbID := pathParam(r, "id")
	docID := pathParam(r, "docId")
	if _, err := store.GetStandaloneKB(r.Context(), d.DB, kbID, u.ID); err != nil {
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
	kb, err := store.GetStandaloneKB(r.Context(), d.DB, kbID, u.ID)
	if err != nil || !(kb.CanDeleteContent || (kb.AccessRole == "write" && doc.UploadedByUserID == u.ID)) {
		writeError(w, http.StatusNotFound, errNotFound)
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

// renameKBDocHandler changes a document's display name. The store binds the
// update to the caller's current document-mutation permission, so a personal
// write collaborator may rename only files they uploaded themselves.
func renameKBDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if _, err := store.GetStandaloneKB(r.Context(), d.DB, pathParam(r, "id"), u.ID); err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	var body struct {
		Filename string `json:"filename"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	filename, valid := normalizeDocumentFilename(body.Filename)
	if !valid {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if err := store.RenameDocumentForUser(
		r.Context(), d.DB, pathParam(r, "docId"), "kb", pathParam(r, "id"), u.ID, filename,
	); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func normalizeDocumentFilename(raw string) (string, bool) {
	filename := strings.TrimSpace(raw)
	if filename == "" || len([]byte(filename)) > 255 || filename == "." || filename == ".." ||
		strings.ContainsAny(filename, `/\\`) || strings.ContainsRune(filename, '\x00') {
		return "", false
	}
	for _, char := range filename {
		if char < 0x20 || char == 0x7f {
			return "", false
		}
	}
	return filename, true
}

// deleteKBDocHandler removes a single document.
func deleteKBDocHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	docID := pathParam(r, "docId")
	if _, err := store.GetStandaloneKB(r.Context(), d.DB, id, u.ID); err != nil {
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
