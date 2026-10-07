package api

import (
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

func reorderConversationHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var input struct {
		TargetID string `json:"target_id"`
		Position string `json:"position"`
	}
	id := pathParam(r, "id")
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	input.TargetID = strings.TrimSpace(input.TargetID)
	if input.TargetID == "" || input.TargetID == id || (input.Position != "before" && input.Position != "after") {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	u := authUser(r)
	updates, err := store.ReorderConversation(r.Context(), d.DB, id, u.ID, input.TargetID, input.Position)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrConversationReorderConflict):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	for _, update := range updates {
		publishUserEvent(d, r, u.ID, "conversation.updated", update.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": updates})
}
