package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestCreateShareFreezesPublicDisplayIdentities(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "share-identities.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,settings) VALUES('owner','owner@example.test','h','Owner Name','{"avatar_url":"/api/icons/owner.png"}')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,settings) VALUES('contributor','contributor@example.test','h','Contributor Name','{"avatar_url":"https://cdn.example.test/contributor.png"}')`)

	channel, err := store.CreateChannel(t.Context(), db, "Share", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(t.Context(), db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "share-model",
		Label:     "Share Model",
		Icon:      "/api/icons/share-model.png",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	conv, err := store.CreateConversation(t.Context(), db, store.Conversation{
		ID: "source-identities", UserID: "owner", Title: "Identity snapshot", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	ownerMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ConversationID: conv.ID,
		Role:           "user",
		Blocks:         json.RawMessage(`[{"kind":"text","text":"owner question"}]`),
		// Empty AuthorID exercises the legacy creator fallback.
	})
	if err != nil {
		t.Fatalf("create owner message: %v", err)
	}
	assistantMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ConversationID: conv.ID,
		ParentID:       ownerMessage.ID,
		Role:           "assistant",
		ModelID:        model.ID,
		Blocks:         json.RawMessage(`[{"kind":"text","text":"model answer"}]`),
	})
	if err != nil {
		t.Fatalf("create assistant message: %v", err)
	}
	contributorMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ConversationID: conv.ID,
		ParentID:       assistantMessage.ID,
		Role:           "user",
		AuthorID:       "contributor",
		Blocks:         json.RawMessage(`[{"kind":"text","text":"contributor question"}]`),
	})
	if err != nil {
		t.Fatalf("create contributor message: %v", err)
	}
	if _, err := store.CreateMessage(t.Context(), db, store.Message{
		ConversationID: conv.ID,
		ParentID:       contributorMessage.ID,
		Role:           "assistant",
		ModelID:        model.ID,
		Fast:           true,
		Blocks:         json.RawMessage(`[{"kind":"text","text":"fast answer"}]`),
	}); err != nil {
		t.Fatalf("create fast assistant message: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/conversations/"+conv.ID+"/share", nil)
	ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": conv.ID})
	ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "owner", Role: "user", Status: "active"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	createShareHandler(Deps{DB: db}, rec, req)
	if rec.Code != 201 {
		t.Fatalf("create share status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created shareInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode share info: %v", err)
	}

	// A public share is a frozen snapshot: later profile/model edits must not
	// silently rewrite the identity shown to people who already have the link.
	mustExec(t, db, `UPDATE users SET name='Renamed Owner', settings='{}' WHERE id='owner'`)
	mustExec(t, db, `UPDATE models SET label='Renamed Model', icon='' WHERE id=?`, model.ID)

	publicReq := httptest.NewRequest("GET", "/api/public/shared/"+created.ID, nil)
	publicReq = publicReq.WithContext(context.WithValue(publicReq.Context(), pathCtxKey{}, map[string]string{"token": created.ID}))
	publicRec := httptest.NewRecorder()
	publicSharedHandler(Deps{DB: db}, publicRec, publicReq)
	if publicRec.Code != 200 {
		t.Fatalf("public share status=%d body=%s", publicRec.Code, publicRec.Body.String())
	}
	var payload struct {
		Title    string               `json:"title"`
		Messages []publicShareMessage `json:"messages"`
	}
	if err := json.Unmarshal(publicRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode public share: %v", err)
	}
	if payload.Title != "Identity snapshot" || len(payload.Messages) != 4 {
		t.Fatalf("public payload = %+v", payload)
	}
	if got := payload.Messages[0]; got.AuthorName != "Owner Name" || got.AuthorAvatar != "/api/icons/owner.png" {
		t.Fatalf("legacy owner identity = %+v", got)
	}
	if got := payload.Messages[1]; got.ModelLabel != "Share Model" || got.ModelIcon != "/api/icons/share-model.png" {
		t.Fatalf("assistant model identity = %+v", got)
	}
	if got := payload.Messages[2]; got.AuthorName != "Contributor Name" || got.AuthorAvatar != "https://cdn.example.test/contributor.png" {
		t.Fatalf("contributor identity = %+v", got)
	}
	if got := payload.Messages[3]; !got.Fast || got.ModelLabel != "" || got.ModelIcon != "" {
		t.Fatalf("fast identity was not masked: %+v", got)
	}
	body := publicRec.Body.String()
	for _, privateField := range []string{"owner@example.test", "contributor@example.test", `"author_id"`, `"model_id"`, `"provider"`, `"cost"`} {
		if strings.Contains(body, privateField) {
			t.Fatalf("public share leaked %q: %s", privateField, body)
		}
	}
}

