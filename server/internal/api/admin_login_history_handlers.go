package api

import (
	"errors"
	"net/http"
	"strconv"

	"aivory/server/internal/store"
)

const (
	adminLoginHistoryDefaultPageSize = 50
	adminLoginHistoryMaxPageSize     = 200
)

func listUserLoginHistoryAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	userID := pathParam(r, "id")
	if _, err := store.FindUserByID(r.Context(), d.DB, userID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))
	if limit <= 0 {
		limit = adminLoginHistoryDefaultPageSize
	}
	if limit > adminLoginHistoryMaxPageSize {
		limit = adminLoginHistoryMaxPageSize
	}
	if offset < 0 {
		offset = 0
	}

	total, err := store.CountLoginHistoriesForUser(r.Context(), d.DB, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	items, err := store.ListLoginHistoriesForUser(r.Context(), d.DB, userID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}
