package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func readAdminOverview(t *testing.T, d Deps, targets ...string) (*adminOverviewResponse, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	target := "/api/admin/overview"
	if len(targets) > 0 {
		target = targets[0]
	}
	adminOverviewHandler(d, recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response adminOverviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	return &response, recorder.Body.String()
}

func TestAdminOverviewReturnsCountsHealthAndOnlySummaryData(t *testing.T) {
	d := newAuthSecurityDeps(t, "admin-overview.db")
	ctx := t.Context()
	if _, err := store.CreateUserWithRole(ctx, d.DB, "overview@example.test", "Overview", "hash", "admin"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, d.DB, "Overview channel", "openai", "openai", "https://provider.example.test", "overview-secret-key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, d.DB, store.Model{
		ID:        "overview-chat-model",
		ChannelID: channel.ID,
		Kind:      " Chat ",
		RequestID: "model-request-id",
		Label:     "Overview chat model",
		Enabled:   true,
		Stream:    true,
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	settings := map[string]any{
		"default_model_id":                 model.ID,
		"task_model_id":                    "",
		"storage_provider":                 "local",
		"email_verification_required":      false,
		"smtp_password":                    "overview-smtp-secret",
		"storage_aliyun_access_key_secret": "overview-storage-secret",
	}
	for key, value := range settings {
		if err := store.SetSetting(d.DB, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	response, raw := readAdminOverview(t, d)
	if response.UserCount != 1 || response.ChannelCount != 1 || response.EnabledChannelCount != 1 || response.ModelCount != 1 {
		t.Fatalf("unexpected overview counts: %+v", response)
	}
	if response.GroupCount < 1 {
		t.Fatalf("group count=%d, want seeded default group", response.GroupCount)
	}
	if !response.Health.AllReady || !response.Health.TaskModelInherited {
		t.Fatalf("overview health=%+v, want ready with inherited task model", response.Health)
	}
	if response.Today == nil {
		t.Fatal("today totals are nil for a healthy deployment")
	}
	if response.Trends == nil || response.Trends.Days != 30 || len(response.Trends.Points) != 30 || response.Trends.Registrations != 1 || response.Trends.PeriodStart%86400 != 0 {
		t.Fatalf("unexpected default trends: %+v", response.Trends)
	}
	for _, days := range []string{"7", "90", "999999", "invalid"} {
		result, _ := readAdminOverview(t, d, "/api/admin/overview?days="+days)
		expected := 30
		if days == "7" {
			expected = 7
		} else if days == "90" {
			expected = 90
		}
		if result.Trends == nil || result.Trends.Days != expected || len(result.Trends.Points) != expected {
			t.Fatalf("range %s: %+v", days, result.Trends)
		}
	}
	for _, forbidden := range []string{"overview-secret-key", "overview-smtp-secret", "overview-storage-secret", "smtp_password", "storage_aliyun_access_key_secret"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("overview response leaked %q: %s", forbidden, raw)
		}
	}
}

func TestAdminOverviewKeepsConfigurationChecksUntilReady(t *testing.T) {
	d := newAuthSecurityDeps(t, "admin-overview-unready.db")
	response, _ := readAdminOverview(t, d, "/api/admin/overview?days=7")
	if response.Health.AllReady || response.Today != nil || response.Trends != nil {
		t.Fatalf("unconfigured deployment must not query usage: %+v", response)
	}
}
