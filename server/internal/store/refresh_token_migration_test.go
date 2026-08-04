package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMigrateLegacyRefreshTokensAddsSessionFamilyBeforeIndex(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-refresh-tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE refresh_tokens (
		jti        TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		revoked    INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO refresh_tokens(jti, user_id, expires_at) VALUES('legacy-jti', 'legacy-user', 4102444800)`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate legacy refresh_tokens table: %v", err)
	}
	// The migration must remain idempotent after the additive column and index
	// have both been installed.
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate upgraded refresh_tokens table again: %v", err)
	}

	var sessionID string
	if err := db.QueryRow(`SELECT session_id FROM refresh_tokens WHERE jti='legacy-jti'`).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if sessionID != "legacy-jti" {
		t.Fatalf("session_id=%q, want legacy JTI", sessionID)
	}

	rows, err := db.Query(`PRAGMA index_info('idx_refresh_tokens_user_session')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var sequence, columnID int
		var name string
		if err := rows.Scan(&sequence, &columnID, &name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"user_id", "session_id"}; !reflect.DeepEqual(columns, want) {
		t.Fatalf("idx_refresh_tokens_user_session columns=%v, want %v", columns, want)
	}
}

func TestEmbeddedSchemasDeferRefreshTokenSessionIndex(t *testing.T) {
	for name, schema := range map[string]string{
		"sqlite":   schemaSQL,
		"postgres": schemaPGSQL,
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(strings.ToLower(schema), "create index if not exists idx_refresh_tokens_user_session") {
				t.Fatal("session-family index must run after the additive refresh_tokens migrations in Migrate")
			}
		})
	}
}
