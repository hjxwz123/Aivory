package store

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDomainExistingUserCandidatesAndAtomicEnrollment(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()

	existingGroup, err := CreateUserGroup(ctx, fx.db, UserGroup{Name: "Existing Plan"})
	if err != nil {
		t.Fatal(err)
	}
	initialGroup, err := CreateUserGroup(ctx, fx.db, UserGroup{Name: "New Domain Plan"})
	if err != nil {
		t.Fatal(err)
	}
	eligible, err := CreateUser(ctx, fx.db, "eligible@company.example", "Eligible", "hash")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := CreateUserWithState(ctx, fx.db, "pending@company.example", "Pending", "hash", "user", "pending", true)
	if err != nil {
		t.Fatal(err)
	}
	rollbackCandidate, err := CreateUser(ctx, fx.db, "rollback@company.example", "Rollback", "hash")
	if err != nil {
		t.Fatal(err)
	}
	subdomain, err := CreateUser(ctx, fx.db, "sub@dept.company.example", "Subdomain", "hash")
	if err != nil {
		t.Fatal(err)
	}
	platformAdmin, err := CreateUserWithRole(ctx, fx.db, "admin@company.example", "Admin", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	alreadyBound, err := CreateUser(ctx, fx.db, "bound@other.example", "Bound", "hash")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, fx.db, `UPDATE users SET group_id=? WHERE id=?`, existingGroup.ID, eligible.ID)

	root, err := CreateConversation(ctx, fx.db, Conversation{ID: "eligible-root", UserID: eligible.ID, Title: "Historical chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateConversation(ctx, fx.db, Conversation{
		ID: "eligible-inline", UserID: eligible.ID, Title: "Inline chat", InlineSourceConv: root.ID,
	}); err != nil {
		t.Fatal(err)
	}

	otherRule := RegistrationDomain{Domain: "other.example", WorkspaceID: fx.workspaceID, Enabled: true}
	if err := SaveRegistrationDomain(ctx, fx.db, otherRule, true); err != nil {
		t.Fatal(err)
	}
	if _, err := EnrollExistingDomainUsers(ctx, fx.db, otherRule.Domain, []string{alreadyBound.ID}); err != nil {
		t.Fatal(err)
	}
	exec(t, fx.db, `UPDATE users SET email='bound@company.example' WHERE id=?`, alreadyBound.ID)

	rule := RegistrationDomain{
		Domain: "company.example", WorkspaceID: fx.workspaceID, LockPersonal: true,
		InitialGroupID: initialGroup.ID, Enabled: true,
	}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListDomainUserCandidates(ctx, fx.db, rule.Domain, "")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]DomainUserCandidate, len(candidates))
	for _, candidate := range candidates {
		got[candidate.UserID] = candidate
	}
	for _, user := range []*User{eligible, pending, rollbackCandidate, subdomain} {
		if _, ok := got[user.ID]; !ok {
			t.Errorf("eligible historical account %s missing from candidates: %+v", user.Email, candidates)
		}
	}
	if candidate := got[eligible.ID]; candidate.PersonalConversationCount != 1 {
		t.Errorf("eligible personal conversation count=%d, want root-only count 1", candidate.PersonalConversationCount)
	}
	for _, user := range []*User{platformAdmin, alreadyBound} {
		if _, ok := got[user.ID]; ok {
			t.Errorf("ineligible account %s appeared in candidates", user.Email)
		}
	}
	searched, err := ListDomainUserCandidates(ctx, fx.db, rule.Domain, "SUBDOMAIN")
	if err != nil || len(searched) != 1 || searched[0].UserID != subdomain.ID {
		t.Fatalf("cross-domain candidate search=%+v err=%v", searched, err)
	}

	added, err := EnrollExistingDomainUsers(ctx, fx.db, rule.Domain, []string{eligible.ID, eligible.ID, pending.ID, subdomain.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 3 {
		t.Fatalf("added=%v, want three unique accounts", added)
	}
	for _, user := range []*User{eligible, pending, subdomain} {
		access, err := GetDomainAccess(ctx, fx.db, user.ID)
		if err != nil || access == nil || !access.Locked || access.WorkspaceID != fx.workspaceID {
			t.Errorf("enrolled access for %s=%+v err=%v", user.Email, access, err)
		}
		if role, err := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, user.ID); err != nil || role != WorkspaceRoleMember {
			t.Errorf("enrolled membership for %s role=%q err=%v", user.Email, role, err)
		}
	}
	refreshed, err := FindUserByID(ctx, fx.db, eligible.ID)
	if err != nil || refreshed.GroupID != existingGroup.ID {
		t.Fatalf("historical enrollment changed existing group: user=%+v err=%v", refreshed, err)
	}

	if _, err := EnrollExistingDomainUsers(ctx, fx.db, rule.Domain, []string{rollbackCandidate.ID, platformAdmin.ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mixed invalid batch error=%v, want ErrForbidden", err)
	}
	if access, err := GetDomainAccess(ctx, fx.db, rollbackCandidate.ID); err != nil || access != nil {
		t.Fatalf("invalid batch partially bound first account: access=%+v err=%v", access, err)
	}
	if role, err := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, rollbackCandidate.ID); err != nil || role != "" {
		t.Fatalf("invalid batch partially added workspace membership: role=%q err=%v", role, err)
	}
	var memberships int
	if err := fx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id=?`, fx.workspaceID, rollbackCandidate.ID).Scan(&memberships); err != nil || memberships != 0 {
		t.Fatalf("invalid batch persisted %d workspace membership rows: %v", memberships, err)
	}
}

func TestRemoveDomainUserHonorsWorkspaceMembershipProvenance(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	rule := RegistrationDomain{Domain: "company.example", WorkspaceID: fx.workspaceID, LockPersonal: true}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}

	createdByRule, err := CreateUser(ctx, fx.db, "external@unrelated.example", "External", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnrollExistingDomainUsers(ctx, fx.db, rule.Domain, []string{createdByRule.ID}); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveDomainUser(ctx, fx.db, rule.Domain, createdByRule.ID)
	if err != nil || !removed.WorkspaceMembershipRemoved {
		t.Fatalf("remove rule-created membership=%+v err=%v", removed, err)
	}
	if access, err := GetDomainAccess(ctx, fx.db, createdByRule.ID); err != nil || access != nil {
		t.Fatalf("domain binding survived removal: access=%+v err=%v", access, err)
	}
	var count int
	if err := fx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id=?`, fx.workspaceID, createdByRule.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rule-created workspace membership count=%d err=%v", count, err)
	}

	if _, err := EnrollExistingDomainUsers(ctx, fx.db, rule.Domain, []string{"member", "owner"}); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"member", "owner"} {
		removed, err := RemoveDomainUser(ctx, fx.db, rule.Domain, userID)
		if err != nil || removed.WorkspaceMembershipRemoved {
			t.Errorf("remove pre-existing member %s=%+v err=%v", userID, removed, err)
			continue
		}
		if role, err := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, userID); err != nil || role == "" {
			t.Errorf("pre-existing workspace membership for %s role=%q err=%v", userID, role, err)
		}
	}
	if _, err := RemoveDomainUser(ctx, fx.db, rule.Domain, createdByRule.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second removal error=%v, want ErrNotFound", err)
	}
}

