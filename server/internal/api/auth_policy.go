package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

const (
	authEntryLoginPage      = "login_page"
	authEntryProviderPicker = "provider_picker"
	authEntryAutoRedirect   = "auto_redirect"

	oauthPasswordRequired = "required"
	oauthPasswordOptional = "optional"
	oauthPasswordDisabled = "disabled"
)

var (
	errPasswordLoginDisabled       = errors.New("password_login_disabled")
	errPasswordManagementDisabled  = errors.New("password_management_disabled")
	errOAuthAutoProvisionDisabled  = errors.New("oauth_auto_provision_disabled")
	errInitialPasswordRequired     = errors.New("initial_password_required")
	errAuthPolicyConflict          = errors.New("auth_policy_conflict")
	errAuthPolicyProviderRequired  = errors.New("auth_policy_provider_required")
	errAuthPolicyAdminLinkRequired = errors.New("auth_policy_admin_identity_required")
	errAuthPolicyAdminRequired     = errors.New("auth_policy_admin_required")
)

func initialPasswordSetupRequest(r *http.Request) bool {
	return (r.Method == http.MethodGet && r.URL.Path == "/api/me") ||
		(r.Method == http.MethodPost && r.URL.Path == "/api/me/password/set")
}

type authPolicy struct {
	PasswordLoginEnabled       bool
	EntryMode                  string
	DefaultProviderID          string
	OAuthInitialPasswordPolicy string
	OAuthAutoProvisionEnabled  bool
}

type publicAuthPolicy struct {
	PasswordLoginEnabled       bool                  `json:"password_login_enabled"`
	EntryMode                  string                `json:"entry_mode"`
	DefaultProvider            *publicOAuthProvider  `json:"default_provider"`
	OAuthInitialPasswordPolicy string                `json:"oauth_initial_password_policy"`
	OAuthAutoProvisionEnabled  bool                  `json:"oauth_auto_provision_enabled"`
	Providers                  []publicOAuthProvider `json:"providers"`
}

func defaultAuthPolicy() authPolicy {
	return authPolicy{
		PasswordLoginEnabled:       true,
		EntryMode:                  authEntryLoginPage,
		OAuthInitialPasswordPolicy: oauthPasswordRequired,
		OAuthAutoProvisionEnabled:  true,
	}
}

func loadAuthPolicy(d Deps) (authPolicy, error) {
	return loadAuthPolicyWith(func(key string) (json.RawMessage, error) {
		return store.GetSetting(d.DB, key)
	})
}

func loadAuthPolicyTx(ctx context.Context, tx *sql.Tx) (authPolicy, error) {
	return loadAuthPolicyWith(func(key string) (json.RawMessage, error) {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&raw)
		return json.RawMessage(raw), err
	})
}

func loadAuthPolicyWith(get func(string) (json.RawMessage, error)) (authPolicy, error) {
	policy := defaultAuthPolicy()
	reads := []struct {
		key string
		dst any
	}{
		{"password_login_enabled", &policy.PasswordLoginEnabled},
		{"auth_entry_mode", &policy.EntryMode},
		{"auth_default_provider_id", &policy.DefaultProviderID},
		{"oauth_initial_password_policy", &policy.OAuthInitialPasswordPolicy},
		{"oauth_auto_provision_enabled", &policy.OAuthAutoProvisionEnabled},
	}
	for _, read := range reads {
		raw, err := get(read.key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return authPolicy{}, err
		}
		if err := json.Unmarshal(raw, read.dst); err != nil {
			return authPolicy{}, err
		}
	}
	policy.EntryMode = strings.TrimSpace(policy.EntryMode)
	policy.DefaultProviderID = strings.TrimSpace(policy.DefaultProviderID)
	policy.OAuthInitialPasswordPolicy = strings.TrimSpace(policy.OAuthInitialPasswordPolicy)
	if !validAuthEntryMode(policy.EntryMode) || !validOAuthPasswordPolicy(policy.OAuthInitialPasswordPolicy) {
		return authPolicy{}, errAuthPolicyConflict
	}
	return policy, nil
}

func validAuthEntryMode(value string) bool {
	switch value {
	case authEntryLoginPage, authEntryProviderPicker, authEntryAutoRedirect:
		return true
	default:
		return false
	}
}

