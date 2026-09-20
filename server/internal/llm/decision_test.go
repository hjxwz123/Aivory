package llm

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
	"aivory/server/internal/typesafe"
)

func decisionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "decisions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash) VALUES('decision-user','decision@example.test','hash')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDecisionAccountingAndPrivacy(t *testing.T) {
	for _, test := range []struct {
		name, model, answer, user, message string
		wantErr                            bool
	}{
		{"success", typesafe.DefaultModel, `{"type":"noul","noul":0.9}`, "decision-user", "message-1", false},
		{"invalid answer", typesafe.DefaultModel, `{"type":"noul","noul":2}`, "decision-user", "message-2", true},
		{"system task", typesafe.DefaultModel, `{"type":"noul","noul":0.9}`, "", "", false},
		{"private task", typesafe.DefaultModel, `{"type":"noul","noul":0.9}`, "decision-user", "private_decision", false},
		{"alias release", "jev-2.0.0", `{"type":"noul","noul":0.9}`, "decision-user", "message-3", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := decisionTestDB(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"model":%q,"answers":{"check":%s},"usage":{"input_tokens":1000000,"output_tokens":50}}`, test.model, test.answer)
			}))
			defer srv.Close()
			var logs bytes.Buffer
			task := NewTaskLLM(db, nil, log.New(&logs, "", 0))
			seedDecisionModel(t, db, srv.URL, "jev-latest")
			req := typesafe.Request{State: "sensitive text", Questions: map[string]typesafe.Question{"check": typesafe.NewNoul("Sensitive rubric", nil)}}
			opts := typesafe.Options{Metadata: typesafe.Metadata{UserID: test.user, MessageID: test.message, ConversationID: "conversation-1", WorkspaceID: "workspace-1", Purpose: "task.test_decision"}}
			result, err := task.RunDecision(context.Background(), "decision-model", req, opts)
			if (err != nil) != test.wantErr || result == nil {
				t.Fatalf("result=%+v error=%v", result, err)
			}

			var count, input, output int
			var cost int64
			var model, conv string
			if err := db.QueryRow(`SELECT COUNT(*),SUM(input_tokens),SUM(output_tokens),SUM(cost_micros),MAX(model_id),MAX(conversation_id) FROM billing_usage WHERE purpose='task.test_decision'`).Scan(&count, &input, &output, &cost, &model, &conv); err != nil {
				t.Fatal(err)
			}
			wantCost := int64(42000)
			if count != 1 || input != 1000000 || output != 50 || cost != wantCost || model != "decision-model" {
				t.Fatalf("wrong ledger: count=%d input=%d output=%d cost=%d model=%s", count, input, output, cost, model)
			}
			if strings.HasPrefix(test.message, "private_") && conv != "" {
				t.Fatal("private conversation retained")
			}
			if test.user != "" {
				var body, headers, status string
				var rowCost float64
				if err := db.QueryRow(`SELECT request_body,request_headers,status,cost FROM usage_logs WHERE purpose='task.test_decision'`).Scan(&body, &headers, &status, &rowCost); err != nil {
					t.Fatal(err)
				}
				if body != "" || headers != "" || (status == "error") != test.wantErr || math.Abs(rowCost-float64(wantCost)/1e6) > 1e-10 {
					t.Fatalf("wrong diagnostic record: %s %f", status, rowCost)
				}
			}
			for _, text := range []string{"sensitive text", "Sensitive rubric", "secret"} {
				if strings.Contains(logs.String(), text) {
					t.Fatalf("private data in log: %s", text)
				}
			}
		})
	}
}

func TestDecisionAccountingFailureNotSilenced(t *testing.T) {
	db := decisionTestDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{"check":{"type":"noul","noul":1}},"usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	defer srv.Close()
	task := NewTaskLLM(db, nil, nil)
	seedDecisionModel(t, db, srv.URL, "jev-1.13.0")
	if _, err := db.Exec(`CREATE TRIGGER reject_decision_billing BEFORE INSERT ON billing_usage BEGIN SELECT RAISE(ABORT, 'injected billing failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := task.RunDecision(context.Background(), "decision-model", typesafe.Request{State: "text", Questions: map[string]typesafe.Question{"check": typesafe.NewNoul("q", nil)}}, typesafe.Options{})
	if !errors.Is(err, ErrTaskBillingRecord) || typesafe.KindOf(err) != typesafe.ErrRecording {
		t.Fatalf("accounting error lost: %v", err)
	}
}

func seedDecisionModel(t *testing.T, db *sql.DB, baseURL, requestID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO channels(id,name,type,base_url,api_key,enabled) VALUES('decision-channel','TypeSafe','typesafe',?,'secret',1)`, baseURL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,channel_id,kind,request_id,label,enabled,price_input) VALUES('decision-model','decision-channel','decision',?,'Jev',1,0.042)`, requestID); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionDatabaseConfiguration(t *testing.T) {
	db := decisionTestDB(t)
	calls := 0
	key := "secret"
	served := "jev-1.13.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("database API key not used")
		}
		fmt.Fprintf(w, `{"model":%q,"answers":{"check":{"type":"noul","noul":1}},"usage":{"input_tokens":1000000,"output_tokens":1}}`, served)
	}))
	defer srv.Close()
	seedDecisionModel(t, db, srv.URL, served)
	task := NewTaskLLM(db, nil, nil)
	req := typesafe.Request{Model: "ignored-request-override", State: "text", Questions: map[string]typesafe.Question{"check": typesafe.NewNoul("q", nil)}}
	run := func() error {
		_, err := task.RunDecision(context.Background(), "decision-model", req, typesafe.Options{})
		return err
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	key, served = "rotated-key", "jev-2.0.0"
	if _, err := db.Exec(`UPDATE channels SET api_key=? WHERE id='decision-channel'`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE models SET request_id=?,price_input=0.25 WHERE id='decision-model'`, served); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	var cost int
	if err := db.QueryRow(`SELECT SUM(cost_micros) FROM billing_usage WHERE model_id='decision-model'`).Scan(&cost); err != nil || cost != 292000 {
		t.Fatalf("database price not used: %d %v", cost, err)
	}
	if _, err := db.Exec(`UPDATE channels SET enabled=0 WHERE id='decision-channel'`); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil || calls != 2 {
		t.Fatalf("disabled channel used: calls=%d err=%v", calls, err)
	}
	if _, err := task.RunDecision(context.Background(), "", req, typesafe.Options{}); typesafe.KindOf(err) != typesafe.ErrDisabled {
		t.Fatalf("unconfigured model: %v", err)
	}
	if _, err := task.RunDecision(context.Background(), "decision-model", req, typesafe.Options{Metadata: typesafe.Metadata{Purpose: "chat"}}); typesafe.KindOf(err) != typesafe.ErrValidation {
		t.Fatalf("invalid purpose: %v", err)
	}
}
