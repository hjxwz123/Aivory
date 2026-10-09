package llm

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func bindPresentationTestSkill(t *testing.T, db *sql.DB, modelID string) *store.Skill {
	t.Helper()
	skill, err := store.CreateSkill(t.Context(), db, store.Skill{
		ID: store.GenerativeUISkillID, Name: "generative-ui", Description: "Useful visual answers",
		Instructions: "PRESENTATION_BODY: choose useful visuals from the conversation without requiring an explicit request.", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_skills(model_id,skill_id) VALUES(?,?)`, modelID, skill.ID); err != nil {
		t.Fatal(err)
	}
	return skill
}

func TestPresentationSkillPreloadedWithoutToolRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, modelMode, turnMode string
		wantRoute                 int
		wantSearchOnly            bool
	}{
		{"ambiguous automatic request", "native", ToolModeAuto, 1, true},
		{"automatic without search", "native", ToolModeAuto, 1, true},
		{"native tools enabled", "native", ToolModeEnabled, 0, false},
		{"prompt tools enabled", "prompt", ToolModeEnabled, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, provider, model, conversation, _, db := setupToolRouteTest(t)
			bindPresentationTestSkill(t, db, model.ID)
			if _, err := db.Exec(`UPDATE models SET tool_mode=? WHERE id=?`, tc.modelMode, model.ID); err != nil {
				t.Fatal(err)
			}
			if tc.name == "automatic without search" {
				if _, err := db.Exec(`UPDATE models SET builtin_tools='["python_execute","use_skill"]' WHERE id=?`, model.ID); err != nil {
					t.Fatal(err)
				}
			}
			ordinary, err := store.CreateSkill(t.Context(), db, store.Skill{
				Name: "ordinary-analysis", Description: "Use for analysis", Instructions: "ORDINARY_SKILL_BODY", Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO model_skills(model_id,skill_id) VALUES(?,?)`, model.ID, ordinary.ID); err != nil {
				t.Fatal(err)
			}
			previous, err := store.CreateMessage(t.Context(), db, store.Message{
				ConversationID: conversation.ID, Role: "user", Blocks: json.RawMessage(`[{"kind":"text","text":"Monthly token counts: June 800, July 1000, August 1200."}]`),
			})
			if err != nil {
				t.Fatal(err)
			}
			provider.routeResponse = "0"
			runToolRouteTurn(t, o, model.ID, conversation.ID, RunRequest{
				ToolMode: tc.turnMode, UserText: "Does this look normal?", ParentID: previous.ID,
			})
			if len(provider.mainRequests) != 1 || provider.routeCalls != tc.wantRoute {
				t.Fatalf("extra model request: main=%d route=%d", len(provider.mainRequests), provider.routeCalls)
			}
			request := provider.mainRequests[0]
			if strings.Count(request.SystemPrompt, "PRESENTATION_BODY") != 1 || strings.Contains(request.SystemPrompt, "- generative-ui:") {
				t.Fatalf("presentation was not preloaded exactly once: %s", request.SystemPrompt)
			}
			if request.SystemPromptOptions == nil || !request.SystemPromptOptions.PresentationSkillsAllowed || request.SearchOnly != tc.wantSearchOnly {
				t.Fatalf("incorrect presentation/route state: %+v", request.SystemPromptOptions)
			}
			if tc.wantSearchOnly {
				if requestHasTool(request, "use_skill") || requestHasTool(request, "python_execute") || request.SystemPromptOptions.SkillsAllowed || strings.Contains(request.SystemPrompt, "ordinary-analysis") {
					t.Fatal("presentation broadened the automatic tool/skill surface")
				}
				if tc.name == "automatic without search" && (request.ToolsEnabled || len(request.Tools) != 0) {
					t.Fatal("presentation enabled tools on an automatic no-tools turn")
				}
			} else if !strings.Contains(request.SystemPrompt, "- ordinary-analysis:") || strings.Contains(request.SystemPrompt, "ORDINARY_SKILL_BODY") {
				t.Fatal("ordinary skills no longer use progressive disclosure")
			}
			history, _ := json.Marshal(request.History)
			if !strings.Contains(string(history), "June 800") {
				t.Fatal("ambiguous request lost the earlier data")
			}
		})
	}
}

