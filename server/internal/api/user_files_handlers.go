package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/store"
)

// ===== User files page (§ user files page) =====
//
// One list over every upload the signed-in user owns — conversation
// attachments and knowledge-base documents — with a storage meter on top.
// The list, delete, and preview endpoints reuse the admin file-inventory
// machinery (store.ListAdminFiles et al.) locked to the caller's user id;
// delete runs the same three-layer cleanup as everywhere else.

var errStorageQuotaExceeded = errors.New("storage quota exceeded")

// checkStorageQuota returns nil when a non-image upload of sizeBytes fits the
// caller's group cap. Image uploads never count (§ user files page).
func checkStorageQuota(r *http.Request, d Deps, userID string, sizeBytes int64) error {
	quota, err := store.StorageQuotaBytes(r.Context(), d.DB, userID)
	if err != nil || quota <= 0 {
		return nil // unlimited, or fail-open on lookup errors — soft limit
	}
	used, err := store.UserStorageUsage(r.Context(), d.DB, userID)
	if err != nil {
		return nil
	}
	if used+sizeBytes > quota {
		return fmt.Errorf("%w: %d MB in use of %d MB — free up space and retry",
			errStorageQuotaExceeded, used/(1024*1024), quota/(1024*1024))
	}
	return nil
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
		Search: q.Get("search"),
		UserID: u.ID, // hard-locked to the caller
		Origin: q.Get("origin"),
		Type:   q.Get("type"),
		Sort:   q.Get("sort"),
		Order:  q.Get("order"),
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

// ownsAdminFileRow reports whether the row belongs to the user, using the
// same ownership derivation as the inventory view (files.user_id directly;
// documents via their KB or conversation).
func ownsAdminFileRow(r *http.Request, d Deps, userID string, ref adminFileRef) bool {
	switch ref.Source {
	case "file":
		f, err := store.GetFile(r.Context(), d.DB, ref.ID, "")
		return err == nil && f != nil && f.UserID == userID
	case "document":
		var owner string
		err := d.DB.QueryRowContext(r.Context(), `
			SELECT COALESCE(k.user_id, c.user_id, '')
			  FROM documents d2
			  LEFT JOIN knowledge_bases k ON k.id = d2.kb_id
			  LEFT JOIN conversations c ON c.id = d2.conversation_id
			 WHERE d2.id=?`, ref.ID).Scan(&owner)
		return err == nil && owner == userID
	default:
		return false
	}
}

// deleteMyFilesHandler removes the caller's own uploads — same three-layer
// cleanup as the admin batch delete, with an ownership gate per item.
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
			docs, err := store.DocumentsByStoragePath(r.Context(), d.DB, f.StoragePath)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			docIDs := make([]string, 0, len(docs))
			for _, doc := range docs {
				docIDs = append(docIDs, doc.ID)
				storagePaths = append(storagePaths, doc.StoragePath)
			}
			if err := store.AdminDeleteFile(r.Context(), d.DB, id); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				writeError(w, 500, err)
				return
			}
			for _, docID := range docIDs {
				_ = store.DeleteDocument(r.Context(), d.DB, docID)
				cleanupRAGDocument(r.Context(), d, docID, "user delete file "+id)
			}
			cleanupStoragePaths(r.Context(), d, storagePaths, "user delete file "+id)
			deleted++
		case "document":
			doc, err := store.GetDocument(r.Context(), d.DB, id)
			if err != nil {
				continue
			}
			if err := store.DeleteDocument(r.Context(), d.DB, id); err != nil {
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
		doc, err := store.GetDocument(r.Context(), d.DB, id)
		if err != nil {
			writeError(w, 404, errNotFound)
			return
		}
		serveStoredFile(d, w, &store.File{Filename: doc.Filename, StoragePath: doc.StoragePath})
	default:
		writeError(w, 400, errInvalidInput)
	}
}
