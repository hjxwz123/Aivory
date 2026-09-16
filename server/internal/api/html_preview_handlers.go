package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"aivory/server/internal/store"
)

const maxHTMLPreviewShareBytes = 1 << 20

func createHTMLPreviewShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var input struct {
		HTML string `json:"html"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if strings.TrimSpace(input.HTML) == "" || !utf8.ValidString(input.HTML) {
		writeError(w, http.StatusBadRequest, errors.New("html is required"))
		return
	}
	if len(input.HTML) > maxHTMLPreviewShareBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("html preview is too large"))
		return
	}
	share, err := store.CreateHTMLPreviewShare(r.Context(), d.DB, authUser(r).ID, input.HTML)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusForbidden, errors.New("sharing is not allowed"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	path := "/api/public/html-previews/" + url.PathEscape(share.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": share.ID, "url": path, "created_at": share.CreatedAt,
	})
}

// publicHTMLPreviewShareHandler returns only the shared HTML bytes. The CSP
// sandbox creates an opaque origin without mutating or wrapping the document,
// so the public page contains no Aivory UI, code viewer, or explanatory copy.
func publicHTMLPreviewShareHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	share, err := store.GetHTMLPreviewShare(r.Context(), d.DB, pathParam(r, "token"))
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox; upgrade-insecure-requests")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(share.HTML))
}
