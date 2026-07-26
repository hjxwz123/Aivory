package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func testAvatarPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 120, G: 80, B: 40, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestAvatarUploadAndServeUseConfiguredObjectStorage(t *testing.T) {
	pngBytes := testAvatarPNG(t)
	localStorageDir := t.TempDir()

	db := openMigrated(t, filepath.Join(t.TempDir(), "avatars.db"))
	defer db.Close()
	if err := store.SetSetting(db, "storage_provider", "local"); err != nil {
		t.Fatalf("set storage provider: %v", err)
	}
	d := Deps{DB: db, Config: config.Config{LocalStorageDir: localStorageDir}}
	user := &store.User{ID: "u-avatar", Role: "user", Status: "active"}

	uploadRec := httptest.NewRecorder()
	uploadAvatar(d, uploadRec, uploadRequestWithFile(t, "/api/me/avatar", user, "portrait.png", pngBytes))
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body=%s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploaded struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(uploadRec.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if uploaded.URL != "/api/avatars/"+uploaded.Filename || !isSafeAvatarFilename(uploaded.Filename) {
		t.Fatalf("upload response = %+v", uploaded)
	}
	storedPath := filepath.Join(localStorageDir, "workspaces", "avatars", uploaded.Filename)
	storedBytes, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatalf("read directly stored avatar: %v", err)
	}
	if !bytes.Equal(storedBytes, pngBytes) {
		t.Fatal("directly stored avatar bytes differ from upload")
	}

	mx := newMux()
	mx.handle(http.MethodGet, "/api/avatars/:filename", func(w http.ResponseWriter, r *http.Request) {
		serveAvatar(d, w, r)
	})
	serveRec := httptest.NewRecorder()
	mx.ServeHTTP(serveRec, httptest.NewRequest(http.MethodGet, uploaded.URL, nil))
	if serveRec.Code != http.StatusOK {
		t.Fatalf("serve status = %d, body=%s", serveRec.Code, serveRec.Body.String())
	}
	if !bytes.Equal(serveRec.Body.Bytes(), pngBytes) {
		t.Fatal("served avatar bytes differ from stored object")
	}
	if got := serveRec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
	if got := serveRec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("cache control = %q", got)
	}
}

func TestAvatarUploadRequiresConfiguredStorage(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "avatars-unconfigured.db"))
	defer db.Close()
	d := Deps{DB: db}
	user := &store.User{ID: "u-avatar", Role: "user", Status: "active"}

	rec := httptest.NewRecorder()
	uploadAvatar(d, rec, uploadRequestWithFile(t, "/api/me/avatar", user, "portrait.png", testAvatarPNG(t)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAvatarUploadRejectsSVG(t *testing.T) {
	d := Deps{}
	user := &store.User{ID: "u-avatar", Role: "user", Status: "active"}
	rec := httptest.NewRecorder()
	uploadAvatar(d, rec, uploadRequestWithFile(t, "/api/me/avatar", user, "portrait.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminIconUploadStillUsesLocalIconDirectory(t *testing.T) {
	uploadDir := t.TempDir()
	rec := httptest.NewRecorder()
	uploadIconAdmin(
		Deps{Config: config.Config{UploadDir: uploadDir}},
		rec,
		uploadRequestWithFile(t, "/api/admin/icons", &store.User{ID: "admin", Role: "admin"}, "model.png", testAvatarPNG(t)),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var uploaded struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("decode icon response: %v", err)
	}
	if uploaded.URL != "/api/icons/"+uploaded.Filename {
		t.Fatalf("icon response = %+v", uploaded)
	}
	if _, err := os.Stat(filepath.Join(uploadDir, "icons", uploaded.Filename)); err != nil {
		t.Fatalf("stored icon: %v", err)
	}
}
