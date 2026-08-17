package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// §workspace RBAC Phase 1 — three roles (admin/member/guest), owner exclusive
// operations, guest read-only hard limits and the unified authorizer.

func TestWorkspaceMemberRoleMigrationRewritesLegacyOwner(t *testing.T) {
	ctx := context.Background()
	dbh, err := Open(filepath.Join(t.TempDir(), "rbac-role-migration.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer dbh.Close()
	if _, err := dbh.Exec(schemaSQL); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	exec(t, dbh, `INSERT INTO users(id,email,password_hash,role,status) VALUES('owner','owner@example.test','h','user','active')`)
	exec(t, dbh, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws-legacy','Legacy','owner','legacy-token')`)
	exec(t, dbh, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-legacy','owner','owner')`)

	if err := Migrate(dbh); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var role string
	if err := dbh.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id='ws-legacy' AND user_id='owner'`).Scan(&role); err != nil || role != WorkspaceRoleAdmin {
		t.Fatalf("migrated role=%q err=%v, want admin", role, err)
	}
	// Reads normalize any surviving legacy rows (e.g. replicas upgraded in place).
	exec(t, dbh, `UPDATE workspace_members SET role='owner' WHERE user_id='owner'`)
	if role, err := IsWorkspaceMember(ctx, dbh, "ws-legacy", "owner"); err != nil || role != WorkspaceRoleAdmin {
		t.Fatalf("legacy owner read as %q err=%v, want admin", role, err)
	}
}

// rbacFixture provisions owner (canonical owner+admin), admin, member and guest
// in one workspace, plus shared resources created by "member".
type rbacFixture struct {
	db           *sql.DB
	workspaceID  string
	sharedConvID string
	memberDocID  string
	otherDocID   string
	kbID         string
	projectID    string
}

func newRBACFixture(t *testing.T) *rbacFixture {
	t.Helper()
	ctx := context.Background()
	dbh, err := Open(filepath.Join(t.TempDir(), "rbac.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })
	if err := Migrate(dbh); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, userID := range []string{"owner", "admin", "member", "guest"} {
		exec(t, dbh, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`, userID, userID+"@example.test")
	}
	workspace, err := CreateWorkspace(ctx, dbh, "owner", "RBAC")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	for _, userID := range []string{"admin", "member", "guest"} {
		if err := JoinWorkspace(ctx, dbh, workspace.ID, userID); err != nil {
			t.Fatalf("join %s: %v", userID, err)
		}
	}
	if _, err := UpdateWorkspaceMemberRole(ctx, dbh, workspace.ID, "owner", "admin", WorkspaceRoleAdmin); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	if _, err := UpdateWorkspaceMemberRole(ctx, dbh, workspace.ID, "owner", "guest", WorkspaceRoleGuest); err != nil {
		t.Fatalf("demote guest: %v", err)
	}

	exec(t, dbh, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	exec(t, dbh, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('emb-a','ch1','embedding','emb-a','Embedding A',3)`)

	conv, err := CreateConversation(ctx, dbh, Conversation{
		UserID: "member", WorkspaceID: workspace.ID, Title: "Shared", IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create shared conversation: %v", err)
	}
	kb, err := CreateKB(ctx, dbh, KnowledgeBase{
		UserID: "member", WorkspaceID: workspace.ID, Name: "Shared KB", IsPublic: true,
		EmbeddingModelID: "emb-a", EmbeddingDim: 3,
	})
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}
	project, err := CreateProject(ctx, dbh, Project{UserID: "member", WorkspaceID: workspace.ID, Name: "Shared project", IsPublic: true})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	exec(t, dbh, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,uploaded_by_user_id) VALUES
		('rbac-member-upload', ?, 'member.txt', 'text/plain', 1, 'ready', '', 'member'),
		('rbac-owner-upload', ?, 'owner.txt', 'text/plain', 1, 'ready', '', 'owner')`, kb.ID, kb.ID)

	return &rbacFixture{
		db: dbh, workspaceID: workspace.ID, sharedConvID: conv.ID,
		kbID: kb.ID, projectID: project.ID,
		memberDocID: "rbac-member-upload", otherDocID: "rbac-owner-upload",
	}
}

func TestWorkspaceRoleChangeLadder(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// Member and guest callers cannot manage roles at all.
	for _, actor := range []string{"member", "guest"} {
		if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, actor, "member", WorkspaceRoleAdmin); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s promote error=%v, want ErrForbidden", actor, err)
		}
	}
	// Ordinary admins cannot promote, demote or re-role admins, nor re-role self.
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "admin", "member", WorkspaceRoleAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin promote error=%v, want ErrForbidden", err)
	}
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "admin", "admin", WorkspaceRoleMember); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin demote admin error=%v, want ErrForbidden", err)
	}
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "admin", "admin", WorkspaceRoleGuest); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin self re-role error=%v, want ErrForbidden", err)
	}
	// Ordinary admins MAY move ordinary users between member and guest.
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "admin", "guest", WorkspaceRoleMember); err != nil {
		t.Fatalf("admin guest->member: %v", err)
	}
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "admin", "guest", WorkspaceRoleGuest); err != nil {
		t.Fatalf("admin member->guest: %v", err)
	}
	// The owner cannot re-role themselves and the owner row is fixed.
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "owner", "owner", WorkspaceRoleMember); !errors.Is(err, ErrForbidden) {
		t.Fatalf("owner self re-role error=%v, want ErrForbidden", err)
	}
	// The owner promotes and demotes admins freely.
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "owner", "member", WorkspaceRoleAdmin); err != nil {
		t.Fatalf("owner promote: %v", err)
	}
	if role, _ := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, "member"); role != WorkspaceRoleAdmin {
		t.Fatalf("promoted role=%q, want admin", role)
	}
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "owner", "member", WorkspaceRoleMember); err != nil {
		t.Fatalf("owner demote: %v", err)
	}
	// The canonical owner always reads as admin.
	if w, err := GetWorkspaceForMember(ctx, fx.db, fx.workspaceID, "owner"); err != nil || w.Role != WorkspaceRoleAdmin || !w.IsOwner {
		t.Fatalf("owner workspace view=%+v err=%v, want role=admin is_owner=true", w, err)
	}
}