func validOAuthPasswordPolicy(value string) bool {
	switch value {
	case oauthPasswordRequired, oauthPasswordOptional, oauthPasswordDisabled:
		return true
	default:
		return false
	}
}

func readyOAuthProviders(ctx context.Context, d Deps) ([]store.OAuthProvider, error) {
	providers, err := store.ListEnabledOAuthProviders(ctx, d.DB)
	if err != nil {
		return nil, err
	}
	ready := make([]store.OAuthProvider, 0, len(providers))
	for _, provider := range providers {
		if oauthProviderReady(provider) {
			ready = append(ready, provider)
		}
	}
	return ready, nil
}

func publicAuthPolicyHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	policy, err := loadAuthPolicy(d)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	providers, err := readyOAuthProviders(r.Context(), d)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	response := publicAuthPolicy{
		PasswordLoginEnabled:       policy.PasswordLoginEnabled,
		EntryMode:                  policy.EntryMode,
		OAuthInitialPasswordPolicy: policy.OAuthInitialPasswordPolicy,
		OAuthAutoProvisionEnabled:  policy.OAuthAutoProvisionEnabled,
		Providers:                  make([]publicOAuthProvider, 0, len(providers)),
	}
	for _, provider := range providers {
		item := publicOAuthProvider{ID: provider.ID, Kind: provider.Kind, Name: provider.Name, Icon: provider.Icon}
		response.Providers = append(response.Providers, item)
		if provider.ID == policy.DefaultProviderID {
			selected := item
			response.DefaultProvider = &selected
		}
	}
	// A stale provider reference must never create an automatic redirect loop.
	// Degrade to the provider picker (or the ordinary login page when none are
	// available) while preserving the stored value for the administrator to fix.
	if response.EntryMode == authEntryAutoRedirect && response.DefaultProvider == nil {
		if len(response.Providers) > 0 {
			response.EntryMode = authEntryProviderPicker
		} else {
			response.EntryMode = authEntryLoginPage
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func passwordLoginEnabled(d Deps) (bool, error) {
	policy, err := loadAuthPolicy(d)
	if err != nil {
		return false, err
	}
	return policy.PasswordLoginEnabled, nil
}

func requirePasswordLoginEnabled(d Deps, w http.ResponseWriter) bool {
	enabled, err := passwordLoginEnabled(d)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if !enabled {
		writeError(w, http.StatusForbidden, errPasswordLoginDisabled)
		return false
	}
	return true
}

func effectiveAuthPolicyForPatch(d Deps, body map[string]json.RawMessage) (authPolicy, error) {
	policy, err := loadAuthPolicy(d)
	if err != nil {
		return authPolicy{}, err
	}
	return applyAuthPolicyPatch(policy, body)
}

func effectiveAuthPolicyForPatchTx(ctx context.Context, tx *sql.Tx, body map[string]json.RawMessage) (authPolicy, error) {
	policy, err := loadAuthPolicyTx(ctx, tx)
	if err != nil {
		return authPolicy{}, err
	}
	return applyAuthPolicyPatch(policy, body)
}

func applyAuthPolicyPatch(policy authPolicy, body map[string]json.RawMessage) (authPolicy, error) {
	decode := func(key string, dst any) error {
		raw, ok := body[key]
		if !ok || strings.TrimSpace(string(raw)) == "null" {
			return nil
		}
		return json.Unmarshal(raw, dst)
	}
	if err := decode("password_login_enabled", &policy.PasswordLoginEnabled); err != nil {
		return authPolicy{}, errInvalidInput
	}
	if err := decode("auth_entry_mode", &policy.EntryMode); err != nil {
		return authPolicy{}, errInvalidInput
	}
	if err := decode("auth_default_provider_id", &policy.DefaultProviderID); err != nil {
		return authPolicy{}, errInvalidInput
	}
	if err := decode("oauth_initial_password_policy", &policy.OAuthInitialPasswordPolicy); err != nil {
		return authPolicy{}, errInvalidInput
	}
	if err := decode("oauth_auto_provision_enabled", &policy.OAuthAutoProvisionEnabled); err != nil {
		return authPolicy{}, errInvalidInput
	}
	policy.EntryMode = strings.TrimSpace(policy.EntryMode)
	policy.DefaultProviderID = strings.TrimSpace(policy.DefaultProviderID)
	policy.OAuthInitialPasswordPolicy = strings.TrimSpace(policy.OAuthInitialPasswordPolicy)
	if !validAuthEntryMode(policy.EntryMode) || !validOAuthPasswordPolicy(policy.OAuthInitialPasswordPolicy) {
		return authPolicy{}, errInvalidInput
	}
	return policy, nil
}

func adminHasReadyOAuthIdentity(ctx context.Context, d Deps, adminID string, providers []store.OAuthProvider, requiredProviderID string) (bool, error) {
	return adminHasReadyOAuthIdentityWith(ctx, adminID, providers, requiredProviderID, func(ctx context.Context, userID string) ([]store.OAuthIdentity, error) {
		return store.ListOAuthIdentitiesForUser(ctx, d.DB, userID)
	})
}

func adminHasReadyOAuthIdentityTx(ctx context.Context, tx *sql.Tx, adminID string, providers []store.OAuthProvider, requiredProviderID string) (bool, error) {
	return adminHasReadyOAuthIdentityWith(ctx, adminID, providers, requiredProviderID, func(ctx context.Context, userID string) ([]store.OAuthIdentity, error) {
		return store.ListOAuthIdentitiesForUserTx(ctx, tx, userID)
	})
}

func adminHasReadyOAuthIdentityWith(
	ctx context.Context,
	adminID string,
	providers []store.OAuthProvider,
	requiredProviderID string,
	listIdentities func(context.Context, string) ([]store.OAuthIdentity, error),
) (bool, error) {
	if strings.TrimSpace(adminID) == "" {
		return false, nil
	}
	readyIDs := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if requiredProviderID == "" || provider.ID == requiredProviderID {
			readyIDs[provider.ID] = struct{}{}
		}
	}
	if len(readyIDs) == 0 {
		return false, nil
	}
	identities, err := listIdentities(ctx, adminID)
	if err != nil {
		return false, err
	}
	for _, identity := range identities {
		if _, ok := readyIDs[identity.ProviderID]; ok {
			return true, nil
		}
	}
	return false, nil
}

func validateAuthPolicyAgainstProviders(ctx context.Context, d Deps, policy authPolicy, providers []store.OAuthProvider, adminID string) error {
	return validateAuthPolicyAgainstProvidersWith(ctx, policy, providers, adminID, func(ctx context.Context, adminID string, providers []store.OAuthProvider, requiredProviderID string) (bool, error) {
		return adminHasReadyOAuthIdentity(ctx, d, adminID, providers, requiredProviderID)
	})
}

func validateAuthPolicyAgainstProvidersTx(ctx context.Context, tx *sql.Tx, policy authPolicy, providers []store.OAuthProvider, adminID string) error {
	return validateAuthPolicyAgainstProvidersWith(ctx, policy, providers, adminID, func(ctx context.Context, adminID string, providers []store.OAuthProvider, requiredProviderID string) (bool, error) {
		return adminHasReadyOAuthIdentityTx(ctx, tx, adminID, providers, requiredProviderID)
	})
}

func validateAuthPolicyAgainstProvidersWith(
	ctx context.Context,
	policy authPolicy,
	providers []store.OAuthProvider,
	adminID string,
	hasIdentity func(context.Context, string, []store.OAuthProvider, string) (bool, error),
) error {
	needsProvider := !policy.PasswordLoginEnabled || policy.EntryMode != authEntryLoginPage
	if !needsProvider {
		return nil
	}
	if len(providers) == 0 {
		return errAuthPolicyProviderRequired
	}
	requiredProviderID := ""
	if policy.EntryMode == authEntryAutoRedirect {
		requiredProviderID = policy.DefaultProviderID
		if requiredProviderID == "" {
			return errAuthPolicyProviderRequired
		}
		found := false
		for _, provider := range providers {
			if provider.ID == requiredProviderID {
				found = true
				break
			}
		}
		if !found {
			return errAuthPolicyProviderRequired
		}
	}
	linked, err := hasIdentity(ctx, adminID, providers, requiredProviderID)
	if err != nil {
		return err
	}
	if !linked {
		return errAuthPolicyAdminLinkRequired
	}
	return nil
}

func validateAuthPolicyPatch(ctx context.Context, d Deps, body map[string]json.RawMessage, adminID string) error {
	policy, err := effectiveAuthPolicyForPatch(d, body)
	if err != nil {
		return err
	}
	providers, err := readyOAuthProviders(ctx, d)
	if err != nil {
		return err
	}
	return validateAuthPolicyAgainstProviders(ctx, d, policy, providers, adminID)
}

func validateAuthPolicyPatchTx(ctx context.Context, tx *sql.Tx, body map[string]json.RawMessage, adminID string) error {
	policy, err := effectiveAuthPolicyForPatchTx(ctx, tx, body)
	if err != nil {
		return err
	}
	providers, err := store.ListEnabledOAuthProvidersTx(ctx, tx)
	if err != nil {
		return err
	}
	ready := make([]store.OAuthProvider, 0, len(providers))
	for _, provider := range providers {
		if oauthProviderReady(provider) {
			ready = append(ready, provider)
		}
	}
	return validateAuthPolicyAgainstProvidersTx(ctx, tx, policy, ready, adminID)
}

func validateCurrentAuthPolicyTx(ctx context.Context, tx *sql.Tx, adminID string) error {
	policy, err := loadAuthPolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	providers, err := store.ListEnabledOAuthProvidersTx(ctx, tx)
	if err != nil {
		return err
	}
	ready := make([]store.OAuthProvider, 0, len(providers))
	for _, provider := range providers {
		if oauthProviderReady(provider) {
			ready = append(ready, provider)
		}
	}
	return validateAuthPolicyAgainstProvidersTx(ctx, tx, policy, ready, adminID)
}

func lockActiveAuthAdminTx(ctx context.Context, tx *sql.Tx, adminID string) error {
	if strings.TrimSpace(adminID) == "" {
		return errAuthPolicyAdminRequired
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET token_ver=token_ver
		WHERE id=? AND role='admin' AND status='active'`, adminID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errAuthPolicyAdminRequired
	}
	return nil
}

func authPolicyKeysPresent(body map[string]json.RawMessage) bool {
	for _, key := range []string{
		"password_login_enabled", "auth_entry_mode", "auth_default_provider_id",
		"oauth_initial_password_policy", "oauth_auto_provision_enabled",
	} {
		if _, ok := body[key]; ok {
			return true
		}
	}
	return false
}

func ensureOAuthProviderMutationAllowed(ctx context.Context, d Deps, adminID string, replacement *store.OAuthProvider, deletingID string) error {
	policy, err := loadAuthPolicy(d)
	if err != nil {
		return err
	}
	if policy.PasswordLoginEnabled && policy.EntryMode == authEntryLoginPage {
		return nil
	}
	current, err := store.ListEnabledOAuthProviders(ctx, d.DB)
	if err != nil {
		return err
	}
	providers := providersAfterOAuthMutation(current, replacement, deletingID)
	return validateAuthPolicyAgainstProviders(ctx, d, policy, providers, adminID)
}

func ensureOAuthProviderMutationAllowedTx(ctx context.Context, tx *sql.Tx, adminID string, replacement *store.OAuthProvider, deletingID string) error {
	policy, err := loadAuthPolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	if policy.PasswordLoginEnabled && policy.EntryMode == authEntryLoginPage {
		return nil
	}
	current, err := store.ListEnabledOAuthProvidersTx(ctx, tx)
	if err != nil {
		return err
	}
	providers := providersAfterOAuthMutation(current, replacement, deletingID)
	return validateAuthPolicyAgainstProvidersTx(ctx, tx, policy, providers, adminID)
}

func providersAfterOAuthMutation(current []store.OAuthProvider, replacement *store.OAuthProvider, deletingID string) []store.OAuthProvider {
	providers := make([]store.OAuthProvider, 0, len(current))
	replaced := false
	for _, provider := range current {
		if provider.ID == deletingID {
			continue
		}
		if replacement != nil && provider.ID == replacement.ID {
			replaced = true
			if replacement.Enabled && oauthProviderReady(*replacement) {
				providers = append(providers, *replacement)
			}
			continue
		}
		if oauthProviderReady(provider) {
			providers = append(providers, provider)
		}
	}
	if replacement != nil && !replaced && replacement.Enabled && oauthProviderReady(*replacement) {
		providers = append(providers, *replacement)
	}
	return providers
}
