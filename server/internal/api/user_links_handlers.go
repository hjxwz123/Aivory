package api

import (
	"errors"
	"net/http"
	"strconv"

	"aivory/server/internal/store"
)

func listUserLinks(d Deps, w http.ResponseWriter, r *http.Request, htmlPreview bool) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var items []store.UserPublishedLink
	var err error
	if htmlPreview {
		items, err = store.ListUserHTMLPreviewShares(r.Context(), d.DB, authUser(r).ID, limit+1, offset)
	} else {
		items, err = store.ListUserConversationShares(r.Context(), d.DB, authUser(r).ID, limit+1, offset)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "limit": limit, "offset": offset, "has_more": hasMore,
	})
}

func listUserConversationShares(d Deps, w http.ResponseWriter, r *http.Request) {
	listUserLinks(d, w, r, false)
}

func listUserHTMLPreviewShares(d Deps, w http.ResponseWriter, r *http.Request) {
	listUserLinks(d, w, r, true)
}

// Owners can clean up their links even after sharing permission is disabled.
// Publishing and public access continue to enforce the existing capability gate.
func deleteUserLink(d Deps, w http.ResponseWriter, r *http.Request, htmlPreview bool) {
	var err error
	if htmlPreview {
		err = store.DeleteUserHTMLPreviewShare(r.Context(), d.DB, pathParam(r, "id"), authUser(r).ID)
	} else {
		err = store.DeleteUserConversationShare(r.Context(), d.DB, pathParam(r, "id"), authUser(r).ID)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func deleteUserConversationShare(d Deps, w http.ResponseWriter, r *http.Request) {
	deleteUserLink(d, w, r, false)
}

func deleteUserHTMLPreviewShare(d Deps, w http.ResponseWriter, r *http.Request) {
	deleteUserLink(d, w, r, true)
}
