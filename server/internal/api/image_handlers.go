package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aurelia/server/internal/store"
)

// §4.20 Image Generation Studio — style endpoints. Image GENERATION itself
// reuses the chat pipeline: pick an image model in a conversation and send (the
// orchestrator's image branch force-calls image_generate). These endpoints only
// serve the admin-managed style catalog.

// listImageStylesPublic returns the enabled styles for the composer's style
// picker. The hidden_prompt is stripped — users must never see it.
func listImageStylesPublic(d Deps, w http.ResponseWriter, r *http.Request) {
	styles, err := store.ListImageStyles(r.Context(), d.DB, true)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	for i := range styles {
		styles[i].HiddenPrompt = "" // never leak to users (omitempty drops it)
	}
	writeJSON(w, 200, styles)
}

// ---- admin CRUD ----

func listImageStylesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	styles, err := store.ListImageStyles(r.Context(), d.DB, false)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, styles)
}

type imageStyleReq struct {
	Name            string `json:"name"`
	ExampleImageURL string `json:"example_image_url"`
	HiddenPrompt    string `json:"hidden_prompt"`
	Enabled         bool   `json:"enabled"`
	SortOrder       int    `json:"sort_order"`
}

func createImageStyleAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var req imageStyleReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	st, err := store.CreateImageStyle(r.Context(), d.DB, store.ImageStyle{
		Name:            req.Name,
		ExampleImageURL: strings.TrimSpace(req.ExampleImageURL),
		HiddenPrompt:    req.HiddenPrompt,
		Enabled:         req.Enabled,
		SortOrder:       req.SortOrder,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, st)
}

func updateImageStyleAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	cur, err := store.GetImageStyle(r.Context(), d.DB, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	var req imageStyleReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	cur.Name = req.Name
	cur.ExampleImageURL = strings.TrimSpace(req.ExampleImageURL)
	cur.HiddenPrompt = req.HiddenPrompt
	cur.Enabled = req.Enabled
	cur.SortOrder = req.SortOrder
	st, err := store.UpdateImageStyle(r.Context(), d.DB, *cur)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, st)
}

func deleteImageStyleAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DeleteImageStyle(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// listMyImages returns the signed-in user's own generated-image gallery (§4.20).
// Each item links back to the conversation that produced it; bytes are served by
// the existing /api/artifacts/:id (ownership-checked).
func listMyImages(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	imgs, err := store.ListUserImageArtifacts(r.Context(), d.DB, u.ID, limit, offset)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	for i := range imgs {
		imgs[i].URL = "/api/artifacts/" + imgs[i].ID
	}
	writeJSON(w, 200, imgs)
}

// listUserImagesAdmin returns a user's generated-image gallery (§8.1 drill-down).
// Each image links back to the conversation that produced it. Admins view the
// bytes via /api/artifacts/:id (downloadArtifactHandler bypasses ownership).
func listUserImagesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	imgs, err := store.ListUserImageArtifacts(r.Context(), d.DB, pathParam(r, "id"), limit, offset)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	for i := range imgs {
		imgs[i].URL = "/api/artifacts/" + imgs[i].ID
	}
	writeJSON(w, 200, imgs)
}
