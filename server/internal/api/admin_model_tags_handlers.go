package api

import (
	"errors"
	"net/http"
	"strings"

	"aurelia/server/internal/store"
)

// Model tags (§ model tags) — admin-managed labels assigned to models. Any
// authenticated user can LIST them (the picker renders the filter chips); only
// an admin can create / rename / delete.

// listModelTagsPublic returns the tags for the model-picker filter.
func listModelTagsPublic(d Deps, w http.ResponseWriter, r *http.Request) {
	tags, err := store.ListModelTags(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, tags)
}

func listModelTagsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	tags, err := store.ListModelTags(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, tags)
}

type modelTagReq struct {
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

func createModelTagAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var req modelTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	if existing, err := store.GetModelTagByName(r.Context(), d.DB, req.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrModelTagNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	tag, err := store.CreateModelTag(r.Context(), d.DB, req.Name, req.SortOrder)
	if err != nil {
		if errors.Is(err, store.ErrModelTagNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, tag)
}

func updateModelTagAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req modelTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	if existing, err := store.GetModelTagByName(r.Context(), d.DB, req.Name); err == nil && existing != nil && existing.ID != id {
		writeError(w, 409, store.ErrModelTagNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	tag, err := store.UpdateModelTag(r.Context(), d.DB, id, req.Name, req.SortOrder)
	if err != nil {
		if errors.Is(err, store.ErrModelTagNameExists) {
			writeError(w, 409, err)
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, tag)
}

func deleteModelTagAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DeleteModelTag(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
