package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/store"
)

const deepResearchLoopTaskModel = "task-dr-loop"

// deepResearchLoopProvider answers each Deep Research task by its system
// prompt and records the writer prompt. Reader calls run concurrently.
type deepResearchLoopProvider struct {
	mu           sync.Mutex
	reflections  int
	reads        int
	writerPrompt string
}

func (p *deepResearchLoopProvider) ID() string { return "openai" }

func (p *deepResearchLoopProvider) Stream(
	_ context.Context,
	req UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	prompt := lastUserText(req.History)
	p.mu.Lock()
	defer p.mu.Unlock()
	var output string
	switch {
	case req.Model.RequestID != deepResearchLoopTaskModel:
		p.writerPrompt = prompt
		output = "Report [1]."
		onEvent(SseEvent{Type: "text_delta", Text: output})
	case strings.Contains(req.SystemPrompt, "planning an investigation"):
		output = `{"title":"Alpha study","research_type":"concept","scope":"global","sub_questions":[` +
			`{"id":"q1","dimension":"basics","question":"What is alpha?","search_queries":["alpha basics","alpha offtopic"]},` +
			`{"id":"q2","dimension":"data","question":"What does the data say about beta?","search_queries":["beta data"]}]}`
	case strings.Contains(req.SystemPrompt, "reading ONE web source"):
		p.reads++
		if strings.Contains(prompt, "offtopic") {
			output = `{"relevant":false,"summary":"A login page."}`
		} else {
			output = `{"relevant":true,"published":"2026-05-01","summary":"Explains the topic.",` +
				`"facts":["The measured value is 42 units."],"leads":["gamma standard"]}`
		}
	case strings.Contains(req.SystemPrompt, "auditing research coverage"):
		p.reflections++
		if p.reflections == 1 {
			// "alpha basics" was already searched and "q9" does not exist: the
			// engine must drop the repeat and re-home nothing else.
			output = `{"thinking":"Alpha basics are established; beta still lacks primary data.","sufficient":false,` +
				`"uncovered":["q2"],"weak_claims":["value is 42"],` +
				`"follow_ups":[{"id":"q2","query":"beta primary statistics"},{"id":"q9","query":"alpha basics"}],` +
				`"new_sub_questions":[{"dimension":"risks","question":"What are the risks of alpha?","search_queries":["alpha risks"]}]}`
		} else {
			output = `{"thinking":"Coverage now holds across all dimensions.","sufficient":true}`
		}
	case strings.Contains(req.SystemPrompt, "cross-validating research evidence"):
		output = `{"confirmed":[{"claim":"The measured value is 42 units.","sources":[1,2]}]}`
	default:
		output = `{}`
	}
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: output}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 2, OutputTokens: 1},
	}, nil
}

// deepResearchLoopTools returns two distinct-domain hits per query and a page
// body for every fetch.
type deepResearchLoopTools struct {
	mu      sync.Mutex
	queries []string
	fetched []string
}

func (t *deepResearchLoopTools) List(string) []ToolDef {
	return []ToolDef{{Name: "aivory_web_search"}, {Name: "web_fetch"}}
}

func (t *deepResearchLoopTools) Run(_ context.Context, name string, input []byte, _ *ToolContext) (string, []Citation, error) {
	var in struct {
		Query string `json:"query"`
		URL   string `json:"url"`
	}
	_ = json.Unmarshal(input, &in)
	t.mu.Lock()
	defer t.mu.Unlock()
	switch name {
	case "aivory_web_search":
		t.queries = append(t.queries, in.Query)
		slug := strings.ReplaceAll(in.Query, " ", "-")
		return "results", []Citation{
			{Title: in.Query + " one", URL: "https://" + slug + ".one.org/page", Snippet: "snippet about " + in.Query, Source: "web"},
			{Title: in.Query + " two", URL: "https://" + slug + ".two.org/page", Snippet: "snippet about " + in.Query, Source: "web"},
		}, nil
	case "web_fetch":
		t.fetched = append(t.fetched, in.URL)
		return "Full text of " + in.URL + ". The measured value is 42 units.", nil, nil
	}
	return "", nil, errors.New("unexpected tool " + name)
}

