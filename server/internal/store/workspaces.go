package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Workspaces (§workspaces) — fully-isolated collaborative spaces. A workspace
// owns conversations/projects/KBs via their workspace_id column ('' = personal);
// every member sees all of them. Membership is granted ONLY through the invite
// link (a 192-bit capability token, rotatable). Roles are admin/member/guest
// (§workspace RBAC): the workspace owner is a member row with role='admin' plus
// workspaces.owner_id, which is the ONLY source of owner-exclusive authority.
// Legacy rows still holding 'owner' are read as 'admin' and rewritten by the
// startup migration.

// Workspace member roles (§workspace RBAC). Values written after the RBAC
// migration: admin, member, guest. The historical 'owner' member role is
// normalized to admin on read; workspaces.owner_id decides ownership.
const (
	WorkspaceRoleAdmin  = "admin"
	WorkspaceRoleMember = "member"
	WorkspaceRoleGuest  = "guest"
	// WorkspaceRoleOwnerLegacy is the pre-RBAC role value kept readable for
	// databases upgraded in place.
	WorkspaceRoleOwnerLegacy = "owner"
)

// normalizeWorkspaceRoleSQL returns a SQL expression mapping a member role
// column onto the canonical role set ('owner' legacy rows read as 'admin').
func normalizeWorkspaceRoleSQL(expr string) string {
	return `CASE WHEN COALESCE(` + expr + `,'')='owner' THEN 'admin' ELSE COALESCE(` + expr + `,'') END`
}

// isAdminRoleSQL matches member roles that carry workspace-admin authority
// (canonical admin plus the legacy 'owner' value).
func isAdminRoleSQL(expr string) string {
	return `COALESCE(` + expr + `,'') IN ('admin','owner')`
}

// isCollaboratorRoleSQL is the write-capable counterpart to isAdminRoleSQL.
// It deliberately uses a closed role set: malformed rows must never become
// writable merely because they are not guests. "owner" is kept only for
// databases that have not yet completed the legacy-role migration.
func isCollaboratorRoleSQL(expr string) string {
	return `COALESCE(` + expr + `,'') IN ('admin','member','owner')`
}

