package api

import "errors"

var (
	errAuthRequired = errors.New("auth required")
	// Stable machine code — the client matches this exact string to show the
	// "your account has been suspended" notice + sign the user out.
	errAccountBlocked = errors.New("account_suspended")
	errSessionExpired = errors.New("session expired, please log in again")
	errAdminOnly      = errors.New("admin only")
	errInvalidInput   = errors.New("invalid input")
	errNotFound       = errors.New("not found")
	// Authenticated but lacking the required workspace role (§workspace RBAC).
	errForbidden = errors.New("forbidden")

	errUploadRateLimited = errors.New("upload rate limit exceeded — try again shortly")

	// Registration anti-abuse. Stable machine codes — the client matches these to
	// refresh the captcha / show the per-network limit notice.
	errCaptcha         = errors.New("captcha_failed")
	errRegisterIPLimit = errors.New("register_ip_limit")

	// Per-group resource caps (§ user groups). Stable machine codes the client
	// maps to a localized "you've reached your plan's limit" notice.
	errProjectLimit = errors.New("project_limit_reached")
	errKBLimit      = errors.New("kb_limit_reached")

	// Knowledge-base retrieval is configured by administrators. User-facing
	// APIs deliberately expose stable capability errors instead of model names,
	// dimensions, or other indexing implementation details.
	errKnowledgeBaseUnavailable            = errors.New("knowledge_base_unavailable")
	errKnowledgeBaseSelectionIncompatible  = errors.New("knowledge_base_selection_incompatible")
	errKnowledgeBaseGroupPermission        = errors.New("knowledge_base_group_permission_required")
	errPromptGroupPermission               = errors.New("prompt_group_permission_required")
	errSkillGroupPermission                = errors.New("skill_group_permission_required")
	errToolGroupPermission                 = errors.New("tool_group_permission_required")
	errSharingGroupPermission              = errors.New("sharing_group_permission_required")
	errKnowledgeBaseSharingGroupPermission = errors.New("knowledge_base_sharing_group_permission_required")
	errFileUploadGroupPermission           = errors.New("file_upload_group_permission_required")
	errConversationExportGroupPermission   = errors.New("conversation_export_group_permission_required")
	errVoiceGroupPermission                = errors.New("voice_transcription_group_permission_required")

	// §workspace RBAC phase 4 — workspace capability policy. Stable machine
	// codes the client maps to a localized "disabled by your workspace" notice.
	errWorkspaceModelDisabled         = errors.New("workspace_model_disabled")
	errWorkspaceImageDisabled         = errors.New("workspace_image_generation_disabled")
	errWorkspaceFileUploadDisabled    = errors.New("workspace_file_upload_disabled")
	errWorkspaceKnowledgeBaseDisabled = errors.New("workspace_knowledge_bases_disabled")
	errWorkspaceCreditLimitExceeded   = errors.New("workspace_member_credit_limit_exceeded")
	errMemoryGroupPermission          = errors.New("memory_group_permission_required")
	errDrawingGroupPermission         = errors.New("drawing_group_permission_required")

	// Workspaces (§workspaces). Stable machine codes: creation gated off for the
	// group / owned-workspace cap reached. Deliberately NOT "account_suspended" —
	// the client force-logs-out on that one.
	errWorkspaceDisabled                  = errors.New("workspace_disabled")
	errWorkspaceLimit                     = errors.New("workspace_limit_reached")
	errWorkspaceProjectCreationPermission = errors.New("workspace_project_creation_permission_required")
	errWorkspaceKBCreationPermission      = errors.New("workspace_kb_creation_permission_required")

	// RAG embedding model lock. Once set, changing the global embedding model
	// would strand existing Qdrant collections/chunks under the old model.
	errEmbeddingModelLocked = errors.New("embedding_model_locked")
	// A channel/model record cannot be deleted because an embedding model it
	// owns (or is) is still referenced by a knowledge base's locked embedding
	// model. Stable machine code — admin UI maps it to a localized notice.
	errEmbeddingModelInUse = errors.New("embedding_model_in_use")

	// Auth-flow error codes (login/register/forgot-reset/2FA-login/first-run
	// setup/OAuth signup). Stable machine codes — every one has a matching
	// src/i18n/*/auth.json `errorCodes.*` key so the client localizes it instead
	// of shipping raw English prose straight to every locale. Never repurpose an
	// existing code's meaning; add a new one instead.
	errInvalidEmail           = errors.New("invalid_email")
	errNameRequired           = errors.New("name_required")
	errPasswordTooShort       = errors.New("password_too_short")
	errPasswordTooLong        = errors.New("password_too_long")
	errAlreadyInitialized     = errors.New("already_initialized")
	errSetupRequired          = errors.New("setup_required")
	errEmailDomainNotAllowed  = errors.New("email_domain_not_allowed")
	errSignupClosed           = errors.New("signup_closed")
	errEmailAlreadyRegistered = errors.New("email_already_registered")
	errInvalidOrExpiredCode   = errors.New("invalid_or_expired_code")
	errInvalidVerificationReq = errors.New("invalid_verification_request")
	errAccountNotFound        = errors.New("account_not_found")
	errInvalidCredentials     = errors.New("invalid_credentials")
	errEmailNotVerified       = errors.New("email_not_verified")
	errTwofaStartFailed       = errors.New("twofa_start_failed")
	errTwofaSessionExpired    = errors.New("twofa_session_expired")
	errTwofaInvalidSession    = errors.New("twofa_invalid_session")
	errTwofaCodeUsed          = errors.New("twofa_code_used")
	errTwofaInvalidCode       = errors.New("twofa_invalid_code")
	errEmailCooldown          = errors.New("email_cooldown")

	// Generic per-IP rate limit (rateLimitedIP — register/login/2FA/refresh/
	// verify-email/send-code/forgot-reset/captcha/first-run-setup/oauth/public
	// share links). Stable machine code, same reasoning as above.
	errRateLimited = errors.New("rate_limited")
)
