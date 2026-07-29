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
		{`INSERT INTO credit_packages(id,name,credits,price_amount_minor) VALUES('cp1','Credits',10,100)`, nil},
		{`INSERT INTO payment_channels(id,name,provider,config) VALUES('paych1','EPay','epay','{}')`, nil},
		{`INSERT INTO payment_methods(id,channel_id,name,type,config) VALUES('paym1','paych1','Alipay','epay','{"type":"alipay"}')`, nil},
		{`INSERT INTO payment_orders(id,user_id,user_email,provider,channel_id,channel_name,method_id,method_name,method_type,product_type,product_id,product_name,amount_minor,currency) VALUES('po1','u1','a@b.c','epay','paych1','EPay','paym1','Alipay','epay','credit_package','cp1','Credits',100,'USD')`, nil},
		{`INSERT INTO payment_order_attempts(merchant_order_id,order_id,provider,channel_id,provider_order_id,status,paid_at) VALUES('pa1','po1','epay','paych1','trade1','paid',123)`, nil},
		{`INSERT INTO payment_events(id,provider,channel_id,event_id,order_id,event_type,processed_at) VALUES('pe1','epay','paych1','trade1:success','po1','payment_notification',123)`, nil},
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
	for tbl, want := range map[string]int{
		"users": 1, "payment_orders": 1, "payment_order_attempts": 1, "payment_events": 1,
		"workspaces": 1, "workspace_members": 1, "conversations": 1, "messages": 2, "chunks": 1, "documents": 1,
	} {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if n != want {
			t.Fatalf("count %s = %d, want %d", tbl, n, want)
		}
	}
}

func TestPaymentOrderBackupRestoresProviderSnapshotsAcrossVersions(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "payment-snapshots.db"))
	if err != nil {
		t.Fatalf("open payment snapshot database: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate payment snapshot database: %v", err)
	}

	baseRow := func(id string) map[string]any {
		return map[string]any{
			"id": id, "user_email": "backup@example.test", "provider": "epay",
			"channel_id": "paych_backup", "channel_name": "Backup EPay",
			"method_id": "paym_backup", "method_name": "Alipay", "method_type": "epay",
			"product_type": PaymentProductCreditPackage, "product_id": "cp_backup",
			"product_name": "Backup package", "amount_minor": 4000, "currency": "USD",
		}
	}
	restoreRow := func(row map[string]any) {
		t.Helper()
		encoded, marshalErr := json.Marshal(row)
		if marshalErr != nil {
			t.Fatalf("marshal payment-order backup row: %v", marshalErr)
		}
		count, restoreErr := RestoreTable(ctx, db, "payment_orders", bytes.NewReader(encoded))
		if restoreErr != nil {
			t.Fatalf("restore payment-order backup row: %v", restoreErr)
		}
		if count != 1 {
			t.Fatalf("restored payment-order rows = %d, want 1", count)
		}
	}

	legacy := baseRow("po_backup_legacy")
	restoreRow(legacy)
	legacyOrder, err := GetPaymentOrder(ctx, db, "po_backup_legacy")
	if err != nil {
		t.Fatalf("get restored legacy payment order: %v", err)
	}
	if legacyOrder.ProviderAmountMinor != 4000 || legacyOrder.ProviderCurrency != "USD" || legacyOrder.ConversionRate != "" {
		t.Fatalf("restored legacy provider snapshots = %+v", legacyOrder)
	}
	if legacyOrder.CheckoutURL != "" {
		t.Fatalf("restored legacy checkout URL = %q, want empty", legacyOrder.CheckoutURL)
	}

	converted := baseRow("po_backup_converted")
	converted["provider_amount_minor"] = 28000
	converted["provider_currency"] = "CNY"
	converted["conversion_rate"] = "7"
	converted["checkout_url"] = "https://checkout.example.test/session/original"
	restoreRow(converted)

	var dump bytes.Buffer
	if _, err := ExportTable(ctx, db, "payment_orders", &dump); err != nil {
		t.Fatalf("export payment-order snapshots: %v", err)
	}
	destination, err := Open(filepath.Join(t.TempDir(), "payment-snapshots-restored.db"))
	if err != nil {
		t.Fatalf("open restored payment snapshot database: %v", err)
	}
	defer destination.Close()
	if err := Migrate(destination); err != nil {
		t.Fatalf("migrate restored payment snapshot database: %v", err)
	}
	if count, err := RestoreTable(ctx, destination, "payment_orders", bytes.NewReader(dump.Bytes())); err != nil {
		t.Fatalf("round-trip payment-order snapshots: %v", err)
	} else if count != 2 {
		t.Fatalf("round-trip payment-order rows = %d, want 2", count)
	}
	convertedOrder, err := GetPaymentOrder(ctx, destination, "po_backup_converted")
	if err != nil {
		t.Fatalf("get round-tripped converted payment order: %v", err)
	}
	if convertedOrder.AmountMinor != 4000 || convertedOrder.Currency != "USD" ||
		convertedOrder.ProviderAmountMinor != 28000 || convertedOrder.ProviderCurrency != "CNY" ||
		convertedOrder.ConversionRate != "7" || convertedOrder.CheckoutURL != "https://checkout.example.test/session/original" {
		t.Fatalf("round-tripped provider snapshots = %+v", convertedOrder)
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
		"user_skills",
		"user_prompts",
	} {
		if configured[tbl] {
			t.Fatalf("config export must not include user/business table %q", tbl)
		}
	}
	for _, tbl := range []string{
		"settings",
		"user_groups",
		"credit_packages",
		"channels",
		"models",
		"model_group_quotas",
		"model_tags",
		"skills",
		"prompts",
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
