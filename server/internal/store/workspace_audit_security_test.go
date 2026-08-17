package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// §workspace RBAC phase 5 — audit trail, permission matrix, cross-workspace
// isolation and revocation/concurrency (TOCTOU) security tests.

type auditRowsQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func fetchAuditRows(t *testing.T, db auditRowsQueryer, workspaceID string) []map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT actor_user_id, action, target_type, target_id, metadata
		FROM workspace_audit_logs WHERE workspace_id=? ORDER BY created_at, id`, workspaceID)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var actor, action, targetType, targetID, metadata string
		if err := rows.Scan(&actor, &action, &targetType, &targetID, &metadata); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		out = append(out, map[string]any{
			"actor": actor, "action": action, "target_type": targetType,
			"target_id": targetID, "metadata": metadata,
		})
	}
	return out
}

func TestWorkspaceAuditTrailRecordsPermissionChanges(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('invitee','invitee@example.test','h','user','active')`)

	// Exercise every §15 event family.
	invite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "admin", "invitee@example.test", WorkspaceRoleMember, 0, 1)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if _, _, err := JoinWorkspaceByInviteRecord(ctx, fx.db, invite.Token, "invitee", "invitee@example.test"); err != nil {
		t.Fatalf("join via invite: %v", err)
	}
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "owner", "invitee", WorkspaceRoleGuest); err != nil {
		t.Fatalf("role update: %v", err)
	}
	denied := WorkspaceMemberPermissions{}
	if _, err := UpdateWorkspaceMemberPermissions(ctx, fx.db, fx.workspaceID, "admin", "invitee", denied); err != nil {
		t.Fatalf("permissions update: %v", err)
	}
	makePrivate := false
	if _, err := UpdateConversation(ctx, fx.db, fx.sharedConvID, "member", ConversationPatch{IsPublic: &makePrivate}); err != nil {
		t.Fatalf("conversation visibility: %v", err)
	}
	if _, err := UpdateProject(ctx, fx.db, fx.projectID, "member", ProjectPatch{IsPublic: &makePrivate}); err != nil {
		t.Fatalf("project visibility: %v", err)
	}
	if _, err := UpdateKBVisibility(ctx, fx.db, fx.kbID, "member", false); err != nil {
		t.Fatalf("kb visibility: %v", err)
	}
	sandboxOff := false
	if _, err := UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "owner", WorkspacePolicyPatch{AllowSandbox: &sandboxOff}); err != nil {
		t.Fatalf("policy update: %v", err)
	}
	if err := RevokeWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", invite.ID); err != nil {
		t.Fatalf("revoke invite: %v", err)
	}
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "owner", "invitee"); err != nil {
		t.Fatalf("kick: %v", err)
	}
	if _, err := TransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "owner", "admin"); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	rows := fetchAuditRows(t, fx.db, fx.workspaceID)
	actions := map[string]int{}
	for _, row := range rows {
		actions[row["action"].(string)]++
	}
	for _, want := range []string{
		AuditMemberJoined, AuditMemberRemoved, AuditMemberRoleUpdated, AuditMemberPermissionsUpdated,
		AuditInviteCreated, AuditInviteUsed, AuditInviteRevoked,
		AuditResourceVisibilityChanged, AuditPolicyUpdated, AuditWorkspaceTransferred, AuditWorkspaceCreated,
	} {
		if actions[want] == 0 {
			t.Fatalf("audit missing action %q; recorded=%v", want, actions)
		}
	}
	// The visibility family covers all three resource kinds.
	kinds := map[string]bool{}
	for _, row := range rows {
		if row["action"] == AuditResourceVisibilityChanged {
			kinds[row["target_type"].(string)] = true
		}
	}
	if !kinds["conversation"] || !kinds["project"] || !kinds["knowledge_base"] {
		t.Fatalf("visibility audits incomplete: %v", kinds)
	}

	// §15 masking: the invite token must appear nowhere in the trail.
	blob := ""
	for _, row := range rows {
		blob += fmt.Sprintf("%v|%v|%v|%v|", row["actor"], row["action"], row["target_id"], row["metadata"])
	}
	if strings.Contains(blob, invite.Token) {
		t.Fatal("audit trail leaks the invite token")
	}
	// Invite audits point at the invite ROW id, never the token.
	for _, row := range rows {
		if row["action"] == AuditInviteCreated && row["target_id"] != invite.ID {
			t.Fatalf("invite audit target=%v, want invite row id", row["target_id"])
		}
	}
	// Role-change audits record from/to for the targeted member.
	roleAuditFound := false
	for _, row := range rows {
		if row["action"] == AuditMemberRoleUpdated && row["target_id"] == "invitee" {
			roleAuditFound = true
			var meta map[string]any
			if err := json.Unmarshal([]byte(row["metadata"].(string)), &meta); err != nil {
				t.Fatalf("role audit metadata: %v", err)
			}
			if meta["from"] != WorkspaceRoleMember || meta["to"] != WorkspaceRoleGuest {
				t.Fatalf("role audit metadata=%v, want member->guest", meta)
			}
		}
	}
	if !roleAuditFound {
		t.Fatal("role change audit missing for invitee")
	}

	// Member/guest callers may not read the trail; admins may.
	if _, err := ListWorkspaceAuditLogs(ctx, fx.db, fx.workspaceID, "member", 50, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member audit read error=%v, want ErrForbidden", err)
	}
	if _, err := ListWorkspaceAuditLogs(ctx, fx.db, fx.workspaceID, "guest", 50, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("guest audit read error=%v, want ErrForbidden", err)
	}
	if _, err := ListWorkspaceAuditLogs(ctx, fx.db, fx.workspaceID, "stranger", 50, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger audit read error=%v, want ErrNotFound", err)
	}
	logs, err := ListWorkspaceAuditLogs(ctx, fx.db, fx.workspaceID, "admin", 500, 0)
	if err != nil || len(logs) == 0 {
		t.Fatalf("admin audit read=%d err=%v", len(logs), err)
	}
	// Ownership transferred to "admin" — the old owner still admin reads too.
	if _, err := ListWorkspaceAuditLogs(ctx, fx.db, fx.workspaceID, "owner", 50, 0); err != nil {
		t.Fatalf("old owner audit read: %v", err)
	}
}

