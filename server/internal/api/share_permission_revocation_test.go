package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestConversationSharePermissionRevocationCoversPublicSurfaces(t *testing.T) {
	root := t.TempDir()
	db := openMigrated(t, filepath.Join(root, "share-permission-revocation.db"))
	defer db.Close()

	allowed := store.DefaultUserGroupPermissions()
	allowedRaw, err := json.Marshal(allowed)
	if err != nil {
		t.Fatalf("marshal allowed permissions: %v", err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,permissions) VALUES('sharing-group','Sharing',?)`, string(allowedRaw))
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,group_id) VALUES('publisher','publisher@example.test','h','user','sharing-group')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('viewer','viewer@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('shared-conversation','publisher','Shared conversation')`)

	message, err := store.CreateMessage(t.Context(), db, store.Message{
		ID:             "shared-assistant-message",
		ConversationID: "shared-conversation",
		Role:           "assistant",
		Blocks:         json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatalf("create artifact message: %v", err)
	}
	filePath := filepath.Join(root, "shared.txt")
	artifactPath := filepath.Join(root, "shared.bin")
	if err := os.WriteFile(filePath, []byte("shared file"), 0o600); err != nil {
		t.Fatalf("write shared file: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("shared artifact"), 0o600); err != nil {
		t.Fatalf("write shared artifact: %v", err)
	}
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('shared-file','publisher','shared-conversation','shared.txt','text/plain',11,?,'other',0)`, filePath)
	mustExec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes)
		VALUES('shared-artifact',?,'shared.bin',?,'application/octet-stream',15)`, message.ID, artifactPath)

	snapshot := []byte(`[
		{"role":"user","blocks":[],"citations":[],"attachments":[{"id":"shared-file","url":"/api/files/shared-file"}],"created_at":1},
		{"role":"assistant","blocks":[{"kind":"artifact","file_ref":"shared-artifact","url":"/api/artifacts/shared-artifact"}],"citations":[],"attachments":[],"created_at":2}
	]`)
	share, err := store.CreateShare(t.Context(), db, "publisher", "shared-conversation", "Shared conversation", snapshot)
	if err != nil {
		t.Fatalf("create share: %v", err)
	}

	deps := Deps{DB: db, Config: config.Config{UploadDir: root, ArtifactDir: root}}
	publicRequest := func(handler handler, path string, params map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, params))
		rec := httptest.NewRecorder()
		handler(deps, rec, req)
		return rec
	}
	clone := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/shared/"+share.ID+"/clone", nil)
		ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"token": share.ID})
		ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "viewer", Role: "user", Status: "active"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		cloneSharedConversationHandler(deps, rec, req)
		return rec
	}
	assertPublicStatus := func(want int) {
		t.Helper()
		requests := []struct {
			name    string
			handler handler
			path    string
			params  map[string]string
		}{
			{"snapshot", publicSharedHandler, "/api/public/shared/" + share.ID, map[string]string{"token": share.ID}},
			{"file", publicSharedFileHandler, "/api/public/shared/" + share.ID + "/files/shared-file", map[string]string{"token": share.ID, "id": "shared-file"}},
			{"artifact", publicSharedArtifactHandler, "/api/public/shared/" + share.ID + "/artifacts/shared-artifact", map[string]string{"token": share.ID, "id": "shared-artifact"}},
		}
		for _, request := range requests {
			rec := publicRequest(request.handler, request.path, request.params)
			if rec.Code != want {
				t.Fatalf("%s status=%d body=%s, want %d", request.name, rec.Code, rec.Body.String(), want)
			}
		}
	}

	assertPublicStatus(http.StatusOK)

	revoked := allowed
	revoked.AllowSharing = false
	revokedRaw, err := json.Marshal(revoked)
	if err != nil {
		t.Fatalf("marshal revoked permissions: %v", err)
	}
	mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='sharing-group'`, string(revokedRaw))

	assertPublicStatus(http.StatusNotFound)
	if rec := clone(); rec.Code != http.StatusNotFound {
		t.Fatalf("clone while revoked status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if _, err := store.GetShareByConversation(t.Context(), db, "shared-conversation", "publisher"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("owner share lookup while revoked error=%v, want ErrNotFound", err)
	}
	if _, err := store.CreateShare(t.Context(), db, "publisher", "shared-conversation", "rotated", snapshot); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("direct share rotation while revoked error=%v, want ErrNotFound", err)
	}
	var retainedToken string
	if err := db.QueryRow(`SELECT id FROM conversation_shares WHERE conversation_id='shared-conversation'`).Scan(&retainedToken); err != nil {
		t.Fatalf("query retained share: %v", err)
	}
	if retainedToken != share.ID {
		t.Fatalf("revocation changed retained token: got %q want %q", retainedToken, share.ID)
	}
	var viewerConversations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE user_id='viewer'`).Scan(&viewerConversations); err != nil {
		t.Fatalf("count viewer conversations: %v", err)
	}
	if viewerConversations != 0 {
		t.Fatalf("revoked clone created %d viewer conversations", viewerConversations)
	}

	// Administrators bypass user-group capability restrictions, including for
	// links they published before their role changed.
	mustExec(t, db, `UPDATE users SET role='admin' WHERE id='publisher'`)
	assertPublicStatus(http.StatusOK)
	mustExec(t, db, `UPDATE users SET role='user' WHERE id='publisher'`)
	assertPublicStatus(http.StatusNotFound)

	mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='sharing-group'`, string(allowedRaw))
	assertPublicStatus(http.StatusOK)
	if rec := clone(); rec.Code != http.StatusCreated {
		t.Fatalf("clone after permission restore status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}
}
