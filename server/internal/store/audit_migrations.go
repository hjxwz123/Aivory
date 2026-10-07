package store

import (
	"database/sql"
	"fmt"
)

func migrateAuditColumns(db *sql.DB) error {
	columns := map[string]string{
		"actor_name": "TEXT NOT NULL DEFAULT ''", "actor_role": "TEXT NOT NULL DEFAULT ''",
		"target_name": "TEXT NOT NULL DEFAULT ''", "result": "TEXT NOT NULL DEFAULT 'success'",
		"severity": "TEXT NOT NULL DEFAULT 'info'", "source": "TEXT NOT NULL DEFAULT ''",
		"client_ip": "TEXT NOT NULL DEFAULT ''", "user_agent": "TEXT NOT NULL DEFAULT ''",
		"request_id": "TEXT NOT NULL DEFAULT ''", "occurred_at_ms": "BIGINT NOT NULL DEFAULT 0",
		"duration_ms": "BIGINT NOT NULL DEFAULT 0", "http_status": "INTEGER NOT NULL DEFAULT 0",
		"method": "TEXT NOT NULL DEFAULT ''", "route": "TEXT NOT NULL DEFAULT ''",
		"reason": "TEXT NOT NULL DEFAULT ''", "changes": "TEXT NOT NULL DEFAULT '{}'",
	}
	for _, table := range []string{"admin_audit_logs", "workspace_audit_logs"} {
		for name, definition := range columns {
			exists, err := columnExists(db, table, name)
			if err != nil {
				return err
			}
			if !exists {
				if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition); err != nil {
					return fmt.Errorf("migrate %s.%s: %w", table, name, err)
				}
			}
		}
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_admin_audit_request ON admin_audit_logs(request_id);
		CREATE INDEX IF NOT EXISTS idx_admin_audit_result ON admin_audit_logs(result,created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_admin_audit_actor ON admin_audit_logs(actor_user_id,created_at DESC)`)
	return err
}
