package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func readAdminOverview(t *testing.T, d Deps) (*adminOverviewResponse, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	adminOverviewHandler(d, recorder, httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil))
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
	for _, forbidden := range []string{"overview-secret-key", "overview-smtp-secret", "overview-storage-secret", "smtp_password", "storage_aliyun_access_key_secret"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("overview response leaked %q: %s", forbidden, raw)
		}
	}
}