// The full role × action permission matrix (§19 角色): every cell has an
// allow AND a deny across the four roles.
func TestWorkspaceRolePermissionMatrixComplete(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member2','member2@example.test','h','user','active')`)
	if err := JoinWorkspace(ctx, fx.db, fx.workspaceID, "member2"); err != nil {
		t.Fatalf("join member2: %v", err)
	}
	// Resources created by member2: the "member" actor below is a DIFFERENT
	// ordinary member, so creator-gated cells must deny for them.
	otherShared, err := CreateConversation(ctx, fx.db, Conversation{
		UserID: "member2", WorkspaceID: fx.workspaceID, Title: "Other shared", IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create other shared conv: %v", err)
	}
	privateConv, err := CreateConversation(ctx, fx.db, Conversation{
		UserID: "member2", WorkspaceID: fx.workspaceID, Title: "Other private", IsPublic: false,
	})
	if err != nil {
		t.Fatalf("create private conv: %v", err)
	}
	otherProject, err := CreateProject(ctx, fx.db, Project{UserID: "member2", WorkspaceID: fx.workspaceID, Name: "Other project", IsPublic: true})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherKB, err := CreateKB(ctx, fx.db, KnowledgeBase{
		UserID: "member2", WorkspaceID: fx.workspaceID, Name: "Other KB",
		EmbeddingModelID: "emb-a", EmbeddingDim: 3, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create other kb: %v", err)
	}

	authorize := func(actor, action, resource, resourceID string) bool {
		t.Helper()
		decision, err := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
			WorkspaceID: fx.workspaceID, UserID: actor, Action: action,
			Resource: resource, ResourceID: resourceID,
		})
		if err != nil {
			t.Fatalf("%s %s: %v", actor, action, err)
		}
		return decision.Allowed
	}

	shared := otherShared.ID
	private := privateConv.ID
	roles := []string{"owner", "admin", "member", "guest"}

	// workspace-level actions; only the owner row passes owner-exclusive ones.
	workspaceMatrix := map[string][4]bool{
		ActionWorkspaceDelete:          {true, false, false, false},
		ActionWorkspaceTransfer:        {true, false, false, false},
		ActionWorkspaceMemberInvite:    {true, true, false, false},
		ActionWorkspaceSettingsUpdate:  {true, true, false, false},
		ActionUsageView:                {true, true, false, false},
		ActionWorkspaceAuditView:       {true, true, false, false},
		ActionWorkspaceMemberView:      {true, true, true, true},
		ActionModelUse:                 {true, true, true, false},
		ActionToolUse:                  {true, true, true, false},
		ActionMCPUse:                   {true, true, true, false},
		ActionSandboxUse:               {true, true, true, false},
		ActionImageGenerate:            {true, true, true, false},
		ActionConversationCreate:       {true, true, true, false},
		ActionProjectCreate:            {true, true, true, false},
		ActionKnowledgeBaseCreate:      {true, true, true, false},
		ActionKnowledgeBaseDocumentAdd: {true, true, true, false},
	}
	for action, expected := range workspaceMatrix {
		for i, actor := range roles {
			if got := authorize(actor, action, "", ""); got != expected[i] {
				t.Fatalf("%s %s allowed=%v, want %v", actor, action, got, expected[i])
			}
		}
	}

	// Resource matrices: shared vs another member's private resource.
	conversationMatrix := map[string][4]bool{
		ActionConversationRead:             {true, true, true, true},   // shared: everyone
		ActionConversationReply:            {true, true, true, false},  // guests read-only
		ActionConversationMetadataUpdate:   {true, true, false, false}, // creator or admin
		ActionConversationVisibilityUpdate: {true, true, false, false},
		ActionConversationDelete:           {true, true, false, false},
	}
	for action, expected := range conversationMatrix {
		for i, actor := range roles {
			if got := authorize(actor, action, "conversation", shared); got != expected[i] {
				t.Fatalf("shared conv %s %s allowed=%v, want %v", actor, action, got, expected[i])
			}
		}
	}
	privateConvMatrix := map[string][4]bool{
		ActionConversationRead:           {true, true, false, false}, // creator+admins only
		ActionConversationReply:          {true, true, false, false},
		ActionConversationMetadataUpdate: {true, true, false, false},
		ActionConversationDelete:         {true, true, false, false},
	}
	for action, expected := range privateConvMatrix {
		for i, actor := range roles {
			if got := authorize(actor, action, "conversation", private); got != expected[i] {
				t.Fatalf("private conv %s %s allowed=%v, want %v", actor, action, got, expected[i])
			}
		}
	}

	projectMatrix := map[string][4]bool{
		ActionProjectRead:             {true, true, true, true},
		ActionProjectUpdate:           {true, true, false, false},
		ActionProjectVisibilityUpdate: {true, true, false, false},
		ActionProjectDelete:           {true, true, false, false},
	}
	for action, expected := range projectMatrix {
		for i, actor := range roles {
			if got := authorize(actor, action, "project", otherProject.ID); got != expected[i] {
				t.Fatalf("project %s %s allowed=%v, want %v", actor, action, got, expected[i])
			}
		}
	}
	kbMatrix := map[string][4]bool{
		ActionKnowledgeBaseRead:              {true, true, true, true},
		ActionKnowledgeBaseUpdate:            {true, true, false, false},
		ActionKnowledgeBaseVisibilityUpdate:  {true, true, false, false},
		ActionKnowledgeBaseDelete:            {true, true, false, false},
		ActionKnowledgeBaseDocumentDeleteAny: {true, true, false, false},
		ActionKnowledgeBaseDocumentDeleteOwn: {true, true, true, false},
	}
	for action, expected := range kbMatrix {
		for i, actor := range roles {
			if got := authorize(actor, action, "knowledge_base", otherKB.ID); got != expected[i] {
				t.Fatalf("kb %s %s allowed=%v, want %v", actor, action, got, expected[i])
			}
		}
	}

	// Member-management targets.
	memberTarget := map[string][4]bool{
		ActionWorkspaceMemberRoleUpdate:  {true, true, false, false},
		ActionWorkspaceMemberPermissions: {true, true, false, false},
		ActionWorkspaceMemberRemove:      {true, true, false, false},
	}
	for action, expected := range memberTarget {
		for i, actor := range roles {
			req := WorkspaceAuthorizationRequest{
				WorkspaceID: fx.workspaceID, UserID: actor, Action: action,
				Resource: "workspace_member", ResourceID: "guest",
			}
			if action == ActionWorkspaceMemberRoleUpdate {
				req.NewRole = WorkspaceRoleMember
			}
			decision, err := AuthorizeWorkspace(ctx, fx.db, req)
			if err != nil {
				t.Fatalf("%s %s: %v", actor, action, err)
			}
			if decision.Allowed != expected[i] {
				t.Fatalf("member-target %s %s allowed=%v, want %v", actor, action, decision.Allowed, expected[i])
			}
		}
	}
	// Admin-targeted actions stay owner-exclusive even for admins.
	adminTarget := map[string][4]bool{
		ActionWorkspaceMemberRoleUpdate: {true, false, false, false},
		ActionWorkspaceMemberRemove:     {true, false, false, false},
	}
	for action, expected := range adminTarget {
		for i, actor := range roles {
			req := WorkspaceAuthorizationRequest{
				WorkspaceID: fx.workspaceID, UserID: actor, Action: action,
				Resource: "workspace_member", ResourceID: "admin",
			}
			if action == ActionWorkspaceMemberRoleUpdate {
				req.NewRole = WorkspaceRoleMember
			}
			decision, _ := AuthorizeWorkspace(ctx, fx.db, req)
			if decision.Allowed != expected[i] {
				t.Fatalf("admin-target %s %s allowed=%v, want %v", actor, action, decision.Allowed, expected[i])
			}
		}
	}
}

