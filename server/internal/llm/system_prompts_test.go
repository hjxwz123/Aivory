package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
	"aivory/server/internal/systemprompts"
	"aivory/server/internal/typesafe"
)

func TestSystemPromptOverridesReachChatTasksAndComposition(t *testing.T) {
	o, provider, _, conv, _, db := setupToolRouteTest(t)
	values := map[string]string{
		"task.tool_route":             "Custom routing. Reply only 0 or 1",
		"task.title":                  "Custom navigation title",
		"conversation.identity.zh":    "模型：{{model}}。100% 保留。",
		"title.language.zh":           "仅用简体中文。",
		"routing.tool_purpose.python": "Custom analysis purpose",
		"compaction.summary":          "Preserve paths within {{max_tokens}} tokens; 100% faithful.",
		"task.compact":                strings.Repeat("Configured compactor. ", 100),
		"tools.final.search":          "Custom final search answer",
		"vision.input":                "Read {{image_label}} faithfully.",
	}
	if err := store.SetSetting(db, systemprompts.SettingKey, values); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []TaskKind{TaskToolRoute, TaskTitle} {
		if _, err := o.task.Run(t.Context(), kind, "hello", RunOpts{UserID: "u1", ConversationID: conv.ID, ToolRouteToolDescriptions: "live tools"}); err != nil {
			t.Fatal(err)
		}
		got := provider.taskRequests[len(provider.taskRequests)-1].SystemPrompt
		if !strings.HasPrefix(got, values[string(kind)]) {
			t.Fatalf("override missing from %s: %s", kind, got)
		}
		if kind == TaskToolRoute && !strings.Contains(got, "live tools") {
			t.Fatal("override dropped live tool context")
		}
	}
	got := composeSystemPrompt(systemPromptOpts{DB: db, Locale: "zh", ModelLabel: "Example"})
	if !strings.HasPrefix(got, "模型：Example。100% 保留。") || strings.Contains(got, "%!") {
		t.Fatalf("literal substitution failed: %s", got)
	}
	if titleLanguageDirective("zh", db) != "仅用简体中文。" {
		t.Fatal("title locale override ignored")
	}
	if !strings.Contains(toolRouteToolDescriptions([]ToolDef{{Name: "python_execute"}}, nil, nil, db), "Custom analysis purpose") {
		t.Fatal("tool purpose ignored")
	}
	if visionEvidencePrompt("a.png", "image/png", db) != "Read a.png, image/png faithfully." {
		t.Fatal("vision input override ignored")
	}
	if visionEvidenceStamp("vision", db) == visionEvidenceStamp("vision") {
		t.Fatal("vision override did not invalidate cached evidence")
	}
	text := buildCompactionPrompt("", compactionInstruction(db), "source", 100)
	if !strings.Contains(text, "100 tokens; 100% faithful.") {
		t.Fatalf("unsafe token substitution: %s", text)
	}
	base := compactionPayloadBudget(10000, 100, "", compactionSummaryInstruction, nil)
	custom := compactionPayloadBudget(10000, 100, "", compactionSummaryInstruction, nil, systemprompts.Text(db, "task.compact"))
	if custom >= base {
		t.Fatal("configured system prompt was omitted from compaction token budgeting")
	}
	ctx := systemprompts.WithDB(t.Context(), db)
	if toolFinalizationInstruction(errSearchRoundComplete, ctx) != values["tools.final.search"] {
		t.Fatal("provider finalization override ignored")
	}
	if err := store.SetSetting(db, systemprompts.SettingKey, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if configuredPromptL10n(db, "zh").defaultStyle != promptL10nFor("zh").defaultStyle {
		t.Fatal("locale fallback changed")
	}
	if titleLanguageDirective("zh", db) != "请用简体中文写这个标题。" {
		t.Fatal("title fallback failed")
	}
}

func TestSystemPromptOverridesReachDecisionInstructionsAndCriteria(t *testing.T) {
	db := decisionTestDB(t)
	if err := store.SetSetting(db, systemprompts.SettingKey, map[string]string{
		"decision.tool_scope.instructions": "Custom instructions",
		"decision.tool_scope.search_only":  "Custom direct answer criterion",
		"decision.tool_scope.full_tools":   "Custom execution criterion",
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req typesafe.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		question := req.Questions["scope"]
		if question.Instructions != "Custom instructions\nAvailable tools and their purposes:\nlive Python" {
			t.Errorf("instructions=%v", question.Instructions)
		}
		criteria := question.Criteria.(map[string]any)
		if criteria["search_only"] != "Custom direct answer criterion" || criteria["full_tools"] != "Custom execution criterion" || len(criteria) != 2 {
			t.Errorf("criteria=%v", criteria)
		}
		fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{"scope":{"type":"choice","choice":"search_only","confidence":0.1,"probabilities":{"search_only":0.55,"full_tools":0.45}}},"usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	defer srv.Close()
	seedDecisionModel(t, db, srv.URL, "jev-1.13.0")
	model, err := store.GetModel(t.Context(), db, "decision-model")
	if err != nil {
		t.Fatal(err)
	}
	full, err := NewTaskLLM(db, nil, nil).decisionToolScope(t.Context(), model, "用户请求", typesafe.Metadata{UserID: "decision-user"}, "live Python")
	if err != nil || full {
		t.Fatalf("choice-only decision changed: full=%v err=%v", full, err)
	}
}