func TestDomainPersonalConversationStatusDismissalAndMigration(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	user, err := CreateUser(ctx, fx.db, "history@company.example", "History", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(ctx, fx.db, Project{UserID: user.ID, Name: "Personal project"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := CreateConversation(ctx, fx.db, Conversation{
		ID: "history-root", UserID: user.ID, ProjectID: project.ID, Title: "Archived history",
		KBIDs: json.RawMessage(`["personal-kb"]`), Archived: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	inline, err := CreateConversation(ctx, fx.db, Conversation{
		ID: "history-inline", UserID: user.ID, ProjectID: project.ID, Title: "Inline history",
		KBIDs: json.RawMessage(`["personal-kb"]`), InlineSourceConv: root.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, conversationID := range []string{root.ID, inline.ID} {
		if _, err := CreateMessage(ctx, fx.db, Message{
			ID: "message-" + conversationID, ConversationID: conversationID, Role: "user",
			Blocks: json.RawMessage(`[{"kind":"text","text":"keep me"}]`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	rule := RegistrationDomain{Domain: "company.example", WorkspaceID: fx.workspaceID, LockPersonal: true, Enabled: true}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	if _, err := EnrollExistingDomainUsers(ctx, fx.db, rule.Domain, []string{user.ID}); err != nil {
		t.Fatal(err)
	}

	status, err := GetDomainPersonalDataStatus(ctx, fx.db, user.ID)
	if err != nil || !status.NeedsAction || status.PersonalConversationCount != 1 || !status.CanMigrate || status.PromptDismissed {
		t.Fatalf("initial personal-data status=%+v err=%v", status, err)
	}
	if err := DismissDomainPersonalDataPrompt(ctx, fx.db, user.ID); err != nil {
		t.Fatal(err)
	}
	status, err = GetDomainPersonalDataStatus(ctx, fx.db, user.ID)
	if err != nil || !status.NeedsAction || !status.PromptDismissed {
		t.Fatalf("dismissed status=%+v err=%v; dismissal must not discard data", status, err)
	}

	exec(t, fx.db, `UPDATE workspace_members SET can_private_conversations=0 WHERE workspace_id=? AND user_id=?`, fx.workspaceID, user.ID)
	status, err = GetDomainPersonalDataStatus(ctx, fx.db, user.ID)
	if err != nil || status.CanMigrate {
		t.Fatalf("migration remained available without private-conversation permission: status=%+v err=%v", status, err)
	}
	if _, err := MigrateDomainPersonalConversations(ctx, fx.db, user.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("migration without permission error=%v, want ErrForbidden", err)
	}
	exec(t, fx.db, `UPDATE workspace_members SET can_private_conversations=1 WHERE workspace_id=? AND user_id=?`, fx.workspaceID, user.ID)
	migrated, err := MigrateDomainPersonalConversations(ctx, fx.db, user.ID)
	if err != nil || migrated != 1 {
		t.Fatalf("migrate count=%d err=%v, want one root conversation", migrated, err)
	}

	for _, expected := range []struct {
		id       string
		archived bool
	}{
		{id: root.ID, archived: true},
		{id: inline.ID, archived: false},
	} {
		conversation, err := GetConversation(ctx, fx.db, expected.id, user.ID)
		if err != nil {
			t.Fatalf("get migrated conversation %s: %v", expected.id, err)
		}
		if conversation.WorkspaceID != fx.workspaceID || conversation.IsPublic || conversation.ProjectID != "" || string(conversation.KBIDs) != "[]" || conversation.Archived != expected.archived {
			t.Errorf("migrated conversation %s=%+v", expected.id, conversation)
		}
		messages, err := ListAllMessages(ctx, fx.db, expected.id)
		if err != nil || len(messages) != 1 || string(messages[0].Blocks) != `[{"kind":"text","text":"keep me"}]` {
			t.Errorf("migrated messages for %s=%+v err=%v", expected.id, messages, err)
		}
	}
	status, err = GetDomainPersonalDataStatus(ctx, fx.db, user.ID)
	if err != nil || status.NeedsAction || status.PersonalConversationCount != 0 {
		t.Fatalf("post-migration status=%+v err=%v", status, err)
	}
}
