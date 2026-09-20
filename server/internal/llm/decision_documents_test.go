package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"aivory/server/internal/rag"
	"aivory/server/internal/store"
	"aivory/server/internal/typesafe"
)

func TestDecisionDocumentRouting(t *testing.T) {
	for _, tc := range []struct {
		name, strategy            string
		confidence, first, second float64
		status                    int
		want                      string
		ids                       []string
	}{
		{"multiple documents", "full_doc", .95, .99, .98, 200, "full_doc", []string{"doc-a", "doc-b"}},
		{"exclude irrelevant", "full_doc", .95, .99, .01, 200, "full_doc", []string{"doc-a"}},
		{"keep uncertain candidate", "full_doc", .95, .99, .5, 200, "full_doc", []string{"doc-a", "doc-b"}},
		{"contradictory selection", "full_doc", .95, .01, .01, 200, "retrieve", nil},
		{"uncertain strategy", "none", .6, .01, .01, 200, "retrieve", nil},
		{"unrelated", "none", .95, .01, .01, 200, "none", nil},
		{"targeted question", "retrieve", .95, .99, .01, 200, "retrieve", nil},
		{"provider failure", "none", .95, .01, .01, 503, "retrieve", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := decisionTestDB(t)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req typesafe.Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if len(req.Questions) != 3 || req.Questions["strategy"].Type != typesafe.Choice || req.Questions["document_0"].Type != typesafe.Noul {
					t.Errorf("wrong questions: %+v", req.Questions)
				}
				state := req.State.(map[string]any)
				if state["message"] != "比较这两份文档" || len(state["documents"].([]any)) != 2 {
					t.Errorf("bad state: %v", state)
				}
				if tc.status != 200 {
					w.WriteHeader(tc.status)
					return
				}
				probabilities := map[string]float64{"full_doc": 0, "none": 0, "retrieve": 0}
				probabilities[tc.strategy] = 1
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "usage": map[string]int{"input_tokens": 100, "output_tokens": 2}, "answers": map[string]any{
					"strategy":   map[string]any{"type": "choice", "choice": tc.strategy, "confidence": tc.confidence, "probabilities": probabilities},
					"document_0": map[string]any{"type": "noul", "noul": tc.first}, "document_1": map[string]any{"type": "noul", "noul": tc.second},
				}})
			}))
			defer srv.Close()
			seedDecisionModel(t, db, srv.URL, "jev-1.13.0")
			if err := store.SetSetting(db, "file_route_model_id", "decision-model"); err != nil {
				t.Fatal(err)
			}
			task := NewTaskLLM(db, nil, nil)
			input := rag.DocumentRouteInput{Message: "比较这两份文档", Documents: []rag.DocumentRouteHint{{DocumentID: "doc-a", Filename: "A.pdf", CurrentTurn: true}, {DocumentID: "doc-b", Filename: "B.pdf", Indexed: true}}}
			got, handled, err := task.RouteDocuments(context.Background(), input, rag.RouterOpts{UserID: "decision-user"})
			if !handled || (err != nil) != (tc.status != 200) || got.Strategy != tc.want || !reflect.DeepEqual(got.DocumentIDs, tc.ids) || calls != 1 {
				t.Fatalf("got=%+v handled=%v err=%v calls=%d", got, handled, err, calls)
			}
			if tc.want == "retrieve" && !reflect.DeepEqual(got.Queries, []string{input.Message}) {
				t.Fatalf("original query lost: %+v", got)
			}
			if tc.status == 200 {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE model_id='decision-model' AND purpose='task.router'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("usage=%d err=%v", count, err)
				}
			}
			if _, err := db.Exec(`UPDATE models SET enabled=0 WHERE id='decision-model'`); err != nil {
				t.Fatal(err)
			}
			_, handled, err = task.RouteDocuments(context.Background(), input, rag.RouterOpts{})
			if !handled || err == nil || calls != 1 {
				t.Fatal("disabled Jev did not retain failure path")
			}
			if err := store.SetSetting(db, "file_route_model_id", ""); err != nil {
				t.Fatal(err)
			}
			_, handled, err = task.RouteDocuments(context.Background(), input, rag.RouterOpts{})
			if handled || err != nil || calls != 1 {
				t.Fatal("unconfigured policy intercepted normal model")
			}
		})
	}
}

func TestDecisionDocumentCandidateBudget(t *testing.T) {
	db := decisionTestDB(t)
	seedDecisionModel(t, db, "http://127.0.0.1:1", "jev-1.13.0")
	if err := store.SetSetting(db, "file_route_model_id", "decision-model"); err != nil {
		t.Fatal(err)
	}
	task := NewTaskLLM(db, nil, nil)
	for _, count := range []int{0, decisionDocumentLimit + 1} {
		result, handled, err := task.RouteDocuments(context.Background(), rag.DocumentRouteInput{Message: "question", Documents: make([]rag.DocumentRouteHint, count)}, rag.RouterOpts{})
		if err != nil || !handled || result.Strategy != "retrieve" {
			t.Fatalf("budget result=%+v handled=%v err=%v", result, handled, err)
		}
	}
}
