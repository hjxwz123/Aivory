// Backup support: a logical, engine-agnostic dump/restore of every table.
//
// Why logical (row JSON) rather than a native dump (sqlite .dump / pg_dump):
// the store runs on either SQLite or PostgreSQL, and a native dump from one
// engine does NOT load into the other. Exporting each row as engine-neutral
// JSON lets an admin migrate a deployment in either direction (and between
// machines) entirely through the API, no DB shell required.
//
// The format is one JSONL file per table inside a zip (assembled by the API
// layer). This file owns the per-row read/write and the FK-safe table order.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// BackupVersion is the archive format version embedded in the manifest. Bump
// it only on a breaking change to the on-disk shape; the importer refuses
// archives newer than the version it understands.
const BackupVersion = 1

// backupTableOrder lists every table in FK-safe INSERT order (parents before
// children). Restore inserts in this order and wipes in reverse, so foreign
// keys hold without disabling constraints on Postgres. The lone self-reference
// (messages.parent_id) is satisfied by exporting messages in creation order —
// a reply is always created after the message it answers.
var backupTableOrder = []string{
	"settings", "users", "workspaces", "workspace_members", "user_groups", "channels", "skills", "oauth_providers",
	"models", "model_group_quotas", "model_tags", "image_styles",
	"redeem_codes", "redeem_redemptions",
	"model_skills", "knowledge_bases", "projects", "conversations", "messages",
	"conversation_shares", "files", "documents", "chunks", "memories",
	"usage_logs", "artifacts", "refresh_tokens", "oauth_identities",
}

// configTableOrder is the non-user, non-conversation admin configuration slice.
// Config imports UPSERT these tables instead of wiping the DB, so existing
// users, conversations, KBs, files, sessions, usage logs, and workspaces remain
// intact. The order still follows FK dependencies: groups/channels/skills before
// models, models before model join tables, groups before redeem codes.
var configTableOrder = []string{
	"settings",
	"user_groups",
	"channels",
	"skills",
	"oauth_providers",
	"model_tags",
	"image_styles",
	"models",
	"model_group_quotas",
	"model_skills",
	"redeem_codes",
}

var backupTableSet = func() map[string]bool {
	m := make(map[string]bool, len(backupTableOrder))
	for _, t := range backupTableOrder {
		m[t] = true
	}
	return m
}()

// BackupTableOrder returns a copy of the FK-safe insertion order.
func BackupTableOrder() []string {
	out := make([]string, len(backupTableOrder))
	copy(out, backupTableOrder)
	return out
}

// ConfigTableOrder returns the admin-configuration tables exported by the
// config archive. These are intentionally a subset of BackupTableOrder.
func ConfigTableOrder() []string {
	out := make([]string, len(configTableOrder))
	copy(out, configTableOrder)
	return out
}

// IsPostgres reports the active SQL dialect (set once by Open). The backup flow
// uses it to label the manifest and to reset serial sequences after a restore.
func IsPostgres() bool { return usePostgres }

// RowQuerier is the read surface export needs — satisfied by *sql.DB, *sql.Tx,
// and *sql.Conn.
type RowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// RowExecer is the read+write surface restore needs — satisfied by *sql.Tx,
// *sql.Conn, and *sql.DB.
type RowExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// binCell is the JSON wrapper for binary column values (BLOB on SQLite / BYTEA
// on Postgres). Text columns are emitted as plain JSON strings; only true binary
// is base64-wrapped, so the importer can tell them apart from text that merely
// scanned as []byte.
type binCell struct {
	B64 string `json:"__b64__"`
}

// dbTypeIsBinary classifies a column as binary from its driver type name.
// go-sqlite3 reports the declared type ("BLOB"); pgx reports "BYTEA".
func dbTypeIsBinary(name string) bool {
	n := strings.ToUpper(name)
	return strings.Contains(n, "BLOB") || strings.Contains(n, "BYTEA")
}