// Workspace is one workspace row.
type Workspace struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnerID string `json:"owner_id"`
	// InviteToken is retained for legacy database compatibility only. It is not
	// an accepted join capability and is cleared from user-facing responses.
	InviteToken string `json:"invite_token,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	// Enriched (not columns):
	Role string `json:"role,omitempty"` // requesting user's role (admin/member/guest)
	// IsOwner marks the requesting user as the canonical workspace owner.
	IsOwner                 bool   `json:"is_owner"`
	MemberCount             int    `json:"member_count,omitempty"` // filled by list queries
	OwnerName               string `json:"owner_name,omitempty"`
	CanCreateProjects       bool   `json:"can_create_projects"`
	CanPrivateConversations bool   `json:"can_private_conversations"`
	CanCreateSkillsPrompts  bool   `json:"can_create_skills_prompts"`
	CanCreatePrompts        bool   `json:"can_create_prompts"`
	CanCreateSkills         bool   `json:"can_create_skills"`
	CanCreateMCP            bool   `json:"can_create_mcp"`
	CanUsePrompts           bool   `json:"can_use_prompts"`
	CanUseSkills            bool   `json:"can_use_skills"`
	CanUseMCP               bool   `json:"can_use_mcp"`
	CanCreateKB             bool   `json:"can_create_kb"`
	CanAddKBFiles           bool   `json:"can_add_kb_files"`
	CanDeleteKBContent      bool   `json:"can_delete_kb_content"`
	CanDeleteConversations  bool   `json:"can_delete_conversations"`
}

// WorkspaceMember is one member row enriched with user identity for display.
type WorkspaceMember struct {
	UserID                  string `json:"user_id"`
	Role                    string `json:"role"`
	IsOwner                 bool   `json:"is_owner"`
	CanCreateProjects       bool   `json:"can_create_projects"`
	CanPrivateConversations bool   `json:"can_private_conversations"`
	CanCreateSkillsPrompts  bool   `json:"can_create_skills_prompts"`
	CanCreatePrompts        bool   `json:"can_create_prompts"`
	CanCreateSkills         bool   `json:"can_create_skills"`
	CanCreateMCP            bool   `json:"can_create_mcp"`
	CanUsePrompts           bool   `json:"can_use_prompts"`
	CanUseSkills            bool   `json:"can_use_skills"`
	CanUseMCP               bool   `json:"can_use_mcp"`
	CanCreateKB             bool   `json:"can_create_kb"`
	CanAddKBFiles           bool   `json:"can_add_kb_files"`
	CanDeleteKBContent      bool   `json:"can_delete_kb_content"`
	CanDeleteConversations  bool   `json:"can_delete_conversations"`
	JoinedAt                int64  `json:"joined_at"`
	Name                    string `json:"name"`
	Email                   string `json:"email"`
	AvatarURL               string `json:"avatar_url"`
}

type WorkspaceMemberPermissions struct {
	CanCreateProjects       bool `json:"can_create_projects"`
	CanPrivateConversations bool `json:"can_private_conversations"`
	CanCreateSkillsPrompts  bool `json:"can_create_skills_prompts"`
	CanCreatePrompts        bool `json:"can_create_prompts"`
	CanCreateSkills         bool `json:"can_create_skills"`
	CanCreateMCP            bool `json:"can_create_mcp"`
	CanUsePrompts           bool `json:"can_use_prompts"`
	CanUseSkills            bool `json:"can_use_skills"`
	CanUseMCP               bool `json:"can_use_mcp"`
	CanCreateKB             bool `json:"can_create_kb"`
	CanAddKBFiles           bool `json:"can_add_kb_files"`
	CanDeleteKBContent      bool `json:"can_delete_kb_content"`
	CanDeleteConversations  bool `json:"can_delete_conversations"`
	// jsonFields is populated only by UnmarshalJSON. It lets the PATCH store
	// path distinguish an omitted field from an explicit false without changing
	// the public response shape or the full-replacement semantics of Go callers.
	jsonFields map[string]json.RawMessage
}

// UnmarshalJSON keeps API clients from before can_delete_conversations was
// introduced on the historical permissive behavior. New clients always send
// the field explicitly, so false remains an intentional capability reduction.
func (p *WorkspaceMemberPermissions) UnmarshalJSON(data []byte) error {
	type plain WorkspaceMemberPermissions
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*p = WorkspaceMemberPermissions(decoded)
	legacyCreate, legacyPresent := fields["can_create_skills_prompts"]
	_, promptCreatePresent := fields["can_create_prompts"]
	_, skillCreatePresent := fields["can_create_skills"]
	_, mcpCreatePresent := fields["can_create_mcp"]
	_, mcpCreateAliasPresent := fields["can_create_mcps"]
	if !promptCreatePresent {
		if legacyPresent {
			if err := json.Unmarshal(legacyCreate, &p.CanCreatePrompts); err != nil {
				return err
			}
		} else {
			p.CanCreatePrompts = true
		}
	}
	if !skillCreatePresent {
		if legacyPresent {
			if err := json.Unmarshal(legacyCreate, &p.CanCreateSkills); err != nil {
				return err
			}
		} else {
			p.CanCreateSkills = true
		}
	}
	if !mcpCreatePresent {
		if raw, aliasPresent := fields["can_create_mcps"]; aliasPresent {
			if err := json.Unmarshal(raw, &p.CanCreateMCP); err != nil {
				return err
			}
		} else if legacyPresent {
			if err := json.Unmarshal(legacyCreate, &p.CanCreateMCP); err != nil {
				return err
			}
		} else {
			p.CanCreateMCP = true
		}
	}
	// Usage permissions are intentionally independent from creation. Omitted
	// fields preserve the historical permissive behavior for older clients.
	if _, present := fields["can_use_prompts"]; !present {
		p.CanUsePrompts = true
	}
	if _, present := fields["can_use_skills"]; !present {
		p.CanUseSkills = true
	}
	if _, present := fields["can_use_mcp"]; !present {
		p.CanUseMCP = true
	}
	// Keep the legacy aggregate as a non-broadening compatibility mirror. When
	// no creation fields were supplied at all, the granular defaults above
	// preserve the historical permissive behavior for an omitted JSON object.
	// A payload containing only the legacy aggregate has already copied that
	// value into all three granular fields, so this AND produces the same value.
	if legacyPresent || promptCreatePresent || skillCreatePresent || mcpCreatePresent || mcpCreateAliasPresent {
		p.CanCreateSkillsPrompts = p.CanCreatePrompts && p.CanCreateSkills && p.CanCreateMCP
	} else {
		p.CanCreateSkillsPrompts = true
	}
	if _, present := fields["can_delete_conversations"]; !present {
		p.CanDeleteConversations = true
	}
	p.jsonFields = fields
	return nil
}

// mergeOmittedJSONFields turns a JSON-decoded permission payload into the
// effective full row written by UpdateWorkspaceMemberPermissions. Older
// clients do not know the granular create/use fields; preserving their current
// values prevents an unrelated edit from silently widening or narrowing them.
//
// The retired aggregate remains usable by old clients. A round-trip of the
// current aggregate preserves the independent values behind it, while an
// actual aggregate toggle still intentionally updates all three create bits.
func (p WorkspaceMemberPermissions) mergeOmittedJSONFields(current WorkspaceMemberPermissions) WorkspaceMemberPermissions {
	if p.jsonFields == nil {
		return p
	}
	present := func(name string) bool {
		_, ok := p.jsonFields[name]
		return ok
	}
	preserve := func(name string, incoming *bool, existing bool) {
		if !present(name) {
			*incoming = existing
		}
	}

	preserve("can_create_projects", &p.CanCreateProjects, current.CanCreateProjects)
	preserve("can_private_conversations", &p.CanPrivateConversations, current.CanPrivateConversations)
	preserve("can_create_kb", &p.CanCreateKB, current.CanCreateKB)
	preserve("can_add_kb_files", &p.CanAddKBFiles, current.CanAddKBFiles)
	preserve("can_delete_kb_content", &p.CanDeleteKBContent, current.CanDeleteKBContent)
	preserve("can_delete_conversations", &p.CanDeleteConversations, current.CanDeleteConversations)
	preserve("can_use_prompts", &p.CanUsePrompts, current.CanUsePrompts)
	preserve("can_use_skills", &p.CanUseSkills, current.CanUseSkills)
	preserve("can_use_mcp", &p.CanUseMCP, current.CanUseMCP)

	promptPresent := present("can_create_prompts")
	skillPresent := present("can_create_skills")
	mcpPresent := present("can_create_mcp") || present("can_create_mcps")
	granularPresent := promptPresent || skillPresent || mcpPresent
	legacyPresent := present("can_create_skills_prompts")
	switch {
	case granularPresent:
		if !promptPresent {
			p.CanCreatePrompts = current.CanCreatePrompts
		}
		if !skillPresent {
			p.CanCreateSkills = current.CanCreateSkills
		}
		if !mcpPresent {
			p.CanCreateMCP = current.CanCreateMCP
		}
	case legacyPresent:
		currentAggregate := normalizeWorkspaceMemberCreationAggregate(
			current.CanCreatePrompts, current.CanCreateSkills, current.CanCreateMCP,
		)
		if p.CanCreateSkillsPrompts == currentAggregate {
			// This is the value an old client received from the server. Treat an
			// unchanged round-trip as a no-op for the hidden granular fields.
			p.CanCreatePrompts = current.CanCreatePrompts
			p.CanCreateSkills = current.CanCreateSkills
			p.CanCreateMCP = current.CanCreateMCP
		} else {
			p.CanCreatePrompts = p.CanCreateSkillsPrompts
			p.CanCreateSkills = p.CanCreateSkillsPrompts
			p.CanCreateMCP = p.CanCreateSkillsPrompts
		}
	default:
		p.CanCreatePrompts = current.CanCreatePrompts
		p.CanCreateSkills = current.CanCreateSkills
		p.CanCreateMCP = current.CanCreateMCP
	}
	p.CanCreateSkillsPrompts = normalizeWorkspaceMemberCreationAggregate(
		p.CanCreatePrompts, p.CanCreateSkills, p.CanCreateMCP,
	)
	return p
}

func fullWorkspaceMemberPermissions() WorkspaceMemberPermissions {
	return WorkspaceMemberPermissions{
		CanCreateProjects: true, CanPrivateConversations: true, CanCreateSkillsPrompts: true,
		CanCreatePrompts: true, CanCreateSkills: true, CanCreateMCP: true,
		CanUsePrompts: true, CanUseSkills: true, CanUseMCP: true, CanCreateKB: true,
		CanAddKBFiles: true, CanDeleteKBContent: true, CanDeleteConversations: true,
	}
}

func applyWorkspacePermissions(workspace *Workspace, permissions WorkspaceMemberPermissions) {
	workspace.CanCreateProjects = permissions.CanCreateProjects
	workspace.CanPrivateConversations = permissions.CanPrivateConversations
	workspace.CanCreatePrompts = permissions.CanCreatePrompts
	workspace.CanCreateSkills = permissions.CanCreateSkills
	workspace.CanCreateMCP = permissions.CanCreateMCP
	workspace.CanCreateSkillsPrompts = permissions.CanCreatePrompts && permissions.CanCreateSkills && permissions.CanCreateMCP
	workspace.CanUsePrompts = permissions.CanUsePrompts
	workspace.CanUseSkills = permissions.CanUseSkills
	workspace.CanUseMCP = permissions.CanUseMCP
	workspace.CanCreateKB = permissions.CanCreateKB
	workspace.CanAddKBFiles = permissions.CanAddKBFiles
	workspace.CanDeleteKBContent = permissions.CanDeleteKBContent
	workspace.CanDeleteConversations = permissions.CanDeleteConversations
}

// normalizeWorkspaceMemberCreationAggregate computes the retired combined
// creation bit from the three granular capabilities. The aggregate is a
// compatibility mirror only; after migration the granular columns are the
// authoritative source of permission decisions.
func normalizeWorkspaceMemberCreationAggregate(
	canCreatePrompts, canCreateSkills, canCreateMCP bool,
) bool {
	return canCreatePrompts && canCreateSkills && canCreateMCP
}

func normalizeWorkspaceCreationAggregate(workspace *Workspace) {
	if workspace == nil {
		return
	}
	// Never let a stale aggregate value rewrite granular fields on read. Legacy
	// aggregate-only rows are expanded during Migrate, while API JSON payloads
	// are expanded by WorkspaceMemberPermissions.UnmarshalJSON.
	workspace.CanCreateSkillsPrompts = normalizeWorkspaceMemberCreationAggregate(
		workspace.CanCreatePrompts, workspace.CanCreateSkills, workspace.CanCreateMCP,
	)
}

func normalizeWorkspaceMemberCreationAggregateFields(member *WorkspaceMember) {
	if member == nil {
		return
	}
	// The aggregate is response-only compatibility metadata. A stale aggregate
	// must not narrow the authoritative granular fields on read.
	member.CanCreateSkillsPrompts = normalizeWorkspaceMemberCreationAggregate(
		member.CanCreatePrompts, member.CanCreateSkills, member.CanCreateMCP,
	)
}

// workspaceResourceAccessPredicate is the authoritative access boundary for a
// row that carries user_id + workspace_id. Personal rows belong to their user;
// workspace rows belong to the workspace, so their original creator must still
// be the canonical workspace owner or a current member. The owner check keeps
// legacy databases safe when their redundant owner membership row is missing.
//
// alias is a trusted SQL identifier supplied by store code (for example "c").
// Callers must append workspaceResourceAccessArgs(userID) in predicate order.
func workspaceResourceAccessPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND ` + prefix + `user_id=?) OR (` +
		`COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces resource_workspace ` +
		`WHERE resource_workspace.id=` + prefix + `workspace_id AND (` +
		`resource_workspace.owner_id=? OR EXISTS (` +
		`SELECT 1 FROM workspace_members resource_member ` +
		`WHERE resource_member.workspace_id=resource_workspace.id AND resource_member.user_id=?` +
		`)` +
		`)` +
		`)` +
		`))`
}

