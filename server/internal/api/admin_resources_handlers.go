package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/store"
)

const (
	adminKBProjectDefaultPageSize = 50
	adminKBProjectMaxPageSize     = 200
)

func parseAdminKBProjectPage(r *http.Request) (limit, offset int) {
	limit = adminKBProjectDefaultPageSize
	if value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && value > 0 {
		limit = value
	}
	if limit > adminKBProjectMaxPageSize {
		limit = adminKBProjectMaxPageSize
	}
	if value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("offset"))); err == nil && value >= 0 {
		offset = value
	}
	return limit, offset
}

func adminKBProjectFilter(r *http.Request) store.AdminResourceFilter {
	return store.AdminResourceFilter{
		Search: r.URL.Query().Get("search"),
		User:   r.URL.Query().Get("user"),
	}
}

func listKnowledgeBasesAdminResource(d Deps, w http.ResponseWriter, r *http.Request) {
	limit, offset := parseAdminKBProjectPage(r)
	filter := adminKBProjectFilter(r)
	total, err := store.CountAdminKnowledgeBases(r.Context(), d.DB, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	items, err := store.ListAdminKnowledgeBases(r.Context(), d.DB, filter, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

func getKnowledgeBaseAdminResource(d Deps, w http.ResponseWriter, r *http.Request) {
	item, err := store.GetAdminKnowledgeBase(r.Context(), d.DB, pathParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func listProjectsAdminResource(d Deps, w http.ResponseWriter, r *http.Request) {
	limit, offset := parseAdminKBProjectPage(r)
	filter := adminKBProjectFilter(r)
	total, err := store.CountAdminProjects(r.Context(), d.DB, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	items, err := store.ListAdminProjects(r.Context(), d.DB, filter, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

func getProjectAdminResource(d Deps, w http.ResponseWriter, r *http.Request) {
	item, err := store.GetAdminProject(r.Context(), d.DB, pathParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func listProjectConversationsAdminResource(d Deps, w http.ResponseWriter, r *http.Request) {
	projectID := pathParam(r, "id")
	if _, err := store.GetAdminProject(r.Context(), d.DB, projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	limit, offset := parseAdminKBProjectPage(r)
	total, err := store.CountAdminProjectConversations(r.Context(), d.DB, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	items, err := store.ListAdminProjectConversations(r.Context(), d.DB, projectID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}