// §19 工作空间隔离 — workspace A members cannot reach workspace B resources
// through direct ids, files, RAG scopes or search.
func TestCrossWorkspaceIsolation(t *testing.T) {
	ctx := context.Background()
	fxA := newRBACFixture(t)

	// Workspace B owned by a different owner, with shared + private resources.
	exec(t, fxA.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('ownerB','ownerb@example.test','h','user','active')`)
	exec(t, fxA.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('memberB','memberb@example.test','h','user','active')`)
	wsB, err := CreateWorkspace(ctx, fxA.db, "ownerB", "Workspace B")
	if err != nil {
		t.Fatalf("create workspace B: %v", err)
	}
	if err := JoinWorkspace(ctx, fxA.db, wsB.ID, "memberB"); err != nil {
		t.Fatalf("join B: %v", err)
	}
	convB, err := CreateConversation(ctx, fxA.db, Conversation{
		UserID: "memberB", WorkspaceID: wsB.ID, Title: "B shared", IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create B conv: %v", err)
	}
	privateB, err := CreateConversation(ctx, fxA.db, Conversation{
		UserID: "memberB", WorkspaceID: wsB.ID, Title: "B private", IsPublic: false,
	})
	if err != nil {
		t.Fatalf("create B private conv: %v", err)
	}
	kbB, err := CreateKB(ctx, fxA.db, KnowledgeBase{
		UserID: "memberB", WorkspaceID: wsB.ID, Name: "B KB",
		EmbeddingModelID: "emb-a", EmbeddingDim: 3, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create B kb: %v", err)
	}
	projectB, err := CreateProject(ctx, fxA.db, Project{UserID: "memberB", WorkspaceID: wsB.ID, Name: "B project", IsPublic: true})
	if err != nil {
		t.Fatalf("create B project: %v", err)
	}
	fileB, err := CreateFile(ctx, fxA.db, File{
		UserID: "memberB", ConversationID: convB.ID, Filename: "b.txt",
		MimeType: "text/plain", Kind: "text", StoragePath: "/tmp/b.txt", SizeBytes: 3,
	})
	if err != nil {
		t.Fatalf("create B file: %v", err)
	}

	// A's ordinary member is rejected everywhere in B — shared AND private.
	for _, actor := range []string{"member", "admin", "owner", "guest"} {
		for _, convID := range []string{convB.ID, privateB.ID} {
			if _, err := GetConversation(ctx, fxA.db, convID, actor); !errors.Is(err, ErrNotFound) {
				t.Fatalf("A:%s read B conversation err=%v, want ErrNotFound", actor, err)
			}
		}
		if _, err := GetProject(ctx, fxA.db, projectB.ID, actor); !errors.Is(err, ErrNotFound) {
			t.Fatalf("A:%s read B project err=%v, want ErrNotFound", actor, err)
		}
		if _, err := GetKB(ctx, fxA.db, kbB.ID, actor); !errors.Is(err, ErrNotFound) {
			t.Fatalf("A:%s read B kb err=%v, want ErrNotFound", actor, err)
		}
		if _, err := GetFile(ctx, fxA.db, fileB.ID, actor); !errors.Is(err, ErrNotFound) {
			t.Fatalf("A:%s read B file err=%v, want ErrNotFound", actor, err)
		}
		if files, err := ListFilesByConversation(ctx, fxA.db, convB.ID, actor); err != nil || len(files) != 0 {
			t.Fatalf("A:%s listed B files=%d err=%v, want empty", actor, len(files), err)
		}
	}
	// RAG attach scope: B's KB cannot be attached while working in A.
	if got := OwnedKBIDs(ctx, fxA.db, "member", fxA.workspaceID, []string{kbB.ID}); len(got) != 0 {
		t.Fatalf("A member attached B kb: %v", got)
	}
	// Authorization entry point denies cross-workspace uniformly.
	decision, _ := AuthorizeWorkspace(ctx, fxA.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fxA.workspaceID, UserID: "member", Action: ActionConversationRead,
		Resource: "conversation", ResourceID: convB.ID,
	})
	if decision.Allowed {
		t.Fatal("cross-workspace conversation authorized inside A's scope")
	}
	// B's own member still reaches B's resources (sanity).
	if _, err := GetConversation(ctx, fxA.db, convB.ID, "memberB"); err != nil {
		t.Fatalf("B member read B conv: %v", err)
	}
	// Search scoped to A does not surface B's conversations.
	titles, hits, err := SearchConversations(ctx, fxA.db, "member", fxA.workspaceID, "B shared", 10, 10)
	if err != nil || len(titles) != 0 || len(hits) != 0 {
		t.Fatalf("A search leaked B content titles=%v hits=%v err=%v", titles, hits, err)
	}
}

// §19 知识库 — private libraries keep their documents away from other members.
func TestPrivateKnowledgeBaseDocumentIsolation(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member2','member2@example.test','h','user','active')`)
	if err := JoinWorkspace(ctx, fx.db, fx.workspaceID, "member2"); err != nil {
		t.Fatalf("join member2: %v", err)
	}

	privateKB, err := CreateKB(ctx, fx.db, KnowledgeBase{
		UserID: "member", WorkspaceID: fx.workspaceID, Name: "Private KB",
		EmbeddingModelID: "emb-a", EmbeddingDim: 3, IsPublic: false,
	})
	if err != nil {
		t.Fatalf("create private kb: %v", err)
	}
	doc, err := CreateDocumentForUser(ctx, fx.db, Document{
		KBID: privateKB.ID, Filename: "secret.txt", MimeType: "text/plain",
	}, "member")
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	// Another ordinary member cannot fetch the document by id.
	if _, err := GetDocumentForUser(ctx, fx.db, doc.ID, "member2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member2 read private kb doc err=%v, want ErrNotFound", err)
	}
	// Admins still can.
	if _, err := GetDocumentForUser(ctx, fx.db, doc.ID, "admin"); err != nil {
		t.Fatalf("admin read private kb doc: %v", err)
	}
}

