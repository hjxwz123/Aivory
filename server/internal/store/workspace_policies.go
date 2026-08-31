package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Workspace capability policy (§workspace RBAC phase 4). A missing row means
// the fully permissive default — the policy can only NARROW what the platform
// already offers, never widen it. Guests are unaffected (read-only) and every
// enforcement point below re-checks the policy at execution time, never only
// at catalog load.

type WorkspacePolicy struct {
	WorkspaceID string
	// Empty = every platform model/tool/MCP server. Tool/MCP entries use the
	// prefixed catalog ids ("builtin:python_execute", "hosted:…", "mcp:…").
	AllowedModelIDs     []string
	AllowedToolIDs      []string
	AllowedMCPServerIDs []string
	// AllowToolCalling is the merged workspace-wide tool capability. When false
	// every local, hosted, administrator MCP, and user MCP tool is unavailable.
	AllowToolCalling bool
	// AllowDrawing controls direct image-model/drawing turns. It is deliberately
	// independent from image-generation tools, which are covered by
	// AllowToolCalling.
	AllowDrawing bool
	AllowMCP     bool
	AllowSkills  bool
	AllowPrompts bool
	// Deprecated compatibility switches. They remain readable for old clients
	// and old policy rows, but are intentionally not consulted by the current
	// runtime. New callers must use AllowToolCalling and AllowDrawing.
	AllowSandbox         bool
	AllowImageGeneration bool
	AllowKnowledgeBases  bool
	AllowFileUpload      bool
	// 0 = unlimited member monthly credits.
	MemberMonthlyCreditLimit float64
	UpdatedBy                string
	UpdatedAt                int64
}

// DefaultWorkspacePolicy is the permissive default for policy-less workspaces.
func DefaultWorkspacePolicy(workspaceID string) WorkspacePolicy {
	return WorkspacePolicy{
		WorkspaceID:              workspaceID,
		AllowedModelIDs:          []string{},
		AllowedToolIDs:           []string{},
		AllowedMCPServerIDs:      []string{},
		AllowToolCalling:         true,
		AllowDrawing:             true,
		AllowMCP:                 true,
		AllowSkills:              true,
		AllowPrompts:             true,
		AllowSandbox:             true,
		AllowImageGeneration:     true,
		AllowKnowledgeBases:      true,
		AllowFileUpload:          true,
		MemberMonthlyCreditLimit: 0,
	}
}

// WorkspacePolicyPatch carries only the fields being changed. Nil pointers
// keep the stored value.
type WorkspacePolicyPatch struct {
	AllowedModelIDs          *[]string
	AllowedToolIDs           *[]string
	AllowedMCPServerIDs      *[]string
	AllowToolCalling         *bool
	AllowDrawing             *bool
	AllowMCP                 *bool
	AllowSkills              *bool
	AllowPrompts             *bool
	AllowSandbox             *bool
	AllowImageGeneration     *bool
	AllowKnowledgeBases      *bool
	AllowFileUpload          *bool
	MemberMonthlyCreditLimit *float64
}

