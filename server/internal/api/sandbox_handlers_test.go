package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
	"aivory/server/internal/tools"
)

func TestConversationSandboxBrowserIsReadOnlyScopedAndPathSafe(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "sandbox-browser.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES
		('owner','owner@example.test','h','user','active'),
		('other','other@example.test','h','user','active')`)
	if _, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "conv", UserID: "owner", Title: "Sandbox", ProviderState: json.RawMessage(`{"sandbox_id":"sid_browser"}`),
	}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	var sidecarCalls atomic.Int32
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sidecarCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer sandbox-secret" {
			t.Errorf("authorization=%q", got)
		}
		var body struct {
			SessionID string `json:"session_id"`
			Path      string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode sidecar request: %v", err)
		}
		if body.SessionID != "sid_browser" {
			t.Errorf("session_id=%q", body.SessionID)
		}
		switch r.URL.Path {
		case "/files/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
				{"path": "outputs/report.txt", "size": 6},
				{"path": "uploads/data.csv", "size": 12},
			}})
		case "/files/get":
			if body.Path != "outputs/report.txt" {
				t.Errorf("path=%q", body.Path)
			}
			_, _ = io.WriteString(w, `{"data_base64":"cmVwb3J0"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer sidecar.Close()
	if err := store.SetSetting(db, "sandbox_base_url", sidecar.URL); err != nil {
		t.Fatalf("set sandbox URL: %v", err)
	}
	if err := store.SetSetting(db, "sandbox_api_key", "sandbox-secret"); err != nil {
		t.Fatalf("set sandbox key: %v", err)
	}
	d := Deps{DB: db, Tools: tools.NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))}

	request := func(method, target, userID string, handler handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "conv"}))
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"}))
		rec := httptest.NewRecorder()
		handler(d, rec, req)
		return rec
	}

	listRec := request(http.MethodGet, "/api/conversations/conv/sandbox", "owner", sandboxFilesHandler)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), "outputs/report.txt") {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}

	fileRec := request(http.MethodGet, "/api/conversations/conv/sandbox/file?path=outputs%2Freport.txt", "owner", sandboxFileGetHandler)
	if fileRec.Code != http.StatusOK || fileRec.Body.String() != "report" {
		t.Fatalf("file status=%d body=%q", fileRec.Code, fileRec.Body.String())
	}
	if got := fileRec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Fatalf("missing sandbox CSP: %q", got)
	}
	if got := fileRec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
		t.Fatalf("content disposition=%q", got)
	}

	callsBeforeDenied := sidecarCalls.Load()
	deniedRec := request(http.MethodGet, "/api/conversations/conv/sandbox", "other", sandboxFilesHandler)
	if deniedRec.Code != http.StatusNotFound {
		t.Fatalf("foreign conversation status=%d body=%s", deniedRec.Code, deniedRec.Body.String())
	}
	if sidecarCalls.Load() != callsBeforeDenied {
		t.Fatal("foreign conversation access reached the sandbox sidecar")
	}

	traversalRec := request(http.MethodGet, "/api/conversations/conv/sandbox/file?path=..%2Fsecret.txt", "owner", sandboxFileGetHandler)
	if traversalRec.Code != http.StatusBadRequest {
		t.Fatalf("traversal status=%d body=%s", traversalRec.Code, traversalRec.Body.String())
	}
	if sidecarCalls.Load() != callsBeforeDenied {
		t.Fatal("unsafe path reached the sandbox sidecar")
	}
}

func TestValidSandboxFilePath(t *testing.T) {
	for _, valid := range []string{"outputs/report.pdf", "README.md", "folder/file with spaces.txt"} {
		if !validSandboxFilePath(valid) {
			t.Errorf("valid path rejected: %q", valid)
		}
	}
	for _, invalid := range []string{"", "/workspace/report.pdf", "../secret", "folder/../secret", "folder//file", `folder\\file`} {
		if validSandboxFilePath(invalid) {
			t.Errorf("invalid path accepted: %q", invalid)
		}
	}
}
