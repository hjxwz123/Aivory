package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestListUserImageArtifactsSeparatesSelfServiceAndAdminScopes(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "image-gallery-workspace-access.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, userID := range []string{"gallery-owner", "gallery-member"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`,
			userID, userID+"@example.test")
	}
	workspace, err := CreateWorkspace(ctx, db, "gallery-owner", "Gallery")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := JoinWorkspace(ctx, db, workspace.ID, "gallery-member"); err != nil {
		t.Fatalf("join member: %v", err)
	}
	conversation, err := CreateConversation(ctx, db, Conversation{
		ID: "gallery-member-conversation", UserID: "gallery-member",
		WorkspaceID: workspace.ID, Title: "Former creator metadata",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	for _, fixture := range []struct {
		messageID  string
		artifactID string
		authorID   string
	}{
		{"gallery-member-message", "gallery-member-artifact", "gallery-member"},
		{"gallery-owner-message", "gallery-owner-artifact", "gallery-owner"},
	} {
		message, createErr := CreateMessageForUser(ctx, db, Message{
			ID: fixture.messageID, ConversationID: conversation.ID, Role: "assistant",
			AuthorID: fixture.authorID, Blocks: json.RawMessage(`[]`), Status: "complete",
		}, fixture.authorID)
		if createErr != nil {
			t.Fatalf("create %s: %v", fixture.messageID, createErr)
		}
		if _, createErr = CreateArtifact(ctx, db, Artifact{
			ID: fixture.artifactID, MessageID: message.ID, Filename: fixture.artifactID + ".png",
			StoragePath: "/tmp/" + fixture.artifactID, MimeType: "image/png", SizeBytes: 10,
		}); createErr != nil {
			t.Fatalf("create %s: %v", fixture.artifactID, createErr)
		}
	}

	assertGallery := func(list func(context.Context, *sql.DB, string, int, int) ([]AdminImageArtifact, error), scope, userID string, wantIDs ...string) {
		t.Helper()
		images, listErr := list(ctx, db, userID, 20, 0)
		if listErr != nil {
			t.Fatalf("list %s %s gallery: %v", scope, userID, listErr)
		}
		got := make([]string, 0, len(images))
		for _, image := range images {
			got = append(got, image.ID)
		}
		if len(got) != len(wantIDs) {
			t.Fatalf("gallery %s %s ids=%v want=%v", scope, userID, got, wantIDs)
		}
		for index := range got {
			if got[index] != wantIDs[index] {
				t.Fatalf("gallery %s %s ids=%v want=%v", scope, userID, got, wantIDs)
			}
		}
	}

	assertGallery(ListUserImageArtifactsForUser, "self", "gallery-member", "gallery-member-artifact")
	assertGallery(ListUserImageArtifactsForUser, "self", "gallery-owner", "gallery-owner-artifact")
	assertGallery(ListUserImageArtifacts, "admin", "gallery-member", "gallery-member-artifact")
	assertGallery(ListUserImageArtifacts, "admin", "gallery-owner", "gallery-owner-artifact")
	if err := RemoveWorkspaceMember(ctx, db, workspace.ID, "gallery-member"); err != nil {
		t.Fatalf("kick member: %v", err)
	}
	// c.user_id is still gallery-member, which was the old vulnerable predicate.
	// Current membership is now absent, so no workspace metadata may be listed.
	assertGallery(ListUserImageArtifactsForUser, "self", "gallery-member")
	assertGallery(ListUserImageArtifactsForUser, "self", "gallery-owner", "gallery-owner-artifact")
	// The admin drill-down is an audit view, so removal cannot erase the former
	// member's historical generated images from that view.
	assertGallery(ListUserImageArtifacts, "admin", "gallery-member", "gallery-member-artifact")
	assertGallery(ListUserImageArtifacts, "admin", "gallery-owner", "gallery-owner-artifact")
}
