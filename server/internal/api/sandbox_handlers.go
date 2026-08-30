package api

import (
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

// sandboxFilesHandler exposes a conversation's live sandbox workspace to an
// authorized participant. It is deliberately read-only: the user surface has
// no route that can put, rename, move, or delete workspace content.
func sandboxFilesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}

	sid, err := store.GetConvProviderStateKey(r.Context(), d.DB, convID, "sandbox_id")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if sid == "" {
		writeJSON(w, http.StatusOK, map[string]any{"session": "", "files": []any{}})
		return
	}

	if d.Tools == nil {
		writeError(w, http.StatusBadRequest, errors.New("sandbox not configured"))
		return
	}
	sb := d.Tools.Sandbox()
	if sb == nil || !sb.Enabled() {
		writeError(w, http.StatusBadRequest, errors.New("sandbox not configured"))
		return
	}
	files, err := sb.ListFiles(r.Context(), sid)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "404") || strings.Contains(msg, "not found") {
			writeJSON(w, http.StatusOK, map[string]any{"session": sid, "files": []any{}, "unavailable": true})
			return
		}
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sid, "files": files})
}

func sandboxFileGetHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	filePath := r.URL.Query().Get("path")
	if !validSandboxFilePath(filePath) {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}

	sid, err := store.GetConvProviderStateKey(r.Context(), d.DB, convID, "sandbox_id")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if sid == "" {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if d.Tools == nil {
		writeError(w, http.StatusBadRequest, errors.New("sandbox not configured"))
		return
	}
	sb := d.Tools.Sandbox()
	if sb == nil || !sb.Enabled() {
		writeError(w, http.StatusBadRequest, errors.New("sandbox not configured"))
		return
	}
	data, err := sb.GetFile(r.Context(), sid, filePath)
	if err != nil {
		if msg := strings.ToLower(err.Error()); strings.Contains(msg, "404") || strings.Contains(msg, "not found") {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if data == nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	serveSandboxFile(w, filePath, data)
}
