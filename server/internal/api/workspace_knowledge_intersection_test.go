package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/rag"
	"aivory/server/internal/store"
)

func TestKnowledgeBaseMemberDialogIncludesUserGroupCeiling(t *testing.T) {
	owner, member, d, _, kbID := openWorkspacePermissionHTTPTest(t)
	permissions := store.DefaultUserGroupPermissions()
	permissions.AllowKnowledgeBases = false
	setIntersectionGroup(t, d, member.ID, permissions)
	rec := httptest.NewRecorder()
	listWorkspaceKBMembersHandler(d, rec, userGroupPermissionRequest(http.MethodGet, "/", owner, map[string]string{"id": kbID}, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d %s", rec.Code, rec.Body.String())
	}
	var members []store.WorkspaceKnowledgeBaseMemberPermission
	if err := json.Unmarshal(rec.Body.Bytes(), &members); err != nil {
		t.Fatal(err)
	}
	for _, row := range members {
		if row.UserID == member.ID {
			if row.TotalCanAddKBFiles || row.TotalCanDeleteKBContent {
				t.Fatalf("group-denied member shown as allowed: %+v", row)
			}
			return
		}
	}
	t.Fatal("member missing")
}

func TestWorkspaceKnowledgeBaseCeilingBlocksExistingResources(t *testing.T) {
	owner, member, d, wsID, kbID := openWorkspacePermissionHTTPTest(t)
	d.Config.UploadDir = t.TempDir()
	filePath := filepath.Join(d.Config.UploadDir, "audit.txt")
	if err := os.WriteFile(filePath, []byte("AUDIT_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := store.CreateDocumentForUser(t.Context(), d.DB, store.Document{KBID: kbID, Filename: "audit.txt", MimeType: "text/plain", Status: "ready", StoragePath: filePath}, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, wsID, owner.ID, store.WorkspacePolicyPatch{AllowKnowledgeBases: &disabled}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		h    handler
		path map[string]string
		body string
	}{
		{"KB detail with workspace disabled", requireKnowledgeBaseHandler(getKBHandler), map[string]string{"id": kbID}, ""},
		{"KB documents with workspace disabled", requireKnowledgeBaseHandler(listKBDocsHandler), map[string]string{"id": kbID}, ""},
		{"KB file content with workspace disabled", documentContentHandler, map[string]string{"id": doc.ID}, ""},
		{"KB uploaders with workspace disabled", listKBDocumentUploadersHandler, map[string]string{"id": kbID}, ""},
		{"KB retry with workspace disabled", retryKBDocHandler, map[string]string{"id": kbID, "docId": doc.ID}, ""},
		{"KB delete with workspace disabled", deleteKBDocHandler, map[string]string{"id": kbID, "docId": doc.ID}, ""},
		{"KB members with workspace disabled", listWorkspaceKBMembersHandler, map[string]string{"id": kbID}, ""},
		{"KB rename with workspace disabled", requireKnowledgeBaseHandler(renameKBDocHandler), map[string]string{"id": kbID, "docId": doc.ID}, `{"filename":"renamed.txt"}`},
	} {
		rec := httptest.NewRecorder()
		tc.h(d, rec, userGroupPermissionRequest(http.MethodGet, "/", member, tc.path, tc.body))
		t.Logf("%s: HTTP %d body=%s", tc.name, rec.Code, rec.Body.String())
		if rec.Code != 403 {
			t.Fatalf("disabled workspace allowed access: HTTP %d", rec.Code)
		}
	}
	enabled := true
	if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, wsID, owner.ID, store.WorkspacePolicyPatch{AllowKnowledgeBases: &enabled}); err != nil {
		t.Fatal(err)
	}
	p := store.DefaultUserGroupPermissions()
	p.AllowKnowledgeBases = false
	setIntersectionGroup(t, d, member.ID, p)
	rec := httptest.NewRecorder()
	requireKnowledgeBaseHandler(getKBHandler)(d, rec, userGroupPermissionRequest(http.MethodGet, "/", member, map[string]string{"id": kbID}, ""))
	t.Logf("same KB disabled by user group: HTTP %d", rec.Code)
	if rec.Code != 403 {
		t.Fatalf("expected group denial, got %d", rec.Code)
	}
}

func TestKnowledgeBaseCreatorPermissionDisplayMatchesEnforcement(t *testing.T) {
	owner, member, d, wsID, kbID := openWorkspacePermissionHTTPTest(t)
	p := store.WorkspaceMemberPermissions{CanCreateKB: true, CanAddKBFiles: false, CanDeleteKBContent: false}
	if _, err := store.UpdateWorkspaceMemberPermissions(t.Context(), d.DB, wsID, owner.ID, member.ID, p); err != nil {
		t.Fatal(err)
	}
	kb, err := store.GetKB(t.Context(), d.DB, kbID, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListWorkspaceKnowledgeBaseMemberPermissions(t.Context(), d.DB, kbID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.UserID == member.ID {
			t.Logf("creator actual upload=%v delete_content=%v; permission dialog totals upload=%v delete_content=%v locked=%v", kb.CanUpload, kb.CanDeleteContent, row.TotalCanAddKBFiles, row.TotalCanDeleteKBContent, row.Locked)
			if kb.CanUpload || kb.CanDeleteContent || row.TotalCanAddKBFiles || row.TotalCanDeleteKBContent || row.Locked {
				t.Fatal("permission enforcement mismatch")
			}
			return
		}
	}
	t.Fatal("creator row missing")
}

func TestPromoteHonorsWorkspaceSwitches(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kb, upload bool
	}{
		{"workspace KB disabled", false, true},
		{"workspace upload disabled", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, member, d, wsID, kbID := openWorkspacePermissionHTTPTest(t)
			mustExec(t, d.DB, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id) VALUES('audit-project',?,'Audit',?,?)`, member.ID, kbID, wsID)
			conv, err := store.CreateConversation(t.Context(), d.DB, store.Conversation{UserID: member.ID, WorkspaceID: wsID, ProjectID: "audit-project", Title: "Audit"})
			if err != nil {
				t.Fatal(err)
			}
			doc, err := store.CreateDocumentForUser(t.Context(), d.DB, store.Document{ConversationID: conv.ID, Filename: "promote.txt", MimeType: "text/plain", Status: "ready"}, member.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, wsID, owner.ID, store.WorkspacePolicyPatch{AllowKnowledgeBases: &tc.kb, AllowFileUpload: &tc.upload}); err != nil {
				t.Fatal(err)
			}
			q := &recordingQueue{}
			d.RAG = rag.New(d.DB, q, log.New(io.Discard, "", 0))
			rec := httptest.NewRecorder()
			requireKnowledgeBaseHandler(requireCapabilityHandler(errFileUploadGroupPermission, func(p store.UserGroupPermissions) bool { return p.AllowFileUpload }, promoteDocumentHandler))(d, rec, userGroupPermissionRequest(http.MethodPost, "/", member, map[string]string{"id": conv.ID, "docId": doc.ID}, ""))
			got, err := store.GetDocument(t.Context(), d.DB, doc.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: promote HTTP %d, moved_to_KB=%v, queued=%d", tc.name, rec.Code, got.KBID == kbID, len(q.names))
			if rec.Code != 403 || got.KBID != "" || got.ConversationID != conv.ID || len(q.names) != 0 {
				t.Fatalf("permission enforcement mismatch: %s", rec.Body.String())
			}
		})
	}
}

func TestAutomaticProjectIngestHonorsWorkspaceKBDisable(t *testing.T) {
	owner, member, d, wsID, kbID := openWorkspacePermissionHTTPTest(t)
	mustExec(t, d.DB, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id,auto_add_uploads) VALUES('audit-project',?,'Audit',?,?,1)`, member.ID, kbID, wsID)
	conv, err := store.CreateConversation(t.Context(), d.DB, store.Conversation{UserID: member.ID, WorkspaceID: wsID, ProjectID: "audit-project", Title: "Audit"})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, wsID, owner.ID, store.WorkspacePolicyPatch{AllowKnowledgeBases: &disabled}); err != nil {
		t.Fatal(err)
	}
	d.Config.UploadDir = t.TempDir()
	d.Config.MaxUploadBytes = 1 << 20
	d.Cache = cache.NewMemory()
	q := &recordingQueue{}
	d.RAG = rag.New(d.DB, q, log.New(io.Discard, "", 0), d.Config.UploadDir)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "auto.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "Audit upload"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := userGroupPermissionRequest(http.MethodPost, "/api/files?conversation_id="+conv.ID, member, nil, body.String())
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	requireCapabilityHandler(errFileUploadGroupPermission, func(p store.UserGroupPermissions) bool { return p.AllowFileUpload }, uploadFileHandler)(d, rec, req)
	var count int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM documents WHERE kb_id=?`, kbID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	t.Logf("workspace KB disabled: upload HTTP %d, new project KB documents=%d, queued=%d", rec.Code, count, len(q.names))
	if rec.Code != 201 || count != 0 || len(q.names) != 0 {
		t.Fatalf("permission enforcement mismatch: %s", rec.Body.String())
	}
}

func TestProjectAutoAddUploadsSettingHonorsPermissionIntersection(t *testing.T) {
	for _, restriction := range []string{
		"none",
		"group knowledge bases",
		"group file upload",
		"workspace knowledge bases",
		"workspace file upload",
		"member file upload",
		"library file upload",
	} {
		t.Run(restriction, func(t *testing.T) {
			owner, member, d, wsID, kbID := openWorkspacePermissionHTTPTest(t)
			mustExec(t, d.DB, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id,auto_add_uploads)
				VALUES('auto-add-project',?,'Auto-add',?,?,0)`, member.ID, kbID, wsID)

			group := store.DefaultUserGroupPermissions()
			group.AllowKnowledgeBases = restriction != "group knowledge bases"
			group.AllowFileUpload = restriction != "group file upload"
			setIntersectionGroup(t, d, member.ID, group)
			allowKB := restriction != "workspace knowledge bases"
			allowUpload := restriction != "workspace file upload"
			if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, wsID, owner.ID, store.WorkspacePolicyPatch{
				AllowKnowledgeBases: &allowKB, AllowFileUpload: &allowUpload,
			}); err != nil {
				t.Fatal(err)
			}
			if restriction == "member file upload" {
				if _, err := store.UpdateWorkspaceMemberPermissions(t.Context(), d.DB, wsID, owner.ID, member.ID,
					store.WorkspaceMemberPermissions{CanCreateKB: true, CanAddKBFiles: false}); err != nil {
					t.Fatal(err)
				}
			}
			if restriction == "library file upload" {
				mustExec(t, d.DB, `INSERT INTO workspace_kb_member_permissions(kb_id,user_id,can_add_files,can_delete_content)
					VALUES(?,?,0,1)`, kbID, member.ID)
			}

			update := func(body string) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				updateProjectHandler(d, rec, userGroupPermissionRequest(http.MethodPatch,
					"/api/projects/auto-add-project", member, map[string]string{"id": "auto-add-project"}, body))
				return rec
			}
			assertStored := func(want bool) {
				t.Helper()
				project, err := store.GetProject(t.Context(), d.DB, "auto-add-project", member.ID)
				if err != nil {
					t.Fatal(err)
				}
				if project.AutoAddUploads != want {
					t.Fatalf("stored auto_add_uploads=%v, want %v", project.AutoAddUploads, want)
				}
			}

			wantStatus := http.StatusForbidden
			if restriction == "none" {
				wantStatus = http.StatusOK
			}
			if rec := update(`{"auto_add_uploads":true}`); rec.Code != wantStatus {
				t.Fatalf("enable status=%d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
			}
			assertStored(restriction == "none")

			// An option enabled before permissions were revoked can still be disabled.
			mustExec(t, d.DB, `UPDATE projects SET auto_add_uploads=1 WHERE id='auto-add-project'`)
			if rec := update(`{"auto_add_uploads":false}`); rec.Code != http.StatusOK {
				t.Fatalf("disable status=%d: %s", rec.Code, rec.Body.String())
			}
			assertStored(false)
		})
	}
}
