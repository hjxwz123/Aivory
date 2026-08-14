package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/store"
)

func listGeneratedImagesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	filter := store.AdminGeneratedImageFilter{
		UserID:  strings.TrimSpace(q.Get("user_id")),
		UserQ:   strings.TrimSpace(q.Get("user")),
		ModelID: strings.TrimSpace(q.Get("model_id")),
	}
	total, err := store.CountAdminGeneratedImages(r.Context(), d.DB, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	images, err := store.ListAdminGeneratedImages(r.Context(), d.DB, filter, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for i := range images {
		images[i].URL = "/api/artifacts/" + images[i].ID
	}
	models, err := store.ListAdminGeneratedImageModels(r.Context(), d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  images,
		"models": models,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func getGeneratedImageAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	image, err := store.GetAdminGeneratedImage(r.Context(), d.DB, pathParam(r, "id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	image.URL = "/api/artifacts/" + image.ID
	writeJSON(w, http.StatusOK, image)
}