// §5/§18 TOCTOU — a kick serializes against in-flight message creation: no
// message may be created AFTER the removal commits.
func TestKickRacesMessageCreate(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	createStopped := make(chan error, 1)
	var start sync.WaitGroup
	start.Add(1)
	go func() {
		start.Wait()
		i := 0
		var lastErr error
		for {
			_, err := CreateMessageForUser(context.Background(), fx.db, Message{
				ConversationID: fx.sharedConvID, Role: "user", AuthorID: "member",
				Blocks: []byte(fmt.Sprintf(`[{"kind":"text","text":"race %d"}]`, i)),
			}, "member")
			if err != nil {
				lastErr = err
				break
			}
			i++
		}
		createStopped <- lastErr
	}()
	start.Done()

	if err := RemoveWorkspaceMember(context.Background(), fx.db, fx.workspaceID, "owner", "member"); err != nil {
		t.Fatalf("kick: %v", err)
	}
	lastErr := <-createStopped
	if lastErr == nil {
		t.Fatal("message creation loop never stopped after kick")
	}
	// After the kick commits, fresh creates fail deterministically.
	if _, err := CreateMessageForUser(ctx, fx.db, Message{
		ConversationID: fx.sharedConvID, Role: "user", AuthorID: "member",
		Blocks: []byte(`[{"kind":"text","text":"after kick"}]`),
	}, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-kick create err=%v, want ErrNotFound", err)
	}
}

