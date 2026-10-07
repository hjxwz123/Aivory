package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditSnapshotsUseExistingColumnsAndRedactSecrets(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	for resource := range auditSnapshotSpecs {
		if _, err := LoadAuditSnapshot(ctx, fx.db, resource, "nonexistent-audit-target"); err != nil {
			t.Errorf("%s snapshot: %v", resource, err)
		}
	}
	for _, resource := range []string{"models-order", "channels-order", "users-order", "model-quotas"} {
		if _, err := LoadAuditSnapshot(ctx, fx.db, resource, "nonexistent-target"); err != nil {
			t.Errorf("%s snapshot: %v", resource, err)
		}
	}
	exec(t, fx.db, `INSERT INTO channels(id,name,type,api_key,base_url) VALUES('audit-channel','Primary','openai','old-secret','https://upstream.example?token=old')`)
	before, err := LoadAuditSnapshot(ctx, fx.db, "channels", "audit-channel")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, fx.db, `UPDATE channels SET enabled=0,api_key='new-secret',base_url='https://upstream.example?token=new',headers='{"A":"private-header"}' WHERE id='audit-channel'`)
	after, err := LoadAuditSnapshot(ctx, fx.db, "channels", "audit-channel")
	if err != nil {
		t.Fatal(err)
	}
	changes := AuditSnapshotChanges(before, after)
	if changes["enabled"].Before != int64(1) || changes["enabled"].After != int64(0) || !changes["api_key"].Redacted || !changes["base_url"].Redacted || !changes["headers"].Redacted {
		t.Fatalf("unexpected changes: %+v", changes)
	}
	raw, _ := json.Marshal(changes)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "https://") || strings.Contains(string(raw), "private-header") {
		t.Fatalf("leaked secrets: %s", raw)
	}
	exec(t, fx.db, `INSERT INTO settings(key,value) VALUES('login_captcha_required','false'),('smtp_password','"secret-a"'),('announcement','{"content":"private-text"}')`)
	before, err = LoadAuditSnapshot(ctx, fx.db, "settings", "")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, fx.db, `UPDATE settings SET value='true' WHERE key='login_captcha_required'`)
	exec(t, fx.db, `UPDATE settings SET value='"secret-b"' WHERE key='smtp_password'`)
	after, err = LoadAuditSnapshot(ctx, fx.db, "settings", "")
	if err != nil {
		t.Fatal(err)
	}
	changes = AuditSnapshotChanges(before, after)
	if changes["login_captcha_required"].Before != false || changes["login_captcha_required"].After != true || !changes["smtp_password"].Redacted {
		t.Fatalf("settings changes: %+v", changes)
	}
}

func TestAuditEvidenceSurvivesActorRemovalAndFilters(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	exec(t, fx.db, `DELETE FROM workspace_audit_logs`)
	event := AdminAuditLog{WorkspaceAuditLog: WorkspaceAuditLog{ID: "audit-evidence", ActorUserID: "deleted-actor", ActorName: "Previous administrator", Type: "users", Action: "admin.users.role", TargetType: "user", TargetID: "member", CreatedAt: 100}, ActorRole: "admin", Result: "denied", RequestID: "request-42", Source: "admin", OccurredAtMS: 100123, Changes: map[string]AuditChange{"role": {Before: "user", After: "admin"}}}
	if err := AppendAdminAudit(ctx, fx.db, event); err != nil {
		t.Fatal(err)
	}
	filter := AdminAuditFilter{Type: "users", Result: "denied", Actor: "deleted-actor", Target: "member", Action: "admin.users.role", Search: "request-42", From: 100123, Until: 100123}
	logs, total, err := ListFilteredAdminAuditLogs(ctx, fx.db, filter, 10, 0)
	if err != nil || total != 1 || len(logs) != 1 {
		t.Fatalf("filtered=%+v total=%d err=%v", logs, total, err)
	}
	if logs[0].ActorName != event.ActorName || logs[0].ActorRole != "admin" || logs[0].Changes["role"].After != "admin" {
		t.Fatalf("lost evidence: %+v", logs[0])
	}
	filter.Result = "success"
	if _, total, err := ListFilteredAdminAuditLogs(ctx, fx.db, filter, 10, 0); err != nil || total != 0 {
		t.Fatalf("wrong result filter: %d %v", total, err)
	}
}

