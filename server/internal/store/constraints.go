package store

import (
	"errors"
	"strings"
)

var (
	ErrChannelNameExists        = errors.New("name_exists")
	ErrOAuthProviderNameExists  = errors.New("name_exists")
	ErrOAuthProviderIDExists    = errors.New("oauth_provider_id_exists")
	ErrInvalidOAuthProviderKind = errors.New("invalid_oauth_provider_kind")
	// ErrOAuthProviderChanged means an OAuth provider's credential/trust
	// configuration changed after the caller read it. Callers must reload the
	// row instead of merging a stale update with a different trust domain.
	ErrOAuthProviderChanged = errors.New("oauth_provider_changed")
	ErrUserGroupNameExists  = errors.New("name_exists")
	ErrModelRequestExists   = errors.New("model_request_exists")
	ErrModelTagNameExists   = errors.New("name_exists")
	ErrImageStyleNameExists = errors.New("name_exists")
	ErrProjectNameExists    = errors.New("name_exists")
	ErrKBNameExists         = errors.New("name_exists")
	ErrProjectLimitExceeded = errors.New("project limit exceeded")
	ErrKBLimitExceeded      = errors.New("knowledge base limit exceeded")
	// ErrOAuthIdentityConflict — the (provider, subject) is already linked to a
	// DIFFERENT local user, so it can't be bound here (§ identity linking).
	ErrOAuthIdentityConflict = errors.New("oauth_identity_conflict")
	// ErrOAuthLastLoginMethod — refusing to unbind the account's only remaining
	// sign-in method (no password + this is the last linked identity).
	ErrOAuthLastLoginMethod = errors.New("oauth_last_login_method")
	// ErrOAuthLinkSessionExpired means the authenticated session that started a
	// delayed OAuth identity-link callback is no longer authoritative.
	ErrOAuthLinkSessionExpired = errors.New("oauth_link_session_expired")
	// ErrOAuthLoginStateChanged means the user status, token version, or expected
	// 2FA state changed before an OAuth callback could atomically issue a session.
	ErrOAuthLoginStateChanged = errors.New("oauth_login_state_changed")
	// ErrPasswordChanged means a current-password change lost a race after its
	// caller verified an older password hash.
	ErrPasswordChanged = errors.New("password_changed")
)

func isUniqueIndexErr(err error, indexNames ...string) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	for _, name := range indexNames {
		if strings.Contains(low, strings.ToLower(name)) {
			return true
		}
	}
	return false
}
