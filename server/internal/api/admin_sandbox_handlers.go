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
	// The workspace bytes are attacker-controlled: per the sandbox threat model the
	// LLM (driven by an untrusted end-user prompt) can write any file the user
	// steers it to under /workspace (e.g. a `<script>`-laden report.html or a
	// scripted .svg). This admin preview must treat them as hostile so a preview
	// can't run script on the admin origin. Belt-and-braces: (1) nosniff + a strict
	// CSP/sandbox neutralise any active document the browser does render, and (2)
	// anything that isn't a known-inert preview type is force-downloaded with an
	// opaque content-type instead of rendered inline. Mirrors serveIcon's SVG
	// hardening in admin_uploads.go.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data: blob:; style-src 'unsafe-inline'; sandbox")
	disposition := "attachment"
	if isInlinePreviewType(ct) {
		disposition = "inline"
	} else {
		// Never hand the browser an active content-type (text/html, image/svg+xml,
		// *xml, *javascript, …) for a download it might still try to render.
		ct = "application/octet-stream"
	}
	w.Header().Set("content-type", ct)
	w.Header().Set("content-disposition", disposition+"; filename=\""+filepath.Base(path)+"\"")
	_, _ = w.Write(data)
}

// isInlinePreviewType reports whether a content type is safe to render inline in
// the admin's browser. Only inert image/document types the inspector wants to
// preview qualify; everything else (html, svg, xml, js, …) is force-downloaded so
// it can't execute script on the admin origin even if the CSP above were bypassed.
func isInlinePreviewType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp",
		"image/x-icon", "image/vnd.microsoft.icon", "application/pdf",
		"text/plain":
		return true
	}
	return false
}

func sandboxClearAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	convID := pathParam(r, "id")
	sb := d.Tools.Sandbox()
	sid, _ := store.GetConvProviderStateKey(r.Context(), d.DB, convID, "sandbox_id")
	if sid != "" && sb != nil {
		// Discard (not archive): a real purge. Pass convID so the sidecar also
		// deletes the stable-key archive — otherwise §4.5-C G2 restore would
		// resurrect every "cleared" file on the next code run.
		_ = sb.ReleaseDiscard(r.Context(), sid, convID)
		// Forget the session so the next python_execute provisions a fresh one.
		_ = store.SetConvProviderStateKey(r.Context(), d.DB, convID, "sandbox_id", "")
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
