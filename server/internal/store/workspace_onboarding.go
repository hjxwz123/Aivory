package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/mail"
	"strings"
	"time"
)

// Workspace domain routing is deliberately small: one normalized domain maps
// to one initial workspace. Provider/tenant/claim rules are intentionally out
// of scope for the enterprise onboarding flow.
type WorkspaceDomainMapping struct {
	ID            string `json:"id"`
	Domain        string `json:"domain"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	CreatedAt     int64  `json:"created_at"`
}

type WorkspaceSettingsPatch struct {
	DefaultNewUserRole       *string                     `json:"default_new_user_role"`
	DefaultMemberPermissions *WorkspaceMemberPermissions `json:"default_member_permissions"`
	AllowPersonalSpace       *bool                       `json:"allow_personal_space"`
	InitialSiteGroupID       *string                     `json:"initial_site_group_id"`
	InitialPermanentCredits  *float64                    `json:"initial_permanent_credits"`
}

var (
	ErrWorkspaceDomainExists       = errors.New("workspace domain already mapped")
	ErrWorkspaceDomainInvalid      = errors.New("invalid workspace email domain")
	ErrWorkspaceDefaultRoleInvalid = errors.New("invalid workspace default role")
)

func normalizeWorkspaceDomain(raw string) (string, error) {
	domain := strings.TrimSpace(strings.ToLower(raw))
	domain = strings.TrimPrefix(domain, "@")
	if domain == "" || strings.ContainsAny(domain, " /\\\t\r\n") || !strings.Contains(domain, ".") || len(domain) > 253 {
		return "", ErrWorkspaceDomainInvalid
	}
	for _, r := range domain {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			continue
		}
		return "", ErrWorkspaceDomainInvalid
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return "", ErrWorkspaceDomainInvalid
	}
	return domain, nil
}

func emailDomain(email string) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return "", ErrWorkspaceDomainInvalid
	}
	// Parse first so an address such as "user@example.com" cannot be treated as
	// an arbitrary string suffix.
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.Contains(parsed.Address, "@") {
		return "", ErrWorkspaceDomainInvalid
	}
	parts := strings.Split(parsed.Address, "@")
	return normalizeWorkspaceDomain(parts[len(parts)-1])
}

func scanWorkspaceSettings(s scanner) (WorkspaceSettings, error) {
	var out WorkspaceSettings
	var role, permissions string
	var personal int
	if err := s.Scan(&out.WorkspaceID, &role, &permissions, &personal, &out.InitialSiteGroupID, &out.InitialPermanentCredits); err != nil {
		return out, err
	}
	out.DefaultNewUserRole = role
	if !ValidWorkspaceMemberRole(role) || role == WorkspaceRoleAdmin {
		out.DefaultNewUserRole = WorkspaceRoleMember
	}
	out.AllowPersonalSpace = personal != 0
	out.DefaultMemberPermissions = fullWorkspaceMemberPermissions()
	if strings.TrimSpace(permissions) != "" {
		if err := json.Unmarshal([]byte(permissions), &out.DefaultMemberPermissions); err != nil {
			out.DefaultMemberPermissions = fullWorkspaceMemberPermissions()
		}
	}
	return out, nil
}

func GetWorkspaceSettings(ctx context.Context, db *sql.DB, workspaceID string) (*WorkspaceSettings, error) {
	settings, err := scanWorkspaceSettings(db.QueryRowContext(ctx,
		`SELECT id, COALESCE(default_new_user_role,'member'), COALESCE(default_member_permissions,'{}'),
		        COALESCE(allow_personal_space,1), COALESCE(initial_site_group_id,''), COALESCE(initial_permanent_credits,0)
		   FROM workspaces WHERE id=?`, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &settings, nil
}

func marshalWorkspacePermissions(p WorkspaceMemberPermissions) string {
	raw, err := json.Marshal(p)
	if err != nil {
		return `{"can_create_projects":true,"can_private_conversations":true,"can_create_skills_prompts":true,"can_create_kb":true,"can_add_kb_files":true,"can_delete_kb_content":true}`
	}
	return string(raw)
}

func validateWorkspaceSettingsPatch(p WorkspaceSettingsPatch) error {
	if p.DefaultNewUserRole != nil && (*p.DefaultNewUserRole != WorkspaceRoleMember && *p.DefaultNewUserRole != WorkspaceRoleGuest) {
		return ErrWorkspaceDefaultRoleInvalid
	}
	if p.InitialPermanentCredits != nil {
		if math.IsNaN(*p.InitialPermanentCredits) || math.IsInf(*p.InitialPermanentCredits, 0) || *p.InitialPermanentCredits < 0 {
			return ErrInvalidCreditAmount
		}
		if *p.InitialPermanentCredits > 0 {
			micros, err := CreditsToMicros(*p.InitialPermanentCredits)
			if err != nil || micros <= 0 {
				return ErrInvalidCreditAmount
			}
		}
	}
	if p.InitialSiteGroupID != nil && strings.TrimSpace(*p.InitialSiteGroupID) != "" {
		// The caller performs the actual lookup inside the same transaction.
	}
	return nil
}

func updateWorkspaceSettingsTx(ctx context.Context, tx *sql.Tx, workspaceID, actorID string, patch WorkspaceSettingsPatch, platformAdmin bool) (*WorkspaceSettings, error) {
	if err := validateWorkspaceSettingsPatch(patch); err != nil {
		return nil, err
	}
	if !platformAdmin {
		var ownerID, role string
		if err := tx.QueryRowContext(ctx,
			`SELECT w.owner_id, COALESCE(m.role,'') FROM workspaces w
			 LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
			 WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`, actorID, workspaceID, actorID, actorID,
		).Scan(&ownerID, &role); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if ownerID != actorID && !isAdminRole(role) {
			return nil, ErrForbidden
		}
	}
	current, err := scanWorkspaceSettings(tx.QueryRowContext(ctx,
		`SELECT id, COALESCE(default_new_user_role,'member'), COALESCE(default_member_permissions,'{}'),
		        COALESCE(allow_personal_space,1), COALESCE(initial_site_group_id,''), COALESCE(initial_permanent_credits,0)
		   FROM workspaces WHERE id=?`, workspaceID))
	if err != nil {
		return nil, err
	}
	if patch.DefaultNewUserRole != nil {
		current.DefaultNewUserRole = *patch.DefaultNewUserRole
	}
	if patch.DefaultMemberPermissions != nil {
		current.DefaultMemberPermissions = *patch.DefaultMemberPermissions
	}
	if patch.AllowPersonalSpace != nil {
		current.AllowPersonalSpace = *patch.AllowPersonalSpace
	}
	if patch.InitialSiteGroupID != nil {
		current.InitialSiteGroupID = strings.TrimSpace(*patch.InitialSiteGroupID)
	}
	if patch.InitialPermanentCredits != nil {
		current.InitialPermanentCredits = *patch.InitialPermanentCredits
	}
	if current.InitialSiteGroupID != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM user_groups WHERE id=?`, current.InitialSiteGroupID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspaces SET default_new_user_role=?, default_member_permissions=?, allow_personal_space=?, initial_site_group_id=?, initial_permanent_credits=? WHERE id=?`,
		current.DefaultNewUserRole, marshalWorkspacePermissions(current.DefaultMemberPermissions), boolInt(current.AllowPersonalSpace), current.InitialSiteGroupID, current.InitialPermanentCredits, workspaceID); err != nil {
		return nil, err
	}
	changed := []string{}
	if patch.DefaultNewUserRole != nil {
		changed = append(changed, "default_new_user_role")
	}
	if patch.DefaultMemberPermissions != nil {
		changed = append(changed, "default_member_permissions")
	}
	if patch.AllowPersonalSpace != nil {
		changed = append(changed, "allow_personal_space")
	}
	if patch.InitialSiteGroupID != nil {
		changed = append(changed, "initial_site_group_id")
	}
	if patch.InitialPermanentCredits != nil {
		changed = append(changed, "initial_permanent_credits")
	}
	if len(changed) > 0 {
		action := AuditWorkspaceSettingsUpdated
		if platformAdmin {
			action = AuditWorkspaceDefaultsUpdated
		}
		if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, action, "workspace", workspaceID, map[string]any{"changed": changed}); err != nil {
			return nil, err
		}
	}
	return &current, nil
}

func isAdminRole(role string) bool {
	return role == WorkspaceRoleAdmin || role == WorkspaceRoleOwnerLegacy
}

func UpdateWorkspaceSettings(ctx context.Context, db *sql.DB, workspaceID, actorID string, patch WorkspaceSettingsPatch) (*WorkspaceSettings, error) {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	settings, err := updateWorkspaceSettingsTx(ctx, tx, workspaceID, actorID, patch, false)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return settings, nil
}

func UpdateWorkspaceSettingsAsAdmin(ctx context.Context, db *sql.DB, workspaceID, actorID string, patch WorkspaceSettingsPatch) (*WorkspaceSettings, error) {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	settings, err := updateWorkspaceSettingsTx(ctx, tx, workspaceID, actorID, patch, true)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return settings, nil
}

func ListWorkspaceDomainMappings(ctx context.Context, db *sql.DB, workspaceID string) ([]WorkspaceDomainMapping, error) {
	rows, err := db.QueryContext(ctx, `SELECT m.id,m.domain,m.workspace_id,w.name,m.created_at
		FROM workspace_domain_mappings m JOIN workspaces w ON w.id=m.workspace_id WHERE m.workspace_id=? ORDER BY m.domain`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkspaceDomainMapping{}
	for rows.Next() {
		var m WorkspaceDomainMapping
		if err := rows.Scan(&m.ID, &m.Domain, &m.WorkspaceID, &m.WorkspaceName, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func ListAllWorkspaceDomainMappings(ctx context.Context, db *sql.DB) ([]WorkspaceDomainMapping, error) {
	rows, err := db.QueryContext(ctx, `SELECT m.id,m.domain,m.workspace_id,w.name,m.created_at
		FROM workspace_domain_mappings m JOIN workspaces w ON w.id=m.workspace_id ORDER BY m.domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkspaceDomainMapping{}
	for rows.Next() {
		var m WorkspaceDomainMapping
		if err := rows.Scan(&m.ID, &m.Domain, &m.WorkspaceID, &m.WorkspaceName, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func CreateWorkspaceDomainMapping(ctx context.Context, db *sql.DB, workspaceID, actorID, rawDomain string) (*WorkspaceDomainMapping, error) {
	domain, err := normalizeWorkspaceDomain(rawDomain)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id=?`, workspaceID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_domain_mappings(id,domain,workspace_id,created_by,created_at) VALUES(?,?,?,?,?)`, genID("wsdm"), domain, workspaceID, actorID, time.Now().Unix()); err != nil {
		if isUniqueIndexErr(err, "idx_workspace_domain_mapping_domain", "workspace_domain_mappings.domain") {
			return nil, ErrWorkspaceDomainExists
		}
		return nil, err
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditWorkspaceDomainMapped, "domain", domain, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var out WorkspaceDomainMapping
	err = db.QueryRowContext(ctx, `SELECT m.id,m.domain,m.workspace_id,w.name,m.created_at FROM workspace_domain_mappings m JOIN workspaces w ON w.id=m.workspace_id WHERE m.domain=?`, domain).Scan(&out.ID, &out.Domain, &out.WorkspaceID, &out.WorkspaceName, &out.CreatedAt)
	return &out, err
}

func DeleteWorkspaceDomainMapping(ctx context.Context, db *sql.DB, mappingID, actorID string) error {
	var workspaceID, domain string
	if err := db.QueryRowContext(ctx, `SELECT workspace_id,domain FROM workspace_domain_mappings WHERE id=?`, mappingID).Scan(&workspaceID, &domain); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `DELETE FROM workspace_domain_mappings WHERE id=?`, mappingID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditWorkspaceDomainUnmapped, "domain", domain, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func ResolveWorkspaceForEmail(ctx context.Context, db *sql.DB, email string) (string, error) {
	domain, err := emailDomain(email)
	if err != nil {
		return "", nil
	}
	var workspaceID string
	err = db.QueryRowContext(ctx, `SELECT workspace_id FROM workspace_domain_mappings WHERE lower(trim(domain))=?`, domain).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return workspaceID, err
}

// ApplyWorkspaceOnboardingTx records a one-time group/credit grant for a newly
// created membership. It deliberately preserves a user's existing paid site
// group; the configured initial group only fills the default free tier.
func ApplyWorkspaceOnboardingTx(ctx context.Context, tx *sql.Tx, workspaceID, userID string) error {
	var groupID string
	var credits float64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(initial_site_group_id,''), COALESCE(initial_permanent_credits,0) FROM workspaces WHERE id=?`, workspaceID).Scan(&groupID, &credits); err != nil {
		return err
	}
	micros, err := CreditsToMicros(math.Max(0, credits))
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO workspace_onboarding_grants(workspace_id,user_id,site_group_granted,credits_granted_micros,created_at) VALUES(?,?,?,?,?) ON CONFLICT(workspace_id,user_id) DO NOTHING`, workspaceID, userID, 0, 0, time.Now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil
	}
	groupGranted := 0
	if strings.TrimSpace(groupID) != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM user_groups WHERE id=?`, groupID).Scan(&exists); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE users SET group_id=? WHERE id=? AND COALESCE(group_id,?)=?`, groupID, userID, DefaultGroupID, DefaultGroupID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			groupGranted = 1
		}
	}
	if micros > 0 {
		var current int64
		var mirror float64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(credits_permanent_micros,0), COALESCE(credits_permanent,0) FROM users WHERE id=?`, userID).Scan(&current, &mirror); err != nil {
			return err
		}
		if current == 0 && mirror != 0 {
			current, err = CreditsToMicros(mirror)
			if err != nil {
				return err
			}
		}
		if current > math.MaxInt64-micros {
			return ErrInvalidCreditAmount
		}
		next := current + micros
		if _, err := tx.ExecContext(ctx, `UPDATE users SET credits_permanent_micros=?, credits_permanent=CAST(? AS DOUBLE PRECISION)/1000000.0 WHERE id=?`, next, next, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO credit_ledger(id,user_id,group_id,cycle_anchor,cycle_start,kind,amount,amount_micros,source_type,source_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, genID("cr"), userID, groupID, 0, 0, "permanent_grant", credits, micros, "workspace_onboarding", workspaceID, time.Now().Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_onboarding_grants SET site_group_granted=?, credits_granted_micros=? WHERE workspace_id=? AND user_id=?`, groupGranted, micros, workspaceID, userID); err != nil {
		return err
	}
	return nil
}

// OnboardUserByEmail applies the domain mapping to an already-authenticated
// user. A missing mapping is a no-op; callers can continue a normal login.
func OnboardUserByEmail(ctx context.Context, db *sql.DB, userID, email, source string) (*Workspace, error) {
	workspaceID, err := ResolveWorkspaceForEmail(ctx, db, email)
	if err != nil || workspaceID == "" {
		return nil, err
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN default_new_user_role='guest' THEN 'guest' ELSE 'member' END FROM workspaces WHERE id=?`, workspaceID).Scan(&role); err != nil {
		return nil, err
	}
	joined, err := joinWorkspaceWithRoleTx(ctx, tx, workspaceID, userID, role)
	if err != nil {
		return nil, err
	}
	if joined {
		if err := ApplyWorkspaceOnboardingTx(ctx, tx, workspaceID, userID); err != nil {
			return nil, err
		}
		if err := recordWorkspaceAudit(ctx, tx, workspaceID, userID, AuditMemberJoined, "workspace_member", userID, map[string]any{"role": role, "source": source, "domain": true}); err != nil {
			return nil, err
		}
	}
	var ws Workspace
	if err := tx.QueryRowContext(ctx, `SELECT id,name,owner_id,created_at,COALESCE(allow_personal_space,1) FROM workspaces WHERE id=?`, workspaceID).Scan(&ws.ID, &ws.Name, &ws.OwnerID, &ws.CreatedAt, &ws.AllowPersonalSpace); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ws.Role = role
	ws.IsOwner = ws.OwnerID == userID
	ws.AllowPersonalSpace = ws.AllowPersonalSpace
	return &ws, nil
}
