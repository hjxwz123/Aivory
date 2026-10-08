package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"aivory/server/internal/store"
)

func listSiteNotifications(d Deps, w http.ResponseWriter, r *http.Request) {
	siteNotificationList(d, w, r, false)
}
func listSiteNotificationsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	siteNotificationList(d, w, r, true)
}
func siteNotificationList(d Deps, w http.ResponseWriter, r *http.Request, admin bool) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	page, err := store.ListSiteNotifications(r.Context(), d.DB, authUser(r).ID, admin, r.URL.Query().Get("search"), limit, offset)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, page)
}

func getSiteNotification(d Deps, w http.ResponseWriter, r *http.Request) {
	siteNotificationGet(d, w, r, false)
}
func getSiteNotificationAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	siteNotificationGet(d, w, r, true)
}
func siteNotificationGet(d Deps, w http.ResponseWriter, r *http.Request, admin bool) {
	notification, err := store.GetSiteNotification(r.Context(), d.DB, pathParam(r, "id"), admin)
	if err != nil {
		siteNotificationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, notification)
}

func saveSiteNotificationAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		Enabled *bool  `json:"enabled"`
	}
	if decodeJSON(r, &req) != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Title, req.Body = strings.TrimSpace(req.Title), strings.TrimSpace(req.Body)
	if req.Title == "" || utf8.RuneCountInString(req.Title) > 120 || req.Body == "" || len(req.Body) > 256*1024 {
		writeError(w, 400, errInvalidInput)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	notification, err := store.SaveSiteNotification(r.Context(), d.DB, pathParam(r, "id"), req.Title, req.Body, enabled)
	if err != nil {
		siteNotificationError(w, err)
		return
	}
	status := 200
	if r.Method == http.MethodPost {
		status = 201
	}
	writeJSON(w, status, notification)
}

func deleteSiteNotificationAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	if err := store.DeleteSiteNotification(r.Context(), d.DB, pathParam(r, "id")); err != nil {
		siteNotificationError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func readSiteNotification(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
		Dismiss bool   `json:"dismiss"`
		Read    *bool  `json:"read"`
	}
	if decodeJSON(r, &req) != nil || req.Version == "" || len(req.Version) > 100 {
		writeError(w, 400, errInvalidInput)
		return
	}
	read := req.Read == nil || *req.Read
	if err := store.ReadSiteNotification(r.Context(), d.DB, authUser(r).ID, pathParam(r, "id"), req.Version, read, req.Dismiss); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 409, errors.New("notification has changed"))
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func siteNotificationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, err)
		return
	}
	writeError(w, 500, err)
}