// §18 TOCTOU — demoting a member to guest revokes write access atomically;
// no message may be created after the demotion commits.
func TestDemoteToGuestRacesMessageCreate(t *testing.T) {
	fx := newRBACFixture(t)

	createStopped := make(chan error, 1)
	var start sync.WaitGroup
	start.Add(1)
	go func() {
		start.Wait()
		var lastErr error
		for {
			_, err := CreateMessageForUser(context.Background(), fx.db, Message{
				ConversationID: fx.sharedConvID, Role: "user", AuthorID: "member",
				Blocks: []byte(`[{"kind":"text","text":"race"}]`),
			}, "member")
			if err != nil {
				lastErr = err
				break
			}
		}
		createStopped <- lastErr
	}()
	start.Done()

	if _, err := UpdateWorkspaceMemberRole(context.Background(), fx.db, fx.workspaceID, "owner", "member", WorkspaceRoleGuest); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if lastErr := <-createStopped; lastErr == nil {
		t.Fatal("message creation loop never stopped after demotion")
	}
	// The demoted guest reads the shared conversation but cannot write.
	if _, err := GetConversation(context.Background(), fx.db, fx.sharedConvID, "member"); err != nil {
		t.Fatalf("guest read shared conv: %v", err)
	}
	if _, err := CreateMessageForUser(context.Background(), fx.db, Message{
		ConversationID: fx.sharedConvID, Role: "user", AuthorID: "member",
		Blocks: []byte(`[{"kind":"text","text":"after demote"}]`),
	}, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-demotion create err=%v, want ErrNotFound", err)
	}
}

