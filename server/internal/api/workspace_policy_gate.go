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
// conversation's workspace policy: model allowlist, image generation,
// attachments, knowledge bases and the member monthly credit limit. A nil
// conversation or personal conversation passes untouched.
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
		if effectiveModel.Kind == "image" && !policy.AllowImageGeneration {
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

// applyWorkspaceToolPolicy folds the workspace tool/MCP allowlists and the
// sandbox / image switches into the group-derived tool policy. The base
// policy stays the hard ceiling; the workspace can only narrow it.
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

	if wsAllowed := append(append([]string(nil), policy.AllowedToolIDs...), policy.AllowedMCPServerIDs...); len(wsAllowed) > 0 {
		if merged.Mode == "" || merged.Mode == store.ResourceAccessAll {
			merged.Mode = store.ResourceAccessSelected
			merged.IDs = wsAllowed
		} else if merged.Mode == store.ResourceAccessSelected {
			allowed := map[string]bool{}
			for _, id := range wsAllowed {
				allowed[id] = true
			}
			intersection := make([]string, 0, len(merged.IDs))
			for _, id := range merged.IDs {
				if allowed[id] {
					intersection = append(intersection, id)
				}
			}
			merged.IDs = intersection
		}
	}
	if !policy.AllowSandbox {
		merged.DenyIDs = append(merged.DenyIDs, "builtin:python_execute", "builtin:fetch_image", "builtin:code_interpreter")
	}
	if !policy.AllowImageGeneration {
		merged.DenyIDs = append(merged.DenyIDs, "builtin:image_generate", "hosted:image_generation")
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
		denied := &llm.ToolAccessPolicy{Mode: store.ResourceAccessNone}
		return denied
	}
	return applyWorkspaceToolPolicy(base, policy)
}