func TestAuditMigrationPreservesLegacyRows(t *testing.T) {
	fx := newRBACFixture(t)
	exec(t, fx.db, `DROP TABLE admin_audit_logs`)
	exec(t, fx.db, `DROP TABLE workspace_audit_logs`)
	exec(t, fx.db, `CREATE TABLE admin_audit_logs(id TEXT PRIMARY KEY,actor_user_id TEXT NOT NULL,event_type TEXT NOT NULL,action TEXT NOT NULL,target_type TEXT NOT NULL DEFAULT '',target_id TEXT NOT NULL DEFAULT '',metadata TEXT NOT NULL DEFAULT '{}',created_at INTEGER NOT NULL)`)
	exec(t, fx.db, `CREATE TABLE workspace_audit_logs(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,actor_user_id TEXT NOT NULL,action TEXT NOT NULL,target_type TEXT NOT NULL DEFAULT '',target_id TEXT NOT NULL DEFAULT '',metadata TEXT NOT NULL DEFAULT '{}',created_at INTEGER NOT NULL)`)
	exec(t, fx.db, `INSERT INTO admin_audit_logs(id,actor_user_id,event_type,action,created_at) VALUES('legacy-event','admin','users','admin.users.role',100)`)
	for i := 0; i < 2; i++ {
		if err := Migrate(fx.db); err != nil {
			t.Fatal(err)
		}
	}
	logs, total, err := ListAdminAuditLogs(t.Context(), fx.db, "legacy-event", "", 10, 0)
	if err != nil || total != 1 || logs[0].Result != "success" || logs[0].OccurredAtMS != 100000 {
		t.Fatalf("legacy event=%+v total=%d err=%v", logs, total, err)
	}
}

func TestBackupPreservesCurrentAuditEvidence(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := context.Background()
	event := AdminAuditLog{WorkspaceAuditLog: WorkspaceAuditLog{ID: "keep-audit", ActorUserID: "admin", ActorName: "Original actor", Type: "system", Action: "admin.system.import"}}
	if err := AppendAdminAudit(ctx, fx.db, event); err != nil {
		t.Fatal(err)
	}
	var dump bytes.Buffer
	if _, err := ExportTable(ctx, fx.db, "admin_audit_logs", &dump); err != nil {
		t.Fatal(err)
	}
	tx, err := fx.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := WipeAll(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreTable(ctx, tx, "admin_audit_logs", &dump); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertTable(ctx, tx, "admin_audit_logs", strings.NewReader(`{"id":"keep-audit","actor_name":"Overwritten actor","actor_user_id":"admin","event_type":"system","action":"tampered"}`)); err != nil {
		t.Fatal(err)
	}
	var actor, action string
	if err := tx.QueryRowContext(ctx, `SELECT actor_name,action FROM admin_audit_logs WHERE id='keep-audit'`).Scan(&actor, &action); err != nil {
		t.Fatal(err)
	}
	if actor != "Original actor" || action != event.Action {
		t.Fatalf("audit overwritten: %s %s", actor, action)
	}
}

func TestWorkspaceAuditSnapshotsAdministratorInsteadOfNewOwner(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := WithAuditContext(t.Context(), &AuditContext{ActorID: "admin", ActorName: "Platform Administrator", ActorRole: "admin", RequestID: "workspace-request", Source: "admin"})
	workspace, err := CreateWorkspace(ctx, fx.db, "member", "Administrator-created workspace")
	if err != nil {
		t.Fatal(err)
	}
	logs, total, err := ListAdminAuditLogs(ctx, fx.db, "workspace-request", "workspace", 10, 0)
	if err != nil || total != 1 || len(logs) != 1 {
		t.Fatalf("total=%d err=%v logs=%+v", total, err, logs)
	}
	if logs[0].ActorUserID != "admin" || logs[0].ActorName != "Platform Administrator" || logs[0].WorkspaceID != workspace.ID {
		t.Fatalf("wrong workspace actor: %+v", logs[0])
	}
}
