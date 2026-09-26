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

func TestOnboardingSearchReadyTreatsDuckDuckGoAsKeyless(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "onboarding-search.db"))
	defer db.Close()

	// DuckDuckGo is the keyless channel: no key and no base URL still counts as
	// configured. auto deliberately stays opt-in, so it must not.
	for _, provider := range []string{"duckduckgo", "ddg"} {
		if err := store.SetSetting(db, "search_provider", provider); err != nil {
			t.Fatal(err)
		}
		ready, err := onboardingSearchReady(Deps{DB: db})
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if !ready {
			t.Fatalf("%s needs no key or base URL but reads as unready", provider)
		}
	}

	if err := store.SetSetting(db, "search_provider", "auto"); err != nil {
		t.Fatal(err)
	}
	if ready, err := onboardingSearchReady(Deps{DB: db}); err != nil || ready {
		t.Fatalf("auto with no key or base URL must stay unready, ready=%v err=%v", ready, err)
	}
}