func TestPresentationSkillRespectsPermissionCeilings(t *testing.T) {
	for _, name := range []string{
		"unbound", "disabled skill", "explicit tools off", "model tools off", "global use_skill off",
		"model use_skill unselected", "turn use_skill unselected", "skills denied", "catalog skill denied", "tool calling denied",
	} {
		t.Run(name, func(t *testing.T) {
			o, provider, model, conversation, _, db := setupToolRouteTest(t)
			skill := bindPresentationTestSkill(t, db, model.ID)
			req := RunRequest{ToolMode: ToolModeEnabled, UserText: "Help me decide"}
			var err error
			switch name {
			case "unbound":
				_, err = db.Exec(`DELETE FROM model_skills WHERE model_id=?`, model.ID)
			case "disabled skill":
				_, err = db.Exec(`UPDATE skills SET enabled=0 WHERE id=?`, skill.ID)
			case "explicit tools off":
				req.ToolMode = ToolModeDisabled
			case "model tools off":
				_, err = db.Exec(`UPDATE models SET tool_mode='none' WHERE id=?`, model.ID)
			case "global use_skill off":
				err = store.SetSetting(db, "disabled_tools", []string{"use_skill"})
			case "model use_skill unselected":
				_, err = db.Exec(`UPDATE models SET builtin_tools='["aivory_web_search"]' WHERE id=?`, model.ID)
			case "turn use_skill unselected":
				req.SelectedToolsConfigured = true
				req.SelectedToolIDs = []string{"builtin:aivory_web_search"}
			case "skills denied":
				req.ToolAccessPolicy = &ToolAccessPolicy{Mode: store.ResourceAccessAll, AllowSkills: false}
			case "catalog skill denied":
				req.ToolAccessPolicy = &ToolAccessPolicy{Mode: store.ResourceAccessAll, AllowSkills: true, SkillMode: store.ResourceAccessSelected, SkillIDs: []string{"other-skill"}}
			case "tool calling denied":
				req.ToolAccessPolicy = &ToolAccessPolicy{Mode: store.ResourceAccessAll, AllowSkills: true, ToolCallingConfigured: true, AllowToolCalling: false}
			}
			if err != nil {
				t.Fatal(err)
			}
			runToolRouteTurn(t, o, model.ID, conversation.ID, req)
			if len(provider.mainRequests) != 1 || strings.Contains(provider.mainRequests[0].SystemPrompt, "PRESENTATION_BODY") {
				t.Fatal("automatic presentation bypassed " + name)
			}
		})
	}
}

func TestPresentationSkillExplicitSelectionWins(t *testing.T) {
	for _, personalCopy := range []bool{false, true} {
		t.Run(map[bool]string{false: "catalog", true: "edited personal copy"}[personalCopy], func(t *testing.T) {
			o, provider, model, conversation, _, db := setupToolRouteTest(t)
			skill := bindPresentationTestSkill(t, db, model.ID)
			ids := []string{store.CatalogSkillCommandPrefix + skill.ID}
			marker := "PRESENTATION_BODY"
			if personalCopy {
				copy, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{
					UserID: "u1", Name: "my-visuals", SourceSkillID: skill.ID, Instructions: "PERSONAL_VISUAL_BODY",
				})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, copy.ID)
				marker = "PERSONAL_VISUAL_BODY"
			}
			runToolRouteTurn(t, o, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeAuto, SelectedUserSkillIDs: ids})
			request := provider.mainRequests[0]
			history, _ := json.Marshal(request.History)
			if strings.Contains(request.SystemPrompt, "PRESENTATION_BODY") || strings.Count(string(history), marker) != 1 || len(request.SystemPromptOptions.PresentationSkills) != 0 {
				t.Fatal("selected presentation instructions were duplicated")
			}
			if personalCopy && strings.Contains(string(history), "PRESENTATION_BODY") {
				t.Fatal("catalog instructions overrode the edited personal copy")
			}
		})
	}
}

