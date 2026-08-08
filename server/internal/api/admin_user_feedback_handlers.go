package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/store"
)

const (
	adminUserFeedbackDefaultLimit = 50
	adminUserFeedbackMaxLimit     = 200
)

func listUserFeedbackAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	limit := adminUserFeedbackDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > adminUserFeedbackMaxLimit {
			writeError(w, http.StatusBadRequest, errors.New("limit must be between 1 and 200"))
			return
		}
		limit = value
	}
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeError(w, http.StatusBadRequest, errors.New("offset must be zero or greater"))
			return
		}
		offset = value
	}
	result, err := store.ListUserFeedbackAdmin(
		r.Context(), d.DB, r.URL.Query().Get("q"), limit, offset,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func userFeedbackScreenshotAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	data, mime, err := store.GetUserFeedbackScreenshot(r.Context(), d.DB, pathParam(r, "id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if mime != "image/jpeg" && mime != "image/png" {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("unsupported screenshot format"))
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", `inline; filename="feedback-screenshot"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
