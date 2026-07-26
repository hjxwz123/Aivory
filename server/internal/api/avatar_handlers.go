package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/storage"
)

var (
	errAvatarBadExt  = errors.New("avatar: extension must be one of png/jpg/jpeg")
	errAvatarStorage = errors.New("avatar storage is not configured; choose local, S3, or Aliyun OSS in admin storage settings")
)

const (
	avatarObjectPrefix   = "avatars/"
	avatarStorageTimeout = 30 * time.Second
)

// uploadAvatar stores profile images directly from the API process in the
// administrator-selected object backend. The returned application URL stays
// stable while S3/OSS buckets remain private and local storage stays internal.
func uploadAvatar(d Deps, w http.ResponseWriter, r *http.Request) {
	data, ext, mimeType, status, err := readValidatedImageUpload(r, false, errAvatarBadExt)
	if err != nil {
		writeError(w, status, err)
		return
	}

	obj := privateObjectStorage(d)
	if obj == nil || !obj.Enabled() {
		writeError(w, http.StatusServiceUnavailable, errAvatarStorage)
		return
	}
	id, err := randomHex(12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	filename := id + "." + ext

	ctx, cancel := context.WithTimeout(r.Context(), avatarStorageTimeout)
	defer cancel()
	if _, err := obj.Put(ctx, avatarObjectPrefix+filename, data, mimeType); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"url":      "/api/avatars/" + filename,
		"filename": filename,
	})
}

// serveAvatar proxies a tightly bounded private object through a stable public
// URL. Filenames are random and content was validated on write; public access
// is required for shared-conversation author identities and unauthenticated
// <img> requests that cannot refresh an expired access token.
func serveAvatar(d Deps, w http.ResponseWriter, r *http.Request) {
	filename := pathParam(r, "filename")
	if !isSafeAvatarFilename(filename) {
		writeError(w, http.StatusNotFound, errors.New("not found"))
		return
	}
	obj := privateObjectStorage(d)
	if obj == nil || !obj.Enabled() {
		writeError(w, http.StatusServiceUnavailable, errAvatarStorage)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), avatarStorageTimeout)
	defer cancel()
	data, err := obj.Get(ctx, avatarObjectPrefix+filename, maxIconBytes)
	if errors.Is(err, storage.ErrObjectNotFound) {
		writeError(w, http.StatusNotFound, errors.New("not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	w.Header().Set("Content-Type", allowedIconExt[ext])
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func isSafeAvatarFilename(name string) bool {
	if !isSafeIconFilename(name) {
		return false
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	return ext == "png" || ext == "jpeg"
}
