package store

import (
	"context"
	"errors"
	"testing"
)

func TestListAdminWorkspaceAuditLogsSearchesAndPaginatesNewestFirst(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := context.Background()
	secondWorkspace, err := CreateWorkspace(ctx, fx.db, "owner", "Second space")
	if err != nil {
		t.Fatalf("create second workspace: %v", err)
	}
	exec(t, fx.db, `DELETE FROM workspace_audit_logs`)
	exec(t, fx.db, `UPDATE users SET name='Admin Actor' WHERE id='admin'`)
	exec(t, fx.db, `UPDATE users SET name='Workspace Owner' WHERE id='owner'`)
	for _, row := range []struct {
		id, workspace, actor, action, targetType, targetID, metadata string
		createdAt                                                    int64
	}{
		{"aud-1", fx.workspaceID, "admin", "member.role_updated", "member", "member-1", `{"role":"admin"}`, 100},
		{"aud-2", secondWorkspace.ID, "member", "invite.created", "invite", "invite-2", `{"email":"person@example.test"}`, 200},
		{"aud-3", fx.workspaceID, "owner", "policy.updated", "workspace", fx.workspaceID, `{}`, 200},
	} {
		exec(t, fx.db, `INSERT INTO workspace_audit_logs(id,workspace_id,actor_user_id,action,target_type,target_id,metadata,created_at)
			VALUES(?,?,?,?,?,?,?,?)`, row.id, row.workspace, row.actor, row.action, row.targetType, row.targetID, row.metadata, row.createdAt)
	}

	rows, total, err := ListAdminWorkspaceAuditLogs(ctx, fx.db, "", 1, 0)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if total != 3 || len(rows) != 1 || rows[0].ID != "aud-3" {
		t.Fatalf("first page = total %d, rows %+v; want total 3 and aud-3", total, rows)
	}
	if rows[0].WorkspaceName != "RBAC" || rows[0].ActorName == "" || string(rows[0].Metadata) != `{}` {
		t.Fatalf("row enrichment/detail = %+v", rows[0])
	}

	rows, total, err = ListAdminWorkspaceAuditLogs(ctx, fx.db, "INVITE.CREATED", 10, 0)
	if err != nil {
		t.Fatalf("search audit records: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != "aud-2" {
		t.Fatalf("search result = total %d, rows %+v; want aud-2", total, rows)
	}

	rows, total, err = ListAdminWorkspaceAuditLogs(ctx, fx.db, "missing", 10, 0)
	if err != nil {
		t.Fatalf("search missing record: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("missing search = total %d, rows %+v; want empty", total, rows)
	}
}

func TestDeleteAdminAuditLogsMatchesListFiltersAcrossSources(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	exec(t, fx.db, `DELETE FROM workspace_audit_logs`)
	exec(t, fx.db, `UPDATE users SET name='Audit Actor' WHERE id='admin'`)
	for _, row := range []struct {
		id, source, result string
		occurred           int64
	}{
		{"workspace-match", "workspace", "failure", 2000},
		{"workspace-success", "workspace", "success", 2000},
		{"admin-match", "admin", "failure", 2000},
		{"admin-newer", "admin", "failure", 4000},
	} {
		if row.source == "workspace" {
			exec(t, fx.db, `INSERT INTO workspace_audit_logs(id,workspace_id,actor_user_id,action,target_type,target_id,result,occurred_at_ms,created_at)
				VALUES(?,?,'admin','policy.updated','workspace',?,?,?,2)`, row.id, fx.workspaceID, fx.workspaceID, row.result, row.occurred)
		} else {
			exec(t, fx.db, `INSERT INTO admin_audit_logs(id,actor_user_id,event_type,action,target_type,target_id,result,occurred_at_ms,created_at)
				VALUES(?,'admin','settings','policy.updated','workspace',?,?,?,2)`, row.id, fx.workspaceID, row.result, row.occurred)
		}
	}
	filter := AdminAuditFilter{Search: "AUDIT ACTOR", Result: "failure", Actor: "admin", Target: fx.workspaceID, Action: "policy.updated", From: 1000, Until: 3000}
	_, total, err := ListFilteredAdminAuditLogs(ctx, fx.db, filter, 50, 0)
	if err != nil || total != 2 {
		t.Fatalf("list matches=%d err=%v", total, err)
	}
	deleted, err := DeleteFilteredAdminAuditLogs(ctx, fx.db, filter)
	if err != nil || deleted != int64(total) {
		t.Fatalf("deleted=%d err=%v; wanted %d", deleted, err, total)
	}
	remaining, total, err := ListAdminAuditLogs(ctx, fx.db, "", "", 50, 0)
	if err != nil || total != 2 || remaining[0].ID != "admin-newer" || remaining[1].ID != "workspace-success" {
		t.Fatalf("unexpected remaining logs=%+v total=%d err=%v", remaining, total, err)
	}
	if err := DeleteAdminAuditLog(ctx, fx.db, "workspace-success"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAdminAuditLog(ctx, fx.db, "admin-newer"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"missing", ""} {
		if err := DeleteAdminAuditLog(ctx, fx.db, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("id=%q err=%v; wanted not found", id, err)
		}
	}
}

func TestDeleteAdminAuditLogsRollsBackBothSources(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	exec(t, fx.db, `DELETE FROM workspace_audit_logs`)
	exec(t, fx.db, `INSERT INTO workspace_audit_logs(id,workspace_id,actor_user_id,action,target_type,target_id,created_at) VALUES('workspace-keep',?,'admin','policy.updated','workspace',?,1)`, fx.workspaceID, fx.workspaceID)
	exec(t, fx.db, `INSERT INTO admin_audit_logs(id,actor_user_id,event_type,action,created_at) VALUES('admin-keep','admin','users','admin.users.delete',1)`)
	exec(t, fx.db, `CREATE TRIGGER prevent_audit_delete BEFORE DELETE ON admin_audit_logs BEGIN SELECT RAISE(ABORT,'test delete failure'); END`)
	if deleted, err := DeleteFilteredAdminAuditLogs(ctx, fx.db, AdminAuditFilter{}); err == nil || deleted != 0 {
		t.Fatalf("failed deletion returned %d, %v", deleted, err)
	}
	if _, total, err := ListAdminAuditLogs(ctx, fx.db, "", "", 50, 0); err != nil || total != 2 {
		t.Fatalf("partial deletion committed: total=%d err=%v", total, err)
	}
}

func TestListAdminAuditLogsCombinesWorkspaceAndAdministratorEventsByType(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := context.Background()
	exec(t, fx.db, `DELETE FROM workspace_audit_logs`)
	exec(t, fx.db, `DELETE FROM admin_audit_logs`)
	exec(t, fx.db, `INSERT INTO workspace_audit_logs(id,workspace_id,actor_user_id,action,target_type,target_id,metadata,created_at)
		VALUES('ws-audit-1',?,'admin','member.role_updated','member','member-1','{}',100)`, fx.workspaceID)
	exec(t, fx.db, `INSERT INTO admin_audit_logs(id,actor_user_id,event_type,action,target_type,target_id,metadata,created_at)
		VALUES('admin-audit-1','admin','users','admin.users.role','user','user-1','{"method":"PATCH"}',200)`)

	rows, total, err := ListAdminAuditLogs(ctx, fx.db, "", "", 10, 0)
	if err != nil {
		t.Fatalf("list combined audit events: %v", err)
	}
	if total != 2 || len(rows) != 2 || rows[0].ID != "admin-audit-1" || rows[0].Type != "users" || rows[1].Type != "workspace" {
		t.Fatalf("combined events = total %d, rows %+v", total, rows)
	}
	if rows[0].WorkspaceID != "" || rows[0].ActorUserID != "admin" {
		t.Fatalf("administrator event fields = %+v", rows[0])
	}

	rows, total, err = ListAdminAuditLogs(ctx, fx.db, "", "users", 10, 0)
	if err != nil {
		t.Fatalf("filter administrator events: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != "admin-audit-1" {
		t.Fatalf("users filter = total %d, rows %+v", total, rows)
	}

	rows, total, err = ListAdminAuditLogs(ctx, fx.db, "member.role_updated", "workspace", 10, 0)
	if err != nil {
		t.Fatalf("search workspace events: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != "ws-audit-1" {
		t.Fatalf("workspace search = total %d, rows %+v", total, rows)
	}
}