func parseIDList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("workspace policy allowlist is empty")
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil || ids == nil {
		return nil, errors.New("workspace policy allowlist is invalid")
	}
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func marshalIDList(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func scanWorkspacePolicy(s scanner) (WorkspacePolicy, error) {
	var p WorkspacePolicy
	var models, tools, mcp string
	var toolCalling, drawing, allowMCP, allowSkills, allowPrompts int
	var sandbox, image, kbs, upload int
	if err := s.Scan(
		&p.WorkspaceID, &models, &tools, &mcp,
		&toolCalling, &drawing, &allowMCP, &allowSkills, &allowPrompts,
		&sandbox, &image, &kbs, &upload,
		&p.MemberMonthlyCreditLimit, &p.UpdatedBy, &p.UpdatedAt,
	); err != nil {
		return p, err
	}
	var err error
	if p.AllowedModelIDs, err = parseIDList(models); err != nil {
		return p, err
	}
	if p.AllowedToolIDs, err = parseIDList(tools); err != nil {
		return p, err
	}
	if p.AllowedMCPServerIDs, err = parseIDList(mcp); err != nil {
		return p, err
	}
	p.AllowToolCalling = toolCalling == 1
	p.AllowDrawing = drawing == 1
	p.AllowMCP = allowMCP == 1
	p.AllowSkills = allowSkills == 1
	p.AllowPrompts = allowPrompts == 1
	p.AllowSandbox = sandbox == 1
	p.AllowImageGeneration = image == 1
	p.AllowKnowledgeBases = kbs == 1
	p.AllowFileUpload = upload == 1
	return p, nil
}

// GetWorkspacePolicy returns the effective policy (permissive default when no
// row exists). Unknown workspaces also return the default — callers gate on
// membership separately.
func GetWorkspacePolicy(ctx context.Context, db *sql.DB, workspaceID string) (WorkspacePolicy, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return DefaultWorkspacePolicy(""), nil
	}
	p, err := scanWorkspacePolicy(db.QueryRowContext(ctx,
		`SELECT workspace_id, allowed_model_ids, allowed_tool_ids, allowed_mcp_server_ids,
		        allow_tool_calling, allow_drawing, allow_mcp, allow_skills, allow_prompts,
		        allow_sandbox, allow_image_generation, allow_knowledge_bases, allow_file_upload,
		        member_monthly_credit_limit, updated_by, updated_at
		   FROM workspace_policies WHERE workspace_id=?`, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultWorkspacePolicy(workspaceID), nil
	}
	if err != nil {
		return DefaultWorkspacePolicy(workspaceID), err
	}
	return p, nil
}

// UpdateWorkspacePolicy applies a partial patch. Actor must be a workspace
// admin (canonical owner or admin role); the guard is re-evaluated inside the
// membership-lock transaction.
func UpdateWorkspacePolicy(ctx context.Context, db *sql.DB, workspaceID, actorID string, patch WorkspacePolicyPatch) (*WorkspacePolicy, error) {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var workspaceOwnerID string
	var actorRole string
	if err := tx.QueryRowContext(ctx,
		`SELECT w.owner_id, COALESCE(am.role,'')
		   FROM workspaces w
		   LEFT JOIN workspace_members am ON am.workspace_id=w.id AND am.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR am.user_id=?)`,
		actorID, workspaceID, actorID, actorID,
	).Scan(&workspaceOwnerID, &actorRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if workspaceOwnerID != actorID && actorRole != "admin" && actorRole != "owner" {
		return nil, ErrForbidden
	}

	current, err := scanWorkspacePolicy(tx.QueryRowContext(ctx,
		`SELECT workspace_id, allowed_model_ids, allowed_tool_ids, allowed_mcp_server_ids,
		        allow_tool_calling, allow_drawing, allow_mcp, allow_skills, allow_prompts,
		        allow_sandbox, allow_image_generation, allow_knowledge_bases, allow_file_upload,
		        member_monthly_credit_limit, updated_by, updated_at
		   FROM workspace_policies WHERE workspace_id=?`, workspaceID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		current = DefaultWorkspacePolicy(workspaceID)
	}

	if patch.AllowedModelIDs != nil {
		current.AllowedModelIDs = *patch.AllowedModelIDs
	}
	if patch.AllowedToolIDs != nil {
		current.AllowedToolIDs = *patch.AllowedToolIDs
	}
	if patch.AllowedMCPServerIDs != nil {
		current.AllowedMCPServerIDs = *patch.AllowedMCPServerIDs
	}
	if patch.AllowToolCalling != nil {
		current.AllowToolCalling = *patch.AllowToolCalling
		// Keep legacy readers from observing a broader policy while an older
		// binary is still serving traffic. A new tool-calling shutdown may narrow
		// the retired fields, but turning the merged switch back on must never
		// erase an older, more restrictive setting. Crucially, this does not touch
		// AllowDrawing: direct drawing is an independent capability now.
		if !*patch.AllowToolCalling {
			current.AllowSandbox = false
			current.AllowImageGeneration = false
		}
	}
	if patch.AllowDrawing != nil {
		current.AllowDrawing = *patch.AllowDrawing
	}
	if patch.AllowMCP != nil {
		current.AllowMCP = *patch.AllowMCP
	}
	if patch.AllowSkills != nil {
		current.AllowSkills = *patch.AllowSkills
	}
	if patch.AllowPrompts != nil {
		current.AllowPrompts = *patch.AllowPrompts
	}
	if patch.AllowSandbox != nil {
		current.AllowSandbox = *patch.AllowSandbox
	}
	if patch.AllowImageGeneration != nil {
		current.AllowImageGeneration = *patch.AllowImageGeneration
	}
	if patch.AllowKnowledgeBases != nil {
		current.AllowKnowledgeBases = *patch.AllowKnowledgeBases
	}
	if patch.AllowFileUpload != nil {
		current.AllowFileUpload = *patch.AllowFileUpload
	}
	if patch.MemberMonthlyCreditLimit != nil {
		if *patch.MemberMonthlyCreditLimit < 0 {
			return nil, errors.New("invalid credit limit")
		}
		current.MemberMonthlyCreditLimit = *patch.MemberMonthlyCreditLimit
	}
	// Track which fields changed for the audit trail (field NAMES only —
	// never capability tokens or document content).
	changed := []string{}
	if patch.AllowedModelIDs != nil {
		changed = append(changed, "allowed_model_ids")
	}
	if patch.AllowedToolIDs != nil {
		changed = append(changed, "allowed_tool_ids")
	}
	if patch.AllowedMCPServerIDs != nil {
		changed = append(changed, "allowed_mcp_server_ids")
	}
	if patch.AllowToolCalling != nil {
		changed = append(changed, "allow_tool_calling")
	}
	if patch.AllowDrawing != nil {
		changed = append(changed, "allow_drawing")
	}
	if patch.AllowMCP != nil {
		changed = append(changed, "allow_mcp")
	}
	if patch.AllowSkills != nil {
		changed = append(changed, "allow_skills")
	}
	if patch.AllowPrompts != nil {
		changed = append(changed, "allow_prompts")
	}
	if patch.AllowSandbox != nil {
		changed = append(changed, "allow_sandbox")
	}
	if patch.AllowImageGeneration != nil {
		changed = append(changed, "allow_image_generation")
	}
	if patch.AllowKnowledgeBases != nil {
		changed = append(changed, "allow_knowledge_bases")
	}
	if patch.AllowFileUpload != nil {
		changed = append(changed, "allow_file_upload")
	}
	if patch.MemberMonthlyCreditLimit != nil {
		changed = append(changed, "member_monthly_credit_limit")
	}
	if len(changed) > 0 {
		if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditPolicyUpdated,
			"workspace", workspaceID, map[string]any{"changed": changed}); err != nil {
			return nil, err
		}
	}

	now := time.Now().Unix()
	current.UpdatedBy = actorID
	current.UpdatedAt = now

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_policies(
		   workspace_id, allowed_model_ids, allowed_tool_ids, allowed_mcp_server_ids,
		   allow_tool_calling, allow_drawing, allow_mcp, allow_skills, allow_prompts,
		   allow_sandbox, allow_image_generation, allow_knowledge_bases, allow_file_upload,
		   member_monthly_credit_limit, updated_by, updated_at
		 ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(workspace_id) DO UPDATE SET
		   allowed_model_ids=excluded.allowed_model_ids,
		   allowed_tool_ids=excluded.allowed_tool_ids,
		   allowed_mcp_server_ids=excluded.allowed_mcp_server_ids,
		   allow_tool_calling=excluded.allow_tool_calling,
		   allow_drawing=excluded.allow_drawing,
		   allow_mcp=excluded.allow_mcp,
		   allow_skills=excluded.allow_skills,
		   allow_prompts=excluded.allow_prompts,
		   allow_sandbox=excluded.allow_sandbox,
		   allow_image_generation=excluded.allow_image_generation,
		   allow_knowledge_bases=excluded.allow_knowledge_bases,
		   allow_file_upload=excluded.allow_file_upload,
		   member_monthly_credit_limit=excluded.member_monthly_credit_limit,
		   updated_by=excluded.updated_by,
		   updated_at=excluded.updated_at`,
		current.WorkspaceID, marshalIDList(current.AllowedModelIDs),
		marshalIDList(current.AllowedToolIDs), marshalIDList(current.AllowedMCPServerIDs),
		boolInt(current.AllowToolCalling), boolInt(current.AllowDrawing), boolInt(current.AllowMCP),
		boolInt(current.AllowSkills), boolInt(current.AllowPrompts),
		boolInt(current.AllowSandbox), boolInt(current.AllowImageGeneration),
		boolInt(current.AllowKnowledgeBases), boolInt(current.AllowFileUpload),
		current.MemberMonthlyCreditLimit, current.UpdatedBy, current.UpdatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &current, nil
}

// ModelAllowedByPolicy reports whether modelID is inside the workspace's
// model allowlist (empty list = all models).
func (p WorkspacePolicy) ModelAllowedByPolicy(modelID string) bool {
	if len(p.AllowedModelIDs) == 0 {
		return true
	}
	for _, id := range p.AllowedModelIDs {
		if id == modelID {
			return true
		}
	}
	return false
}

// ToolDeniedByPolicy reports whether a prefixed catalog tool id
// ("builtin:…", "hosted:…", "mcp:…", or "usermcp:…") is outside the
// workspace's tool/MCP allowlists or blocked by a capability switch.
func (p WorkspacePolicy) ToolDeniedByPolicy(id string) bool {
	id = strings.TrimSpace(id)
	if !p.AllowToolCalling && isWorkspaceToolID(id) {
		return true
	}
	if !p.AllowMCP && isWorkspaceMCPID(id) {
		return true
	}
	if !p.AllowSkills && id == "builtin:use_skill" {
		return true
	}
	// User-owned MCP services are selected from the caller's scoped library and
	// are not enumerable by a workspace administrator's official-server
	// allowlist. The workspace-wide AllowToolCalling/AllowMCP switches above
	// still apply; member/group ownership checks are enforced by the API and
	// runtime registry.
	if strings.HasPrefix(id, "usermcp:") {
		return false
	}
	if len(p.AllowedToolIDs) > 0 || len(p.AllowedMCPServerIDs) > 0 {
		allowed := false
		for _, candidate := range p.AllowedToolIDs {
			if candidate == id {
				allowed = true
				break
			}
		}
		if !allowed {
			for _, candidate := range p.AllowedMCPServerIDs {
				if workspaceMCPIDsEqual(candidate, id) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return true
		}
	}
	return false
}

func isWorkspaceToolID(id string) bool {
	return strings.HasPrefix(id, "builtin:") || strings.HasPrefix(id, "hosted:") ||
		strings.HasPrefix(id, "mcp:") || strings.HasPrefix(id, "usermcp:")
}

func isWorkspaceMCPID(id string) bool {
	return strings.HasPrefix(id, "mcp:") || strings.HasPrefix(id, "usermcp:")
}

// workspaceMCPIDsEqual accepts both namespaces for compatibility with policy
// rows created before user-owned MCP servers had a distinct `usermcp:` prefix.
func workspaceMCPIDsEqual(left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == right {
		return true
	}
	if strings.HasPrefix(left, "mcp:") && strings.HasPrefix(right, "usermcp:") {
		return strings.TrimPrefix(left, "mcp:") == strings.TrimPrefix(right, "usermcp:")
	}
	if strings.HasPrefix(left, "usermcp:") && strings.HasPrefix(right, "mcp:") {
		return strings.TrimPrefix(left, "usermcp:") == strings.TrimPrefix(right, "mcp:")
	}
	return false
}

// SkillAllowedByPolicy and PromptAllowedByPolicy are the resource-level
// counterparts to ToolDeniedByPolicy. They intentionally only address the
// workspace-wide switch; administrator/member allowlists are folded by the
// API/orchestrator separately.
func (p WorkspacePolicy) SkillAllowedByPolicy() bool  { return p.AllowSkills }
func (p WorkspacePolicy) PromptAllowedByPolicy() bool { return p.AllowPrompts }
func (p WorkspacePolicy) MCPAllowedByPolicy() bool    { return p.AllowMCP }

// WorkspaceMemberMonthlyUsage returns the credits a member has consumed in
// one workspace since the current calendar month began (UTC).
func WorkspaceMemberMonthlyUsage(ctx context.Context, db *sql.DB, workspaceID, userID string) (float64, error) {
	monthStart := time.Now().UTC().Truncate(time.Hour * 24)
	monthStart = time.Date(monthStart.Year(), monthStart.Month(), 1, 0, 0, 0, 0, time.UTC)
	var total sql.NullFloat64
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(credits),0) FROM usage_logs
		  WHERE workspace_id=? AND user_id=? AND created_at>=?`,
		workspaceID, userID, monthStart.Unix()).Scan(&total)
	return total.Float64, err
}

// WorkspaceUsageRow is one member's usage rollup inside a workspace.
type WorkspaceUsageRow struct {
	UserID       string  `json:"user_id"`
	Name         string  `json:"name"`
	Email        string  `json:"email"`
	Messages     int64   `json:"messages"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Credits      float64 `json:"credits"`
}

// SumWorkspaceUsageByMember rolls up usage_logs per member for the last N
// days (admins' usage view, §workspace RBAC phase 4).
func SumWorkspaceUsageByMember(ctx context.Context, db *sql.DB, workspaceID string, days int) ([]WorkspaceUsageRow, error) {
	if days <= 0 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days).Unix()
	rows, err := db.QueryContext(ctx,
		`SELECT u.user_id, COALESCE(users.name,''), COALESCE(users.email,''),
		        COUNT(*), COALESCE(SUM(u.input_tokens),0), COALESCE(SUM(u.output_tokens),0),
		        COALESCE(SUM(u.credits),0)
		   FROM usage_logs u
		   JOIN workspace_members m ON m.workspace_id=u.workspace_id AND m.user_id=u.user_id
		   LEFT JOIN users ON users.id=u.user_id
		  WHERE u.workspace_id=? AND u.created_at>=?
		  GROUP BY u.user_id, users.name, users.email
		  ORDER BY COALESCE(SUM(u.credits),0) DESC, u.user_id`, workspaceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkspaceUsageRow{}
	for rows.Next() {
		var row WorkspaceUsageRow
		if err := rows.Scan(&row.UserID, &row.Name, &row.Email, &row.Messages,
			&row.InputTokens, &row.OutputTokens, &row.Credits); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
