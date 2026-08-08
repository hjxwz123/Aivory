package api

import (
	"net/http"

	"aivory/server/internal/store"
)

func claimCreditAdjustmentNotificationHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	notice, err := store.ClaimCreditAdjustmentNotification(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notification": notice})
}
