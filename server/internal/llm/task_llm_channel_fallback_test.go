package llm

import (
	"context"
	"database/sql"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"aivory/server/internal/store"
)

const taskFallbackTestUserID = "task-fallback-user"

func openTaskChannelFallbackTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "task-channel-fallback.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO users(id,email,password_hash,role) VALUES(?,?,?,?)`,
		taskFallbackTestUserID, "task-fallback@example.test", "hash", "user",
	); err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	return db
}

func createTaskChannelFallbackModel(
	t *testing.T,
	db *sql.DB,
	primaryURL string,
	fallbackURL string,
) (*store.Model, *store.Channel, *store.Channel) {
	t.Helper()
	ctx := context.Background()
	primary, err := store.CreateChannel(ctx, db, "Task primary", "openai", "chat", primaryURL, "primary-key")
	if err != nil {
		t.Fatalf("create primary channel: %v", err)
	}
	fallback, err := store.CreateChannel(ctx, db, "Task fallback", "openai", "chat", fallbackURL, "fallback-key")
	if err != nil {
		t.Fatalf("create fallback channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID:         primary.ID,
		FallbackChannelID: fallback.ID,
		Kind:              "chat",
		RequestID:         "task-fallback-model",
		Label:             "Task fallback model",
		Enabled:           true,
		PriceInput:        1,
		PriceOutput:       2,
		Currency:          "USD",
	})
	if err != nil {
		t.Fatalf("create task model: %v", err)
	}
	return model, primary, fallback
}

func newTaskChannelFallbackRunner(db *sql.DB) *TaskLLM {
	logger := log.New(io.Discard, "", 0)
	return NewTaskLLM(db, NewRegistry(logger), logger)
}

func writeOpenAIChatSSEError(w http.ResponseWriter, message string) {
	w.Header().Set("content-type", "text/event-stream")
	_, _ = io.WriteString(w, `data: {"error":{"message":"`+message+`"}}`+"\n\n")
}

type taskFallbackUsageRow struct {
	Status                    string
	Error                     string
	ChannelID                 string
	Fallback                  int
	InputTokens, OutputTokens int
	Cost, Credits             float64
	RequestBody               string
}