// ExportTable streams every row of one table to w as JSONL (one JSON object per
// line). Returns the row count. The table name is validated against the known
// set, so it is safe to interpolate into the query.
func ExportTable(ctx context.Context, q RowQuerier, table string, w io.Writer) (int64, error) {
	if !backupTableSet[table] {
		return 0, fmt.Errorf("backup: unknown table %q", table)
	}
	order := ""
	if table == "messages" {
		order = " ORDER BY created_at, id"
	}
	rows, err := q.QueryContext(ctx, "SELECT * FROM "+table+order) //nolint:gosec // table is whitelisted above
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return 0, err
	}
	isBin := make([]bool, len(cols))
	for i := range cols {
		isBin[i] = dbTypeIsBinary(colTypes[i].DatabaseTypeName())
	}

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	enc := json.NewEncoder(w)
	var n int64
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		obj := make(map[string]any, len(cols))
		for i, c := range cols {
			if skipLogicalBackupColumn(table, c) {
				continue
			}
			switch v := vals[i].(type) {
			case nil:
				obj[c] = nil
			case []byte:
				if isBin[i] {
					obj[c] = binCell{B64: base64.StdEncoding.EncodeToString(v)}
				} else {
					obj[c] = string(v) // text that scanned as bytes
				}
			default:
				obj[c] = v // int64, float64, string, bool
			}
		}
		if err := enc.Encode(obj); err != nil { // Encode appends '\n'
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// tableColumns returns the live table's column order and which columns are
// binary, by inspecting an empty result set. Used by restore so it can insert
// only columns that exist in the current schema (resilient to version skew) and
// decode binary cells correctly.
func tableColumns(ctx context.Context, q RowQuerier, table string) (order []string, isBin map[string]bool, err error) {
	rows, err := q.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0") //nolint:gosec // whitelisted
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, err
	}
	isBin = make(map[string]bool, len(cols))
	for i, c := range cols {
		isBin[c] = dbTypeIsBinary(cts[i].DatabaseTypeName())
	}
	return cols, isBin, nil
}

// RestoreTable inserts rows read from r (JSONL produced by ExportTable) into the
// table. Columns absent from the live schema are dropped (forward/backward
// version tolerance); columns present in the schema but missing from a row keep
// their DB default. Returns the inserted row count.
func RestoreTable(ctx context.Context, ex RowExecer, table string, r io.Reader) (int64, error) {
	if !backupTableSet[table] {
		return 0, fmt.Errorf("backup: unknown table %q", table)
	}
	liveCols, isBin, err := tableColumns(ctx, ex, table)
	if err != nil {
		return 0, err
	}
	liveSet := make(map[string]bool, len(liveCols))
	for _, c := range liveCols {
		liveSet[c] = true
	}

	dec := json.NewDecoder(r)
	dec.UseNumber()
	var n int64
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			return n, fmt.Errorf("decode %s row: %w", table, err)
		}
		cols := make([]string, 0, len(liveCols))
		args := make([]any, 0, len(liveCols))
		for _, c := range liveCols { // stable, schema-defined order
			if skipLogicalBackupColumn(table, c) {
				continue
			}
			rm, ok := raw[c]
			if !ok {
				continue
			}
			val, err := decodeBackupValue(rm, isBin[c])
			if err != nil {
				return n, fmt.Errorf("decode %s.%s: %w", table, c, err)
			}
			cols = append(cols, c)
			args = append(args, val)
		}
		if len(cols) == 0 {
			continue
		}
		q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders(len(cols)) + ")"
		if _, err := ex.ExecContext(ctx, q, args...); err != nil { //nolint:gosec // table+cols are schema-derived
			return n, fmt.Errorf("insert into %s: %w", table, err)
		}
		n++
	}
	return n, nil
}

var tablePrimaryKeys = map[string][]string{
	"settings":            {"key"},
	"users":               {"id"},
	"workspaces":          {"id"},
	"workspace_members":   {"workspace_id", "user_id"},
	"user_groups":         {"id"},
	"channels":            {"id"},
	"skills":              {"id"},
	"oauth_providers":     {"id"},
	"models":              {"id"},
	"model_group_quotas":  {"model_id", "group_id"},
	"model_tags":          {"id"},
	"image_styles":        {"id"},
	"redeem_codes":        {"id"},
	"redeem_redemptions":  {"id"},
	"model_skills":        {"model_id", "skill_id"},
	"knowledge_bases":     {"id"},
	"projects":            {"id"},
	"conversations":       {"id"},
	"messages":            {"id"},
	"conversation_shares": {"id"},
	"files":               {"id"},
	"documents":           {"id"},
	"chunks":              {"id"},
	"memories":            {"id"},
	"usage_logs":          {"id"},
	"artifacts":           {"id"},
	"refresh_tokens":      {"jti"},
	"oauth_identities":    {"provider_id", "subject"},
}

