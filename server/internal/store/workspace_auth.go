package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Unified workspace authorizer (§workspace RBAC). One backend entry point
// answers "may userID perform action on resource inside workspaceID". The
// authorizer DEFAULTS TO DENY: missing membership, unknown actions, malformed
// roles and database errors all fail closed. Resource mutations additionally
// re-authorize inside their store transactions through the SQL predicates in
// workspaces.go/kbs.go — this entry point covers handler-level gates and
// member-management endpoints.

// WorkspaceAuthorizationRequest names the actor, the target workspace and the
// action being attempted. Resource/ResourceID are optional; member-management
// actions pass Resource="workspace_member", ResourceID=<target user id> and,
// for role updates, NewRole (admin/member/guest).
type WorkspaceAuthorizationRequest struct {
	WorkspaceID string
	UserID      string
	Action      string
	Resource    string
	ResourceID  string
	NewRole     string
}

// WorkspaceAuthorizationDecision explains why an action was allowed or denied.
type WorkspaceAuthorizationDecision struct {
	Allowed   bool
	IsOwner   bool
	IsAdmin   bool
	IsMember  bool
	IsGuest   bool
	IsCreator bool
	IsPublic  bool
	Role      string
	Reason    string
}

// Workspace action identifiers (§workspace RBAC).
const (
	ActionWorkspaceSettingsUpdate    = "workspace.settings.update"
	ActionWorkspaceMemberView        = "workspace.member.view"
	ActionWorkspaceMemberInvite      = "workspace.member.invite"
	ActionWorkspaceMemberRemove      = "workspace.member.remove"
	ActionWorkspaceMemberPermissions = "workspace.member.permissions.update"
	ActionWorkspaceMemberRoleUpdate  = "workspace.member.role.update"
	ActionWorkspaceDelete            = "workspace.delete"
	ActionWorkspaceTransfer          = "workspace.transfer"

	ActionConversationCreate           = "conversation.create"
	ActionConversationRead             = "conversation.read"
	ActionConversationReply            = "conversation.reply"
	ActionConversationMetadataUpdate   = "conversation.metadata.update"
	ActionConversationVisibilityUpdate = "conversation.visibility.update"
	ActionConversationDelete           = "conversation.delete"

	ActionProjectCreate           = "project.create"
	ActionProjectRead             = "project.read"
	ActionProjectUpdate           = "project.update"
	ActionProjectVisibilityUpdate = "project.visibility.update"
	ActionProjectDelete           = "project.delete"

	ActionKnowledgeBaseCreate            = "knowledge_base.create"
	ActionKnowledgeBaseRead              = "knowledge_base.read"
	ActionKnowledgeBaseDocumentAdd       = "knowledge_base.document.add"
	ActionKnowledgeBaseDocumentDeleteOwn = "knowledge_base.document.delete_own"
	ActionKnowledgeBaseDocumentDeleteAny = "knowledge_base.document.delete_any"
	ActionKnowledgeBaseUpdate            = "knowledge_base.update"
	ActionKnowledgeBaseVisibilityUpdate  = "knowledge_base.visibility.update"
	ActionKnowledgeBaseDelete            = "knowledge_base.delete"

	ActionModelUse      = "model.use"
	ActionToolUse       = "tool.use"
	ActionMCPUse        = "mcp.use"
	ActionSandboxUse    = "sandbox.use"
	ActionImageGenerate = "image.generate"

	ActionUsageView          = "usage.view"
	ActionWorkspaceAuditView = "workspace.audit.view"
)

// workspaceAuthorizationContext carries everything the action table needs.
type workspaceAuthorizationContext struct {
	decision WorkspaceAuthorizationDecision
	// targetRole is the normalized role of the ResourceID member for member
	// actions ("" when the target is absent).
	targetRole string
	// targetIsOwner marks the target member as the canonical workspace owner.
	targetIsOwner bool
}

