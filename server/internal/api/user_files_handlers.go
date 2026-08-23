package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/msgcache"
	"aivory/server/internal/store"
)

// ===== User files page (§ user files page) =====
//
// One list over every upload the signed-in user owns — conversation
// attachments and knowledge-base documents — with a storage meter on top.
// The list, delete, and preview endpoints reuse the admin file-inventory
// machinery (store.ListAdminFiles et al.) locked to the caller's user id;
// delete runs the same three-layer cleanup as everywhere else.

var errStorageQuotaExceeded = store.ErrStorageQuotaExceeded

// checkStorageQuota returns nil when a non-image upload of sizeBytes fits the
// caller's group cap. Image uploads never count (§ user files page).
func checkStorageQuota(r *http.Request, d Deps, userID string, sizeBytes int64) error {
	return store.CheckStorageQuota(r.Context(), d.DB, userID, sizeBytes)
}

// myStorageHandler reports the caller's storage meter.
func myStorageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	used, err := store.UserStorageUsage(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	quota, err := store.StorageQuotaBytes(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"used_bytes": used, "quota_bytes": quota})
}

// listMyFilesHandler is the user-scoped file inventory.
func listMyFilesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = adminFileListPageSizeCap
	}
	if limit > 200 {
		limit = 200
	}
	filter := store.AdminFileFilter{
		Search:        q.Get("search"),
		BillingUserID: u.ID, // hard-locked to the caller's quota principal
		AccessUserID:  u.ID,
		Origin:        q.Get("origin"),
		Type:          q.Get("type"),
		Sort:          q.Get("sort"),
		Order:         q.Get("order"),
	}
	total, err := store.CountAdminFiles(r.Context(), d.DB, filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	rows, err := store.ListAdminFiles(r.Context(), d.DB, filter, limit, offset)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"files": rows, "total": total, "limit": limit, "offset": offset})
}

// ownsAdminFileRow reports whether the row is billed to the user using the
// same canonical-owner derivation as the inventory and quota meter.
func ownsAdminFileRow(r *http.Request, d Deps, userID string, ref adminFileRef) bool {
	billingUserID, err := store.StorageItemBillingUser(r.Context(), d.DB, ref.Source, ref.ID, userID)
	return err == nil && billingUserID == userID
}

// deleteMyFilesHandler removes rows billed to the caller. For workspace
// committed content the caller is the canonical owner even when a member was
// the uploader; member drafts remain private and can only be removed by that
// uploader.
func deleteMyFilesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var body struct {
		Items []adminFileRef `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.Items) == 0 {
		writeError(w, 400, errInvalidInput)
		return
	}
	deleted := 0
	for _, item := range body.Items {
		id := strings.TrimSpace(item.ID)
		if id == "" || !ownsAdminFileRow(r, d, u.ID, item) {
			continue
		}
		switch item.Source {
		case "file":
			f, err := store.GetFile(r.Context(), d.DB, id, u.ID)
			if err != nil || f == nil {
				continue
			}
			storagePaths := []string{f.StoragePath}
			docs, err := store.DocumentsByStoragePathForUser(r.Context(), d.DB, f.StoragePath, u.ID)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			docIDs := make([]string, 0, len(docs))
			if f.ConversationID != "" {
				for _, doc := range docs {
					if doc.ConversationID != f.ConversationID {
						continue
					}
					docIDs = append(docIDs, doc.ID)
					storagePaths = append(storagePaths, doc.StoragePath)
				}
			}
			if f.ConversationID != "" {
				err = store.DeleteConversationFileAndDocuments(r.Context(), d.DB, id, f.ConversationID, u.ID, docIDs)
			} else {
				err = store.DeleteStandaloneFileForUser(r.Context(), d.DB, id, u.ID)
			}
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				writeError(w, 500, err)
				return
			}
			if f.ConversationID != "" {
				// The transaction marked any historical references unavailable. Drop
				// cached transcripts before notifying a tab that may edit/retry one.
				msgcache.Bump(d.Cache, f.ConversationID)
				publishUserEvent(d, r, u.ID, "conversation.updated", f.ConversationID)
				if f.UserID != u.ID {
					publishUserEvent(d, r, f.UserID, "conversation.updated", f.ConversationID)
				}
			}
			for _, docID := range docIDs {
				cleanupRAGDocument(r.Context(), d, docID, "user delete file "+id)
			}
			cleanupStoragePaths(r.Context(), d, storagePaths, "user delete file "+id)
			deleted++
		case "document":
			doc, err := store.GetDocumentForUser(r.Context(), d.DB, id, u.ID)
			if err != nil {
				continue
			}
			scope, parentID := "conversation", doc.ConversationID
			if doc.KBID != "" {
				scope, parentID = "kb", doc.KBID
			}
			if err := store.DeleteDocumentForUser(r.Context(), d.DB, id, scope, parentID, u.ID); err != nil {
				writeError(w, 500, err)
				return
			}
			cleanupRAGDocument(r.Context(), d, id, "user delete document "+id)
			cleanupStoragePaths(r.Context(), d, []string{doc.StoragePath}, "user delete document "+id)
			deleted++
		default:
			writeError(w, 400, errInvalidInput)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// myFileContentHandler streams one of the caller's uploads for preview.
func myFileContentHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	source := r.URL.Query().Get("source")
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" || !ownsAdminFileRow(r, d, u.ID, adminFileRef{Source: source, ID: id}) {
		writeError(w, 404, errNotFound)
		return
	}
	switch source {
	case "file":
		f, err := store.GetFile(r.Context(), d.DB, id, u.ID)
		if err != nil || f == nil {
			writeError(w, 404, errNotFound)
			return
		}
		serveStoredFile(d, w, f)
	case "document":
		doc, err := store.GetDocumentForUser(r.Context(), d.DB, id, u.ID)
		if err != nil {
			writeError(w, 404, errNotFound)
			return
		}
		serveStoredFile(d, w, &store.File{Filename: doc.Filename, StoragePath: doc.StoragePath})
	default:
		writeError(w, 400, errInvalidInput)
	}
}
