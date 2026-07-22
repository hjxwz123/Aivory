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

	// Auth-flow error codes (login/register/forgot-reset/2FA-login/first-run
	// setup/OAuth signup). Stable machine codes — every one has a matching
	// src/i18n/*/auth.json `errorCodes.*` key so the client localizes it instead
	// of shipping raw English prose straight to every locale. Never repurpose an
	// existing code's meaning; add a new one instead.
	errInvalidEmail           = errors.New("invalid_email")
	errNameRequired           = errors.New("name_required")
	errPasswordTooShort       = errors.New("password_too_short")
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
