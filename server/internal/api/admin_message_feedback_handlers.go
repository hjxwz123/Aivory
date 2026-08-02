package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/store"
)

const (
	adminMessageFeedbackDefaultDays  = 30
	adminMessageFeedbackMaxDays      = 365
	adminMessageFeedbackDefaultLimit = 50
	adminMessageFeedbackMaxLimit     = 200
)

// listMessageFeedbackAdmin returns model-quality aggregates plus the exact
// rated question/response examples administrators use for diagnosis.
func listMessageFeedbackAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	days := adminMessageFeedbackDefaultDays
	if raw := strings.TrimSpace(query.Get("days")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > adminMessageFeedbackMaxDays {
			writeError(w, http.StatusBadRequest, errors.New("days must be between 1 and 365"))
			return
		}
		days = value
	}
	rating := strings.TrimSpace(query.Get("rating"))
	if rating != "" && rating != store.MessageFeedbackLike && rating != store.MessageFeedbackDislike {
		writeError(w, http.StatusBadRequest, errors.New("rating must be 'like', 'dislike', or empty"))
		return
	}
	reason := strings.TrimSpace(query.Get("reason"))
	if reason != "" && !store.IsMessageFeedbackReason(reason) {
		writeError(w, http.StatusBadRequest, errors.New("invalid feedback reason"))
		return
	}
	limit := adminMessageFeedbackDefaultLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > adminMessageFeedbackMaxLimit {
			writeError(w, http.StatusBadRequest, errors.New("limit must be between 1 and 200"))
			return
		}
		limit = value
	}
	offset := 0
	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeError(w, http.StatusBadRequest, errors.New("offset must be zero or greater"))
			return
		}
		offset = value
	}

	report, err := store.AdminMessageFeedbackReportData(r.Context(), d.DB, store.AdminMessageFeedbackFilter{
		Since:   time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix(),
		Rating:  rating,
		ModelID: strings.TrimSpace(query.Get("model_id")),
		Reason:  reason,
	}, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
