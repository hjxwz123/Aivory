package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestListFilesByConversationBranchIsolatesSiblingUploads(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "branch-files.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	conversation, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "branches"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	storageDir := t.TempDir()
	createFile := func(id, anchor string, draft bool) {
		t.Helper()
		if _, err := CreateFile(ctx, db, File{
			ID: id, UserID: "u1", ConversationID: conversation.ID,
			Filename: id + ".txt", MimeType: "text/plain", Kind: "text",
			StoragePath: filepath.Join(storageDir, id), BranchMessageID: anchor, Draft: draft,
		}); err != nil {
			t.Fatalf("create file %s: %v", id, err)
		}
	}
	attachments := func(ids ...string) json.RawMessage {
		t.Helper()
		rows := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, map[string]string{"id": id})
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			t.Fatalf("marshal attachments: %v", err)
		}
		return raw
	}
	createMessage := func(id, parent string, atts json.RawMessage) {
		t.Helper()
		if _, err := CreateMessage(ctx, db, Message{
			ID: id, ConversationID: conversation.ID, ParentID: parent,
			Role: "user", AuthorID: "u1", Blocks: json.RawMessage(`[]`), Attachments: atts,
		}); err != nil {
			t.Fatalf("create message %s: %v", id, err)
		}
	}

	createFile("parent", "m-root", false)
	createFile("legacy-drawer", "", false)
	createMessage("m-root", "", attachments("parent"))

	// A composer upload starts at the parent leaf, then must move to the user
	// message that commits it. Leaving it on m-root would leak it to branch 2.
	createFile("branch-1", "m-root", true)
	createFile("legacy-attachment", "", false)
	createFile("shared", "m-branch-1", false)
	createMessage("m-branch-1", "m-root", attachments("branch-1", "legacy-attachment", "shared"))

	createFile("branch-2", "m-branch-2", false)
	createMessage("m-branch-2", "m-root", attachments("branch-2", "shared"))

	var draft int
	var anchor string
	if err := db.QueryRowContext(ctx, `SELECT draft, branch_message_id FROM files WHERE id='branch-1'`).Scan(&draft, &anchor); err != nil {
		t.Fatalf("read committed composer file: %v", err)
	}
	if draft != 0 || anchor != "m-branch-1" {
		t.Fatalf("committed composer file draft=%d anchor=%q; want 0, m-branch-1", draft, anchor)
	}

	assertFiles := func(leaf string, want ...string) {
		t.Helper()
		rows, err := ListFilesByConversationBranch(ctx, db, conversation.ID, "u1", leaf)
		if err != nil {
			t.Fatalf("list branch %s: %v", leaf, err)
		}
		got := make(map[string]bool, len(rows))
		for _, file := range rows {
			got[file.ID] = true
		}
		if len(got) != len(want) {
			t.Fatalf("branch %s files=%v; want %v", leaf, got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("branch %s files=%v; missing %s", leaf, got, id)
			}
		}
	}

	assertFiles("m-branch-1", "parent", "legacy-drawer", "branch-1", "legacy-attachment", "shared")
	assertFiles("m-branch-2", "parent", "legacy-drawer", "branch-2", "shared")

	for _, id := range []string{"parent", "branch-1", "branch-2"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES(?,?,?,'text/plain',1,'ready',?)`,
			"doc-"+id, conversation.ID, id+".txt", filepath.Join(storageDir, id),
		); err != nil {
			t.Fatalf("create document %s: %v", id, err)
		}
	}
	assertDocuments := func(leaf string, want ...string) {
		t.Helper()
		ids, err := ConversationDocumentIDsForBranch(ctx, db, conversation.ID, "u1", leaf)
		if err != nil {
			t.Fatalf("list branch documents %s: %v", leaf, err)
		}
		got := make(map[string]bool, len(ids))
		for _, id := range ids {
			got[id] = true
		}
		if len(got) != len(want) {
			t.Fatalf("branch %s documents=%v; want %v", leaf, got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("branch %s documents=%v; missing %s", leaf, got, id)
			}
		}
	}
	assertDocuments("m-branch-1", "doc-parent", "doc-branch-1")
	assertDocuments("m-branch-2", "doc-parent", "doc-branch-2")

	for _, artifact := range []Artifact{
		{ID: "artifact-parent", MessageID: "m-root", Filename: "parent.png", StoragePath: filepath.Join(storageDir, "parent.png"), MimeType: "image/png"},
		{ID: "artifact-branch-1", MessageID: "m-branch-1", Filename: "branch-1.png", StoragePath: filepath.Join(storageDir, "branch-1.png"), MimeType: "image/png"},
		{ID: "artifact-branch-2", MessageID: "m-branch-2", Filename: "branch-2.png", StoragePath: filepath.Join(storageDir, "branch-2.png"), MimeType: "image/png"},
	} {
		if _, err := CreateArtifact(ctx, db, artifact); err != nil {
			t.Fatalf("create artifact %s: %v", artifact.ID, err)
		}
	}
	assertArtifacts := func(leaf string, want ...string) {
		t.Helper()
		rows, err := ListImageArtifactsByConversationBranch(ctx, db, conversation.ID, "u1", leaf)
		if err != nil {
			t.Fatalf("list branch artifacts %s: %v", leaf, err)
		}
		got := make(map[string]bool, len(rows))
		for _, artifact := range rows {
			got[artifact.ID] = true
		}
		if len(got) != len(want) {
			t.Fatalf("branch %s artifacts=%v; want %v", leaf, got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("branch %s artifacts=%v; missing %s", leaf, got, id)
			}
		}
	}
	assertArtifacts("m-branch-1", "artifact-parent", "artifact-branch-1")
	assertArtifacts("m-branch-2", "artifact-parent", "artifact-branch-2")
}

func TestMigrateAddsFileBranchMessageColumnToLegacyDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-branch-column.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := db.Exec(`DROP INDEX idx_files_conversation_branch`); err != nil {
		t.Fatalf("drop branch index: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE files DROP COLUMN branch_message_id`); err != nil {
		t.Fatalf("remove branch column from legacy fixture: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("legacy migrate: %v", err)
	}
	exists, err := columnExists(db, "files", "branch_message_id")
	if err != nil {
		t.Fatalf("inspect branch_message_id: %v", err)
	}
	if !exists {
		t.Fatal("branch_message_id was not restored")
	}
}
