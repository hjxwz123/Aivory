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

	errUploadRateLimited = errors.New("upload rate limit exceeded — try again shortly")

	// Registration anti-abuse. Stable machine codes — the client matches these to
	// refresh the captcha / show the per-network limit notice.
	errCaptcha         = errors.New("captcha_failed")
	errRegisterIPLimit = errors.New("register_ip_limit")

	// Per-group resource caps (§ user groups). Stable machine codes the client
	// maps to a localized "you've reached your plan's limit" notice.
	errProjectLimit = errors.New("project_limit_reached")
	errKBLimit      = errors.New("kb_limit_reached")

	// Workspaces (§workspaces). Stable machine codes: creation gated off for the
	// group / owned-workspace cap reached. Deliberately NOT "account_suspended" —
	// the client force-logs-out on that one.
	errWorkspaceDisabled = errors.New("workspace_disabled")
	errWorkspaceLimit    = errors.New("workspace_limit_reached")

	// RAG embedding model lock. Once set, changing the global embedding model
	// would strand existing Qdrant collections/chunks under the old model.
	errEmbeddingModelLocked = errors.New("embedding_model_locked")
)
