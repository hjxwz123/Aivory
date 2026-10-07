package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

type AuditSnapshot struct {
	Name   string
	Fields map[string]any
	hidden map[string][32]byte
}

type auditSnapshotSpec struct{ table, key, name, visible, hidden string }

// Only these columns can enter the audit store. Credential/content columns
// are compared using transient hashes and produce a redacted change marker.
var auditSnapshotSpecs = map[string]auditSnapshotSpec{
	"users":            {"users", "id", "name", "name,email,role,status,group_id,totp_enabled,password_set,credits_permanent_micros,sort_order", "password_hash"},
	"channels":         {"channels", "id", "name", "name,type,api_format,enabled,sort_order", "api_key,base_url,headers"},
	"models":           {"models", "id", "label", "label,channel_id,kind,request_id,enabled,sort_order,fallback_channel_id,tool_mode,vision,stream,research_enabled,fast,moderation_enabled,moderation_mode,price_input,price_output,price_cache_read,price_cache_write,price_per_image,currency,dim,compaction_token_threshold,image_timeout_sec", "system_prompt,extra_params,official_tools,param_controls,builtin_tools,mcp_server_ids,tags"},
	"user-groups":      {"user_groups", "id", "name", "name,is_default,sort_order,max_projects,max_kbs,max_storage_mb,credit_allowance_micros,credit_period_seconds,is_purchasable,monthly_price_amount_minor,yearly_price_amount_minor", "permissions"},
	"model-tags":       {"model_tags", "id", "name", "name,sort_order", ""},
	"mcp":              {"mcp_servers", "id", "name", "name,enabled,protocol_version,last_synced_at", "headers,url,discovered_tools"},
	"oauth-providers":  {"oauth_providers", "id", "name", "name,kind,enabled,sort_order", "client_secret,client_id,issuer_url,jwks_url,auth_url,token_url,userinfo_url,scopes"},
	"skills":           {"skills", "id", "name", "name,enabled,sort_order", "instructions,assets"},
	"prompts":          {"prompts", "id", "name", "name,enabled,sort_order", "content"},
	"credit-packages":  {"credit_packages", "id", "name", "name,credits,price_amount_minor,enabled,sort_order", ""},
	"payment-channels": {"payment_channels", "id", "name", "name,provider,environment,enabled,sort_order", "config"},
	"payment-methods":  {"payment_methods", "id", "name", "name,channel_id,type,enabled,sort_order", "config"},
	"payment-orders":   {"payment_orders", "id", "product_name", "product_name,user_id,amount_minor,paid_amount_minor,currency,status", ""},
	"workspaces":       {"workspaces", "id", "name", "name,owner_id", ""},
	"conversations":    {"conversations", "id", "", "user_id,workspace_id", ""},
}

func LoadAuditSnapshot(ctx context.Context, db *sql.DB, resource, id string) (*AuditSnapshot, error) {
	if resource == "model-quotas" {
		return loadAuditRowsSnapshot(ctx, db, `SELECT group_id,period_seconds,limit_type,limit_value FROM model_group_quotas WHERE model_id=? ORDER BY group_id`, []any{id}, "quotas")
	}
	if strings.HasSuffix(resource, "-order") {
		spec, ok := auditSnapshotSpecs[strings.TrimSuffix(resource, "-order")]
		if !ok || !strings.Contains(","+spec.visible+",", ",sort_order,") {
			return nil, nil
		}
		return loadAuditRowsSnapshot(ctx, db, `SELECT id,sort_order FROM `+spec.table+` ORDER BY sort_order,id`, nil, "order")
	}
	if resource == "settings" {
		return loadSettingsAuditSnapshot(ctx, db)
	}
	spec, ok := auditSnapshotSpecs[resource]
	if !ok || id == "" {
		return nil, nil
	}
	columns := strings.Split(spec.visible, ",")
	visibleCount := len(columns)
	if spec.hidden != "" {
		columns = append(columns, strings.Split(spec.hidden, ",")...)
	}
	dest, values := make([]any, len(columns)), make([]any, len(columns))
	for i := range dest {
		dest[i] = &values[i]
	}
	err := db.QueryRowContext(ctx, `SELECT `+strings.Join(columns, ",")+` FROM `+spec.table+` WHERE `+spec.key+`=?`, id).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot := &AuditSnapshot{Fields: map[string]any{}, hidden: map[string][32]byte{}}
	for i, key := range columns {
		value := values[i]
		if b, ok := value.([]byte); ok {
			value = string(b)
		}
		if i < visibleCount {
			snapshot.Fields[key] = value
			if key == spec.name {
				snapshot.Name, _ = value.(string)
			}
		} else {
			raw, _ := json.Marshal(value)
			snapshot.hidden[key] = sha256.Sum256(raw)
		}
	}
	return snapshot, nil
}

func loadAuditRowsSnapshot(ctx context.Context, db *sql.DB, query string, args []any, key string) (*AuditSnapshot, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for rows.Next() {
		values, dest := make([]any, len(cols)), make([]any, len(cols))
		for i := range dest {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		item := map[string]any{}
		for i, col := range cols {
			if b, ok := values[i].([]byte); ok {
				values[i] = string(b)
			}
			item[col] = values[i]
		}
		items = append(items, item)
	}
	return &AuditSnapshot{Fields: map[string]any{key: items}}, rows.Err()
}

func loadSettingsAuditSnapshot(ctx context.Context, db *sql.DB) (*AuditSnapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT key,value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snapshot := &AuditSnapshot{Name: "settings", Fields: map[string]any{}, hidden: map[string][32]byte{}}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var value any
		_ = json.Unmarshal([]byte(raw), &value)
		// Free-form strings/objects may contain provider secrets or user text.
		// Safe booleans/numbers are restricted to recognized policy namespaces.
		safeKey := strings.HasPrefix(key, "allow_") || strings.HasPrefix(key, "max_") || strings.HasSuffix(key, "_enabled") || strings.HasSuffix(key, "_required") || strings.HasSuffix(key, "_timeout_sec") || strings.HasSuffix(key, "_timeout_ms")
		switch value.(type) {
		case bool, float64:
			if safeKey {
				snapshot.Fields[key] = value
				continue
			}
		}
		snapshot.hidden[key] = sha256.Sum256([]byte(raw))
	}
	return snapshot, rows.Err()
}

func AuditSnapshotChanges(before, after *AuditSnapshot) map[string]AuditChange {
	changes := map[string]AuditChange{}
	if before == nil {
		before = &AuditSnapshot{}
	}
	if after == nil {
		after = &AuditSnapshot{}
	}
	keys := map[string]bool{}
	for key := range before.Fields {
		keys[key] = true
	}
	for key := range after.Fields {
		keys[key] = true
	}
	for key := range keys {
		b, bok := before.Fields[key]
		a, aok := after.Fields[key]
		if bok != aok || !reflect.DeepEqual(b, a) {
			changes[key] = AuditChange{Before: b, After: a}
		}
	}
	keys = map[string]bool{}
	for key := range before.hidden {
		keys[key] = true
	}
	for key := range after.hidden {
		keys[key] = true
	}
	for key := range keys {
		b, bok := before.hidden[key]
		a, aok := after.hidden[key]
		if bok != aok || b != a {
			changes[key] = AuditChange{Redacted: true}
		}
	}
	return changes
}