func TestPresentationSkillFallbackRebuildsBindingsAndRevalidates(t *testing.T) {
	o, provider, model, conversation, _, db := setupToolRouteTest(t)
	skill := bindPresentationTestSkill(t, db, model.ID)
	provider.routeResponse = "0"
	runToolRouteTurn(t, o, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeAuto})
	base := provider.mainRequests[0]
	fallback, err := store.CreateModel(t.Context(), db, store.Model{
		ChannelID: model.ChannelID, Kind: "chat", RequestID: "fallback", Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, err := o.buildFallbackRequest(t.Context(), base, fallback.ID)
	if err != nil || strings.Contains(got.SystemPrompt, "PRESENTATION_BODY") {
		t.Fatalf("unbound fallback inherited primary skill: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO model_skills(model_id,skill_id) VALUES(?,?)`, fallback.ID, skill.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE skills SET name='renamed-visuals',instructions='FALLBACK_VISUAL_BODY' WHERE id=?`, skill.ID); err != nil {
		t.Fatal(err)
	}
	got, _, _, err = o.buildFallbackRequest(t.Context(), base, fallback.ID)
	if err != nil || strings.Contains(got.SystemPrompt, "PRESENTATION_BODY") || strings.Count(got.SystemPrompt, "FALLBACK_VISUAL_BODY") != 1 {
		t.Fatalf("fallback did not reload renamed skill: %v", err)
	}
	if got.SystemPromptOptions.SkillsAllowed || requestHasTool(got, "use_skill") {
		t.Fatal("fallback enabled ordinary skills/tools after auto=false")
	}
	if !strings.Contains(base.SystemPrompt, "PRESENTATION_BODY") {
		t.Fatal("fallback mutated primary prompt")
	}
	options := *base.SystemPromptOptions
	options.PresentationSkillsAllowed = false
	deniedBase := base
	deniedBase.SystemPromptOptions = &options
	denied, _, _, err := o.buildFallbackRequest(t.Context(), deniedBase, fallback.ID)
	if err != nil || strings.Contains(denied.SystemPrompt, "FALLBACK_VISUAL_BODY") {
		t.Fatalf("fallback broadened primary presentation ceiling: %v", err)
	}
	if _, err := db.Exec(`UPDATE models SET tool_mode='none' WHERE id=?`, fallback.ID); err != nil {
		t.Fatal(err)
	}
	denied, _, _, err = o.buildFallbackRequest(t.Context(), base, fallback.ID)
	if err != nil || strings.Contains(denied.SystemPrompt, "FALLBACK_VISUAL_BODY") {
		t.Fatalf("fallback ignored model tool ceiling: %v", err)
	}
	if _, err := db.Exec(`UPDATE models SET tool_mode='native' WHERE id=?`, fallback.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_groups(id,name,permissions) VALUES('presentation-revoked','Revoked','{}');
		UPDATE users SET role='user',group_id='presentation-revoked' WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	permissions := store.DefaultUserGroupPermissions()
	permissions.AllowSkills = false
	raw, _ := json.Marshal(permissions)
	if _, err := db.Exec(`UPDATE user_groups SET permissions=? WHERE id='presentation-revoked'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	denied, _, _, err = o.buildFallbackRequest(t.Context(), base, fallback.ID)
	if err != nil || strings.Contains(denied.SystemPrompt, "FALLBACK_VISUAL_BODY") {
		t.Fatalf("fallback ignored current permission revocation: %v", err)
	}
}

func TestPresentationSkillPromptLocalization(t *testing.T) {
	for _, locale := range []string{"en", "zh", "zh-Hant", "ja", "fr"} {
		t.Run(locale, func(t *testing.T) {
			local := promptL10nFor(locale)
			if local.presentationHeader == "" || local.presentationBody == "" {
				t.Fatal("missing localized presentation guidance")
			}
			opts := systemPromptOpts{
				Locale: locale, PresentationSkillsAllowed: true, SkillsAllowed: false,
				PresentationSkills: []SkillFull{{Name: "generative-ui", Instructions: "VISUAL_BODY"}},
			}
			prompt := composeSystemPrompt(opts)
			if !strings.Contains(prompt, local.presentationHeader+local.presentationBody) || strings.Count(prompt, "VISUAL_BODY") != 1 {
				t.Fatal("missing localized preloaded instructions")
			}
			opts.PresentationSkillsAllowed = false
			if strings.Contains(composeSystemPrompt(opts), "VISUAL_BODY") {
				t.Fatal("presentation denial leaked instructions")
			}
		})
	}
}
