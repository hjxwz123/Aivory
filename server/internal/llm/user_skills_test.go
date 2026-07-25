package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

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
