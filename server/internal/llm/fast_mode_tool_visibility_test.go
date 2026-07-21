package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestFastModeProviderRequestHidesPythonExecuteFromEveryToolSurface(t *testing.T) {
	for _, tc := range []struct {
		name     string
		official bool
	}{
		{name: "self-built tools"},
		{name: "official tools", official: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
			if tc.official {
				if _, err := db.Exec(`UPDATE channels SET api_format='responses' WHERE id=?`, model.ChannelID); err != nil {
					t.Fatalf("enable Responses API: %v", err)
				}
				if _, err := db.Exec(`UPDATE models SET official_tools='["web_search","code_interpreter"]' WHERE id=?`, model.ID); err != nil {
					t.Fatalf("configure official tools: %v", err)
				}
			}
			seedFastModePythonHistory(t, db, model, conversation)
			if err := store.SetFastModel(context.Background(), db, model.ID); err != nil {
				t.Fatalf("set fast model: %v", err)
			}

			runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{
				ToolMode: ToolModeAuto,
				Fast:     true,
				UserText: "Give me a current answer",
			})
			if provider.routeCalls != 0 {
				t.Fatalf("fast mode called the tool router %d times", provider.routeCalls)
			}
			if len(provider.mainRequests) != 1 {
				t.Fatalf("provider requests = %d, want 1", len(provider.mainRequests))
			}

			request := provider.mainRequests[0]
			if !requestHasTool(request, "web_search") {
				t.Fatalf("fast request lost web_search: tools=%+v official=%v", request.Tools, request.OfficialTools)
			}
			if tc.official {
				if len(request.Tools) != 0 || len(request.OfficialTools) == 0 {
					t.Fatalf("official request used wrong tool surface: tools=%+v official=%v", request.Tools, request.OfficialTools)
				}
			} else if len(request.Tools) == 0 || len(request.OfficialTools) != 0 {
				t.Fatalf("self-built request used wrong tool surface: tools=%+v official=%v", request.Tools, request.OfficialTools)
			}

			for _, official := range request.OfficialTools {
				if official == "code_interpreter" {
					t.Errorf("fast request exposed official code_interpreter: %v", request.OfficialTools)
				}
			}

			visibleRequest, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("marshal provider request: %v", err)
			}
			for _, forbidden := range []string{"python_execute", "code_interpreter"} {
				if strings.Contains(string(visibleRequest), forbidden) {
					t.Errorf("fast provider request exposed %s outside Raw: tools=%+v official=%v system=%q",
						forbidden, request.Tools, request.OfficialTools, request.SystemPrompt)
				}
			}

			var priorAssistant *UnifiedMessage
			for index := range request.History {
				if request.History[index].Role == "assistant" {
					priorAssistant = &request.History[index]
					break
				}
			}
			if priorAssistant == nil {
				t.Fatal("captured history lost the prior assistant message")
			}
			blocksJSON, err := json.Marshal(priorAssistant.Blocks)
			if err != nil {
				t.Fatalf("marshal prior assistant blocks: %v", err)
			}
			for _, forbidden := range []string{"python_execute", "code_interpreter"} {
				if strings.Contains(string(priorAssistant.Raw), forbidden) {
					t.Errorf("fast history Raw exposed %s: %s", forbidden, priorAssistant.Raw)
				}
				if strings.Contains(string(blocksJSON), forbidden) {
					t.Errorf("fast history Blocks exposed %s: %s", forbidden, blocksJSON)
				}
			}
		})
	}
}

func seedFastModePythonHistory(t *testing.T, db *sql.DB, model *store.Model, conversation *store.Conversation) {
	t.Helper()
	userBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "Analyze the old dataset"}})
	previousUser, err := store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conversation.ID,
		Role:           "user",
		Provider:       "openai",
		ModelID:        model.ID,
		Blocks:         userBlocks,
	})
	if err != nil {
		t.Fatalf("seed previous user: %v", err)
	}
	assistantBlocks, _ := json.Marshal([]UnifiedBlock{
		{
			Kind:     "tool_call",
			ToolName: "python_execute",
			ToolID:   "old-python-call",
			Input:    json.RawMessage(`{"code":"print(42)"}`),
			Summary:  "Executed Python",
		},
		{
			Kind:     "tool_output",
			ToolName: "python_execute",
			ToolID:   "old-python-call",
			Text:     "42",
		},
	})
	_, err = store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conversation.ID,
		ParentID:       previousUser.ID,
		Role:           "assistant",
		Provider:       "openai",
		ModelID:        model.ID,
		Blocks:         assistantBlocks,
		Raw: json.RawMessage(`[
			{"type":"function_call","name":"python_execute","call_id":"old-python-call","arguments":"{\\"code\\":\\"print(42)\\"}"},
			{"type":"function_call_output","call_id":"old-python-call","output":"42"}
		]`),
		StopReason: "stop",
		Status:     "complete",
	})
	if err != nil {
		t.Fatalf("seed previous assistant: %v", err)
	}
}

func TestStripFastModeCodeBlocksPreservesSafeHistory(t *testing.T) {
	history := []UnifiedMessage{
		{
			Role: "assistant",
			Blocks: []UnifiedBlock{
				{Kind: "tool_call", ToolName: "web_search", ToolID: "safe", Summary: "found a source"},
				{Kind: "tool_call", ToolName: "python_execute", ToolID: "python", Input: json.RawMessage(`{"code":"print(42)"}`)},
				// Legacy output blocks did not always repeat the tool name; the call ID
				// must still keep the prohibited result paired with its call.
				{Kind: "tool_output", ToolID: "python", Text: "42"},
				{Kind: "text", Text: "Keep the final answer."},
			},
			Raw: json.RawMessage(`[{"type":"function_call","name":"python_execute"}]`),
		},
		{
			Role: "assistant",
			Blocks: []UnifiedBlock{
				{Kind: "tool_call", ToolName: "code_interpreter", ToolID: "hosted"},
			},
		},
	}

	filtered := stripFastModeCodeBlocks(history)
	if len(filtered) != 2 {
		t.Fatalf("filtered history length = %d, want 2", len(filtered))
	}
	if len(filtered[0].Raw) != 0 {
		t.Fatalf("fast history retained provider Raw: %s", filtered[0].Raw)
	}
	firstJSON, _ := json.Marshal(filtered[0].Blocks)
	for _, forbidden := range []string{"python_execute", "code_interpreter", "print(42)", `"42"`} {
		if strings.Contains(string(firstJSON), forbidden) {
			t.Fatalf("filtered blocks retained %q: %s", forbidden, firstJSON)
		}
	}
	if !strings.Contains(string(firstJSON), "web_search") || !strings.Contains(string(firstJSON), "Keep the final answer") {
		t.Fatalf("filter lost safe history: %s", firstJSON)
	}
	if len(filtered[1].Blocks) != 1 || filtered[1].Blocks[0].Text != fastModeCodeHistoryPlaceholder {
		t.Fatalf("code-only history needs a provider-safe placeholder: %+v", filtered[1].Blocks)
	}
	// Filtering must not mutate cached/shared history or nested Raw/Input slices.
	filtered[0].Blocks[0].ToolName = "changed"
	if history[0].Blocks[0].ToolName != "web_search" || len(history[0].Raw) == 0 {
		t.Fatalf("filter mutated its input: %+v", history[0])
	}
}
