package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/store"
)

// ===== Admin file inventory (§ admin files) =====
//
// One view over every user upload: files rows (conversation attachments) plus
// documents rows (KB docs and conversation docs without a files twin). Delete
// re-uses the exact cleanup chain of the user-facing handlers — DB rows in a
// transaction, then vectors, then physical bytes — so an admin delete leaves
// nothing behind either.

const adminFileListPageSizeCap = 50

func listFilesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
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
		UserID: strings.TrimSpace(q.Get("user_id")),
		UserQ:  strings.TrimSpace(q.Get("user")),
		Origin: q.Get("origin"),
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

// adminFileRef identifies one row of the union view.
type adminFileRef struct {
	Source string `json:"source"` // "file" | "document"
	ID     string `json:"id"`
}

// deleteFilesAdmin removes one or many uploads. Per item it mirrors the
// user-facing delete: DB rows first (missing rows are counted as already
// gone, so a double-click or a stale list never fails the batch), then
// vectors, then physical bytes.
func deleteFilesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
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
		if id == "" {
			continue
		}
		switch item.Source {
		case "file":
			f, err := store.GetFile(r.Context(), d.DB, id, "")
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
				cleanupRAGDocument(r.Context(), d, docID, "admin delete file "+id)
			}
			cleanupStoragePaths(r.Context(), d, storagePaths, "admin delete file "+id)
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
			cleanupRAGDocument(r.Context(), d, id, "admin delete document "+id)
			cleanupStoragePaths(r.Context(), d, []string{doc.StoragePath}, "admin delete document "+id)
			deleted++
		default:
			writeError(w, 400, errInvalidInput)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// adminFileContentHandler streams the raw bytes of any upload for the admin
// preview dialog. serveStoredFile enforces the UploadDir jail and re-derives
// the MIME type server-side, same as the user-facing download route.
func adminFileContentHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	switch source {
	case "file":
		f, err := store.GetFile(r.Context(), d.DB, id, "")
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