func workspaceResourceAccessArgs(userID string) []any {
	return []any{userID, userID, userID}
}

// workspaceScopedVisibilityPredicate is the §workspace RBAC phase-2 read
// boundary for resources that carry an is_public column (projects, knowledge
// bases): shared rows (is_public=1) are visible to every current member —
// guests included; private rows (is_public=0) only to their creator and
// workspace admins (canonical owner plus admin-role members). A creator who
// leaves the workspace loses access to their own private rows. Personal rows
// (workspace_id=”) remain creator-only. Callers must append
// workspaceScopedVisibilityArgs(userID) in predicate order.
func workspaceScopedVisibilityPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND ` + prefix + `user_id=?) OR (` +
		`COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces scoped_workspace ` +
		`WHERE scoped_workspace.id=` + prefix + `workspace_id AND (` +
		`scoped_workspace.owner_id=? OR EXISTS (` +
		`SELECT 1 FROM workspace_members scoped_member ` +
		`WHERE scoped_member.workspace_id=scoped_workspace.id AND scoped_member.user_id=? ` +
		`AND (` + isAdminRoleSQL("scoped_member.role") + ` OR ` + prefix + `is_public=1)` +
		`) OR (` + prefix + `user_id=? AND EXISTS (` +
		`SELECT 1 FROM workspace_members scoped_creator ` +
		`WHERE scoped_creator.workspace_id=scoped_workspace.id AND scoped_creator.user_id=?` +
		`)` +
		`)` +
		`)` +
		`)` +
		`))`
}

func workspaceScopedVisibilityArgs(userID string) []any {
	return []any{userID, userID, userID, userID, userID}
}

// conversationResourceAccessPredicate adds per-conversation visibility to the
// standard personal/workspace boundary without changing the three-argument
// shape. In a workspace: admins (including the canonical owner) see every
// conversation; other current members see public rows plus their own private
// rows; guests — who are read-only — still see public rows. Keeping the same
// three-argument shape lets document/file/message subqueries share the existing
// authorization plumbing.
func conversationResourceAccessPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND ` + prefix + `user_id=?) OR (` +
		`COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces conversation_workspace ` +
		`WHERE conversation_workspace.id=` + prefix + `workspace_id AND (` +
		`conversation_workspace.owner_id=? OR EXISTS (` +
		`SELECT 1 FROM workspace_members conversation_member ` +
		`WHERE conversation_member.workspace_id=conversation_workspace.id AND conversation_member.user_id=? ` +
		`AND (` + isAdminRoleSQL("conversation_member.role") + ` ` +
		`OR ` + prefix + `is_public=1 ` +
		`OR ` + prefix + `user_id=conversation_member.user_id)` +
		`)` +
		`)` +
		`)` +
		`))`
}

// conversationMemberMutationPredicate is the boundary for mutations against a
// conversation carried out by ordinary collaborators (sending messages,
// switching branches, attaching uploads): read access PLUS the workspace actor
// must not be a guest. Guests are strictly read-only (§workspace RBAC).
// conversationMemberMutationArgs supplies the five positional parameters.
func conversationMemberMutationPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `(` + conversationResourceAccessPredicate(alias) + ` ` +
		`AND (COALESCE(` + prefix + `workspace_id,'')='' OR EXISTS (` +
		`SELECT 1 FROM workspaces mutation_workspace ` +
		`WHERE mutation_workspace.id=` + prefix + `workspace_id AND (` +
		`mutation_workspace.owner_id=? OR EXISTS (` +
		`SELECT 1 FROM workspace_members mutation_member ` +
		`WHERE mutation_member.workspace_id=mutation_workspace.id AND mutation_member.user_id=? ` +
		`AND ` + isCollaboratorRoleSQL("mutation_member.role") + `)` +
		`)` +
		`)))`
}

func conversationMemberMutationArgs(userID string) []any {
	return append(workspaceResourceAccessArgs(userID), userID, userID)
}

