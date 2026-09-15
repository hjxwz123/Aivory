package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestWorkspaceUserSkillsBypassPersonalCatalogPolicy(t *testing.T) {
	shared := store.UserSkill{ID: "shared", WorkspaceID: "workspace-1", SourceSkillID: "blocked-catalog-skill"}
	personal := store.UserSkill{ID: "personal", SourceSkillID: "blocked-catalog-skill"}

	for _, policy := range []*ToolAccessPolicy{
		{SkillMode: store.ResourceAccessNone},
		{SkillMode: store.ResourceAccessSelected, SkillIDs: []string{"other-catalog-skill"}},
	} {
		if !userSkillAccessPolicyAllows(policy, shared) {
			t.Fatalf("workspace skill denied by personal policy %+v", policy)
		}
		if userSkillAccessPolicyAllows(policy, personal) {
			t.Fatalf("personal skill bypassed personal policy %+v", policy)
		}
		if err := validateUserSkillAccessPolicy(policy, []store.UserSkill{shared}); err != nil {
			t.Fatalf("workspace skill validation failed for policy %+v: %v", policy, err)
		}
		if err := validateUserSkillAccessPolicy(policy, []store.UserSkill{personal}); !errors.Is(err, store.ErrInvalidUserSkillSelection) {
			t.Fatalf("personal skill validation error=%v, want ErrInvalidUserSkillSelection", err)
		}
	}
}

