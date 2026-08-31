package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

// §workspace RBAC phase 4 — workspace capability policy enforcement. Every
// helper here RE-CHECKS the policy at execution time (never only at catalog
// load) and fails closed on database errors. The policy can only narrow what
// the platform offers; guests are unaffected because their read-only
// boundary is role-based.

// enforceWorkspaceTurnPolicy validates a generation turn against the
// conversation's workspace policy: model allowlist, direct drawing,
// attachments, knowledge bases and the member monthly credit limit. A nil
// conversation or personal conversation passes untouched. Direct drawing is
// intentionally separate from tool-calling image generation; the latter is
// covered by AllowToolCalling at the tool boundary.
func enforceWorkspaceTurnPolicy(
	ctx context.Context,
	db *sql.DB,
	conv *store.Conversation,
	userID string,
	effectiveModel *store.Model,
	attachmentCount int,
	knowledgeBaseUsed bool,
) error {
	if conv == nil || conv.WorkspaceID == "" {
		return nil
	}
	policy, err := store.GetWorkspacePolicy(ctx, db, conv.WorkspaceID)
	if err != nil {
		return err // fail closed on lookup errors
	}
	if effectiveModel != nil {
		if !policy.ModelAllowedByPolicy(effectiveModel.ID) {
			return errWorkspaceModelDisabled
		}
		if effectiveModel.Kind == "image" && !policy.AllowDrawing {
			return errWorkspaceImageDisabled
		}
	}
	if attachmentCount > 0 && !policy.AllowFileUpload {
		return errWorkspaceFileUploadDisabled
	}
	if knowledgeBaseUsed && !policy.AllowKnowledgeBases {
		return errWorkspaceKnowledgeBaseDisabled
	}
	if policy.MemberMonthlyCreditLimit > 0 {
		used, usageErr := store.WorkspaceMemberMonthlyUsage(ctx, db, conv.WorkspaceID, userID)
		if usageErr != nil {
			return usageErr
		}
		if used >= policy.MemberMonthlyCreditLimit {
			return errWorkspaceCreditLimitExceeded
		}
	}
	return nil
}

// enforceWorkspaceConversationModelPolicy applies the model-related subset of
// the turn policy when a conversation stores or inherits a model selection.
// Keeping invalid state out of the conversation avoids a picker that appears to
// accept a model only for the subsequent message request to fail. Credit limits
// and content capabilities intentionally remain turn-time checks.
func enforceWorkspaceConversationModelPolicy(
	ctx context.Context,
	db *sql.DB,
	workspaceID string,
	model *store.Model,
) error {
	if workspaceID == "" || model == nil {
		return nil
	}
	policy, err := store.GetWorkspacePolicy(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	if !policy.ModelAllowedByPolicy(model.ID) {
		return errWorkspaceModelDisabled
	}
	if model.Kind == "image" && !policy.AllowDrawing {
		return errWorkspaceImageDisabled
	}
	return nil
}

// enforceWorkspaceKnowledgeBasePolicy rejects KB-scoped mutations when the
// workspace has knowledge bases disabled. Fail closed on lookup errors.
func enforceWorkspaceKnowledgeBasePolicy(ctx context.Context, db *sql.DB, workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	policy, err := store.GetWorkspacePolicy(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	if !policy.AllowKnowledgeBases {
		return errWorkspaceKnowledgeBaseDisabled
	}
	return nil
}

// enforceWorkspaceFileUploadPolicy applies the workspace upload switch to
// every document surface, including knowledge-base and project libraries.
// Conversation attachments already pass through enforceWorkspaceTurnPolicy.
func enforceWorkspaceFileUploadPolicy(ctx context.Context, db *sql.DB, workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	policy, err := store.GetWorkspacePolicy(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	if !policy.AllowFileUpload {
		return errWorkspaceFileUploadDisabled
	}
	return nil
}

// workspacePolicyErrorStatus maps the policy errors onto HTTP statuses.
func workspacePolicyErrorStatus(err error) int {
	if errors.Is(err, errWorkspaceCreditLimitExceeded) ||
		errors.Is(err, errWorkspaceModelDisabled) ||
		errors.Is(err, errWorkspaceImageDisabled) ||
		errors.Is(err, errWorkspaceFileUploadDisabled) ||
		errors.Is(err, errWorkspaceKnowledgeBaseDisabled) {
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

// applyWorkspaceToolPolicy folds workspace capability switches into the
// group-derived tool policy. Official tool/MCP allowlists stay in
// WorkspacePolicy and are applied by the catalog, orchestrator (including
// fallback), and runtime registry. Mixing those official ids into Mode/IDs
// would incorrectly exclude user-owned MCP servers, which use a separate
// namespace and scoped ownership checks. Tool calling is the sole workspace
// gate for every tool; AllowDrawing only governs direct image-model turns.
func applyWorkspaceToolPolicy(
	base *llm.ToolAccessPolicy,
	policy store.WorkspacePolicy,
) *llm.ToolAccessPolicy {
	if base == nil {
		return nil
	}
	merged := *base
	merged.IDs = append([]string(nil), base.IDs...)
	merged.DenyIDs = append([]string(nil), base.DenyIDs...)
	// Workspace capability switches are hard ceilings. Mark them configured even
	// when the stored value is false so the orchestrator cannot mistake an
	// explicit shutdown for an omitted field from an older caller.
	baseToolCallingAllowed := !base.ToolCallingConfigured || base.AllowToolCalling
	merged.AllowToolCalling = baseToolCallingAllowed && policy.AllowToolCalling
	merged.ToolCallingConfigured = true
	baseMCPAllowed := !base.MCPConfigured || base.AllowMCP
	merged.AllowMCP = baseMCPAllowed && policy.AllowMCP
	merged.MCPConfigured = true
	merged.AllowDrawing = base.AllowDrawing && policy.AllowDrawing
	merged.AllowSkills = base.AllowSkills && policy.AllowSkills
	if !policy.AllowToolCalling {
		merged.Mode = store.ResourceAccessNone
		merged.IDs = nil
	}
	if !policy.AllowSkills {
		merged.SkillMode = store.ResourceAccessNone
		merged.SkillIDs = nil
	}
	return &merged
}

// workspaceTurnToolPolicy resolves the effective tool policy for a workspace
// turn (group ceiling ∩ workspace policy). Personal turns return the base
// policy unchanged.
func workspaceTurnToolPolicy(
	ctx context.Context,
	db *sql.DB,
	conv *store.Conversation,
	base *llm.ToolAccessPolicy,
) *llm.ToolAccessPolicy {
	if conv == nil || conv.WorkspaceID == "" {
		return base
	}
	policy, err := store.GetWorkspacePolicy(ctx, db, conv.WorkspaceID)
	if err != nil {
		// Fail closed: an unreadable policy denies every tool.
		denied := &llm.ToolAccessPolicy{
			Mode:                  store.ResourceAccessNone,
			AllowToolCalling:      false,
			ToolCallingConfigured: true,
			AllowMCP:              false,
			MCPConfigured:         true,
			AllowDrawing:          false,
			AllowMemory:           false,
			AllowSkills:           false,
			SkillMode:             store.ResourceAccessNone,
		}
		return denied
	}
	return applyWorkspaceToolPolicy(base, policy)
}
