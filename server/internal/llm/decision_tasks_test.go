package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"aivory/server/internal/store"
	"aivory/server/internal/typesafe"
)

func TestDecisionPolicyWorkflows(t *testing.T) {
	db := decisionTestDB(t)
	calls := 0
	choice, confidence, probability, status := "search_only", 0.95, 0.01, 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer admin-key" {
			t.Errorf("bad request path/auth")
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		var req typesafe.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Model != "jev-1.13.0" {
			t.Errorf("model=%s", req.Model)
		}
		answers := map[string]any{}
		for id, q := range req.Questions {
			if q.Type == typesafe.Noul {
				answers[id] = map[string]any{"type": "noul", "noul": probability}
				continue
			}
			selected := choice
			if id == "duplicate" && choice == "stale" {
				selected = "none"
			}
			probabilities := map[string]float64{}
			for option := range q.Criteria.(map[string]any) {
				probabilities[option] = 0
			}
			probabilities[selected] = 1
			answers[id] = map[string]any{"type": "choice", "choice": selected, "confidence": confidence, "probabilities": probabilities}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "answers": answers, "usage": map[string]int{"input_tokens": 1000, "output_tokens": 2}})
	}))
	defer srv.Close()
	if _, err := db.Exec(`INSERT INTO channels(id,name,type,base_url,api_key,enabled) VALUES('decision-channel','TypeSafe','typesafe',?,'admin-key',1)`, srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,channel_id,kind,request_id,label,enabled,price_input) VALUES('decision-model','decision-channel','decision','jev-1.13.0','Jev',1,0.25)`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tool_route_model_id", "memory_dedup_model_id", "memory_adjudicate_model_id", "moderation_model_id"} {
		if err := store.SetSetting(db, key, "decision-model"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetSetting(db, "daily_token_limit", 100000); err != nil {
		t.Fatal(err)
	}
	task := NewTaskLLM(db, nil, nil)
	o := &Orchestrator{db: db, task: task}
	worker := NewMemoryWorker(db, task, nil)
	ctx := context.Background()
	oldThreshold := toolRouteSchemaTokenThreshold
	toolRouteSchemaTokenThreshold = 0
	defer func() { toolRouteSchemaTokenThreshold = oldThreshold }()
	route := func() bool {
		return o.autoTurnNeedsTools(ctx, RunRequest{UserID: "decision-user", UserText: "请总结这段文字"}, nil, nil, nil, nil, nil, nil, false, "", "message")
	}
	if route() {
		t.Fatal("confident search-only route retained all tools")
	}
	confidence = 0.7
	if !route() {
		t.Fatal("uncertain route narrowed tools")
	}
	confidence, choice = 0.95, "full_tools"
	if !route() {
		t.Fatal("full-tools verdict ignored")
	}
	status = 503
	if !route() {
		t.Fatal("failure narrowed tools")
	}
	status = 200
	before := calls
	if !o.autoTurnNeedsTools(ctx, RunRequest{}, nil, nil, nil, nil, nil, nil, true, "", "") || calls != before {
		t.Fatal("local selected-skill rule invoked model")
	}

	if _, err := db.Exec(`INSERT INTO memories(id,user_id,memory_text) VALUES('memory-old','decision-user','请用中文回复')`); err != nil {
		t.Fatal(err)
	}
	choice, confidence = "memory-old", 0.95
	candidate := memoryCandidate{MemoryText: "我希望你使用中文回答", Slot: "language", Value: "Chinese"}
	if got := worker.findSemanticDuplicate(ctx, "decision-user", "", candidate); got != "memory-old" {
		t.Fatalf("duplicate=%q", got)
	}
	confidence = 0.8
	if got := worker.findSemanticDuplicate(ctx, "decision-user", "", candidate); got != "" {
		t.Fatalf("uncertain duplicate=%q", got)
	}
	choice, confidence = "none", 0.99
	if got := worker.findSemanticDuplicate(ctx, "decision-user", "", candidate); got != "" {
		t.Fatalf("no-match duplicate=%q", got)
	}
	before = calls
	if got := worker.findSemanticDuplicate(ctx, "decision-user", "", memoryCandidate{MemoryText: "请用中文回复"}); got != "memory-old" || calls != before {
		t.Fatal("exact match made an API call")
	}
	choice = "stale"
	existing := []existingMem{{ID: "memory-old", Value: "English"}}
	if got := worker.adjudicate(ctx, "decision-user", "", candidate, existing); got["memory-old"] != "stale" {
		t.Fatalf("verdict=%v", got)
	}
	confidence = 0.7
	if got := worker.adjudicate(ctx, "decision-user", "", candidate, existing); got["memory-old"] != "unknown_current" {
		t.Fatalf("uncertain verdict=%v", got)
	}

	if _, err := db.Exec(`INSERT INTO user_groups(id,name,is_default) VALUES('decision-group','Default',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE memories SET slot='language',value='English',memory_text='Reply in English' WHERE id='memory-old'`); err != nil {
		t.Fatal(err)
	}
	if !store.MemoryEnabledForUser(ctx, db, "decision-user") {
		t.Fatal("memory fixture is disabled")
	}
	worker.adjudicateAndWrite(ctx, "decision-user", "", nil, candidate)
	var uncertainCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE user_id='decision-user' AND status='UNKNOWN_CURRENT'`).Scan(&uncertainCount); err != nil || uncertainCount != 2 {
		t.Fatalf("uncertain memory writes=%d err=%v", uncertainCount, err)
	}
	status = 503
	if got := worker.adjudicate(ctx, "decision-user", "", candidate, existing); len(got) != 0 {
		t.Fatalf("failure verdict=%v", got)
	}
	status = 200
	if err := store.SetSetting(db, "moderation_categories", []string{"test category"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "moderation_keywords", []string{"keyword"}); err != nil {
		t.Fatal(err)
	}
	chat := &store.Model{ModerationEnabled: true, ModerationMode: "model"}
	for _, tc := range []struct {
		probability float64
		status      int
		text        string
		blocked     bool
	}{
		{0.99, 200, "message", true}, {0.01, 200, "keyword", false}, {0.5, 200, "keyword", true}, {0.5, 200, "ordinary", false}, {0.01, 503, "keyword", true},
	} {
		probability, status = tc.probability, tc.status
		blocked, _, err := o.moderatePrompt(ctx, chat, tc.text, "decision-user", "", "message")
		if err != nil || blocked != tc.blocked {
			t.Fatalf("moderation %+v => %v, %v", tc, blocked, err)
		}
	}
	var count, logs int
	var cost int64
	if err := db.QueryRow(`SELECT COUNT(*),SUM(cost_micros) FROM billing_usage WHERE model_id='decision-model'`).Scan(&count, &cost); err != nil {
		t.Fatal(err)
	}
	if count == 0 || cost != int64(count)*250 {
		t.Fatalf("billing count=%d cost=%d", count, cost)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE model_id='decision-model' AND channel_id='decision-channel' AND status='ok'`).Scan(&logs); err != nil || logs != count {
		t.Fatalf("usage logs=%d billing=%d err=%v", logs, count, err)
	}
	for _, purpose := range []string{"task.tool_route", "task.memory_dedup", "task.memory_adjudicate", "task.moderation"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM billing_usage WHERE purpose=?`, purpose).Scan(&n); err != nil || n == 0 {
			t.Fatalf("purpose %s missing: %v", purpose, err)
		}
	}
	var reserved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM quota_ledger WHERE status='reserved'`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("leaked reservations: %d %v", reserved, err)
	}
	before = calls
	if _, err := db.Exec(`UPDATE channels SET enabled=0 WHERE id='decision-channel'`); err != nil {
		t.Fatal(err)
	}
	if !route() || calls != before {
		t.Fatal("disabled channel invoked or narrowed tools")
	}
	if _, err := db.Exec(`UPDATE channels SET enabled=1 WHERE id='decision-channel'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "daily_token_limit", 1); err != nil {
		t.Fatal(err)
	}
	model := task.policyDecisionModel(ctx, "tool_route_model_id")
	_, err := task.decisionToolScope(ctx, model, "text", typesafe.Metadata{UserID: "decision-user"})
	if !errors.Is(err, store.ErrDailyTokenQuotaExceeded) || calls != before {
		t.Fatalf("quota admission err=%v calls=%d", err, calls)
	}
}