// workspaceResourceManagerPredicate is the stricter resource-management
// boundary: a personal resource's creator; or, in a workspace, an admin (the
// canonical owner always, plus admin-role members) or the resource creator
// while that creator is still a current member. Other ordinary members may
// collaborate on content but may not rename, republish or destroy the share.
func workspaceResourceManagerPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND ` + prefix + `user_id=?) OR (` +
		`COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces resource_workspace ` +
		`WHERE resource_workspace.id=` + prefix + `workspace_id AND (` +
		`resource_workspace.owner_id=? OR EXISTS (` +
		`SELECT 1 FROM workspace_members resource_admin_member ` +
		`WHERE resource_admin_member.workspace_id=resource_workspace.id ` +
		`AND resource_admin_member.user_id=? ` +
		`AND ` + isAdminRoleSQL("resource_admin_member.role") + ` ` +
		`) OR (` + prefix + `user_id=? AND EXISTS (` +
		`SELECT 1 FROM workspace_members resource_manager_member ` +
		`WHERE resource_manager_member.workspace_id=resource_workspace.id AND resource_manager_member.user_id=? ` +
		`AND ` + isCollaboratorRoleSQL("resource_manager_member.role") +
		`)` +
		`)` +
		`)` +
		`)` +
		`))`
}

func workspaceResourceManagerArgs(userID string) []any {
	return []any{userID, userID, userID, userID, userID}
}

// workspaceAcceptsResourceCreationPredicate blocks new shared resources once
// either the canonical owner or the creating user has entered account deletion.
// alias is a trusted workspaces-table alias; the single placeholder is the
// creating user's id.
func workspaceAcceptsResourceCreationPredicate(alias string) string {
	return `COALESCE(` + alias + `.deleting,0)=0 AND EXISTS (
		SELECT 1 FROM users creation_owner
		 WHERE creation_owner.id=` + alias + `.owner_id AND creation_owner.status='active'
	) AND EXISTS (
		SELECT 1 FROM users creation_user
		 WHERE creation_user.id=? AND creation_user.status='active'
	)`
}

// workspaceMemberCapabilityPredicate is used inside mutations that already
// lock the workspace row. Workspace admins (canonical owner plus admin-role
// members) always have every capability; ordinary members must still exist,
// not be guests, and have the requested total permission.
// capability is a trusted column name supplied only by store code.
func workspaceMemberCapabilityPredicate(workspaceAlias, capability string) string {
	return `(` + workspaceAlias + `.owner_id=? OR EXISTS (
		SELECT 1 FROM workspace_members capability_member
		 WHERE capability_member.workspace_id=` + workspaceAlias + `.id
		   AND capability_member.user_id=?
		   AND (capability_member.` + capability + `=1 OR ` + isAdminRoleSQL("capability_member.role") + `)
		   AND ` + isCollaboratorRoleSQL("capability_member.role") + `
	))`
}

func workspaceMemberCapabilityArgs(userID string) []any {
	return []any{userID, userID}
}

// workspaceConversationDeletionCapabilityPredicate adds the workspace member
// capability used only for destructive conversation actions. Personal
// conversations are unaffected here; their user-group capability is checked by
// the API. In a workspace, owners and admins keep their full authority while
// ordinary members must have can_delete_conversations at mutation time.
func workspaceConversationDeletionCapabilityPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `(COALESCE(` + prefix + `workspace_id,'')='' OR EXISTS (
		SELECT 1 FROM workspaces deletion_workspace
		 WHERE deletion_workspace.id=` + prefix + `workspace_id
		   AND ` + workspaceMemberCapabilityPredicate("deletion_workspace", "can_delete_conversations") + `
	))`
}

func beginWorkspaceMutationTx(ctx context.Context, db *sql.DB, workspaceID string) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// avatarFromSettings extracts settings.avatar_url from the users.settings JSON
// blob (the same field the sidebar reads client-side).
func avatarFromSettings(settings string) string {
	if settings == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(settings), &m) != nil {
		return ""
	}
	url, _ := m["avatar_url"].(string)
	return url
}

// CreateWorkspace inserts the workspace plus the owner's member row in one tx.
// The per-group cap is the HANDLER's job (needs group config); this is pure
// storage.
func CreateWorkspace(ctx context.Context, db *sql.DB, ownerID, name string) (*Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("workspace name required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	id := genID("ws")
	// Keep a unique compatibility value for pre-invite-record databases. The
	// application never resolves this token; all joins use workspace_invites.
	token := "wsi_" + genToken()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspaces(id, name, owner_id, invite_token) VALUES(?, ?, ?, ?)`,
		id, name, ownerID, token); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_members(workspace_id, user_id, role) VALUES(?, ?, 'admin')`,
		id, ownerID); err != nil {
		return nil, err
	}
	if err := recordWorkspaceAudit(ctx, tx, id, ownerID, AuditWorkspaceCreated, "workspace", id,
		map[string]any{"name": name}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	w, err := GetWorkspace(ctx, db, id)
	if err != nil {
		return nil, err
	}
	// The creator is, by definition, the owner (owner_id) and an admin. Set the
	// enriched fields GetWorkspace can't (it reads columns only) so the create
	// response is complete — the client's Members dialog gates management on
	// is_owner/admin, and without this it stays hidden until a page reload
	// re-fetches the list.
	w.Role = WorkspaceRoleAdmin
	w.IsOwner = true
	w.InviteToken = ""
	w.MemberCount = 1
	applyWorkspacePermissions(w, fullWorkspaceMemberPermissions())
	return w, nil
}

// GetWorkspace returns a workspace by id (no membership check — callers gate).
func GetWorkspace(ctx context.Context, db *sql.DB, id string) (*Workspace, error) {
	var w Workspace
	err := db.QueryRowContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.invite_token, w.created_at, COALESCE(u.name,'')
		   FROM workspaces w
		   LEFT JOIN users u ON u.id=w.owner_id
		  WHERE w.id=?`, id,
	).Scan(&w.ID, &w.Name, &w.OwnerID, &w.InviteToken, &w.CreatedAt, &w.OwnerName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// GetWorkspaceForMember returns the workspace only when userID is a member or
// its canonical owner. The owner fallback supports legacy rows missing from
// workspace_members without letting an ordinary former member back in.
// This is the standard access gate for workspace endpoints.
func GetWorkspaceForMember(ctx context.Context, db *sql.DB, id, userID string) (*Workspace, error) {
	var w Workspace
	err := db.QueryRowContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.invite_token, w.created_at,
		        CASE WHEN w.owner_id=? THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("m.role")+` END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_projects,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_private_conversations,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_skills_prompts,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_prompts,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_skills,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_mcp,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_use_prompts,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_use_skills,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_use_mcp,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_kb,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_add_kb_files,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_delete_kb_content,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_delete_conversations,0) END
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`,
		userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID,
		userID, id, userID, userID,
	).Scan(
		&w.ID, &w.Name, &w.OwnerID, &w.InviteToken, &w.CreatedAt, &w.Role,
		&w.CanCreateProjects, &w.CanPrivateConversations, &w.CanCreateSkillsPrompts,
		&w.CanCreatePrompts, &w.CanCreateSkills, &w.CanCreateMCP,
		&w.CanUsePrompts, &w.CanUseSkills, &w.CanUseMCP, &w.CanCreateKB,
		&w.CanAddKBFiles, &w.CanDeleteKBContent, &w.CanDeleteConversations,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	w.IsOwner = w.OwnerID == userID
	normalizeWorkspaceCreationAggregate(&w)
	w.InviteToken = ""
	return &w, nil
}

// GetWorkspaceByInviteToken is retained for source compatibility only. The
// historical workspaces.invite_token is an unbounded legacy capability and is
// intentionally never resolved; callers must use workspace_invites records.
func GetWorkspaceByInviteToken(_ context.Context, _ *sql.DB, _ string) (*Workspace, error) {
	return nil, ErrNotFound
}

// ListWorkspacesForUser returns every workspace the user belongs to, with the
// user's role and the member count. Invite tokens are included ONLY for the
// owner (members must not be able to read/leak the link... they could share it
// anyway by joining flow, but least-privilege costs nothing).
func ListWorkspacesForUser(ctx context.Context, db *sql.DB, userID string) ([]Workspace, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.invite_token, w.created_at,
		        CASE WHEN w.owner_id=? THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("m.role")+` END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_projects,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_private_conversations,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_skills_prompts,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_prompts,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_skills,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_mcp,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_use_prompts,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_use_skills,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_use_mcp,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_create_kb,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_add_kb_files,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_delete_kb_content,0) END,
		        CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_delete_conversations,0) END,
		        (SELECT COUNT(*) FROM workspace_members mm WHERE mm.workspace_id=w.id)
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.owner_id=? OR m.user_id=? ORDER BY w.created_at ASC`,
		userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID,
		userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(
			&w.ID, &w.Name, &w.OwnerID, &w.InviteToken, &w.CreatedAt, &w.Role,
			&w.CanCreateProjects, &w.CanPrivateConversations, &w.CanCreateSkillsPrompts,
			&w.CanCreatePrompts, &w.CanCreateSkills, &w.CanCreateMCP,
			&w.CanUsePrompts, &w.CanUseSkills, &w.CanUseMCP, &w.CanCreateKB,
			&w.CanAddKBFiles, &w.CanDeleteKBContent, &w.CanDeleteConversations, &w.MemberCount,
		); err != nil {
			return nil, err
		}
		w.IsOwner = w.OwnerID == userID
		normalizeWorkspaceCreationAggregate(&w)
		// Legacy workspace tokens are never a user-facing capability.
		w.InviteToken = ""
		out = append(out, w)
	}
	return out, rows.Err()
}

