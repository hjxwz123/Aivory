package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestBackupRoundTrip exercises the export → wipe → restore cycle on SQLite,
// covering the things most likely to break: a self-referential FK
// (messages.parent_id), workspace FK ordering, and int/float fidelity (token
// counts vs cost).
func TestBackupRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rt.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	seed := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO settings(key,value) VALUES('default_model_id','"m_x"')`, nil},
		{`INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','a@b.c','h','A','user')`, nil},
		{`INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws1','Team','u1','invite1')`, nil},
		{`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','u1','owner')`, nil},
		{`INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('c1','u1','T','ws1')`, nil},
		{`INSERT INTO messages(id,conversation_id,parent_id,role,input_tokens,cost) VALUES('m1','c1',NULL,'user',1234567,0)`, nil},
		{`INSERT INTO messages(id,conversation_id,parent_id,role,input_tokens,cost) VALUES('m2','c1','m1','assistant',42,0.0125)`, nil},
		{`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d1','c1','f.txt','text/plain',10,'ready')`, nil},
		{`INSERT INTO chunks(id,document_id,seq,content,embedding_model) VALUES('ch1','d1',0,'hello','e')`, nil},
	}
	for _, s := range seed {
		if _, err := db.ExecContext(ctx, s.q, s.args...); err != nil {
			t.Fatalf("seed %q: %v", s.q, err)
		}
	}

	// Export every table to an in-memory buffer.
	dumps := make(map[string]*bytes.Buffer)
	for _, tbl := range BackupTableOrder() {
		var buf bytes.Buffer
		if _, err := ExportTable(ctx, db, tbl, &buf); err != nil {
			t.Fatalf("export %s: %v", tbl, err)
		}
		dumps[tbl] = &buf
	}

	// Wipe + restore with FK enforcement off (mirrors the import handler).
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := WipeAll(ctx, tx); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	for _, tbl := range BackupTableOrder() {
		if _, err := RestoreTable(ctx, tx, tbl, bytes.NewReader(dumps[tbl].Bytes())); err != nil {
			t.Fatalf("restore %s: %v", tbl, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("fk on: %v", err)
	}

	// FK integrity: the self-referential parent_id must still resolve.
	var fkBad int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&fkBad); err != nil {
		t.Fatalf("fk check: %v", err)
	}
	if fkBad != 0 {
		t.Fatalf("foreign_key_check reported %d violations after restore", fkBad)
	}

	// Int vs float fidelity.
	var tokens int64
	var cost float64
	if err := db.QueryRowContext(ctx, "SELECT input_tokens, cost FROM messages WHERE id='m2'").Scan(&tokens, &cost); err != nil {
		t.Fatalf("read msg: %v", err)
	}
	if tokens != 42 || cost != 0.0125 {
		t.Fatalf("numeric mismatch: tokens=%d cost=%v", tokens, cost)
	}

	// Self-reference preserved.
	var parent sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT parent_id FROM messages WHERE id='m2'").Scan(&parent); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if !parent.Valid || parent.String != "m1" {
		t.Fatalf("parent_id not preserved: %+v", parent)
	}

	// Row counts.
	for tbl, want := range map[string]int{"users": 1, "workspaces": 1, "workspace_members": 1, "conversations": 1, "messages": 2, "chunks": 1, "documents": 1} {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if n != want {
			t.Fatalf("count %s = %d, want %d", tbl, n, want)
		}
	}
}

func TestMigrateDropsLegacyChunkEmbedding(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE chunks ADD COLUMN embedding BLOB`); err != nil {
		t.Fatalf("add legacy embedding column: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','a@b.c','h','A','user')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`,
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d1','c1','f.txt','text/plain',10,'ready')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed legacy db %q: %v", q, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO chunks(id,document_id,seq,content,embedding,embedding_model) VALUES('ch1','d1',0,'hello',?,'emb:test')`, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("seed legacy chunk: %v", err)
	}
	var dump bytes.Buffer
	if _, err := ExportTable(ctx, db, "chunks", &dump); err != nil {
		t.Fatalf("export legacy chunks: %v", err)
	}
	raw := dump.Bytes()
	var row map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&row); err != nil {
		t.Fatalf("decode legacy chunks export: %v", err)
	}
	if _, ok := row["embedding"]; ok {
		t.Fatalf("chunks export unexpectedly contains embedding column: %s", string(raw))
	}
	if _, ok := row["embedding_model"]; !ok {
		t.Fatalf("chunks export lost embedding_model metadata: %s", string(raw))
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT embedding FROM chunks WHERE 1=0`); !isMissingColumnErr(err) {
		t.Fatalf("legacy chunks.embedding still exists or failed unexpectedly: %v", err)
	}
	var content string
	if err := db.QueryRowContext(ctx, `SELECT content FROM chunks WHERE id='ch1'`).Scan(&content); err != nil {
		t.Fatalf("legacy chunk was not preserved: %v", err)
	}
	if content != "hello" {
		t.Fatalf("legacy chunk content changed: %q", content)
	}
}

func TestBackupTableOrderCoversSchemaTables(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	covered := map[string]bool{}
	for _, tbl := range BackupTableOrder() {
		covered[tbl] = true
	}

	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tbl string
		if err := rows.Scan(&tbl); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		if !covered[tbl] {
			t.Fatalf("backup table order does not include schema table %q", tbl)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables rows: %v", err)
	}
}

func TestConfigTableOrderExcludesUserDataTables(t *testing.T) {
	configured := map[string]bool{}
	for _, tbl := range ConfigTableOrder() {
		configured[tbl] = true
		if !backupTableSet[tbl] {
			t.Fatalf("config table %q is not a known backup table", tbl)
		}
	}
	for _, tbl := range []string{
		"users",
		"workspaces",
		"workspace_members",
		"knowledge_bases",
		"projects",
		"conversations",
		"messages",
		"conversation_shares",
		"files",
		"documents",
		"chunks",
		"memories",
		"usage_logs",
		"artifacts",
		"refresh_tokens",
		"oauth_identities",
		"redeem_redemptions",
	} {
		if configured[tbl] {
			t.Fatalf("config export must not include user/business table %q", tbl)
		}
	}
	for _, tbl := range []string{
		"settings",
		"user_groups",
		"channels",
		"models",
		"model_group_quotas",
		"model_tags",
		"skills",
		"model_skills",
		"oauth_providers",
		"image_styles",
		"redeem_codes",
	} {
		if !configured[tbl] {
			t.Fatalf("config export should include admin config table %q", tbl)
		}
	}
}
