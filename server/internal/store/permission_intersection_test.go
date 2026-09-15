package store

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNewGroupCapabilitiesPreserveExplicitDenialsAndInvalidateSnapshots(t *testing.T) {
	defaults := DefaultUserGroupPermissions()
	for _, key := range []string{"allow_prompts", "allow_skills", "allow_workspace_deletion"} {
		t.Run(key, func(t *testing.T) {
			p, err := NormalizeUserGroupPermissions(json.RawMessage(`{"` + key + `":false}`))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if fields[key] != false {
				t.Fatalf("lost explicit denial: %s", raw)
			}
			if UserGroupPermissionsEqual(defaults, p) {
				t.Fatal("capability changes must invalidate cached permission snapshots")
			}
			roundTrip, err := NormalizeUserGroupPermissions(raw)
			if err != nil || !UserGroupPermissionsEqual(p, roundTrip) {
				t.Fatalf("round trip changed policy: %v", err)
			}
		})
	}
}

func TestKnowledgeBaseCreatorObeysPerLibraryRestrictionsAndGuestRole(t *testing.T) {
	for _, reason := range []string{"library", "guest"} {
		t.Run(reason, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			ctx := t.Context()
			doc, err := CreateDocumentForUser(ctx, db, Document{KBID: "workspace-kb", Filename: "owned.txt", Status: "error"}, "creator")
			if err != nil {
				t.Fatal(err)
			}
			if reason == "guest" {
				_, err = db.Exec(`UPDATE workspace_members SET role='guest' WHERE workspace_id='ws1' AND user_id='creator'`)
			} else {
				_, err = db.Exec(`INSERT INTO workspace_kb_member_permissions(kb_id,user_id,can_add_files,can_delete_content)
					VALUES('workspace-kb','creator',0,0)`)
			}
			if err != nil {
				t.Fatal(err)
			}
			kb, err := GetKB(ctx, db, "workspace-kb", "creator")
			if err != nil || kb.CanUpload || kb.CanDeleteContent {
				t.Fatalf("creator retained content access: %+v %v", kb, err)
			}
			if _, err := CreateDocumentForUser(ctx, db, Document{KBID: "workspace-kb", Filename: "blocked.txt"}, "creator"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("upload error=%v", err)
			}
			if err := RenameDocumentForUser(ctx, db, doc.ID, "kb", "workspace-kb", "creator", "renamed.txt"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("rename error=%v", err)
			}
			if err := RetryKBDocumentForUser(ctx, db, doc.ID, "workspace-kb", "creator"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("retry error=%v", err)
			}
			if err := DeleteDocumentForUser(ctx, db, doc.ID, "kb", "workspace-kb", "creator"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("delete error=%v", err)
			}
			adminKB, err := GetKB(ctx, db, "workspace-kb", "owner")
			if err != nil || !adminKB.CanUpload || !adminKB.CanDeleteContent {
				t.Fatalf("workspace owner lost management: %+v %v", adminKB, err)
			}
		})
	}
}