// loadWorkspaceAuthorizationContext resolves actor identity plus, for member
// actions, the target member's role. Any lookup failure returns an error so
// callers fail closed.
func loadWorkspaceAuthorizationContext(ctx context.Context, db *sql.DB, req WorkspaceAuthorizationRequest) (workspaceAuthorizationContext, error) {
	if strings.TrimSpace(req.WorkspaceID) == "" || strings.TrimSpace(req.UserID) == "" {
		return workspaceAuthorizationContext{}, errors.New("workspace authorization: missing ids")
	}
	var ownerID, role, target string
	var targetIsOwner bool
	err := db.QueryRowContext(ctx,
		`SELECT w.owner_id,
		        CASE WHEN w.owner_id=? THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("m.role")+` END,
		        COALESCE((
		          SELECT CASE WHEN t.user_id=w.owner_id THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("t.role")+` END
		          FROM workspace_members t
		         WHERE t.workspace_id=w.id AND t.user_id=?
		        ),''),
		        COALESCE((
		          SELECT t.user_id=w.owner_id
		          FROM workspace_members t
		         WHERE t.workspace_id=w.id AND t.user_id=?
		        ),FALSE)
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`,
		req.UserID, req.ResourceID, req.ResourceID, req.UserID, req.WorkspaceID, req.UserID, req.UserID,
	).Scan(&ownerID, &role, &target, &targetIsOwner)
	if err != nil {
		return workspaceAuthorizationContext{}, err
	}
	d := WorkspaceAuthorizationDecision{IsOwner: ownerID == req.UserID, Role: role}
	switch role {
	case WorkspaceRoleAdmin:
		d.IsAdmin = true
		d.IsMember = true
	case WorkspaceRoleMember, WorkspaceRoleGuest:
		// Guests are members too — read-only ones (see IsGuest/isCollaborator).
		d.IsMember = true
		if role == WorkspaceRoleGuest {
			d.IsGuest = true
		}
	case "":
		return workspaceAuthorizationContext{decision: d}, nil
	default:
		// Unknown role value: fail closed by treating as the weakest role.
		d.IsMember = true
		d.IsGuest = true
		d.Role = WorkspaceRoleGuest
	}
	return workspaceAuthorizationContext{decision: d, targetRole: target, targetIsOwner: targetIsOwner}, nil
}

// workspaceResourceScope resolves creator + visibility for a resource row.
// Supported: conversation / project / knowledge_base.
func workspaceResourceScope(ctx context.Context, db *sql.DB, resource, resourceID string) (workspaceID, creatorID string, isPublic bool, err error) {
	switch resource {
	case "conversation":
		err = db.QueryRowContext(ctx,
			`SELECT COALESCE(workspace_id,''), user_id, COALESCE(is_public,0) FROM conversations WHERE id=?`, resourceID,
		).Scan(&workspaceID, &creatorID, &isPublic)
	case "project":
		err = db.QueryRowContext(ctx,
			`SELECT COALESCE(workspace_id,''), user_id, COALESCE(is_public,1) FROM projects WHERE id=?`, resourceID,
		).Scan(&workspaceID, &creatorID, &isPublic)
	case "knowledge_base":
		err = db.QueryRowContext(ctx,
			`SELECT COALESCE(workspace_id,''), user_id, COALESCE(is_public,1) FROM knowledge_bases WHERE id=?`, resourceID,
		).Scan(&workspaceID, &creatorID, &isPublic)
	case "":
		return "", "", false, nil
	default:
		return "", "", false, errors.New("workspace authorization: unknown resource " + resource)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, ErrNotFound
	}
	return workspaceID, creatorID, isPublic, err
}

// AuthorizeWorkspace is the single backend authorization entry point for
// workspace-scoped actions. It never fails open: errors deny with Reason set.
func AuthorizeWorkspace(
	ctx context.Context,
	db *sql.DB,
	req WorkspaceAuthorizationRequest,
) (WorkspaceAuthorizationDecision, error) {
	actx, err := loadWorkspaceAuthorizationContext(ctx, db, req)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkspaceAuthorizationDecision{Reason: "workspace not found"}, nil
		}
		return WorkspaceAuthorizationDecision{Reason: "authorization lookup failed"}, err
	}
	d := actx.decision
	if !d.IsMember && !d.IsAdmin {
		d.Reason = "not a workspace member"
		return d, nil
	}

	// Resource actions verify the resource exists inside the named workspace
	// before the role table applies (uniform 404 semantics, no enumeration).
	if req.Resource == "conversation" || req.Resource == "project" || req.Resource == "knowledge_base" {
		if strings.TrimSpace(req.ResourceID) == "" {
			return WorkspaceAuthorizationDecision{Reason: "missing resource id"}, nil
		}
		resourceWorkspace, creatorID, isPublic, rerr := workspaceResourceScope(ctx, db, req.Resource, req.ResourceID)
		if rerr != nil {
			if errors.Is(rerr, ErrNotFound) {
				return WorkspaceAuthorizationDecision{Reason: "resource not found"}, nil
			}
			return WorkspaceAuthorizationDecision{Reason: "resource lookup failed"}, rerr
		}
		if resourceWorkspace != req.WorkspaceID {
			return WorkspaceAuthorizationDecision{Reason: "resource not found"}, nil
		}
		d.IsCreator = creatorID == req.UserID
		d.IsPublic = isPublic
	}

	allowed := false
	reason := ""
	isAdmin := d.IsAdmin
	isCollaborator := d.IsMember && !d.IsGuest // admin, member; NOT guest
	targetMissing := req.ResourceID != "" && actx.targetRole == ""
	targetAdmin := actx.targetIsOwner || actx.targetRole == WorkspaceRoleAdmin

	switch req.Action {
	// --- workspace management ----------------------------------------------
	case ActionWorkspaceMemberView:
		allowed = true // every current member (guest included) may list
	case ActionWorkspaceMemberInvite:
		allowed = isAdmin
		reason = "only workspace admins may manage invites"
	case ActionWorkspaceMemberRemove:
		// Nobody removes the owner; only the owner removes fellow admins;
		// any admin removes ordinary members and guests.
		allowed = isAdmin && !targetMissing && !actx.targetIsOwner && (!targetAdmin || d.IsOwner)
		reason = "only the owner may remove workspace admins"
	case ActionWorkspaceMemberPermissions:
		allowed = isAdmin && !targetMissing && !targetAdmin
		reason = "capability limits apply to ordinary members only"
	case ActionWorkspaceMemberRoleUpdate:
		// §12 ladder: the owner re-roles anyone else; ordinary admins only
		// member<->guest; nobody re-roles the owner, an admin, or themselves.
		switch {
		case targetMissing || actx.targetIsOwner || req.ResourceID == req.UserID:
			reason = "this member's role cannot be changed"
		case !d.IsOwner && targetAdmin:
			reason = "only the owner may change an admin's role"
		case !d.IsOwner && req.NewRole == WorkspaceRoleAdmin:
			reason = "only the owner may grant the admin role"
		case d.IsOwner || isAdmin:
			allowed = ValidWorkspaceMemberRole(req.NewRole)
			if !allowed {
				reason = "invalid role"
			}
		default:
			reason = "only workspace admins may change roles"
		}
	case ActionWorkspaceSettingsUpdate, ActionUsageView, ActionWorkspaceAuditView:
		allowed = isAdmin
		reason = "only workspace admins may manage settings or usage"
	case ActionWorkspaceDelete, ActionWorkspaceTransfer:
		allowed = d.IsOwner
		reason = "owner-exclusive operation"

	// --- conversations ------------------------------------------------------
	case ActionConversationCreate,
		ActionModelUse, ActionToolUse, ActionMCPUse, ActionSandboxUse, ActionImageGenerate:
		allowed = isCollaborator
		reason = "guests have read-only access"
	case ActionConversationReply:
		// Replying to a SPECIFIC conversation requires read access: private
		// conversations stay with their creator and workspace admins (mirrors
		// the store boundary). Unscoped asks only the role question.
		allowed = isCollaborator
		reason = "guests cannot reply"
		if req.Resource == "conversation" && !d.IsCreator && !d.IsPublic {
			allowed = isAdmin
			reason = "private conversations exclude other members"
		}
	case ActionConversationRead:
		allowed = isAdmin || d.IsPublic || d.IsCreator
		reason = "private conversations are visible to their creator and admins"
	case ActionConversationMetadataUpdate, ActionConversationVisibilityUpdate, ActionConversationDelete:
		allowed = isAdmin || (isCollaborator && d.IsCreator)
		reason = "only the creator or a workspace admin may manage this conversation"

	// --- projects & knowledge bases ----------------------------------------
	case ActionProjectCreate, ActionKnowledgeBaseCreate, ActionKnowledgeBaseDocumentAdd:
		allowed = isCollaborator
		reason = "guests cannot create or modify workspace resources"
	case ActionProjectRead, ActionKnowledgeBaseRead:
		// §workspace RBAC phase 2: shared rows serve every member (guests
		// included); private rows only their creator and workspace admins.
		allowed = isAdmin || d.IsCreator || d.IsPublic
		reason = "private resources are visible to their creator and admins only"
	case ActionProjectUpdate, ActionProjectVisibilityUpdate, ActionProjectDelete,
		ActionKnowledgeBaseUpdate, ActionKnowledgeBaseVisibilityUpdate, ActionKnowledgeBaseDelete:
		allowed = isAdmin || (isCollaborator && d.IsCreator)
		reason = "only the creator or a workspace admin may manage this resource"
	case ActionKnowledgeBaseDocumentDeleteAny:
		allowed = isAdmin || (isCollaborator && d.IsCreator)
		reason = "only the creator or a workspace admin may delete other members' documents"
	case ActionKnowledgeBaseDocumentDeleteOwn:
		allowed = isCollaborator
		reason = "guests cannot delete content"

	default:
		return WorkspaceAuthorizationDecision{Reason: "unknown action " + req.Action}, nil
	}

	d.Allowed = allowed
	if !allowed && d.Reason == "" {
		d.Reason = reason
	}
	return d, nil
}
