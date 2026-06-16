package api

import (
	"errors"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"aurelia/server/internal/store"
)

// Admin sandbox inspector (§ admin tools): list / preview / clear the files in a
// conversation's sandbox workspace. The session id lives on the conversation's
// provider_state ("sandbox_id"); an empty id means no sandbox has been spun up.

func sandboxFilesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	convID := pathParam(r, "id")
	sb := d.Tools.Sandbox()
	if sb == nil || !sb.Enabled() {
		writeError(w, 400, errors.New("sandbox not configured"))
		return
	}
	sid, _ := store.GetConvProviderStateKey(r.Context(), d.DB, convID, "sandbox_id")
	if sid == "" {
		writeJSON(w, 200, map[string]any{"session": "", "files": []any{}})
		return
	}
	files, err := sb.ListFiles(r.Context(), sid)
	if err != nil {
		// An old sandbox-sidecar (pre /files/list) returns 404, and a recycled
		// session is also "gone". Degrade to an empty list + a flag instead of a
		// scary 502 so the admin UI stays usable across a version skew.
		msg := err.Error()
		if strings.Contains(msg, "404") || strings.Contains(strings.ToLower(msg), "not found") {
			writeJSON(w, 200, map[string]any{"session": sid, "files": []any{}, "unavailable": true})
			return
		}
		writeError(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"session": sid, "files": files})
}

func sandboxFileGetAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	convID := pathParam(r, "id")
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	sb := d.Tools.Sandbox()
	if sb == nil || !sb.Enabled() {
		writeError(w, 400, errors.New("sandbox not configured"))
		return
	}
	sid, _ := store.GetConvProviderStateKey(r.Context(), d.DB, convID, "sandbox_id")
	if sid == "" {
		writeError(w, 404, errNotFound)
		return
	}
	data, err := sb.GetFile(r.Context(), sid, path)
	if err != nil {
		writeError(w, 502, err)
		return
	}
	if data == nil {
		writeError(w, 404, errNotFound)
		return
	}
	ct := mime.TypeByExtension(filepath.Ext(path))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("content-type", ct)
	w.Header().Set("content-disposition", "inline; filename=\""+filepath.Base(path)+"\"")
	_, _ = w.Write(data)
}

func sandboxClearAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	convID := pathParam(r, "id")
	sb := d.Tools.Sandbox()
	sid, _ := store.GetConvProviderStateKey(r.Context(), d.DB, convID, "sandbox_id")
	if sid != "" && sb != nil {
		_ = sb.Release(r.Context(), sid)
		// Forget the session so the next python_execute provisions a fresh one.
		_ = store.SetConvProviderStateKey(r.Context(), d.DB, convID, "sandbox_id", "")
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
