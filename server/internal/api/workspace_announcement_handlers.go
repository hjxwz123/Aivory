package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/store"
)

const (
	maxWorkspaceAnnouncementBody = 64 * 1024
	maxWorkspaceAnnouncementBar  = 8 * 1024
	maxWorkspaceAnnouncementURL  = 2 * 1024
)

func workspaceAnnouncementDefault() announcement {
	return announcement{RememberDismiss: true}
}

func normalizeWorkspaceAnnouncement(a announcement) (announcement, error) {
	a.Title = strings.TrimSpace(a.Title)
	a.Body = strings.TrimSpace(a.Body)
	a.ImageURL = strings.TrimSpace(a.ImageURL)
	a.BarHTML = strings.TrimSpace(a.BarHTML)
	if len(a.Title) > 120 || len(a.Body) > maxWorkspaceAnnouncementBody || len(a.ImageURL) > maxWorkspaceAnnouncementURL || len(a.BarHTML) > maxWorkspaceAnnouncementBar {
		return announcement{}, errors.New("announcement content is too large")
	}
	// Timestamps are server-owned and are always refreshed on save.
	a.UpdatedAt = time.Now().Unix()
	if !a.BarEnabled {
		a.BarHTML = ""
	}
	a.BarUpdatedAt = a.UpdatedAt
	return a, nil
}

func workspaceAnnouncementHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	raw, err := store.GetWorkspaceAnnouncement(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	var a announcement
	if json.Unmarshal([]byte(raw), &a) != nil {
		a = workspaceAnnouncementDefault()
	}
	writeJSON(w, http.StatusOK, a)
}

func updateWorkspaceAnnouncementHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	decision, authErr := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: id, UserID: u.ID, Action: store.ActionWorkspaceSettingsUpdate,
	})
	if authErr != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if !decision.Allowed {
		writeError(w, http.StatusForbidden, errForbidden)
		return
	}
	var incoming announcement
	if err := decodeJSON(r, &incoming); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	a, err := normalizeWorkspaceAnnouncement(incoming)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Keep the popup and top-bar dismiss versions independent, matching the
	// global announcement behavior: editing only the popup must not resurrect a
	// bar that members already dismissed.
	if previousRaw, readErr := store.GetWorkspaceAnnouncement(r.Context(), d.DB, id, u.ID); readErr == nil {
		var previous announcement
		if json.Unmarshal([]byte(previousRaw), &previous) == nil && previous.BarEnabled == a.BarEnabled && strings.TrimSpace(previous.BarHTML) == a.BarHTML {
			a.BarUpdatedAt = previous.BarUpdatedAt
		}
	}
	raw, err := json.Marshal(a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	updatedAt, err := store.UpdateWorkspaceAnnouncementAt(r.Context(), d.DB, id, u.ID, string(raw), a.UpdatedAt)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			writeError(w, http.StatusForbidden, errForbidden)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	a.UpdatedAt = updatedAt
	publishWorkspaceAccessEvent(d, r, id, "workspace.announcement_updated", u.ID)
	writeJSON(w, http.StatusOK, a)
}

func uploadWorkspaceAnnouncementImageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	decision, err := store.AuthorizeWorkspace(r.Context(), d.DB, store.WorkspaceAuthorizationRequest{
		WorkspaceID: id, UserID: u.ID, Action: store.ActionWorkspaceSettingsUpdate,
	})
	if err != nil || !decision.Allowed {
		if err == nil && !decision.Allowed {
			writeError(w, http.StatusForbidden, errForbidden)
			return
		}
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	data, ext, _, status, err := readValidatedImageUpload(r, false, errIconBadExt)
	if err != nil {
		writeError(w, status, err)
		return
	}
	filename, err := saveUploadedIcon(d, data, ext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": "/api/icons/" + filename, "filename": filename})
}