// CountOwnedWorkspaces backs the per-group creation cap.
func CountOwnedWorkspaces(ctx context.Context, db *sql.DB, userID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE owner_id=?`, userID).Scan(&n)
	return n, err
}

// IsWorkspaceMember reports membership + normalized role ("" when not a
// member; legacy 'owner' reads as 'admin'). The canonical owner remains
// authoritative even if a legacy owner membership row is missing.
func IsWorkspaceMember(ctx context.Context, db *sql.DB, workspaceID, userID string) (string, error) {
	var role string
	err := db.QueryRowContext(ctx,
		`SELECT CASE WHEN w.owner_id=? THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("m.role")+` END
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`,
		userID, userID, workspaceID, userID, userID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// ListWorkspaceMembers returns members joined with display identity.
func ListWorkspaceMembers(ctx context.Context, db *sql.DB, workspaceID string) ([]WorkspaceMember, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT m.user_id, CASE WHEN w.owner_id=m.user_id THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("m.role")+` END,
		        CASE WHEN w.owner_id=m.user_id THEN 1 ELSE 0 END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_projects END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_private_conversations END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_skills_prompts END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_prompts END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_skills END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_mcp END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_use_prompts END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_use_skills END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_use_mcp END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_kb END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_add_kb_files END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_delete_kb_content END,
		        CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_delete_conversations END,
		        m.joined_at, COALESCE(u.name,''), COALESCE(u.email,''), COALESCE(u.settings,'')
		   FROM workspace_members m
		   JOIN workspaces w ON w.id=m.workspace_id
		   LEFT JOIN users u ON u.id = m.user_id
		  WHERE m.workspace_id=? ORDER BY m.joined_at ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkspaceMember{}
	for rows.Next() {
		var m WorkspaceMember
		var settings string
		if err := rows.Scan(
			&m.UserID, &m.Role, &m.IsOwner,
			&m.CanCreateProjects, &m.CanPrivateConversations, &m.CanCreateSkillsPrompts,
			&m.CanCreatePrompts, &m.CanCreateSkills, &m.CanCreateMCP,
			&m.CanUsePrompts, &m.CanUseSkills, &m.CanUseMCP,
			&m.CanCreateKB, &m.CanAddKBFiles, &m.CanDeleteKBContent, &m.CanDeleteConversations,
			&m.JoinedAt, &m.Name, &m.Email, &settings,
		); err != nil {
			return nil, err
		}
		m.AvatarURL = avatarFromSettings(settings)
		normalizeWorkspaceMemberCreationAggregateFields(&m)
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateWorkspaceMemberPermissions changes the member-wide capability ceiling.
// Actor must be a workspace admin (canonical owner or admin-role member); the
// target must be an ordinary member or guest — admins are not subject to
// member capability limits and only the owner may manage them at all.
func UpdateWorkspaceMemberPermissions(
	ctx context.Context,
	db *sql.DB,
	workspaceID, actorID, memberID string,
	permissions WorkspaceMemberPermissions,
) (*WorkspaceMember, error) {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if permissions.jsonFields != nil {
		var current WorkspaceMemberPermissions
		err := tx.QueryRowContext(ctx, `SELECT
				can_create_projects,can_private_conversations,can_create_skills_prompts,
				can_create_prompts,can_create_skills,can_create_mcp,
				can_use_prompts,can_use_skills,can_use_mcp,can_create_kb,
				can_add_kb_files,can_delete_kb_content,can_delete_conversations
			FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspaceID, memberID).Scan(
			&current.CanCreateProjects, &current.CanPrivateConversations, &current.CanCreateSkillsPrompts,
			&current.CanCreatePrompts, &current.CanCreateSkills, &current.CanCreateMCP,
			&current.CanUsePrompts, &current.CanUseSkills, &current.CanUseMCP, &current.CanCreateKB,
			&current.CanAddKBFiles, &current.CanDeleteKBContent, &current.CanDeleteConversations,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		permissions = permissions.mergeOmittedJSONFields(current)
	}
	// The aggregate column is a compatibility mirror only. Keep it at the
	// intersection of all three independent creation capabilities so an older
	// reader cannot regain MCP/prompt/skill creation that a new caller disabled.
	// Direct Go callers use full-replacement semantics and must populate the
	// granular fields explicitly; JSON PATCH callers were merged above.
	permissions.CanCreateSkillsPrompts = normalizeWorkspaceMemberCreationAggregate(
		permissions.CanCreatePrompts, permissions.CanCreateSkills, permissions.CanCreateMCP,
	)
	res, err := tx.ExecContext(ctx, `UPDATE workspace_members
		SET can_create_projects=?, can_private_conversations=?, can_create_skills_prompts=?,
		    can_create_prompts=?, can_create_skills=?, can_create_mcp=?,
		    can_use_prompts=?, can_use_skills=?, can_use_mcp=?, can_create_kb=?,
		    can_add_kb_files=?, can_delete_kb_content=?, can_delete_conversations=?
		WHERE workspace_id=? AND user_id=?
		  AND NOT EXISTS (SELECT 1 FROM workspaces w
		                  WHERE w.id=workspace_members.workspace_id AND w.owner_id=workspace_members.user_id)
		  AND COALESCE(role,'') NOT IN ('admin','owner')
		  AND EXISTS (SELECT 1 FROM workspaces w WHERE w.id=workspace_members.workspace_id AND (
		      w.owner_id=? OR EXISTS (
		        SELECT 1 FROM workspace_members actor_member
		         WHERE actor_member.workspace_id=w.id AND actor_member.user_id=?
		           AND `+isAdminRoleSQL("actor_member.role")+`
		      )))`,
		boolInt(permissions.CanCreateProjects), boolInt(permissions.CanPrivateConversations),
		boolInt(permissions.CanCreateSkillsPrompts),
		boolInt(permissions.CanCreatePrompts), boolInt(permissions.CanCreateSkills), boolInt(permissions.CanCreateMCP),
		boolInt(permissions.CanUsePrompts), boolInt(permissions.CanUseSkills), boolInt(permissions.CanUseMCP),
		boolInt(permissions.CanCreateKB), boolInt(permissions.CanAddKBFiles),
		boolInt(permissions.CanDeleteKBContent), boolInt(permissions.CanDeleteConversations), workspaceID, memberID, actorID, actorID)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditMemberPermissionsUpdated,
		"workspace_member", memberID, map[string]any{
			"can_create_projects":       permissions.CanCreateProjects,
			"can_private_conversations": permissions.CanPrivateConversations,
			"can_create_skills_prompts": permissions.CanCreateSkillsPrompts,
			"can_create_prompts":        permissions.CanCreatePrompts,
			"can_create_skills":         permissions.CanCreateSkills,
			"can_create_mcp":            permissions.CanCreateMCP,
			"can_use_prompts":           permissions.CanUsePrompts,
			"can_use_skills":            permissions.CanUseSkills,
			"can_use_mcp":               permissions.CanUseMCP,
			"can_create_kb":             permissions.CanCreateKB,
			"can_add_kb_files":          permissions.CanAddKBFiles,
			"can_delete_kb_content":     permissions.CanDeleteKBContent,
			"can_delete_conversations":  permissions.CanDeleteConversations,
		}); err != nil {
		return nil, err
	}
	var member WorkspaceMember
	var settings string
	err = tx.QueryRowContext(ctx, `SELECT m.user_id,
			CASE WHEN w.owner_id=m.user_id THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("m.role")+` END,
			CASE WHEN w.owner_id=m.user_id THEN 1 ELSE 0 END,
			m.can_create_projects,m.can_private_conversations,m.can_create_skills_prompts,
			m.can_create_prompts,m.can_create_skills,m.can_create_mcp,
			m.can_use_prompts,m.can_use_skills,m.can_use_mcp,
			m.can_create_kb,m.can_add_kb_files,m.can_delete_kb_content,m.can_delete_conversations,m.joined_at,
			COALESCE(u.name,''),COALESCE(u.email,''),COALESCE(u.settings,'')
		FROM workspace_members m
		JOIN workspaces w ON w.id=m.workspace_id
		LEFT JOIN users u ON u.id=m.user_id
		WHERE m.workspace_id=? AND m.user_id=?`, workspaceID, memberID).Scan(
		&member.UserID, &member.Role, &member.IsOwner,
		&member.CanCreateProjects, &member.CanPrivateConversations, &member.CanCreateSkillsPrompts,
		&member.CanCreatePrompts, &member.CanCreateSkills, &member.CanCreateMCP,
		&member.CanUsePrompts, &member.CanUseSkills, &member.CanUseMCP,
		&member.CanCreateKB, &member.CanAddKBFiles, &member.CanDeleteKBContent, &member.CanDeleteConversations,
		&member.JoinedAt, &member.Name, &member.Email, &settings,
	)
	if err != nil {
		return nil, err
	}
	member.AvatarURL = avatarFromSettings(settings)
	normalizeWorkspaceMemberCreationAggregateFields(&member)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &member, nil
}

// ValidWorkspaceMemberRole reports whether role is a writable membership role.
func ValidWorkspaceMemberRole(role string) bool {
	return role == WorkspaceRoleAdmin || role == WorkspaceRoleMember || role == WorkspaceRoleGuest
}

// UpdateWorkspaceMemberRole applies the §workspace RBAC role-change ladder under
// the workspace membership lock:
//
//	owner:          admin <-> member <-> guest on OTHERS (never self)
//	ordinary admin: member <-> guest only (never touches admins or self)
//	member / guest: no role management
//
// The canonical owner row can never be re-roled. Returns the updated member.
func UpdateWorkspaceMemberRole(
	ctx context.Context,
	db *sql.DB,
	workspaceID, actorID, memberID, newRole string,
) (*WorkspaceMember, error) {
	if !ValidWorkspaceMemberRole(newRole) {
		return nil, errors.New("invalid role")
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var workspaceOwnerID string
	var actorIsOwner bool
	var actorRole string
	if err := tx.QueryRowContext(ctx,
		`SELECT w.owner_id, w.owner_id=?, COALESCE(am.role,'')
		   FROM workspaces w
		   LEFT JOIN workspace_members am ON am.workspace_id=w.id AND am.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR am.user_id=?)`,
		actorID, actorID, workspaceID, actorID, actorID,
	).Scan(&workspaceOwnerID, &actorIsOwner, &actorRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	actorAdmin := actorIsOwner || actorRole == "admin" || actorRole == "owner"
	if !actorAdmin {
		return nil, ErrForbidden
	}
	if memberID == workspaceOwnerID {
		return nil, ErrForbidden // the owner row is fixed
	}

	var targetRole string
	if err := tx.QueryRowContext(ctx,
		`SELECT `+normalizeWorkspaceRoleSQL("role")+` FROM workspace_members
		  WHERE workspace_id=? AND user_id=?`, workspaceID, memberID,
	).Scan(&targetRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Ordinary admins may only flip ordinary users between member and guest.
	// Promoting to admin, demoting an admin, or re-roling an admin is
	// owner-exclusive.
	if !actorIsOwner && (targetRole == "admin" || newRole == WorkspaceRoleAdmin) {
		return nil, ErrForbidden
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`,
		newRole, workspaceID, memberID); err != nil {
		return nil, err
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditMemberRoleUpdated,
		"workspace_member", memberID, map[string]any{"from": targetRole, "to": newRole}); err != nil {
		return nil, err
	}

	var member WorkspaceMember
	var settings string
	if err := tx.QueryRowContext(ctx, `SELECT m.user_id,
			CASE WHEN w.owner_id=m.user_id THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("m.role")+` END,
			CASE WHEN w.owner_id=m.user_id THEN 1 ELSE 0 END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_projects END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_private_conversations END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_skills_prompts END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_prompts END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_skills END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_mcp END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_use_prompts END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_use_skills END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_use_mcp END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_create_kb END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_add_kb_files END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_delete_kb_content END,
			CASE WHEN w.owner_id=m.user_id OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE m.can_delete_conversations END,
			m.joined_at,
			COALESCE(u.name,''),COALESCE(u.email,''),COALESCE(u.settings,'')
		FROM workspace_members m
		JOIN workspaces w ON w.id=m.workspace_id
		LEFT JOIN users u ON u.id=m.user_id
		WHERE m.workspace_id=? AND m.user_id=?`, workspaceID, memberID).Scan(
		&member.UserID, &member.Role, &member.IsOwner,
		&member.CanCreateProjects, &member.CanPrivateConversations, &member.CanCreateSkillsPrompts,
		&member.CanCreatePrompts, &member.CanCreateSkills, &member.CanCreateMCP,
		&member.CanUsePrompts, &member.CanUseSkills, &member.CanUseMCP,
		&member.CanCreateKB, &member.CanAddKBFiles, &member.CanDeleteKBContent, &member.CanDeleteConversations,
		&member.JoinedAt, &member.Name, &member.Email, &settings,
	); err != nil {
		return nil, err
	}
	member.AvatarURL = avatarFromSettings(settings)
	normalizeWorkspaceMemberCreationAggregateFields(&member)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &member, nil
}

// JoinWorkspace adds userID as a member (idempotent — re-joining is a no-op).
func JoinWorkspace(ctx context.Context, db *sql.DB, workspaceID, userID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		return err
	}
	joined, err := joinWorkspaceWithRoleTx(ctx, tx, workspaceID, userID, WorkspaceRoleMember)
	if err != nil {
		return err
	}
	if joined {
		if err := recordWorkspaceAudit(ctx, tx, workspaceID, userID, AuditMemberJoined,
			"workspace_member", userID, map[string]any{"role": WorkspaceRoleMember, "source": "direct"}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// JoinWorkspaceByInviteToken is retained for source compatibility only. The
// permanent legacy workspace token is deliberately not consumable; callers
// must use JoinWorkspaceByInviteRecord.
func JoinWorkspaceByInviteToken(_ context.Context, _ *sql.DB, _ string, _ string) (*Workspace, error) {
	return nil, ErrNotFound
}

func joinWorkspaceTx(ctx context.Context, tx *sql.Tx, workspaceID, userID string) error {
	_, err := joinWorkspaceWithRoleTx(ctx, tx, workspaceID, userID, WorkspaceRoleMember)
	return err
}

// joinWorkspaceWithRoleTx inserts the membership row with the granted role and
// reports whether a new membership was created (an existing member re-joining
// is a no-op that grants nothing).
func joinWorkspaceWithRoleTx(ctx context.Context, tx *sql.Tx, workspaceID, userID, role string) (bool, error) {
	if !ValidWorkspaceMemberRole(role) {
		return false, errors.New("invalid role")
	}
	var ownerStatus, joiningStatus string
	if err := tx.QueryRowContext(ctx,
		`SELECT owner.status, joining_user.status
		   FROM workspaces w
		   JOIN users owner ON owner.id=w.owner_id
		   JOIN users joining_user ON joining_user.id=?
		  WHERE w.id=?`, userID, workspaceID,
	).Scan(&ownerStatus, &joiningStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if ownerStatus != "active" || joiningStatus != "active" {
		return false, ErrNotFound
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_members(workspace_id, user_id, role) VALUES(?, ?, ?)
		 ON CONFLICT(workspace_id, user_id) DO NOTHING`, workspaceID, userID, role)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// LeaveWorkspace removes a member. The owner cannot leave — they must delete
