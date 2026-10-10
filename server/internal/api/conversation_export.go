package api

import (
	"net/http"
	"strconv"

	"aivory/server/internal/store"
)

// Export lists personal roots and inline descendants together, including
// archived history. The sidebar's root-only filters cannot define a backup.
func listConversationExportHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 499 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	rows, err := store.ListDomainPersonalConversations(r.Context(), d.DB, authUser(r).ID, limit+1, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	for i := range rows {
		stripServerConvFields(&rows[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": rows, "has_more": hasMore, "limit": limit, "offset": offset})
}
