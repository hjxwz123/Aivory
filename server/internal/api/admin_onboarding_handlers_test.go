package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestAdminOnboardingOptionalTaskAndToolRouteModels(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-onboarding.db"))
	defer db.Close()

	channel, err := store.CreateChannel(
		context.Background(),
		db,
		"Onboarding",
		"openai",
		"chat",
		"https://example.invalid/v1",
		"key",
	)
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(context.Background(), db, store.Model{
		ID:        "onboarding-chat",
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "onboarding-chat",
		Label:     "Onboarding chat",
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "default_model_id", model.ID); err != nil {
		t.Fatal(err)
	}

	build := func() adminOnboardingResponse {
		t.Helper()
		response, buildErr := buildAdminOnboardingResponse(
			httptest.NewRequest(http.MethodGet, "/api/admin/onboarding", nil),
			Deps{DB: db},
			nil,
		)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		return response
	}
	step := func(response adminOnboardingResponse, id string) adminOnboardingStep {
		t.Helper()
		for _, candidate := range response.Optional {
			if candidate.ID == id {
				return candidate
			}
		}
		t.Fatalf("optional onboarding step %q not found", id)
		return adminOnboardingStep{}
	}

	unconfigured := build()
	if !adminOnboardingRequiredReady(unconfigured) {
		t.Fatal("optional model settings must not block completion of required onboarding")
	}
	if step(unconfigured, "task_model").Complete {
		t.Fatal("unset task model should remain an incomplete optional step")
	}
	if step(unconfigured, "tool_route_model").Complete {
		t.Fatal("unset tool route model should remain an incomplete optional step")
	}

	if err := store.SetSetting(db, "task_model_id", model.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "tool_route_model_id", model.ID); err != nil {
		t.Fatal(err)
	}
	configured := build()
	if !step(configured, "task_model").Complete {
		t.Fatal("valid task model should complete its optional step")
	}
	if !step(configured, "tool_route_model").Complete {
		t.Fatal("valid tool route model should complete its optional step")
	}
}