// the workspace instead (there is no ownership transfer).
func LeaveWorkspace(ctx context.Context, db *sql.DB, workspaceID, userID string) error {
	_, err := LeaveWorkspaceWithRevokedGenerations(ctx, db, workspaceID, userID)
	return err
}

// LeaveWorkspaceWithRevokedGenerations returns the assistant message ids that
// were terminalized in the membership transaction. The API uses those ids as
// immutable generation epochs: their cache streams are tombstoned before the
// leave response is acknowledged, and a later rejoin cannot revive them.
func LeaveWorkspaceWithRevokedGenerations(ctx context.Context, db *sql.DB, workspaceID, userID string) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_shares
		  WHERE user_id=? AND EXISTS (
		    SELECT 1 FROM conversations c
		     WHERE c.id=conversation_shares.conversation_id AND c.workspace_id=?
		  )`, userID, workspaceID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_kb_member_permissions
		WHERE user_id=? AND EXISTS (
			SELECT 1 FROM knowledge_bases k
			 WHERE k.id=workspace_kb_member_permissions.kb_id AND k.workspace_id=?
		)`, userID, workspaceID); err != nil {
		return nil, err
	}
	revokedMessageIDs, err := scrubWorkspaceUserStreamingMessagesTx(ctx, tx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM workspace_members
		  WHERE workspace_id=? AND user_id=? AND role<>'owner'
		    AND NOT EXISTS (
		      SELECT 1 FROM workspaces w
		       WHERE w.id=workspace_members.workspace_id AND w.owner_id=workspace_members.user_id
		    )`, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, userID, AuditMemberLeft,
		"workspace_member", userID, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return revokedMessageIDs, nil
}

// RemoveWorkspaceMember is the admin kick. The owner row itself is protected.
func RemoveWorkspaceMember(ctx context.Context, db *sql.DB, workspaceID, actorID, memberID string) error {
	_, err := RemoveWorkspaceMemberWithRevokedGenerations(ctx, db, workspaceID, actorID, memberID)
	return err
}

// RemoveWorkspaceMemberWithRevokedGenerations is the cache-aware kick. Actor
// must be a workspace admin; only the canonical owner may remove another
// admin. See LeaveWorkspaceWithRevokedGenerations for the returned id contract.
func RemoveWorkspaceMemberWithRevokedGenerations(ctx context.Context, db *sql.DB, workspaceID, actorID, memberID string) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		return nil, err
	}
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
	if memberID == workspaceOwnerID {
		return nil, ErrForbidden
	}
	// Only the owner may remove a fellow admin.
	if workspaceOwnerID != actorID {
		var targetRole string
		if err := tx.QueryRowContext(ctx,
			`SELECT `+normalizeWorkspaceRoleSQL("role")+` FROM workspace_members
			  WHERE workspace_id=? AND user_id=?`, workspaceID, memberID,
		).Scan(&targetRole); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if targetRole == WorkspaceRoleAdmin {
			return nil, ErrForbidden
		}
	}
	// Public capability links published by the departing creator stay revoked even
	// if that account later receives a fresh invitation and rejoins.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_shares
		  WHERE user_id=? AND EXISTS (
		    SELECT 1 FROM conversations c
		     WHERE c.id=conversation_shares.conversation_id AND c.workspace_id=?
		  )`, memberID, workspaceID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_kb_member_permissions
		WHERE user_id=? AND EXISTS (
			SELECT 1 FROM knowledge_bases k
			 WHERE k.id=workspace_kb_member_permissions.kb_id AND k.workspace_id=?
		)`, memberID, workspaceID); err != nil {
		return nil, err
	}
	revokedMessageIDs, err := scrubWorkspaceUserStreamingMessagesTx(ctx, tx, workspaceID, memberID)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM workspace_members
		  WHERE workspace_id=? AND user_id=? AND role<>'owner'
		    AND NOT EXISTS (
		      SELECT 1 FROM workspaces w
		       WHERE w.id=workspace_members.workspace_id AND w.owner_id=workspace_members.user_id
		    )`,
		workspaceID, memberID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditMemberRemoved,
		"workspace_member", memberID, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return revokedMessageIDs, nil
}