func TestCloneSharedConversationCopiesSnapshotForCurrentUser(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "share-clone.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('viewer','viewer@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('source','owner','Source chat')`)
	snapshot := `[
		{
			"role":"user",
			"blocks":[{"kind":"text","text":"please inspect this image"}],
			"attachments":[{"id":"f_img","filename":"scan.png","kind":"image","url":"/api/files/f_img"}],
			"citations":[],
			"created_at":100
		},
		{
			"role":"assistant",
			"blocks":[
				{"kind":"text","text":"looks good"},
				{"kind":"artifact","title":"result.png","url":"/api/artifacts/art_img","summary":"image/png"}
			],
			"attachments":[],
			"citations":[],
			"created_at":101
		}
	]`
	mustExec(t, db, `INSERT INTO conversation_shares(id,conversation_id,user_id,title,snapshot) VALUES('sh_clone','source','owner','Shared title',?)`, snapshot)

	req := httptest.NewRequest("POST", "/api/shared/sh_clone/clone", nil)
	ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"token": "sh_clone"})
	ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "viewer", Role: "user", Status: "active"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	cloneSharedConversationHandler(Deps{DB: db}, rec, req)
	if rec.Code != 201 {
		t.Fatalf("clone status=%d body=%s", rec.Code, rec.Body.String())
	}
	var conv store.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatalf("decode cloned conversation: %v", err)
	}
	if conv.UserID != "viewer" || conv.Title != "Shared title" {
		t.Fatalf("cloned conversation = %+v, want viewer-owned Shared title", conv)
	}
	msgs, err := store.ListMessages(context.Background(), db, conv.ID, "")
	if err != nil {
		t.Fatalf("list cloned messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("cloned messages = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].AuthorID != "viewer" || msgs[1].ParentID != msgs[0].ID {
		t.Fatalf("unexpected cloned message chain: %+v", msgs)
	}
	if got := string(msgs[0].Attachments); !strings.Contains(got, "/api/public/shared/sh_clone/files/f_img") {
		t.Fatalf("attachment URL was not share-scoped: %s", got)
	}
	if got := string(msgs[1].Blocks); !strings.Contains(got, "/api/public/shared/sh_clone/artifacts/art_img") {
		t.Fatalf("artifact URL was not share-scoped: %s", got)
	}
}