func TestSkillFeatureRevocationBlocksSelectionRegenerationAndFallback(t *testing.T) {
	for _, scope := range []string{"personal", "workspace"} {
		t.Run(scope, func(t *testing.T) {
			o, provider, model, conversation, _, db := setupToolRouteTest(t)
			if _, err := db.Exec(`INSERT INTO user_groups(id,name,permissions) VALUES('skill-intersection','Skill intersection','{}');
				UPDATE users SET role='user',group_id='skill-intersection' WHERE id='u1'`); err != nil {
				t.Fatal(err)
			}
			if err := store.SetModelQuotas(t.Context(), db, model.ID, []store.ModelGroupQuota{{
				GroupID: "skill-intersection", LimitType: "count", LimitValue: 0,
			}}); err != nil {
				t.Fatal(err)
			}
			workspaceID := ""
			if scope == "workspace" {
				ws, err := store.CreateWorkspace(t.Context(), db, "u1", "Skill revocation")
				if err != nil {
					t.Fatal(err)
				}
				workspaceID = ws.ID
				if _, err := db.Exec(`UPDATE conversations SET workspace_id=? WHERE id=?`, ws.ID, conversation.ID); err != nil {
					t.Fatal(err)
				}
			}
			skill, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{
				UserID: "u1", WorkspaceID: workspaceID, Name: "revocation-test", Description: "Test", Instructions: "REVOKED_SKILL_CONTENT",
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := o.Run(t.Context(), RunRequest{
				UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
				UserText: "Hello", ToolMode: ToolModeDisabled, SelectedUserSkillIDs: []string{skill.ID},
			}, func(SseEvent) {})
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.mainRequests) != 1 {
				t.Fatalf("provider requests=%d", len(provider.mainRequests))
			}
			primary := provider.mainRequests[0]
			if len(primary.SelectedUserSkillIDs) != 1 || primary.SelectedUserSkillIDs[0] != skill.ID {
				t.Fatal("fallback lost selected skill identity")
			}
			p := store.DefaultUserGroupPermissions()
			p.AllowSkills = false
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE user_groups SET permissions=? WHERE id=(SELECT group_id FROM users WHERE id='u1')`, string(raw)); err != nil {
				t.Fatal(err)
			}
			for _, req := range []RunRequest{
				{UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID, UserText: "New selection", SelectedUserSkillIDs: []string{skill.ID}},
				{UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID, UserText: "Hello", ParentID: result.UserMessage.ID, ReuseExistingUserMessage: true},
			} {
				// A stale caller explicitly allows everything; the current database policy wins.
				req.ToolAccessPolicy = groupToolAccessPolicy(store.DefaultUserGroupPermissions())
				if _, err := o.Run(t.Context(), req, func(SseEvent) {}); !errors.Is(err, store.ErrInvalidUserSkillSelection) {
					t.Fatalf("revoked skill request error=%v", err)
				}
			}
			if _, _, _, err := o.buildFallbackRequest(t.Context(), primary, model.ID); !errors.Is(err, store.ErrInvalidUserSkillSelection) {
				t.Fatalf("fallback accepted revoked skill: %v", err)
			}
			if len(provider.mainRequests) != 1 {
				t.Fatal("revoked skill reached provider")
			}
			if _, err := o.Run(t.Context(), RunRequest{
				UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID, UserText: "Ordinary message", ToolMode: ToolModeDisabled,
			}, func(SseEvent) {}); err != nil {
				t.Fatalf("ordinary chat blocked by skill denial: %v", err)
			}
		})
	}
}

func TestSelectedUserSkillsPersistInjectAtUserAuthorityAndRegenerate(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	skill, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{
		UserID: "u1", Name: "meeting-follow-up", Description: "Extract action items",
		Instructions: "PRIVATE_SKILL_BODY: list decisions, owners, and deadlines.",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := orchestrator.Run(context.Background(), RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "Summarize this meeting", ToolMode: ToolModeDisabled,
		SelectedUserSkillIDs: []string{skill.ID, skill.ID},
	}, func(SseEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.mainRequests) != 1 {
		t.Fatalf("provider requests=%d", len(provider.mainRequests))
	}
	request := provider.mainRequests[0]
	if strings.Contains(request.SystemPrompt, "PRIVATE_SKILL_BODY") {
		t.Fatalf("private skill was elevated into system prompt: %s", request.SystemPrompt)
	}
	if len(request.History) == 0 || request.History[len(request.History)-1].Role != "user" {
		t.Fatalf("history has no last user turn: %+v", request.History)
	}
	lastUser := renderBlocksAsText(request.History[len(request.History)-1].Blocks)
	if !strings.Contains(lastUser, "Summarize this meeting") || !strings.Contains(lastUser, "PRIVATE_SKILL_BODY") {
		t.Fatalf("last user history lost prompt/skill: %s", lastUser)
	}
	var persisted []string
	if err := json.Unmarshal(result.UserMessage.SelectedUserSkillIDs, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0] != skill.ID {
		t.Fatalf("persisted selection=%v", persisted)
	}

	_, err = orchestrator.Run(context.Background(), RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "Summarize this meeting", ToolMode: ToolModeDisabled,
		ParentID: result.UserMessage.ID, ReuseExistingUserMessage: true,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.mainRequests) != 2 {
		t.Fatalf("provider requests after regenerate=%d", len(provider.mainRequests))
	}
	regeneratedHistory := provider.mainRequests[1].History
	if len(regeneratedHistory) == 0 || !strings.Contains(renderBlocksAsText(regeneratedHistory[len(regeneratedHistory)-1].Blocks), "PRIVATE_SKILL_BODY") {
		t.Fatalf("regenerate did not restore selected skill: %+v", regeneratedHistory)
	}
}

func TestSelectedUserSkillsRejectNotOwnedIDBeforeMessagePersistence(t *testing.T) {
	orchestrator, _, model, conversation, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash) VALUES('u2','u2@example.test','h')`); err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{
		UserID: "u2", Name: "other-user-skill", Description: "private", Instructions: "do not leak",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = orchestrator.Run(context.Background(), RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "try", ToolMode: ToolModeDisabled, SelectedUserSkillIDs: []string{other.ID},
	}, func(SseEvent) {})
	if !errors.Is(err, store.ErrInvalidUserSkillSelection) {
		t.Fatalf("cross-user selection err=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=?`, conversation.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid selection persisted %d messages", count)
	}
}

func TestSelectedUserSkillsAppendOnlyToLastUserHistoryEntry(t *testing.T) {
	history := []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "first"}}},
		{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "answer"}}},
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "last"}}},
	}
	out := injectSelectedUserSkillsIntoHistory(history, []store.UserSkill{{
		Name: "last-only", Description: "d", Instructions: "LAST_ONLY_MARKER",
	}})
	if strings.Contains(renderBlocksAsText(out[0].Blocks), "LAST_ONLY_MARKER") {
		t.Fatal("skill was appended to an earlier user message")
	}
	if !strings.Contains(renderBlocksAsText(out[2].Blocks), "LAST_ONLY_MARKER") {
		t.Fatal("skill was not appended to the last user message")
	}
}