// RecordWorkspaceDeleted writes the workspace.deleted audit entry AFTER a
// successful teardown. The audit table intentionally has no FK, so the row
// outlives the workspace itself.
func RecordWorkspaceDeleted(ctx context.Context, db *sql.DB, workspaceID, actorID, name string) error {
	return recordWorkspaceAudit(ctx, db, workspaceID, actorID, AuditWorkspaceDeleted,
		"workspace", workspaceID, map[string]any{"name": name})
}

// scrubWorkspaceUserStreamingMessagesTx makes membership revocation durable
// across a later legitimate rejoin. Current membership alone cannot distinguish
// a fresh generation from one started under the previous membership epoch, so
// kick/leave terminalizes every still-streaming placeholder authored by the
// departing principal before removing the membership row.
func scrubWorkspaceUserStreamingMessagesTx(ctx context.Context, tx *sql.Tx, workspaceID, userID string) ([]string, error) {
	return scrubWorkspaceStreamingMessagesTx(ctx, tx, workspaceID, userID)
}

// ScrubWorkspaceStreamingMessages terminalizes every active generation before
// workspace teardown and returns the message ids whose cache streams must be
// independently revoked. The workspace lock also serializes this sweep with
// scoped generation persistence on PostgreSQL.
func ScrubWorkspaceStreamingMessages(ctx context.Context, db *sql.DB, workspaceID string) ([]string, error) {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	messageIDs, err := scrubWorkspaceStreamingMessagesTx(ctx, tx, workspaceID, "")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return messageIDs, nil
}