func setupDeepResearchLoopTest(t *testing.T) (*Orchestrator, *deepResearchLoopProvider, *deepResearchLoopTools, *store.Model, *store.Conversation) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "deep-research-loop.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','dr@example.com','h','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "DR", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	taskModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: deepResearchLoopTaskModel, Label: "Task", Enabled: true, Stream: true, ToolMode: "none",
	})
	if err != nil {
		t.Fatalf("create task model: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "chat-dr-loop", Label: "Chat", Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatalf("create chat model: %v", err)
	}
	if err := store.SetSetting(db, "task_model_id", taskModel.ID); err != nil {
		t.Fatalf("set task model: %v", err)
	}
	// The settings cache is process-global in tests; reset it like the other
	// orchestrator fixtures so a disabled-tools test cannot leak in.
	if err := store.SetSetting(db, "disabled_tools", []string{}); err != nil {
		t.Fatalf("reset disabled tools: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c1", UserID: "u1", Title: "Existing title", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	logger := log.New(io.Discard, "", 0)
	provider := &deepResearchLoopProvider{}
	registry := NewRegistry(logger)
	registry.Register(provider)
	tools := &deepResearchLoopTools{}
	orchestrator := NewOrchestrator(db, registry, tools, nil, nil, nil, NewTaskLLM(db, registry, logger), nil, logger)
	return orchestrator, provider, tools, model, conversation
}

func TestDeepResearchLoopReadsReflectsAndFollowsLeads(t *testing.T) {
	orchestrator, provider, tools, model, conversation := setupDeepResearchLoopTest(t)
	var mu sync.Mutex
	var events []SseEvent
	result, err := orchestrator.Run(context.Background(), RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "Research alpha", ToolMode: ToolModeEnabled, Mode: ModeDeepResearch,
	}, func(event SseEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Round 1 runs every plan query; the reflection's follow-up and the new
	// sub-question's query run in round 2; the repeated query never runs again.
	// Searches inside a round run concurrently, so compare each round as a set.
	if len(tools.queries) != 5 ||
		!sameStrings(tools.queries[:3], []string{"alpha basics", "beta data", "alpha offtopic"}) ||
		!sameStrings(tools.queries[3:], []string{"beta primary statistics", "alpha risks"}) {
		t.Fatalf("searches = %q, want the 3 plan queries then the 2 follow-ups", tools.queries)
	}
	if len(tools.fetched) != 10 || provider.reads != 10 {
		t.Fatalf("fetched %d pages and read %d, want every fetched page read (10)", len(tools.fetched), provider.reads)
	}
	if provider.reflections != 2 {
		t.Fatalf("reflections = %d, want one after each of the two rounds", provider.reflections)
	}

	phases := map[string]bool{}
	notes := 0
	newTask := false
	offTopicSettled := 0
	for _, event := range events {
		switch event.Type {
		case "research_phase":
			phases[event.Status] = true
		case "research_note":
			notes++
		case "research_task":
			if event.ID == "q3" && event.Text == "What are the risks of alpha?" {
				newTask = true
			}
		case "research_source":
			if event.Status == "read" {
				offTopicSettled++
			}
		}
	}
	for _, phase := range []string{"planning", "searching", "reading", "reflecting", "validating", "writing"} {
		if !phases[phase] {
			t.Fatalf("missing research_phase %q in %v", phase, phases)
		}
	}
	if notes != 2 || !newTask || offTopicSettled != 2 {
		t.Fatalf("notes=%d newTask=%v offTopicSettled=%d, want 2/true/2", notes, newTask, offTopicSettled)
	}

	// The writer reasons over reading notes and cross-validation, never over
	// pages the reader rejected.
	writer := provider.writerPrompt
	for _, want := range []string{
		"Key facts:", "The measured value is 42 units.", "Published: 2026-05-01",
		"CONFIRMED [1][2]", "risks: What are the risks of alpha?", "Coverage now holds across all dimensions.",
	} {
		if !strings.Contains(writer, want) {
			t.Fatalf("writer prompt is missing %q:\n%s", want, writer)
		}
	}
	if strings.Contains(writer, "offtopic") {
		t.Fatalf("writer prompt kept an off-topic source:\n%s", writer)
	}

	// The research log is persisted with the panel state for reloads.
	var blocks []UnifiedBlock
	if err := json.Unmarshal(result.AssistantMessage.Blocks, &blocks); err != nil {
		t.Fatalf("decode assistant blocks: %v", err)
	}
	var state drState
	for _, block := range blocks {
		if block.Kind == "research" {
			if err := json.Unmarshal([]byte(block.Text), &state); err != nil {
				t.Fatalf("decode research state: %v", err)
			}
		}
	}
	if state.Rounds != 2 || len(state.Notes) != 2 || len(state.Tasks) != 3 {
		t.Fatalf("persisted state rounds=%d notes=%d tasks=%d, want 2/2/3", state.Rounds, len(state.Notes), len(state.Tasks))
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

func TestDeepResearchFollowUpDecodesBothShapes(t *testing.T) {
	var ref drReflection
	raw := `{"follow_ups":[{"id":"q2","query":"a"},{"sub_question":"q3","query":"b"}],"new_queries":["c"]}`
	if err := json.Unmarshal([]byte(raw), &ref); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(ref.FollowUps) != 2 || ref.FollowUps[0] != (drFollowUp{ID: "q2", Query: "a"}) ||
		ref.FollowUps[1] != (drFollowUp{ID: "q3", Query: "b"}) {
		t.Fatalf("follow_ups = %+v", ref.FollowUps)
	}
	if len(ref.NewQueries) != 1 || ref.NewQueries[0].Query != "c" || ref.NewQueries[0].ID != "" {
		t.Fatalf("legacy new_queries = %+v", ref.NewQueries)
	}
}

func TestSanitizeSubQuestionsDeduplicatesIDsAndCapsQueries(t *testing.T) {
	got := sanitizeSubQuestions([]drSubQuestion{
		{ID: "q1", Question: "First", SearchQueries: []string{"a", "b", "c", "d", "e"}},
		{ID: "q1", Question: "Second"},
		{ID: "", Question: "   "},
		{Question: "Third", SearchQueries: []string{" ", ""}},
	}, 8)
	if len(got) != 3 {
		t.Fatalf("got %d sub-questions, want 3: %+v", len(got), got)
	}
	if got[0].ID != "q1" || got[1].ID == "q1" || got[2].ID == got[1].ID {
		t.Fatalf("ids are not unique: %+v", got)
	}
	if len(got[0].SearchQueries) != drMaxQueriesPerSubQuestion {
		t.Fatalf("queries not capped: %v", got[0].SearchQueries)
	}
	if len(got[1].SearchQueries) != 1 || got[1].SearchQueries[0] != "Second" ||
		len(got[2].SearchQueries) != 1 || got[2].SearchQueries[0] != "Third" {
		t.Fatalf("empty query lists must fall back to the question: %+v", got)
	}
	if capped := sanitizeSubQuestions(got, 2); len(capped) != 2 {
		t.Fatalf("limit not applied: %+v", capped)
	}
}

func TestDeepResearchStopsStartingRoundsNearTheBudget(t *testing.T) {
	rs := &researcher{tc: &ToolContext{DeepResearch: true}}
	ctx, cancel := context.WithTimeout(context.Background(), drRoundReserve/2)
	defer cancel()
	if rs.timeForAnotherRound(ctx) {
		t.Fatal("a round must not start with less than the round reserve left")
	}
	roomy, cancelRoomy := context.WithTimeout(context.Background(), drRoundReserve+time.Minute)
	defer cancelRoomy()
	if !rs.timeForAnotherRound(roomy) {
		t.Fatal("a round should start when the reserve fits")
	}
}

func TestWriterSourcesPrefersNotesAndRespectsBudget(t *testing.T) {
	previous := drWriterSourceChars
	drWriterSourceChars = 6000
	t.Cleanup(func() { drWriterSourceChars = previous })
	long := strings.Repeat("x", 20000)
	rendered := writerSources([]evidenceItem{
		{Index: 1, Grade: "A", URL: "https://a.org", Title: "Read", Body: long, Read: true,
			Summary: "Summary one.", Facts: []string{"Fact one."}},
		{Index: 2, Grade: "C", URL: "https://b.org", Title: "Unread", Body: long},
		{Index: 3, Grade: "C", URL: "https://c.org", Title: "Snippet only", Snippet: "Snippet three."},
	})
	if !strings.Contains(rendered, "Key facts:\n- Fact one.") || !strings.Contains(rendered, "Snippet three.") {
		t.Fatalf("notes or snippet missing:\n%s", rendered)
	}
	if len(rendered) > drWriterSourceChars+1000 {
		t.Fatalf("rendered %d bytes, budget %d", len(rendered), drWriterSourceChars)
	}
}
