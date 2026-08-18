package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func openLibraryTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash) VALUES
		('u1','u1@example.test','h'),('u2','u2@example.test','h')`); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

func TestMigrateAddsIconToLegacyUserSkillsTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-user-skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("seed current schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash) VALUES('u1','u1@example.test','h')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_skills(id,user_id,name,description,instructions) VALUES('usk1','u1','legacy-skill','d','body')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE user_skills DROP COLUMN icon`); err != nil {
		t.Fatalf("simulate legacy user_skills table: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	var icon string
	if err := db.QueryRow(`SELECT icon FROM user_skills WHERE id='usk1'`).Scan(&icon); err != nil {
		t.Fatalf("read migrated icon: %v", err)
	}
	if icon != "" {
		t.Fatalf("migrated icon=%q, want empty default", icon)
	}
}

func TestPrivateLibraryOwnershipAndCatalogCopiesRemainIndependent(t *testing.T) {
	db, ctx := openLibraryTestDB(t)
	adminSkill, err := CreateSkill(ctx, db, Skill{
		Name: "admin-skill", Description: "trigger", DisplayDescription: "display",
		Icon: "Presentation", Instructions: "original instructions", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	adminPrompt, err := CreatePrompt(ctx, db, Prompt{
		Name: "Admin prompt", Description: "display", Content: "original prompt", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	userSkill, err := CreateUserSkill(ctx, db, UserSkill{
		UserID: "u1", Name: "admin-skill", Description: adminSkill.Description,
		Icon: adminSkill.Icon, Instructions: adminSkill.Instructions, SourceSkillID: adminSkill.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	userPrompt, err := CreateUserPrompt(ctx, db, UserPrompt{
		UserID: "u1", Name: adminPrompt.Name, Description: adminPrompt.Description,
		Content: adminPrompt.Content, SourcePromptID: adminPrompt.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := GetUserSkill(ctx, db, userSkill.ID, "u2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user skill read err=%v", err)
	}
	if _, err := UpdateUserPrompt(ctx, db, userPrompt.ID, "u2", UserPrompt{Name: "stolen", Description: "x", Content: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user prompt update err=%v", err)
	}
	if err := DeleteUserSkill(ctx, db, userSkill.ID, "u2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user skill delete err=%v", err)
	}

	if err := DeleteSkill(ctx, db, adminSkill.ID); err != nil {
		t.Fatal(err)
	}
	if err := DeletePrompt(ctx, db, adminPrompt.ID); err != nil {
		t.Fatal(err)
	}
	gotSkill, err := GetUserSkill(ctx, db, userSkill.ID, "u1")
	if err != nil || gotSkill.Icon != "Presentation" || gotSkill.Instructions != "original instructions" || gotSkill.SourceSkillID != "" {
		t.Fatalf("independent skill copy=%+v err=%v", gotSkill, err)
	}
	gotPrompt, err := GetUserPrompt(ctx, db, userPrompt.ID, "u1")
	if err != nil || gotPrompt.Content != "original prompt" || gotPrompt.SourcePromptID != "" {
		t.Fatalf("independent prompt copy=%+v err=%v", gotPrompt, err)
	}
}

func TestWorkspaceLibraryIsScopedAndRoleAware(t *testing.T) {
	db, ctx := openLibraryTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO users(id,email,password_hash) VALUES ('u3','u3@example.test','h'),('u4','u4@example.test','h');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws1','Workspace one','u1','token-ws1');
		INSERT INTO workspace_members(workspace_id,user_id,role) VALUES
			('ws1','u1','admin'),('ws1','u2','member'),('ws1','u3','guest')
	`); err != nil {
		t.Fatal(err)
	}

	ownerSkill, err := CreateUserSkill(ctx, db, UserSkill{
		UserID: "u1", WorkspaceID: "ws1", Name: "owner-skill", Description: "owner", Instructions: "owner instructions",
	})
	if err != nil {
		t.Fatal(err)
	}
	memberSkill, err := CreateUserSkill(ctx, db, UserSkill{
		UserID: "u2", WorkspaceID: "ws1", Name: "member-skill", Description: "member", Instructions: "member instructions",
	})
	if err != nil {
		t.Fatal(err)
	}

	personal, err := ListUserSkillsScoped(ctx, db, "u1", "")
	if err != nil || len(personal) != 0 {
		t.Fatalf("workspace skill leaked into personal library: rows=%+v err=%v", personal, err)
	}
	memberRows, err := ListUserSkillsScoped(ctx, db, "u2", "ws1")
	if err != nil || len(memberRows) != 2 {
		t.Fatalf("member workspace rows=%+v err=%v", memberRows, err)
	}
	for _, row := range memberRows {
		wantManage := row.ID == memberSkill.ID
		if row.CanManage != wantManage {
			t.Fatalf("member can_manage for %s=%v want=%v", row.ID, row.CanManage, wantManage)
		}
	}
	guestRows, err := ListUserSkillsScoped(ctx, db, "u3", "ws1")
	if err != nil || len(guestRows) != 2 {
		t.Fatalf("guest workspace rows=%+v err=%v", guestRows, err)
	}
	for _, row := range guestRows {
		if row.CanManage {
			t.Fatalf("guest unexpectedly can manage %s", row.ID)
		}
	}
	outsiderRows, err := ListUserSkillsScoped(ctx, db, "u4", "ws1")
	if err != nil || len(outsiderRows) != 0 {
		t.Fatalf("outsider workspace rows=%+v err=%v", outsiderRows, err)
	}

	ownerSkill.Description = "attempted edit"
	if _, err := UpdateUserSkillScoped(ctx, db, ownerSkill.ID, "u2", "ws1", *ownerSkill); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member edited another member's skill: err=%v", err)
	}
	memberSkill.Description = "edited by creator"
	if _, err := UpdateUserSkillScoped(ctx, db, memberSkill.ID, "u2", "ws1", *memberSkill); err != nil {
		t.Fatalf("member could not edit own skill: %v", err)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET can_create_skills_prompts=0 WHERE workspace_id='ws1' AND user_id='u2'`); err != nil {
		t.Fatal(err)
	}
	memberRows, err = ListUserSkillsScoped(ctx, db, "u2", "ws1")
	if err != nil || len(memberRows) != 2 {
		t.Fatalf("revoked member workspace rows=%+v err=%v", memberRows, err)
	}
	for _, row := range memberRows {
		if row.CanManage {
			t.Fatalf("revoked member unexpectedly can manage %s", row.ID)
		}
	}
	if _, err := CreateUserPrompt(ctx, db, UserPrompt{
		UserID: "u2", WorkspaceID: "ws1", Name: "revoked-prompt", Description: "revoked", Content: "should fail",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked member prompt create err=%v, want ErrNotFound", err)
	}
	if _, err := UpdateUserSkillScoped(ctx, db, memberSkill.ID, "u2", "ws1", *memberSkill); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked member skill update err=%v, want ErrNotFound", err)
	}
	if _, err := CreateUserPrompt(ctx, db, UserPrompt{
		UserID: "u3", WorkspaceID: "ws1", Name: "guest-prompt", Description: "guest", Content: "should fail",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest workspace prompt create err=%v, want ErrNotFound", err)
	}
	if _, err := CreateUserSkill(ctx, db, UserSkill{
		UserID: "u3", WorkspaceID: "ws1", Name: "guest-skill", Description: "guest", Instructions: "should fail",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest workspace skill create err=%v, want ErrNotFound", err)
	}
	if _, _, err := ResolveUserSkillSelectionScoped(ctx, db, "u4", "ws1", []string{ownerSkill.ID}, true); !errors.Is(err, ErrInvalidUserSkillSelection) {
		t.Fatalf("outsider selected workspace skill err=%v", err)
	}
}

func TestUserSkillIconRoundTripsAcrossReadAndUpdatePaths(t *testing.T) {
	db, ctx := openLibraryTestDB(t)
	created, err := CreateUserSkill(ctx, db, UserSkill{
		UserID: "u1", Name: "icon-skill", Description: "description",
		Icon: "  Presentation  ", Instructions: "instructions",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Icon != "Presentation" {
		t.Fatalf("created icon=%q", created.Icon)
	}

	created.Description = "updated description"
	created.Icon = "  BookOpen  "
	updated, err := UpdateUserSkill(ctx, db, created.ID, "u1", *created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Icon != "BookOpen" {
		t.Fatalf("updated icon=%q", updated.Icon)
	}

	listed, err := ListUserSkills(ctx, db, "u1")
	if err != nil || len(listed) != 1 || listed[0].Icon != "BookOpen" {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	resolved, _, err := ResolveUserSkillSelection(ctx, db, "u1", []string{created.ID}, true)
	if err != nil || len(resolved) != 1 || resolved[0].Icon != "BookOpen" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
}

func TestResolveUserSkillSelectionEnforcesOwnershipCountOrderAndTotalBytes(t *testing.T) {
	db, ctx := openLibraryTestDB(t)
	ids := make([]string, 0, MaxSelectedUserSkills+1)
	for i := 0; i < MaxSelectedUserSkills+1; i++ {
		skill, err := CreateUserSkill(ctx, db, UserSkill{
			UserID: "u1", Name: "skill-" + string(rune('a'+i)), Description: "d", Instructions: "body",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, skill.ID)
	}
	rows, normalized, err := ResolveUserSkillSelection(ctx, db, "u1", []string{ids[1], ids[0], ids[1]}, true)
	if err != nil || len(rows) != 2 || rows[0].ID != ids[1] || rows[1].ID != ids[0] || strings.Join(normalized, ",") != ids[1]+","+ids[0] {
		t.Fatalf("ordered resolution rows=%+v ids=%v err=%v", rows, normalized, err)
	}
	if _, _, err := ResolveUserSkillSelection(ctx, db, "u1", ids, true); !errors.Is(err, ErrInvalidUserSkillSelection) {
		t.Fatalf("over-count err=%v", err)
	}
	if _, _, err := ResolveUserSkillSelection(ctx, db, "u2", []string{ids[0]}, true); !errors.Is(err, ErrInvalidUserSkillSelection) {
		t.Fatalf("cross-user selection err=%v", err)
	}
	large, err := CreateUserSkill(ctx, db, UserSkill{
		UserID: "u2", Name: "large-skill", Description: "d",
		Instructions: strings.Repeat("x", MaxSelectedUserSkillInstructionBytes+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveUserSkillSelection(ctx, db, "u2", []string{large.ID}, true); !errors.Is(err, ErrInvalidUserSkillSelection) {
		t.Fatalf("over-byte selection err=%v", err)
	}
}

func TestMessageSelectedUserSkillsPersistButNeverSerialize(t *testing.T) {
	db, ctx := openLibraryTestDB(t)
	conv, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "skills"})
	if err != nil {
		t.Fatal(err)
	}
	selected := json.RawMessage(`["usk_private"]`)
	created, err := CreateMessage(ctx, db, Message{
		ConversationID: conv.ID, Role: "user", Blocks: json.RawMessage(`[{"kind":"text","text":"hello"}]`),
		SelectedUserSkillIDs: selected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(created.SelectedUserSkillIDs) != string(selected) {
		t.Fatalf("selected ids=%s want=%s", created.SelectedUserSkillIDs, selected)
	}
	wire, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "selected_user_skill_ids") || strings.Contains(string(wire), "usk_private") {
		t.Fatalf("private skill ids leaked to JSON: %s", wire)
	}
}
