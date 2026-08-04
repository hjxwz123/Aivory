package api

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"aivory/server/internal/store"
)

type analyticsAdminTestResponse struct {
	Totals         store.UsageTotals                    `json:"totals"`
	PreviousTotals store.UsageTotals                    `json:"previous_totals"`
	Trend          []store.UsageBucket                  `json:"trend"`
	PreviousTrend  []store.UsageBucket                  `json:"previous_trend"`
	Breakdowns     map[string][]store.UsageBreakdownRow `json:"breakdowns"`
	FilterOptions  map[string][]store.UsageBreakdownRow `json:"filter_options"`
}

func TestAnalyticsAdminComposesFiltersAndKeepsUnfilteredOptions(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-analytics.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u1','alice@example.test','Alice Analyst','h','user')`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u2','bob@example.test','Bob Operator','h','user')`)

	type fact struct {
		userID, modelID, workspaceID, purpose, channelID string
		cost                                             float64
		createdAt                                        int64
	}
	now := time.Now().Unix()
	facts := []fact{
		{userID: "u1", modelID: "model-a", workspaceID: "workspace-a", purpose: "chat", channelID: "channel-a", cost: 0.11, createdAt: now - 60},
		{userID: "u1", modelID: "model-a", workspaceID: "", purpose: "chat", channelID: "channel-a", cost: 0.22, createdAt: now - 60},
		{userID: "u1", modelID: "model-a", workspaceID: "workspace-a", purpose: "chat", channelID: "", cost: 0.33, createdAt: now - 60},
		{userID: "u2", modelID: "model-b", workspaceID: "workspace-b", purpose: "image", channelID: "channel-b", cost: 0.44, createdAt: now - 60},
		{userID: "u1", modelID: "model-a", workspaceID: "workspace-a", purpose: "chat", channelID: "channel-a", cost: 0.05, createdAt: now - 36*3600},
		{userID: "u2", modelID: "model-b", workspaceID: "workspace-b", purpose: "image", channelID: "channel-b", cost: 0.06, createdAt: now - 36*3600},
	}
	for i, row := range facts {
		mustExec(t, db, `INSERT INTO usage_stats(
			source_log_id, user_id, conversation_id, message_id, model_id, purpose,
			input_tokens, output_tokens, cost, currency, credits, workspace_id, channel_id, created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			i+1, row.userID, "conversation-"+row.modelID+row.workspaceID+row.channelID,
			"message-"+strconv.Itoa(i), row.modelID, row.purpose, 10+i, 5+i, row.cost,
			"USD", row.cost*2, row.workspaceID, row.channelID, row.createdAt)
	}

	request := func(target string) analyticsAdminTestResponse {
		t.Helper()
		recorder := httptest.NewRecorder()
		analyticsAdmin(Deps{DB: db}, recorder, httptest.NewRequest("GET", target, nil))
		if recorder.Code != 200 {
			t.Fatalf("GET %s status=%d body=%s", target, recorder.Code, recorder.Body.String())
		}
		var response analyticsAdminTestResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode GET %s: %v", target, err)
		}
		return response
	}

	filtered := request("/api/admin/analytics?days=1&user=ALICE+ANALYST&model=model-a&workspace=workspace-a&purpose=chat&channel=channel-a")
	if filtered.Totals.Calls != 1 || math.Abs(filtered.Totals.Cost-0.11) > 1e-12 {
		t.Fatalf("filtered current totals = %+v, want one 0.11 call", filtered.Totals)
	}
	if filtered.PreviousTotals.Calls != 1 || math.Abs(filtered.PreviousTotals.Cost-0.05) > 1e-12 {
		t.Fatalf("filtered previous totals = %+v, want one 0.05 call", filtered.PreviousTotals)
	}
	if len(filtered.Trend) != 1 || filtered.Trend[0].Calls != 1 ||
		len(filtered.PreviousTrend) != 1 || filtered.PreviousTrend[0].Calls != 1 {
		t.Fatalf("filtered current/previous trend = %+v / %+v, want one call each", filtered.Trend, filtered.PreviousTrend)
	}
	for dimension, wantKey := range map[string]string{
		"model": "model-a", "user": "u1", "workspace": "workspace-a",
		"purpose": "chat", "channel": "channel-a",
	} {
		rows := filtered.Breakdowns[dimension]
		if len(rows) != 1 || rows[0].Key != wantKey || rows[0].Calls != 1 {
			t.Fatalf("filtered %s breakdown = %+v, want one %q call", dimension, rows, wantKey)
		}
	}
	if len(filtered.FilterOptions["model"]) != 2 || len(filtered.FilterOptions["user"]) != 2 ||
		len(filtered.FilterOptions["workspace"]) != 3 || len(filtered.FilterOptions["purpose"]) != 2 ||
		len(filtered.FilterOptions["channel"]) != 3 {
		t.Fatalf("unfiltered filter options = %+v, want the complete current-period dimensions", filtered.FilterOptions)
	}

	personal := request("/api/admin/analytics?days=1&workspace=__personal__")
	if personal.Totals.Calls != 1 || math.Abs(personal.Totals.Cost-0.22) > 1e-12 {
		t.Fatalf("personal workspace response totals = %+v, want one 0.22 call", personal.Totals)
	}
	unattributed := request("/api/admin/analytics?days=1&channel=__unattributed__")
	if unattributed.Totals.Calls != 1 || math.Abs(unattributed.Totals.Cost-0.33) > 1e-12 {
		t.Fatalf("unattributed channel response totals = %+v, want one 0.33 call", unattributed.Totals)
	}

	unfiltered := request("/api/admin/analytics?days=1")
	if !reflect.DeepEqual(unfiltered.FilterOptions, unfiltered.Breakdowns) {
		t.Fatalf("unfiltered filter options differ from breakdowns\noptions: %+v\nbreakdowns: %+v", unfiltered.FilterOptions, unfiltered.Breakdowns)
	}
}