func TestShareSnapshotReferencesAssetUsesOnlyStructuredResourceFields(t *testing.T) {
	tests := []struct {
		name      string
		snapshot  string
		assetType string
		id        string
		want      bool
	}{
		{
			name:      "attachment id",
			snapshot:  `[{"attachments":[{"id":"f_allowed"}]}]`,
			assetType: "file",
			id:        "f_allowed",
			want:      true,
		},
		{
			name:      "legacy attachment url",
			snapshot:  `[{"attachments":[{"url":"/api/files/f_allowed"}]}]`,
			assetType: "file",
			id:        "f_allowed",
			want:      true,
		},
		{
			name:      "artifact file ref",
			snapshot:  `[{"blocks":[{"kind":"artifact","file_ref":"a_allowed"}]}]`,
			assetType: "artifact",
			id:        "a_allowed",
			want:      true,
		},
		{
			name:      "legacy artifact url",
			snapshot:  `[{"blocks":[{"kind":"artifact","url":"/api/artifacts/a_allowed"}]}]`,
			assetType: "artifact",
			id:        "a_allowed",
			want:      true,
		},
		{
			name:      "nested canonical artifact",
			snapshot:  `[{"blocks":[{"kind":"artifact","artifacts":[{"id":"a_allowed"}]}]}]`,
			assetType: "artifact",
			id:        "a_allowed",
			want:      true,
		},
		{
			name:      "quoted id in text",
			snapshot:  `[{"blocks":[{"kind":"text","text":"fetch \"f_victim\" from /api/files/f_victim"}]}]`,
			assetType: "file",
			id:        "f_victim",
		},
		{
			name:      "artifact-shaped fields on text block",
			snapshot:  `[{"blocks":[{"kind":"text","id":"a_victim","file_ref":"a_victim","url":"/api/artifacts/a_victim"}]}]`,
			assetType: "artifact",
			id:        "a_victim",
		},
		{
			name:      "id in citation",
			snapshot:  `[{"citations":[{"id":"f_victim","url":"/api/files/f_victim"}]}]`,
			assetType: "file",
			id:        "f_victim",
		},
		{
			name:      "malformed snapshot",
			snapshot:  `[{`,
			assetType: "file",
			id:        "f_victim",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shareSnapshotReferencesAsset([]byte(tt.snapshot), tt.assetType, tt.id); got != tt.want {
				t.Fatalf("shareSnapshotReferencesAsset() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPublicShareAssetsRequireStructuredReferenceAndConversationOwnership(t *testing.T) {
	root := t.TempDir()
	uploadDir := filepath.Join(root, "uploads")
	artifactDir := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	writeAsset := func(dir, name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	db := openMigrated(t, filepath.Join(root, "share-assets.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('other','other@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('shared-conv','owner','Shared assets')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('other-conv','other','Private assets')`)

	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('f_allowed','owner','shared-conv','allowed.txt','text/plain',12,?,'other',0)`, writeAsset(uploadDir, "allowed.txt", "allowed-file"))
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('f_text_victim','other','other-conv','text-victim.txt','text/plain',11,?,'other',0)`, writeAsset(uploadDir, "text-victim.txt", "text-victim"))
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('f_cross_conv','other','other-conv','cross.txt','text/plain',10,?,'other',0)`, writeAsset(uploadDir, "cross.txt", "cross-file"))
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('f_unreferenced','owner','shared-conv','hidden.txt','text/plain',11,?,'other',0)`, writeAsset(uploadDir, "hidden.txt", "hidden-file"))

	userMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ID:             "shared-user-message",
		ConversationID: "shared-conv",
		Role:           "user",
		AuthorID:       "owner",
		Blocks:         json.RawMessage(`[{"kind":"text","text":"Pasted only: \"f_text_victim\" and /api/artifacts/a_text_victim"}]`),
		Attachments: json.RawMessage(`[
			{"id":"f_allowed","filename":"allowed.txt","kind":"other","url":"/api/files/f_allowed"},
			{"id":"f_cross_conv","filename":"cross.txt","kind":"other","url":"/api/files/f_cross_conv"},
			{"id":"f_draft","filename":"draft.txt","kind":"other","url":"/api/files/f_draft"}
		]`),
	})
	if err != nil {
		t.Fatalf("create shared user message: %v", err)
	}
	assistantMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ID:             "shared-assistant-message",
		ConversationID: "shared-conv",
		ParentID:       userMessage.ID,
		Role:           "assistant",
		Blocks: json.RawMessage(`[
			{"kind":"artifact","file_ref":"a_allowed","url":"/api/artifacts/a_allowed","title":"allowed.bin"},
			{"kind":"artifact","file_ref":"a_cross_conv","url":"/api/artifacts/a_cross_conv","title":"cross.bin"}
		]`),
	})
	if err != nil {
		t.Fatalf("create shared assistant message: %v", err)
	}
	otherMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ID:             "other-message",
		ConversationID: "other-conv",
		Role:           "assistant",
		Blocks:         json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatalf("create other message: %v", err)
	}
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('f_draft','owner','shared-conv','draft.txt','text/plain',10,?,'other',1)`, writeAsset(uploadDir, "draft.txt", "draft-file"))
	mustExec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes)
		VALUES('a_allowed',?,'allowed.bin',?,'application/octet-stream',16)`, assistantMessage.ID, writeAsset(artifactDir, "allowed.bin", "allowed-artifact"))
	mustExec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes)
		VALUES('a_text_victim',?,'text-victim.bin',?,'application/octet-stream',15)`, otherMessage.ID, writeAsset(artifactDir, "text-victim.bin", "text-artifact"))
	mustExec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes)
		VALUES('a_cross_conv',?,'cross.bin',?,'application/octet-stream',14)`, otherMessage.ID, writeAsset(artifactDir, "cross.bin", "cross-artifact"))
	mustExec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes)
		VALUES('a_unreferenced',?,'hidden.bin',?,'application/octet-stream',15)`, assistantMessage.ID, writeAsset(artifactDir, "hidden.bin", "hidden-artifact"))

	deps := Deps{DB: db, Config: config.Config{UploadDir: uploadDir, ArtifactDir: artifactDir}}
	createShare := func() shareInfo {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/conversations/shared-conv/share", nil)
		ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "shared-conv"})
		ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "owner", Role: "user", Status: "active"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		createShareHandler(deps, rec, req)
		if rec.Code != 201 {
			t.Fatalf("create share status=%d body=%s", rec.Code, rec.Body.String())
		}
		var share shareInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &share); err != nil {
			t.Fatalf("decode share: %v", err)
		}
		return share
	}
	getAsset := func(assetType, token, id string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/public/shared/"+token+"/"+assetType+"/"+id, nil)
		req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"token": token, "id": id}))
		rec := httptest.NewRecorder()
		if assetType == "files" {
			publicSharedFileHandler(deps, rec, req)
		} else {
			publicSharedArtifactHandler(deps, rec, req)
		}
		return rec
	}
	assertStatus := func(assetType, token, id string, want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := getAsset(assetType, token, id)
		if rec.Code != want {
			t.Fatalf("GET %s/%s status=%d body=%s, want %d", assetType, id, rec.Code, rec.Body.String(), want)
		}
		return rec
	}

	first := createShare()
	if got := assertStatus("files", first.ID, "f_allowed", 200).Body.String(); got != "allowed-file" {
		t.Fatalf("allowed file body=%q", got)
	}
	if got := assertStatus("artifacts", first.ID, "a_allowed", 200).Body.String(); got != "allowed-artifact" {
		t.Fatalf("allowed artifact body=%q", got)
	}
	for _, denied := range []struct {
		assetType string
		id        string
	}{
		{"files", "f_text_victim"},     // ID appears only in ordinary message text.
		{"artifacts", "a_text_victim"}, // URL appears only in ordinary message text.
		{"files", "f_cross_conv"},      // Structured, but belongs to another conversation.
		{"artifacts", "a_cross_conv"},  // Structured, but belongs to another conversation.
		{"files", "f_unreferenced"},    // Same conversation, but absent from the snapshot.
		{"artifacts", "a_unreferenced"},
		{"files", "f_draft"}, // Structured and same conversation, but not committed.
	} {
		assertStatus(denied.assetType, first.ID, denied.id, 404)
	}

	second := createShare()
	if second.ID == first.ID {
		t.Fatal("re-share reused the public token")
	}
	assertStatus("files", first.ID, "f_allowed", 404)
	assertStatus("files", second.ID, "f_allowed", 200)

	revokeReq := httptest.NewRequest("DELETE", "/api/conversations/shared-conv/share", nil)
	revokeCtx := context.WithValue(revokeReq.Context(), pathCtxKey{}, map[string]string{"id": "shared-conv"})
	revokeCtx = context.WithValue(revokeCtx, userCtxKey{}, &store.User{ID: "owner", Role: "user", Status: "active"})
	revokeReq = revokeReq.WithContext(revokeCtx)
	revokeRec := httptest.NewRecorder()
	deleteShareHandler(deps, revokeRec, revokeReq)
	if revokeRec.Code != 200 {
		t.Fatalf("revoke share status=%d body=%s", revokeRec.Code, revokeRec.Body.String())
	}
	assertStatus("files", second.ID, "f_allowed", 404)
}

func TestWorkspaceShareManagementAuthorization(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-share-auth.db"))
	defer db.Close()
	for _, user := range []struct {
		id    string
		email string
	}{
		{"workspace-owner", "workspace-owner@example.test"},
		{"conversation-owner", "conversation-owner@example.test"},
		{"member", "member@example.test"},
		{"outsider", "outsider@example.test"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES(?,?, 'h','user')`, user.id, user.email)
	}
	mustExec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws-share','Shared','workspace-owner','invite-share')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-share','workspace-owner','owner')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-share','conversation-owner','member')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-share','member','member')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('workspace-conv','conversation-owner','Workspace conversation','ws-share')`)
	if _, err := store.CreateMessage(t.Context(), db, store.Message{
		ID:             "workspace-message",
		ConversationID: "workspace-conv",
		Role:           "user",
		AuthorID:       "conversation-owner",
		Blocks:         json.RawMessage(`[{"kind":"text","text":"workspace secret"}]`),
	}); err != nil {
		t.Fatalf("create workspace message: %v", err)
	}

	deps := Deps{DB: db}
	request := func(method, userID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/conversations/workspace-conv/share", nil)
		ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "workspace-conv"})
		ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		switch method {
		case "GET":
			getShareHandler(deps, rec, req)
		case "POST":
			createShareHandler(deps, rec, req)
		case "DELETE":
			deleteShareHandler(deps, rec, req)
		default:
			t.Fatalf("unsupported method %q", method)
		}
		return rec
	}
	decodeShare := func(rec *httptest.ResponseRecorder) shareInfo {
		t.Helper()
		var share shareInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &share); err != nil {
			t.Fatalf("decode share response: %v body=%s", err, rec.Body.String())
		}
		return share
	}
	assertLive := func(token string) {
		t.Helper()
		share, err := store.GetShareByToken(t.Context(), db, token)
		if err != nil || share == nil {
			t.Fatalf("share %q is not live: %v", token, err)
		}
	}
	assertRevoked := func(token string) {
		t.Helper()
		if _, err := store.GetShareByToken(t.Context(), db, token); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("share %q lookup error=%v, want ErrNotFound", token, err)
		}
	}

	for _, userID := range []string{"member", "outsider"} {
		if rec := request("GET", userID); rec.Code != 404 {
			t.Fatalf("%s GET status=%d body=%s, want 404", userID, rec.Code, rec.Body.String())
		}
		if rec := request("POST", userID); rec.Code != 404 {
			t.Fatalf("%s POST status=%d body=%s, want 404", userID, rec.Code, rec.Body.String())
		}
		if rec := request("DELETE", userID); rec.Code != 404 {
			t.Fatalf("%s DELETE status=%d body=%s, want 404", userID, rec.Code, rec.Body.String())
		}
	}
	var shareCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_shares WHERE conversation_id='workspace-conv'`).Scan(&shareCount); err != nil {
		t.Fatalf("count shares: %v", err)
	}
	if shareCount != 0 {
		t.Fatalf("unauthorized requests created %d shares", shareCount)
	}
	// Rows created before this authorization boundary may still exist after an
	// upgrade. They must not remain usable public capability links, and managers
	// must not be handed the stale token as if it were a valid share.
	mustExec(t, db, `INSERT INTO conversation_shares(id,conversation_id,user_id,title,snapshot)
		VALUES('legacy-member-share','workspace-conv','member','legacy','[]')`)
	assertRevoked("legacy-member-share")
	legacyPublicReq := httptest.NewRequest("GET", "/api/public/shared/legacy-member-share", nil)
	legacyPublicReq = legacyPublicReq.WithContext(context.WithValue(legacyPublicReq.Context(), pathCtxKey{}, map[string]string{"token": "legacy-member-share"}))
	legacyPublicRec := httptest.NewRecorder()
	publicSharedHandler(deps, legacyPublicRec, legacyPublicReq)
	if legacyPublicRec.Code != 404 {
		t.Fatalf("legacy member share public status=%d body=%s, want 404", legacyPublicRec.Code, legacyPublicRec.Body.String())
	}
	legacyManagerGet := request("GET", "workspace-owner")
	if legacyManagerGet.Code != 200 || !strings.Contains(legacyManagerGet.Body.String(), `"share":null`) {
		t.Fatalf("workspace owner GET legacy share status=%d body=%s", legacyManagerGet.Code, legacyManagerGet.Body.String())
	}

	creatorRec := request("POST", "conversation-owner")
	if creatorRec.Code != 201 {
		t.Fatalf("conversation owner POST status=%d body=%s", creatorRec.Code, creatorRec.Body.String())
	}
	creatorShare := decodeShare(creatorRec)
	assertLive(creatorShare.ID)

	managerGet := request("GET", "workspace-owner")
	if managerGet.Code != 200 || !strings.Contains(managerGet.Body.String(), creatorShare.ID) {
		t.Fatalf("workspace owner GET status=%d body=%s", managerGet.Code, managerGet.Body.String())
	}
	managerRec := request("POST", "workspace-owner")
	if managerRec.Code != 201 {
		t.Fatalf("workspace owner POST status=%d body=%s", managerRec.Code, managerRec.Body.String())
	}
	managerShare := decodeShare(managerRec)
	if managerShare.ID == creatorShare.ID {
		t.Fatal("workspace owner rotation reused the previous token")
	}
	assertRevoked(creatorShare.ID)
	assertLive(managerShare.ID)

	creatorGet := request("GET", "conversation-owner")
	if creatorGet.Code != 200 || !strings.Contains(creatorGet.Body.String(), managerShare.ID) {
		t.Fatalf("conversation owner GET after manager rotation status=%d body=%s", creatorGet.Code, creatorGet.Body.String())
	}
	if rec := request("POST", "member"); rec.Code != 404 {
		t.Fatalf("member rotation status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if rec := request("DELETE", "member"); rec.Code != 404 {
		t.Fatalf("member revoke status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	assertLive(managerShare.ID)

	if _, err := store.CreateShare(t.Context(), db, "member", "workspace-conv", "forged", []byte(`[]`)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("direct member CreateShare error=%v, want ErrNotFound", err)
	}
	if err := store.DeleteShareByConversation(t.Context(), db, "workspace-conv", "member"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("direct member DeleteShare error=%v, want ErrNotFound", err)
	}
	assertLive(managerShare.ID)

	if rec := request("DELETE", "conversation-owner"); rec.Code != 200 {
		t.Fatalf("conversation owner DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertRevoked(managerShare.ID)
	// Authorized revoke stays idempotent when no share exists.
	if rec := request("DELETE", "workspace-owner"); rec.Code != 200 {
		t.Fatalf("workspace owner idempotent DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceShareConcurrentRotationPreservesAuthorizationAndSingleToken(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-share-concurrent.db"))
	defer db.Close()
	for _, id := range []string{"workspace-owner", "conversation-owner", "member"} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES(?,?, 'h','user')`, id, id+"@example.test")
	}
	mustExec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws-share','Shared','workspace-owner','invite-share')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-share','workspace-owner','owner')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-share','conversation-owner','member')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-share','member','member')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('workspace-conv','conversation-owner','Workspace conversation','ws-share')`)
	if _, err := store.CreateMessage(t.Context(), db, store.Message{
		ID:             "workspace-message",
		ConversationID: "workspace-conv",
		Role:           "user",
		AuthorID:       "conversation-owner",
		Blocks:         json.RawMessage(`[{"kind":"text","text":"concurrent share"}]`),
	}); err != nil {
		t.Fatalf("create workspace message: %v", err)
	}
	initial, err := store.CreateShare(t.Context(), db, "conversation-owner", "workspace-conv", "initial", []byte(`[]`))
	if err != nil {
		t.Fatalf("create initial share: %v", err)
	}

	deps := Deps{DB: db}
	request := func(method, userID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/conversations/workspace-conv/share", nil)
		ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "workspace-conv"})
		ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		if method == "POST" {
			createShareHandler(deps, rec, req)
		} else {
			deleteShareHandler(deps, rec, req)
		}
		return rec
	}
	currentToken := func() string {
		t.Helper()
		var token string
		if err := db.QueryRow(`SELECT id FROM conversation_shares WHERE conversation_id='workspace-conv'`).Scan(&token); err != nil {
			t.Fatalf("query current share: %v", err)
		}
		return token
	}

	const attempts = 12
	start := make(chan struct{})
	codes := make([]int, attempts*2)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(2)
		go func(index int) {
			defer wg.Done()
			<-start
			codes[index] = request("POST", "member").Code
		}(i)
		go func(index int) {
			defer wg.Done()
			<-start
			codes[attempts+index] = request("DELETE", "member").Code
		}(i)
	}
	close(start)
	wg.Wait()
	for i, code := range codes {
		if code != 404 {
			t.Fatalf("unauthorized concurrent request %d status=%d, want 404", i, code)
		}
	}
	if got := currentToken(); got != initial.ID {
		t.Fatalf("unauthorized concurrency replaced token: got %s want %s", got, initial.ID)
	}

	start = make(chan struct{})
	codes = make([]int, attempts)
	tokens := make([]string, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			userID := "conversation-owner"
			if index%2 == 1 {
				userID = "workspace-owner"
			}
			rec := request("POST", userID)
			codes[index] = rec.Code
			if rec.Code == 201 {
				var info shareInfo
				if json.Unmarshal(rec.Body.Bytes(), &info) == nil {
					tokens[index] = info.ID
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	returned := make(map[string]bool, attempts)
	for i, code := range codes {
		if code != 201 || tokens[i] == "" {
			t.Fatalf("authorized concurrent rotation %d status=%d token=%q, want 201 and token", i, code, tokens[i])
		}
		returned[tokens[i]] = true
	}
	finalToken := currentToken()
	if !returned[finalToken] {
		t.Fatalf("final token %q was not returned by an authorized rotation", finalToken)
	}
	if finalToken == initial.ID {
		t.Fatalf("authorized concurrency did not rotate initial token %q", initial.ID)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_shares WHERE conversation_id='workspace-conv'`).Scan(&count); err != nil {
		t.Fatalf("count final shares: %v", err)
	}
	if count != 1 {
		t.Fatalf("concurrent rotations left %d shares, want 1", count)
	}
}