// UpsertTable merges rows from a JSONL table dump by primary key. Config import
// uses this instead of RestoreTable so it can bring channels/models/settings
// across without wiping existing users, conversations, sessions, files, or logs.
func UpsertTable(ctx context.Context, ex RowExecer, table string, r io.Reader) (int64, error) {
	if !backupTableSet[table] {
		return 0, fmt.Errorf("backup: unknown table %q", table)
	}
	pk := tablePrimaryKeys[table]
	if len(pk) == 0 {
		return 0, fmt.Errorf("backup: primary key for table %q is unknown", table)
	}
	liveCols, isBin, err := tableColumns(ctx, ex, table)
	if err != nil {
		return 0, err
	}
	pkSet := make(map[string]bool, len(pk))
	for _, c := range pk {
		pkSet[c] = true
	}

	dec := json.NewDecoder(r)
	dec.UseNumber()
	var n int64
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			return n, fmt.Errorf("decode %s row: %w", table, err)
		}
		cols := make([]string, 0, len(liveCols))
		args := make([]any, 0, len(liveCols))
		colSet := make(map[string]bool, len(liveCols))
		for _, c := range liveCols { // stable, schema-defined order
			if skipLogicalBackupColumn(table, c) {
				continue
			}
			rm, ok := raw[c]
			if !ok {
				continue
			}
			val, err := decodeBackupValue(rm, isBin[c])
			if err != nil {
				return n, fmt.Errorf("decode %s.%s: %w", table, c, err)
			}
			cols = append(cols, c)
			args = append(args, val)
			colSet[c] = true
		}
		if len(cols) == 0 {
			continue
		}
		for _, c := range pk {
			if !colSet[c] {
				return n, fmt.Errorf("upsert %s: row missing primary key column %s", table, c)
			}
		}
		q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders(len(cols)) + ") " + upsertClause(pk, cols, pkSet)
		if _, err := ex.ExecContext(ctx, q, args...); err != nil { //nolint:gosec // table+cols are schema-derived
			return n, fmt.Errorf("upsert into %s: %w", table, err)
		}
		n++
	}
	return n, nil
}

func upsertClause(pk, cols []string, pkSet map[string]bool) string {
	updates := make([]string, 0, len(cols))
	for _, c := range cols {
		if pkSet[c] {
			continue
		}
		updates = append(updates, c+"=excluded."+c)
	}
	target := strings.Join(pk, ", ")
	if len(updates) == 0 {
		return "ON CONFLICT (" + target + ") DO NOTHING"
	}
	return "ON CONFLICT (" + target + ") DO UPDATE SET " + strings.Join(updates, ", ")
}

func skipLogicalBackupColumn(table, column string) bool {
	return table == "chunks" && column == "embedding"
}

// decodeBackupValue turns one JSON cell back into a driver-ready Go value.
// Numbers stay integral when they have no fraction/exponent (avoids float
// precision loss on BIGINT token sums); binary cells base64-decode to []byte.
func decodeBackupValue(rm json.RawMessage, isBinary bool) (any, error) {
	t := bytes.TrimSpace(rm)
	if len(t) == 0 || string(t) == "null" {
		return nil, nil
	}
	if isBinary {
		var b binCell
		if err := json.Unmarshal(rm, &b); err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(b.B64)
	}
	switch t[0] {
	case '"':
		var s string
		if err := json.Unmarshal(rm, &s); err != nil {
			return nil, err
		}
		return s, nil
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(rm, &b); err != nil {
			return nil, err
		}
		return b, nil
	default:
		num := json.Number(string(t))
		s := num.String()
		if strings.ContainsAny(s, ".eE") {
			f, err := num.Float64()
			return f, err
		}
		i, err := num.Int64()
		if err != nil {
			f, ferr := num.Float64()
			return f, ferr
		}
		return i, nil
	}
}

// WipeAll deletes every row from every table in reverse FK order, so child
// rows go before the parents they reference (keeps Postgres FK checks happy;
// SQLite restore additionally runs with foreign_keys=OFF).
func WipeAll(ctx context.Context, ex RowExecer) error {
	for i := len(backupTableOrder) - 1; i >= 0; i-- {
		t := backupTableOrder[i]
		if _, err := ex.ExecContext(ctx, "DELETE FROM "+t); err != nil { //nolint:gosec // whitelisted
			return fmt.Errorf("wipe %s: %w", t, err)
		}
	}
	return nil
}

// ResetSerialSequences re-aligns Postgres serial sequences with the restored
// data so the next auto-id doesn't collide with an inserted one. usage_logs.id
// is the only BIGSERIAL column. No-op on SQLite (AUTOINCREMENT self-heals).
func ResetSerialSequences(ctx context.Context, ex RowExecer) error {
	if !usePostgres {
		return nil
	}
	_, err := ex.ExecContext(ctx,
		`SELECT setval(pg_get_serial_sequence('usage_logs','id'), GREATEST((SELECT COALESCE(MAX(id),0) FROM usage_logs), 1))`)
	return err
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}