func TestWorkspaceKickRoleRules(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// Ordinary members cannot kick.
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "member", "guest"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member kick error=%v, want ErrForbidden", err)
	}
	// Ordinary admins kick members and guests — but never admins or the owner.
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "admin", "guest"); err != nil {
		t.Fatalf("admin kick guest: %v", err)
	}
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "owner", "member", WorkspaceRoleAdmin); err != nil {
		t.Fatalf("owner promote member: %v", err)
	}
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "admin", "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin kick admin error=%v, want ErrForbidden", err)
	}
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "admin", "owner"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin kick owner error=%v, want ErrForbidden", err)
	}
	// The owner kicks fellow admins but never the owner row.
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "owner", "member"); err != nil {
		t.Fatalf("owner kick admin: %v", err)
	}
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "owner", "owner"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("owner kick self error=%v, want ErrForbidden", err)
	}
}

func TestWorkspaceGuestIsReadOnly(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// Guests read shared conversations.
	if _, err := GetConversation(ctx, fx.db, fx.sharedConvID, "guest"); err != nil {
		t.Fatalf("guest read shared conversation: %v", err)
	}
	// Guests cannot send messages (the model-call path).
	if _, err := CreateMessageForUser(ctx, fx.db, Message{
		ConversationID: fx.sharedConvID, Role: "user", AuthorID: "guest",
		Blocks: json.RawMessage(`[{"kind":"text","text":"hi"}]`),
	}, "guest"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest reply error=%v, want ErrNotFound", err)
	}
	// Guests cannot create conversations.
	if _, err := CreateConversation(ctx, fx.db, Conversation{
		UserID: "guest", WorkspaceID: fx.workspaceID, Title: "Guest conv", IsPublic: true,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest create conversation error=%v, want ErrNotFound", err)
	}
	// Guests cannot mutate conversation state (branch switch).
	leaf := "leaf-x"
	if _, err := UpdateConversation(ctx, fx.db, fx.sharedConvID, "guest", ConversationPatch{ActiveLeafID: &leaf}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest branch switch error=%v, want ErrNotFound", err)
	}
	// Guests cannot upload files into shared conversations.
	if _, err := CreateFile(ctx, fx.db, File{
		UserID: "guest", ConversationID: fx.sharedConvID, Filename: "g.txt",
		MimeType: "text/plain", Kind: "text", StoragePath: "/tmp/g.txt", SizeBytes: 1,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest upload error=%v, want ErrNotFound", err)
	}
	// Guests cannot create projects or KBs.
	if _, err := CreateProject(ctx, fx.db, Project{UserID: "guest", WorkspaceID: fx.workspaceID, Name: "Guest project"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest create project error=%v, want ErrNotFound", err)
	}
	if _, err := CreateKB(ctx, fx.db, KnowledgeBase{
		UserID: "guest", WorkspaceID: fx.workspaceID, Name: "Guest KB",
		EmbeddingModelID: "emb-a", EmbeddingDim: 3,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest create kb error=%v, want ErrNotFound", err)
	}
	// Guests cannot upload into or delete from shared KBs.
	if _, err := CreateDocumentForUser(ctx, fx.db, Document{
		KBID: fx.kbID, Filename: "g.txt", MimeType: "text/plain",
	}, "guest"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest kb upload error=%v, want ErrNotFound", err)
	}
	if err := DeleteDocumentForUser(ctx, fx.db, fx.memberDocID, "kb", fx.kbID, "guest"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest kb delete error=%v, want ErrNotFound", err)
	}
	// Guests see shared projects/KBs (read-only usage).
	if _, err := GetProject(ctx, fx.db, fx.projectID, "guest"); err != nil {
		t.Fatalf("guest read shared project: %v", err)
	}
	if _, err := GetKB(ctx, fx.db, fx.kbID, "guest"); err != nil {
		t.Fatalf("guest read shared kb: %v", err)
	}
}

func TestWorkspaceRoleDowngradeStopsConversationAttachmentAndMessageWrites(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// Create every mutable conversation child while the actor is still a normal
	// member. The post-downgrade checks must not be bypassed by their original
	// creator/uploader identity.
	document, err := CreateDocumentForUser(ctx, fx.db, Document{
		ConversationID: fx.sharedConvID,
		Filename:       "member-document.txt",
		MimeType:       "text/plain",
		StoragePath:    "/tmp/member-conversation-document.txt",
		SizeBytes:      1,
	}, "member")
	if err != nil {
		t.Fatalf("member create conversation document: %v", err)
	}
	file, err := CreateFile(ctx, fx.db, File{
		UserID:         "member",
		ConversationID: fx.sharedConvID,
		Filename:       "member-file.txt",
		MimeType:       "text/plain",
		Kind:           "text",
		StoragePath:    "/tmp/member-conversation-file.txt",
		SizeBytes:      1,
	})
	if err != nil {
		t.Fatalf("member create conversation file: %v", err)
	}
	message, err := CreateMessageForUser(ctx, fx.db, Message{
		ConversationID: fx.sharedConvID,
		Role:           "user",
		AuthorID:       "member",
		Blocks:         json.RawMessage(`[{"kind":"text","text":"before downgrade"}]`),
	}, "member")
	if err != nil {
		t.Fatalf("member create conversation message: %v", err)
	}
	if err := UpdateDocumentStatus(ctx, fx.db, document.ID, "failed", "retry me", 0); err != nil {
		t.Fatalf("mark document failed: %v", err)
	}

	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "owner", "member", WorkspaceRoleGuest); err != nil {
		t.Fatalf("demote member: %v", err)
	}
	if _, err := CreateDocumentForUser(ctx, fx.db, Document{
		ConversationID: fx.sharedConvID, Filename: "new-after-downgrade.txt", MimeType: "text/plain",
	}, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("downgraded member create document=%v, want ErrNotFound", err)
	}
	if err := RenameDocumentForUser(ctx, fx.db, document.ID, "conversation", fx.sharedConvID, "member", "renamed.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("downgraded member rename document=%v, want ErrNotFound", err)
	}
	if err := RetryDocumentForUser(ctx, fx.db, document.ID, fx.sharedConvID, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("downgraded member retry document=%v, want ErrNotFound", err)
	}
	if err := DeleteDocumentForUser(ctx, fx.db, document.ID, "conversation", fx.sharedConvID, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("downgraded member delete document=%v, want ErrNotFound", err)
	}
	if err := DeleteConversationFile(ctx, fx.db, file.ID, fx.sharedConvID, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("downgraded member delete file=%v, want ErrNotFound", err)
	}
	if err := UpdateMessageContentForUser(ctx, fx.db, fx.sharedConvID, "member", message.ID,
		json.RawMessage(`[{"kind":"text","text":"after downgrade"}]`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("downgraded member edit message=%v, want ErrNotFound", err)
	}
}

func TestWorkspaceDemotedCreatorAndUnknownRoleCannotWrite(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// The member created these rows while write-capable. Once demoted, creator
	// identity must not bypass the guest read-only boundary.
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "owner", "member", WorkspaceRoleGuest); err != nil {
		t.Fatalf("demote creator: %v", err)
	}
	title := "guest must not rename"
	if _, err := UpdateConversation(ctx, fx.db, fx.sharedConvID, "member", ConversationPatch{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("demoted conversation creator update=%v, want ErrNotFound", err)
	}
	name := "guest must not rename project"
	if _, err := UpdateProject(ctx, fx.db, fx.projectID, "member", ProjectPatch{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("demoted project creator update=%v, want ErrNotFound", err)
	}
	if _, err := UpdateKBVisibility(ctx, fx.db, fx.kbID, "member", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("demoted kb creator visibility update=%v, want ErrNotFound", err)
	}

	// A malformed row is treated as the weakest role by every transaction
	// predicate, not merely by the handler-level authorizer.
	exec(t, fx.db, `UPDATE workspace_members SET role='unexpected_role' WHERE workspace_id=? AND user_id='member'`, fx.workspaceID)
	if _, err := CreateMessageForUser(ctx, fx.db, Message{
		ConversationID: fx.sharedConvID, Role: "user", AuthorID: "member",
		Blocks: json.RawMessage(`[{"kind":"text","text":"must not write"}]`),
	}, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown role reply=%v, want ErrNotFound", err)
	}
}

func TestWorkspaceDeletionFenceStopsCreateAndOwnershipTransfer(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	if err := MarkWorkspaceDeleting(ctx, fx.db, fx.workspaceID, "owner"); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	if _, err := CreateConversation(ctx, fx.db, Conversation{
		UserID: "member", WorkspaceID: fx.workspaceID, Title: "late", IsPublic: true,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("create behind deletion fence=%v, want ErrNotFound", err)
	}
	if _, err := TransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "owner", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transfer behind deletion fence=%v, want ErrNotFound", err)
	}
	if err := ClearWorkspaceDeleting(ctx, fx.db, fx.workspaceID, "owner"); err != nil {
		t.Fatalf("clear deletion fence: %v", err)
	}
}

func TestWorkspaceMemberMetadataBoundaries(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// A genuine second member cannot rename the creator's conversation...
	secondMember := "member2"
	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member2','member2@example.test','h','user','active')`)
	if err := JoinWorkspace(ctx, fx.db, fx.workspaceID, secondMember); err != nil {
		t.Fatalf("join member2: %v", err)
	}
	rename := "Hijacked title"
	if _, err := UpdateConversation(ctx, fx.db, fx.sharedConvID, secondMember, ConversationPatch{Title: &rename}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second member rename error=%v, want ErrNotFound", err)
	}
	// ...but collaborative state stays open to members.
	leaf := "leaf-y"
	if _, err := UpdateConversation(ctx, fx.db, fx.sharedConvID, secondMember, ConversationPatch{ActiveLeafID: &leaf}); err != nil {
		t.Fatalf("second member branch switch: %v", err)
	}
	// The creator and admins may rename.
	if _, err := UpdateConversation(ctx, fx.db, fx.sharedConvID, "member", ConversationPatch{Title: &rename}); err != nil {
		t.Fatalf("creator rename: %v", err)
	}
	adminRename := "Admin title"
	if _, err := UpdateConversation(ctx, fx.db, fx.sharedConvID, "admin", ConversationPatch{Title: &adminRename}); err != nil {
		t.Fatalf("admin rename: %v", err)
	}
	// A second member cannot modify the creator's project metadata.
	desc := "Hijacked"
	if _, err := UpdateProject(ctx, fx.db, fx.projectID, secondMember, ProjectPatch{Description: &desc}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second member project rename error=%v, want ErrNotFound", err)
	}
	if _, err := UpdateProject(ctx, fx.db, fx.projectID, "admin", ProjectPatch{Description: &desc}); err != nil {
		t.Fatalf("admin project rename: %v", err)
	}
	// A second member cannot delete the creator's conversation or project.
	if _, err := DeleteConversation(ctx, fx.db, fx.sharedConvID, secondMember); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second member delete conversation error=%v, want ErrNotFound", err)
	}
	if err := DeleteProject(ctx, fx.db, fx.projectID, secondMember); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second member delete project error=%v, want ErrNotFound", err)
	}
	// Admins may delete the creator's conversation (§9.1).
	if _, err := DeleteConversation(ctx, fx.db, fx.sharedConvID, "admin"); err != nil {
		t.Fatalf("admin delete conversation: %v", err)
	}
}

func TestWorkspaceKnowledgeBaseDocumentOwnership(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// "member" created the library and uploaded memberDocID; "owner" uploaded
	// otherDocID. A second ordinary member may delete only their own uploads.
	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member2','member2@example.test','h','user','active')`)
	if err := JoinWorkspace(ctx, fx.db, fx.workspaceID, "member2"); err != nil {
		t.Fatalf("join member2: %v", err)
	}
	if err := DeleteDocumentForUser(ctx, fx.db, fx.memberDocID, "kb", fx.kbID, "member2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second member delete other's upload error=%v, want ErrNotFound", err)
	}
	// Guests never delete content.
	if err := DeleteDocumentForUser(ctx, fx.db, fx.memberDocID, "kb", fx.kbID, "guest"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest delete error=%v, want ErrNotFound", err)
	}
	// Ordinary members still delete their own uploads.
	if err := DeleteDocumentForUser(ctx, fx.db, fx.memberDocID, "kb", fx.kbID, "member"); err != nil {
		t.Fatalf("member delete own upload: %v", err)
	}
	// The library creator manages every document...
	if err := DeleteDocumentForUser(ctx, fx.db, fx.otherDocID, "kb", fx.kbID, "member"); err != nil {
		t.Fatalf("creator delete other's upload: %v", err)
	}
	// ...and so do workspace admins.
	exec(t, fx.db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,uploaded_by_user_id) VALUES
		('rbac-admin-target', ?, 'again.txt', 'text/plain', 1, 'ready', '', 'owner')`, fx.kbID)
	if err := DeleteDocumentForUser(ctx, fx.db, "rbac-admin-target", "kb", fx.kbID, "admin"); err != nil {
		t.Fatalf("admin delete any upload: %v", err)
	}
}

func TestAuthorizeWorkspaceMatrix(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	cases := []struct {
		actor  string
		action string
		want   bool
	}{
		{"owner", ActionWorkspaceDelete, true},
		{"admin", ActionWorkspaceDelete, false},
		{"member", ActionWorkspaceDelete, false},
		{"guest", ActionWorkspaceDelete, false},
		{"owner", ActionWorkspaceMemberInvite, true},
		{"admin", ActionWorkspaceMemberInvite, true},
		{"member", ActionWorkspaceMemberInvite, false},
		{"guest", ActionWorkspaceMemberInvite, false},
		{"owner", ActionWorkspaceMemberView, true},
		{"guest", ActionWorkspaceMemberView, true},
		{"guest", ActionModelUse, false},
		{"guest", ActionConversationReply, false},
		{"guest", ActionToolUse, false},
		{"guest", ActionSandboxUse, false},
		{"guest", ActionImageGenerate, false},
		{"member", ActionModelUse, true},
		{"member", ActionConversationReply, true},
		{"member", ActionUsageView, false},
		{"admin", ActionUsageView, true},
	}
	for _, tc := range cases {
		decision, err := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
			WorkspaceID: fx.workspaceID, UserID: tc.actor, Action: tc.action,
		})
		if err != nil {
			t.Fatalf("%s %s: %v", tc.actor, tc.action, err)
		}
		if decision.Allowed != tc.want {
			t.Fatalf("%s %s allowed=%v reason=%q, want %v", tc.actor, tc.action, decision.Allowed, decision.Reason, tc.want)
		}
	}

	// Conversation-scoped decisions.
	shared, err := GetConversation(ctx, fx.db, fx.sharedConvID, "member")
	if err != nil {
		t.Fatalf("load shared conversation: %v", err)
	}
	_ = shared
	convCases := []struct {
		actor  string
		action string
		want   bool
	}{
		{"member", ActionConversationMetadataUpdate, true}, // creator
		{"admin", ActionConversationMetadataUpdate, true},  // admin
		{"guest", ActionConversationMetadataUpdate, false}, // guest
		{"member", ActionConversationRead, true},           // shared
		{"guest", ActionConversationRead, true},            // shared read
	}
	for _, tc := range convCases {
		decision, err := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
			WorkspaceID: fx.workspaceID, UserID: tc.actor, Action: tc.action,
			Resource: "conversation", ResourceID: fx.sharedConvID,
		})
		if err != nil {
			t.Fatalf("%s %s: %v", tc.actor, tc.action, err)
		}
		if decision.Allowed != tc.want {
			t.Fatalf("%s %s(conv) allowed=%v reason=%q, want %v", tc.actor, tc.action, decision.Allowed, decision.Reason, tc.want)
		}
	}

	// A non-member gets a uniform denial; cross-workspace resources 404.
	decision, _ := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fx.workspaceID, UserID: "stranger", Action: ActionConversationRead,
		Resource: "conversation", ResourceID: fx.sharedConvID,
	})
	if decision.Allowed {
		t.Fatal("stranger authorized against foreign workspace")
	}
	bogus, _ := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fx.workspaceID, UserID: "member", Action: "bogus.action",
	})
	if bogus.Allowed {
		t.Fatal("bogus action authorized")
	}
	decision, _ = AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fx.workspaceID, UserID: "member", Action: ActionConversationRead,
		Resource: "conversation", ResourceID: "missing-conv",
	})
	if decision.Allowed {
		t.Fatal("missing resource authorized")
	}
}