// §18 — a member's destructive conversation delete serializes against their
// kick: whichever wins, the loser fails closed and no partial state remains.
func TestKickRacesConversationDelete(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := context.Background()

	delDone := make(chan error, 1)
	kickDone := make(chan error, 1)
	var start sync.WaitGroup
	start.Add(1)
	go func() {
		start.Wait()
		_, err := DeleteConversationWithState(context.Background(), fx.db, fx.sharedConvID, "member")
		delDone <- err
	}()
	go func() {
		start.Wait()
		kickDone <- RemoveWorkspaceMember(context.Background(), fx.db, fx.workspaceID, "owner", "member")
	}()
	start.Done()
	delErr := <-delDone
	kickErr := <-kickDone

	if kickErr != nil {
		t.Fatalf("kick failed: %v", kickErr)
	}
	deleted := delErr == nil
	if !deleted && !errors.Is(delErr, ErrNotFound) {
		t.Fatalf("delete error=%v, want success or ErrNotFound", delErr)
	}
	// Post-state consistency: the removed member can neither read nor delete.
	if _, err := GetConversation(ctx, fx.db, fx.sharedConvID, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed member read err=%v, want ErrNotFound", err)
	}
	if _, err := DeleteConversationWithState(ctx, fx.db, fx.sharedConvID, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed member delete err=%v, want ErrNotFound", err)
	}
	// If the delete lost the race the conversation survives for other members.
	if !deleted {
		if _, err := GetConversation(ctx, fx.db, fx.sharedConvID, "admin"); err != nil {
			t.Fatalf("admin lost access to surviving conversation: %v", err)
		}
	}
}