func scrubWorkspaceStreamingMessagesTx(ctx context.Context, tx *sql.Tx, workspaceID, userID string) ([]string, error) {
	query := `SELECT messages.id
		  FROM messages JOIN conversations revoked_generation_conversation
		    ON revoked_generation_conversation.id=messages.conversation_id
		 WHERE messages.role='assistant' AND messages.status='streaming'
		   AND revoked_generation_conversation.workspace_id=?`
	args := []any{workspaceID}
	if userID != "" {
		query += ` AND COALESCE(messages.author_id,'')=?`
		args = append(args, userID)
	}
	query += ` ORDER BY messages.id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	messageIDs := []string{}
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		messageIDs = append(messageIDs, messageID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(messageIDs) == 0 {
		return messageIDs, nil
	}
	updateArgs := anySlice(messageIDs)
	_, err = tx.ExecContext(ctx,
		`UPDATE messages
		    SET blocks='[]', raw=NULL, citations='[]', stop_reason='stopped',
		        input_tokens=0, output_tokens=0, cache_read_tokens=0, cache_write_tokens=0,
		        cost=0, credits=0, status='stopped', error='', gen_ms=0,
		        verify='', search_text=''
		  WHERE id IN (`+idPlaceholders(len(messageIDs))+`)
		    AND role='assistant' AND status='streaming'`, updateArgs...)
	if err != nil {
		return nil, err
	}
	return messageIDs, nil
}

// lockWorkspaceMembershipTx is the serialization point shared by membership
// revocation and multi-statement workspace mutations. An operation that obtains
// this lock before a kick is allowed to finish; one that obtains it afterwards
// observes the revoked membership and fails closed. This is needed on Postgres,
// where SQLite's database-wide writer lock is not available.
func lockWorkspaceMembershipTx(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	res, err := tx.ExecContext(ctx, `UPDATE workspaces SET id=id WHERE id=?`, workspaceID)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// DeleteWorkspaceRow removes the workspace row itself; member rows cascade via
// FK. Content teardown (conversations/projects/KBs — which needs vector-store
// cleanup) is orchestrated by the HANDLER through the existing per-entity
// deleters, then this finishes the job.
func MarkWorkspaceDeleting(ctx context.Context, db *sql.DB, workspaceID, expectedOwnerID string) error {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx,
		`UPDATE workspaces SET deleting=1 WHERE id=? AND owner_id=? AND COALESCE(deleting,0)=0`,
		workspaceID, expectedOwnerID,
	)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ClearWorkspaceDeleting makes a failed teardown retryable. The expected owner
// guards against a stale handler clearing a fence after ownership changed.
func ClearWorkspaceDeleting(ctx context.Context, db *sql.DB, workspaceID, expectedOwnerID string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE workspaces SET deleting=0 WHERE id=? AND owner_id=? AND COALESCE(deleting,0)=1`,
		workspaceID, expectedOwnerID,
	)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

func DeleteWorkspaceRow(ctx context.Context, db *sql.DB, workspaceID, expectedOwnerID string) error {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_skills WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_prompts WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	// User MCP rows deliberately keep workspace_id as a plain scope column (the
	// same shape as user skills/prompts), so deleting the workspace cannot rely
	// on an FK cascade. Remove them in this transaction as well: their headers
	// may contain credentials and must not survive as unreachable orphan rows.
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_mcp_servers WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM workspaces WHERE id=? AND owner_id=? AND COALESCE(deleting,0)=1`, workspaceID, expectedOwnerID,
	)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

// WorkspaceContentIDs lists the conversation/project/KB ids belonging to a
// workspace — the handler's teardown worklist for DeleteWorkspace.
func WorkspaceContentIDs(ctx context.Context, db *sql.DB, workspaceID string) (convIDs, projectIDs, kbIDs []string, err error) {
	collect := func(q string) ([]string, error) {
		rows, err := db.QueryContext(ctx, q, workspaceID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}
	if convIDs, err = collect(`SELECT id FROM conversations WHERE workspace_id=?`); err != nil {
		return
	}
	if projectIDs, err = collect(`SELECT id FROM projects WHERE workspace_id=?`); err != nil {
		return
	}
	// Project libraries are deleted through DeleteProjectWithState so its exact
	// vector and storage cleanup worklist is preserved. The standalone loop must
	// not try (and fail) to delete them through DeleteKB first.
	kbIDs, err = collect(`SELECT id FROM knowledge_bases WHERE workspace_id=? AND ` + standaloneKnowledgeBasePredicate("knowledge_bases"))
	return
}

// ListAllWorkspaces is the admin listing (owner identity + member count).
func ListAllWorkspaces(ctx context.Context, db *sql.DB, limit, offset int) ([]Workspace, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := db.QueryContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.created_at, COALESCE(u.name,''),
		        (SELECT COUNT(*) FROM workspace_members m WHERE m.workspace_id=w.id)
		   FROM workspaces w LEFT JOIN users u ON u.id = w.owner_id
		  ORDER BY w.created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.OwnerID, &w.CreatedAt, &w.OwnerName, &w.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UserIdentity is a display-only projection of a user (name + avatar) used to
// label message authors and sidebar rows (§workspaces).
type UserIdentity struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// UserIdentities resolves a set of user ids to display identities in one query.
func UserIdentities(ctx context.Context, db *sql.DB, ids []string) (map[string]UserIdentity, error) {
	out := map[string]UserIdentity{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(name,''), COALESCE(settings,'') FROM users WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, settings string
		if err := rows.Scan(&id, &name, &settings); err != nil {
			return nil, err
		}
		out[id] = UserIdentity{Name: name, AvatarURL: avatarFromSettings(settings)}
	}
	return out, rows.Err()
}