func taskFallbackUsageRows(t *testing.T, db *sql.DB, modelID string) []taskFallbackUsageRow {
	t.Helper()
	rows, err := db.Query(`
		SELECT status, error, channel_id, fallback, input_tokens, output_tokens, cost, credits, request_body
		FROM usage_logs
		WHERE user_id=? AND model_id=? AND purpose=?
		ORDER BY id`, taskFallbackTestUserID, modelID, string(TaskTitle))
	if err != nil {
		t.Fatalf("query task usage: %v", err)
	}
	defer rows.Close()
	out := []taskFallbackUsageRow{}
	for rows.Next() {
		var row taskFallbackUsageRow
		if err := rows.Scan(&row.Status, &row.Error, &row.ChannelID, &row.Fallback,
			&row.InputTokens, &row.OutputTokens, &row.Cost, &row.Credits, &row.RequestBody); err != nil {
			t.Fatalf("scan task usage: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task usage: %v", err)
	}
	return out
}

func TestTaskLLMFallbackSuccessLogsPrimaryErrorAndFallbackUsage(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		writeOpenAIChatSSEError(w, "primary SSE failure")
	}))
	t.Cleanup(primary.Close)

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"fallback answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	t.Cleanup(fallback.Close)

	db := openTaskChannelFallbackTestDB(t)
	model, primaryChannel, fallbackChannel := createTaskChannelFallbackModel(t, db, primary.URL, fallback.URL)
	outerVisible := new(atomic.Bool)
	outerVisible.Store(true)
	ctx := contextWithProviderVisibleOutput(context.Background(), outerVisible)
	ctx = withTaskBillingMessageID(ctx, "task-parent-message")
	answer, err := newTaskChannelFallbackRunner(db).Run(ctx, TaskTitle, "hello", RunOpts{
		ModelID: model.ID,
		UserID:  taskFallbackTestUserID,
	})
	if err != nil {
		t.Fatalf("TaskLLM.Run: %v", err)
	}
	if answer != "fallback answer" {
		t.Fatalf("answer = %q, want fallback answer", answer)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 1 {
		t.Fatalf("request counts primary/fallback = %d/%d, want 1/1", primaryHits.Load(), fallbackHits.Load())
	}
	var billingMessageID string
	if err := db.QueryRow(`SELECT message_id FROM billing_usage WHERE user_id=? AND purpose=?`,
		taskFallbackTestUserID, string(TaskTitle)).Scan(&billingMessageID); err != nil {
		t.Fatalf("query durable task billing: %v", err)
	}
	if billingMessageID != "task-parent-message" {
		t.Fatalf("durable task billing message = %q, want task-parent-message", billingMessageID)
	}

	usageRows := taskFallbackUsageRows(t, db, model.ID)
	if len(usageRows) != 2 {
		t.Fatalf("matching usage rows = %d, want primary error + fallback success", len(usageRows))
	}
	primaryFailure := usageRows[0]
	if primaryFailure.Status != "error" || !strings.Contains(primaryFailure.Error, "primary SSE failure") {
		t.Fatalf("primary status/error = %q/%q", primaryFailure.Status, primaryFailure.Error)
	}
	if primaryFailure.ChannelID != primaryChannel.ID || primaryFailure.Fallback != 0 {
		t.Fatalf("primary channel/fallback = %q/%d, want %q/0", primaryFailure.ChannelID, primaryFailure.Fallback, primaryChannel.ID)
	}
	if primaryFailure.InputTokens != 0 || primaryFailure.OutputTokens != 0 || primaryFailure.Cost != 0 || primaryFailure.Credits != 0 {
		t.Fatalf("primary failure must be free: %+v", primaryFailure)
	}
	if primaryFailure.RequestBody == "" {
		t.Fatal("primary error row must retain its sanitized request body")
	}

	fallbackSuccess := usageRows[1]
	if fallbackSuccess.Status != "ok" || fallbackSuccess.Error != "" {
		t.Fatalf("fallback status/error = %q/%q, want ok with no error", fallbackSuccess.Status, fallbackSuccess.Error)
	}
	if fallbackSuccess.ChannelID != fallbackChannel.ID || fallbackSuccess.Fallback != 1 {
		t.Fatalf("fallback channel/fallback = %q/%d, want %q/1", fallbackSuccess.ChannelID, fallbackSuccess.Fallback, fallbackChannel.ID)
	}
	if fallbackSuccess.InputTokens != 7 || fallbackSuccess.OutputTokens != 3 {
		t.Fatalf("fallback usage tokens = %d/%d, want 7/3", fallbackSuccess.InputTokens, fallbackSuccess.OutputTokens)
	}
}

