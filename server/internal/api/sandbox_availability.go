package api

import (
	"encoding/json"
	"strings"

	"aivory/server/internal/store"
)

// sandboxConfigured reports whether the effective sandbox settings contain a
// base URL. Admin settings take precedence when non-empty; otherwise the
// boot-time SANDBOX_BASE_URL remains the fallback used by the tool registry.
func sandboxConfigured(d Deps) bool {
	if d.Tools != nil {
		sb := d.Tools.Sandbox()
		return sb != nil && sb.Enabled()
	}

	baseURL := strings.TrimSpace(d.Config.SandboxBaseURL)
	if d.DB == nil {
		return baseURL != ""
	}
	raw, err := store.GetSetting(d.DB, "sandbox_base_url")
	if err != nil {
		return baseURL != ""
	}
	var configuredURL string
	if json.Unmarshal(raw, &configuredURL) == nil && strings.TrimSpace(configuredURL) != "" {
		return true
	}
	return baseURL != ""
}
