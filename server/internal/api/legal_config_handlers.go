package api

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"

	"aivory/server/internal/store"
)

const (
	defaultContactEmail     = "admin@aivory.local"
	legalPolicyTextMaxBytes = 100_000
)

type publicLegalConfig struct {
	ContactEmail string `json:"contact_email"`
	TermsText    string `json:"terms_text"`
	PrivacyText  string `json:"privacy_text"`
}

func validContactEmail(value string) bool {
	if len(value) > 320 {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && strings.Contains(value, "@")
}

func legalSettingString(d Deps, key string) string {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil || len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	if (key == "terms_text" || key == "privacy_text") && len(value) > legalPolicyTextMaxBytes {
		return ""
	}
	return strings.TrimSpace(value)
}

// legalConfigPublicHandler deliberately exposes only operator-authored public
// policy/support fields. It must never proxy the broader admin settings map,
// which contains credentials and infrastructure details.
func legalConfigPublicHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	contactEmail := legalSettingString(d, "contact_email")
	if !validContactEmail(contactEmail) {
		contactEmail = defaultContactEmail
	}
	writeJSON(w, http.StatusOK, publicLegalConfig{
		ContactEmail: contactEmail,
		TermsText:    legalSettingString(d, "terms_text"),
		PrivacyText:  legalSettingString(d, "privacy_text"),
	})
}