// §19 邀请 — max_uses=N admits exactly N concurrent joins.
func TestInviteMaxUsesConcurrency(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	racers := 5
	maxUses := 3
	for i := 0; i < racers; i++ {
		exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?,'h','user','active')`,
			"muser"+string(rune('a'+i)), "muser"+string(rune('a'+i))+"@example.test")
	}
	invite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleMember, 0, int64(maxUses))
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	results := make(chan error, racers)
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			_, _, err := JoinWorkspaceByInviteRecord(context.Background(), fx.db, invite.Token, "muser"+string(rune('a'+i)), "")
			results <- err
		}(i)
	}
	start.Done()
	wg.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatalf("unexpected join error: %v", err)
		}
	}
	if succeeded != maxUses {
		t.Fatalf("max_uses=%d invite succeeded %d times, want exactly %d", maxUses, succeeded, maxUses)
	}
	var used int64
	if err := fx.db.QueryRow(`SELECT used_count FROM workspace_invites WHERE id=?`, invite.ID).Scan(&used); err != nil || used != int64(maxUses) {
		t.Fatalf("used_count=%d err=%v, want %d", used, err, maxUses)
	}
}

// §19 所有权 — two concurrent transfers linearize: exactly one wins and the
// workspace ends with a single consistent owner.
func TestOwnershipTransferRace(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	transferDone := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for _, target := range []string{"admin", "member"} {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			start.Wait()
			_, err := TransferWorkspaceOwnership(context.Background(), fx.db, fx.workspaceID, "owner", target)
			transferDone <- err
		}(target)
	}
	start.Done()
	wg.Wait()
	close(transferDone)

	succeeded := 0
	for err := range transferDone {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			t.Fatalf("unexpected transfer error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent transfers succeeded %d times, want exactly 1", succeeded)
	}
	var finalOwner string
	if err := fx.db.QueryRow(`SELECT owner_id FROM workspaces WHERE id=?`, fx.workspaceID).Scan(&finalOwner); err != nil {
		t.Fatal(err)
	}
	if finalOwner != "admin" && finalOwner != "member" {
		t.Fatalf("final owner=%q, want admin or member", finalOwner)
	}
	// The winner holds owner-exclusive authority; the old owner does not.
	winnerDecision, _ := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fx.workspaceID, UserID: finalOwner, Action: ActionWorkspaceDelete,
	})
	if !winnerDecision.Allowed {
		t.Fatal("transfer winner lacks owner-exclusive authority")
	}
	oldOwnerDecision, _ := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fx.workspaceID, UserID: "owner", Action: ActionWorkspaceDelete,
	})
	if oldOwnerDecision.Allowed {
		t.Fatal("old owner kept owner-exclusive authority after transfer race")
	}
}