func TestTaskLLMFinalFailureLogsBothChannelErrors(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		writeOpenAIChatSSEError(w, "primary SSE failure")
	}))
	t.Cleanup(primary.Close)

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		writeOpenAIChatSSEError(w, "fallback SSE failure")
	}))
	t.Cleanup(fallback.Close)

	db := openTaskChannelFallbackTestDB(t)
	model, primaryChannel, fallbackChannel := createTaskChannelFallbackModel(t, db, primary.URL, fallback.URL)
	_, runErr := newTaskChannelFallbackRunner(db).Run(context.Background(), TaskTitle, "hello", RunOpts{
		ModelID: model.ID,
		UserID:  taskFallbackTestUserID,
	})
	if runErr == nil {
		t.Fatal("TaskLLM.Run unexpectedly succeeded")
	}
	if !strings.Contains(runErr.Error(), "fallback SSE failure") {
		t.Fatalf("TaskLLM.Run error = %q, want final fallback failure", runErr)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 1 {
		t.Fatalf("request counts primary/fallback = %d/%d, want 1/1", primaryHits.Load(), fallbackHits.Load())
	}

	usageRows := taskFallbackUsageRows(t, db, model.ID)
	if len(usageRows) != 2 {
		t.Fatalf("matching usage rows = %d, want both channel failures", len(usageRows))
	}
	if usageRows[0].Status != "error" || !strings.Contains(usageRows[0].Error, "primary SSE failure") ||
		usageRows[0].ChannelID != primaryChannel.ID || usageRows[0].Fallback != 0 {
		t.Fatalf("primary failure row = %+v", usageRows[0])
	}
	if usageRows[1].Status != "error" || !strings.Contains(usageRows[1].Error, "fallback SSE failure") ||
		usageRows[1].ChannelID != fallbackChannel.ID || usageRows[1].Fallback != 1 {
		t.Fatalf("fallback failure row = %+v", usageRows[1])
	}
	for _, row := range usageRows {
		if row.InputTokens != 0 || row.OutputTokens != 0 || row.Cost != 0 || row.Credits != 0 {
			t.Fatalf("failed attempts must be free: %+v", row)
		}
	}
}

func TestResolveFallbackChannelForModelProviderAliasesAndWireFormat(t *testing.T) {
	tests := []struct {
		name         string
		primaryType  string
		fallbackType string
		primaryFmt   string
		fallbackFmt  string
		wantAccepted bool
	}{
		{name: "anthropic to claude alias", primaryType: "anthropic", fallbackType: "claude", primaryFmt: "chat", fallbackFmt: "chat", wantAccepted: true},
		{name: "claude to anthropic alias", primaryType: "claude", fallbackType: "anthropic", primaryFmt: "chat", fallbackFmt: "chat", wantAccepted: true},
		{name: "google to gemini alias", primaryType: "google", fallbackType: "gemini", primaryFmt: "chat", fallbackFmt: "chat", wantAccepted: true},
		{name: "gemini to google alias", primaryType: "gemini", fallbackType: "google", primaryFmt: "chat", fallbackFmt: "chat", wantAccepted: true},
		{name: "cross provider rejected", primaryType: "anthropic", fallbackType: "google", primaryFmt: "chat", fallbackFmt: "chat"},
		{name: "different api format rejected", primaryType: "google", fallbackType: "gemini", primaryFmt: "chat", fallbackFmt: "responses"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTaskChannelFallbackTestDB(t)
			ctx := context.Background()
			primary, err := store.CreateChannel(ctx, db, "Alias primary", tt.primaryType, tt.primaryFmt, "https://primary.example", "primary-key")
			if err != nil {
				t.Fatalf("create primary channel: %v", err)
			}
			fallback, err := store.CreateChannel(ctx, db, "Alias fallback", tt.fallbackType, tt.fallbackFmt, "https://fallback.example", "fallback-key")
			if err != nil {
				t.Fatalf("create fallback channel: %v", err)
			}
			primaryWithKey, err := store.GetChannel(ctx, db, primary.ID)
			if err != nil {
				t.Fatalf("load primary channel: %v", err)
			}
			model := &store.Model{ID: "alias-model", ChannelID: primary.ID, FallbackChannelID: fallback.ID}

			creds, channelID := resolveFallbackChannelForModel(ctx, db, log.New(io.Discard, "", 0), model, primaryWithKey)
			if !tt.wantAccepted {
				if creds != nil || channelID != "" {
					t.Fatalf("fallback accepted with creds=%+v channel=%q, want rejection", creds, channelID)
				}
				return
			}
			if creds == nil {
				t.Fatal("fallback rejected, want provider aliases accepted")
			}
			if channelID != fallback.ID || creds.BaseURL != "https://fallback.example" || creds.APIKey != "fallback-key" {
				t.Fatalf("resolved fallback = creds %+v channel %q, want fallback channel %q", creds, channelID, fallback.ID)
			}
		})
	}
}
