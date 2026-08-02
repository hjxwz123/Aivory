package store

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func openUsageStatsTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "usage-stats.db"))
	if err != nil {
		t.Fatalf("open usage stats database: %v", err)
	}
	if err := Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate usage stats database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, context.Background()
}

func seedUsageStatsUser(t *testing.T, ctx context.Context, db *sql.DB, id, email string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash,role) VALUES(?,?,?,'user')`,
		id, email, "hash",
	); err != nil {
		t.Fatalf("seed usage stats user %s: %v", id, err)
	}
}

func countUsageStatsRows(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_stats`).Scan(&count); err != nil {
		t.Fatalf("count usage_stats: %v", err)
	}
	return count
}

func TestUsageStatsRecordSuccessfulCallsOnly(t *testing.T) {
	db, ctx := openUsageStatsTestDB(t)
	seedUsageStatsUser(t, ctx, db, "stats-user", "stats@example.test")

	success := UsageLog{
		UserID:            "stats-user",
		ConversationID:    "stats-conversation",
		MessageID:         "stats-message",
		ModelID:           "stats-model",
		Purpose:           "stats-write",
		InputTokens:       31,
		OutputTokens:      17,
		CacheReadTokens:   11,
		CacheWriteTokens:  7,
		ImagesCount:       2,
		Cost:              0.125,
		Currency:          "USD",
		Credits:           0.25,
		WorkspaceID:       "stats-workspace",
		ChannelID:         "stats-channel",
		Fallback:          true,
		TTFTFallbackModel: "Backup Model",
	}
	if err := LogUsage(ctx, db, success); err != nil {
		t.Fatalf("log successful usage: %v", err)
	}
	if err := LogUsage(ctx, db, UsageLog{
		UserID: "stats-user", MessageID: "failed-message", ModelID: "failed-model",
		Purpose: "error-call", Status: "error", Error: "provider failed",
	}); err != nil {
		t.Fatalf("log failed usage: %v", err)
	}
	if err := LogUsage(ctx, db, UsageLog{
		UserID: "stats-user", MessageID: "zero-message", ModelID: "zero-model",
		Purpose: "zero-call", Currency: "USD",
	}); err != nil {
		t.Fatalf("log zero-metric successful usage: %v", err)
	}

	var logCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&logCount); err != nil {
		t.Fatalf("count usage logs: %v", err)
	}
	if logCount != 3 {
		t.Fatalf("usage_logs rows = %d, want 3", logCount)
	}
	if got := countUsageStatsRows(t, ctx, db); got != 2 {
		t.Fatalf("usage_stats rows = %d, want 2 successful calls", got)
	}

	var failedStats, zeroStats int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_stats WHERE purpose='error-call'`).Scan(&failedStats); err != nil {
		t.Fatalf("count failed stats: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_stats WHERE purpose='zero-call'`).Scan(&zeroStats); err != nil {
		t.Fatalf("count zero-call stats: %v", err)
	}
	if failedStats != 0 || zeroStats != 1 {
		t.Fatalf("usage stats by outcome = failed:%d zero-success:%d, want 0/1", failedStats, zeroStats)
	}

	var got UsageLog
	var fallback int
	if err := db.QueryRowContext(ctx, `SELECT user_id, COALESCE(conversation_id,''), COALESCE(message_id,''),
		model_id, purpose, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		images_count, cost, currency, credits, workspace_id, channel_id, fallback, ttft_fallback_model
		FROM usage_stats WHERE purpose='stats-write'`).Scan(
		&got.UserID, &got.ConversationID, &got.MessageID, &got.ModelID, &got.Purpose,
		&got.InputTokens, &got.OutputTokens, &got.CacheReadTokens, &got.CacheWriteTokens,
		&got.ImagesCount, &got.Cost, &got.Currency, &got.Credits, &got.WorkspaceID,
		&got.ChannelID, &fallback, &got.TTFTFallbackModel,
	); err != nil {
		t.Fatalf("read durable usage stat: %v", err)
	}
	got.Fallback = fallback == 1
	if !reflect.DeepEqual(got, success) {
		t.Fatalf("durable usage stat = %+v, want %+v", got, success)
	}

	totals, err := AdminUsageTotals(ctx, db, 1)
	if err != nil {
		t.Fatalf("usage totals: %v", err)
	}
	if totals.Calls != 2 || totals.InputTokens != 31 || totals.OutputTokens != 17 || totals.Users != 1 || math.Abs(totals.Cost-0.125) > 1e-12 {
		t.Fatalf("usage totals = %+v, want 2 successful calls including the zero-metric call", totals)
	}
	if cost, messages, err := SumUsageByUser(ctx, db, "stats-user", 1); err != nil {
		t.Fatalf("sum usage by user: %v", err)
	} else if math.Abs(cost-0.125) > 1e-12 || messages != 2 {
		t.Fatalf("user usage = cost:%v messages:%d, want 0.125/2", cost, messages)
	}
}

func TestBackfillUsageStatsIsIdempotentAndSkipsErrors(t *testing.T) {
	db, ctx := openUsageStatsTestDB(t)
	seedUsageStatsUser(t, ctx, db, "legacy-user", "legacy@example.test")
	if err := DisableUsageStatsMirror(ctx, db); err != nil {
		t.Fatalf("disable usage stats mirror: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO usage_logs(
		user_id, message_id, model_id, purpose, input_tokens, output_tokens, cost, currency, status
	) VALUES(?,?,?,?,?,?,?,?,?)`,
		"legacy-user", "legacy-ok-message", "legacy-model", "chat", 9, 4, 0.09, "USD", "ok",
	); err != nil {
		t.Fatalf("insert legacy successful log: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_logs(
		user_id, message_id, model_id, purpose, status, error
	) VALUES(?,?,?,?,?,?)`,
		"legacy-user", "legacy-error-message", "legacy-model", "chat", "error", "provider failed",
	); err != nil {
		t.Fatalf("insert legacy error log: %v", err)
	}
	if got := countUsageStatsRows(t, ctx, db); got != 0 {
		t.Fatalf("usage_stats before legacy backfill = %d, want 0 with mirror disabled", got)
	}

	inserted, err := BackfillUsageStats(ctx, db)
	if err != nil {
		t.Fatalf("first usage stats backfill: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("first usage stats backfill inserted %d rows, want 1 successful row", inserted)
	}
	inserted, err = BackfillUsageStats(ctx, db)
	if err != nil {
		t.Fatalf("second usage stats backfill: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("second usage stats backfill inserted %d rows, want 0", inserted)
	}
	if got := countUsageStatsRows(t, ctx, db); got != 1 {
		t.Fatalf("usage_stats after repeated backfill = %d, want 1", got)
	}

	var okRows, errorRows int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_stats WHERE message_id='legacy-ok-message'`,
	).Scan(&okRows); err != nil {
		t.Fatalf("count backfilled successful row: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_stats WHERE message_id='legacy-error-message'`,
	).Scan(&errorRows); err != nil {
		t.Fatalf("count backfilled error row: %v", err)
	}
	if okRows != 1 || errorRows != 0 {
		t.Fatalf("backfilled rows by status = ok:%d error:%d, want 1/0", okRows, errorRows)
	}
}

type usageAnalyticsSnapshot struct {
	User1Cost       float64
	User1Messages   int
	User2Cost       float64
	User2Messages   int
	Totals          UsageTotals
	Trend           []UsageBucket
	ByModel         []UsageBreakdownRow
	ByUser          []UsageBreakdownRow
	ModelSeries     []UsageSeriesPoint
	UserSeries      []UsageSeriesPoint
	DurableStatRows int
}

func takeUsageAnalyticsSnapshot(t *testing.T, ctx context.Context, db *sql.DB) usageAnalyticsSnapshot {
	t.Helper()
	var snapshot usageAnalyticsSnapshot
	var err error
	if snapshot.User1Cost, snapshot.User1Messages, err = SumUsageByUser(ctx, db, "usage-u1", 1); err != nil {
		t.Fatalf("sum usage-u1: %v", err)
	}
	if snapshot.User2Cost, snapshot.User2Messages, err = SumUsageByUser(ctx, db, "usage-u2", 1); err != nil {
		t.Fatalf("sum usage-u2: %v", err)
	}
	if snapshot.Totals, err = AdminUsageTotals(ctx, db, 1); err != nil {
		t.Fatalf("admin usage totals: %v", err)
	}
	if snapshot.Trend, err = AdminUsageTrend(ctx, db, 1); err != nil {
		t.Fatalf("admin usage trend: %v", err)
	}
	if snapshot.ByModel, err = AdminUsageBreakdown(ctx, db, 1, "model_id", 100); err != nil {
		t.Fatalf("admin model breakdown: %v", err)
	}
	if snapshot.ByUser, err = AdminUsageBreakdown(ctx, db, 1, "user_id", 100); err != nil {
		t.Fatalf("admin user breakdown: %v", err)
	}
	if snapshot.ModelSeries, err = AdminUsageSeries(ctx, db, 1, "model_id", []string{"model-a", "model-b", "model-c"}); err != nil {
		t.Fatalf("admin model series: %v", err)
	}
	if snapshot.UserSeries, err = AdminUsageSeries(ctx, db, 1, "user_id", []string{"usage-u1", "usage-u2"}); err != nil {
		t.Fatalf("admin user series: %v", err)
	}
	// Aggregate SQL is free to return equal-cost groups in either order. Sort by
	// stable identity so this test measures value changes, not query-plan order.
	sort.Slice(snapshot.ByModel, func(i, j int) bool { return snapshot.ByModel[i].Key < snapshot.ByModel[j].Key })
	sort.Slice(snapshot.ByUser, func(i, j int) bool { return snapshot.ByUser[i].Key < snapshot.ByUser[j].Key })
	sort.Slice(snapshot.ModelSeries, func(i, j int) bool {
		if snapshot.ModelSeries[i].BucketStart != snapshot.ModelSeries[j].BucketStart {
			return snapshot.ModelSeries[i].BucketStart < snapshot.ModelSeries[j].BucketStart
		}
		return snapshot.ModelSeries[i].Key < snapshot.ModelSeries[j].Key
	})
	sort.Slice(snapshot.UserSeries, func(i, j int) bool {
		if snapshot.UserSeries[i].BucketStart != snapshot.UserSeries[j].BucketStart {
			return snapshot.UserSeries[i].BucketStart < snapshot.UserSeries[j].BucketStart
		}
		return snapshot.UserSeries[i].Key < snapshot.UserSeries[j].Key
	})
	snapshot.DurableStatRows = countUsageStatsRows(t, ctx, db)
	return snapshot
}

func assertUsageAnalyticsSnapshot(t *testing.T, stage string, want, got usageAnalyticsSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s changed durable analytics after deleting logs\nwant: %+v\n got: %+v", stage, want, got)
	}
}

func usageLogInventoryCount(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	count, _, err := AdminUsageCount(ctx, db, UsageFilter{})
	if err != nil {
		t.Fatalf("count usage log inventory: %v", err)
	}
	return count
}

func TestUsageAnalyticsSurviveSingleBulkAndAllLogDeletion(t *testing.T) {
	db, ctx := openUsageStatsTestDB(t)
	seedUsageStatsUser(t, ctx, db, "usage-u1", "one@example.test")
	seedUsageStatsUser(t, ctx, db, "usage-u2", "two@example.test")

	rows := []UsageLog{
		{UserID: "usage-u1", MessageID: "shared-message", ModelID: "model-a", Purpose: "single-delete", InputTokens: 10, OutputTokens: 5, Cost: 0.11, Currency: "USD"},
		{UserID: "usage-u1", MessageID: "shared-message", ModelID: "model-b", Purpose: "chat", InputTokens: 20, OutputTokens: 10, Cost: 0.22, Currency: "USD"},
		{UserID: "usage-u1", MessageID: "zero-message", ModelID: "model-b", Purpose: "zero-call", Currency: "USD"},
		{UserID: "usage-u2", MessageID: "user-two-a", ModelID: "model-a", Purpose: "chat", InputTokens: 30, OutputTokens: 15, Cost: 0.33, Currency: "USD"},
		{UserID: "usage-u2", MessageID: "user-two-c", ModelID: "model-c", Purpose: "chat", InputTokens: 40, OutputTokens: 20, Cost: 0.44, Currency: "USD"},
		{UserID: "usage-u1", MessageID: "failed-message", ModelID: "model-a", Purpose: "chat", Status: "error", Error: "upstream failed"},
	}
	for i, row := range rows {
		if err := LogUsage(ctx, db, row); err != nil {
			t.Fatalf("log usage row %d: %v", i, err)
		}
	}
	if got := usageLogInventoryCount(t, ctx, db); got != 6 {
		t.Fatalf("initial usage log inventory = %d, want 6", got)
	}

	baseline := takeUsageAnalyticsSnapshot(t, ctx, db)
	if baseline.Totals.Calls != 5 || baseline.Totals.InputTokens != 100 || baseline.Totals.OutputTokens != 50 ||
		baseline.Totals.Users != 2 || math.Abs(baseline.Totals.Cost-1.10) > 1e-12 {
		t.Fatalf("baseline totals = %+v, want calls/tokens/users/cost 5/100/50/2/1.10", baseline.Totals)
	}
	if baseline.User1Messages != 2 || math.Abs(baseline.User1Cost-0.33) > 1e-12 {
		t.Fatalf("usage-u1 baseline = cost:%v messages:%d, want 0.33/2 (shared message counted once)", baseline.User1Cost, baseline.User1Messages)
	}
	if baseline.DurableStatRows != 5 {
		t.Fatalf("baseline durable stat rows = %d, want 5", baseline.DurableStatRows)
	}

	listed, err := AdminUsageRecords(ctx, db, UsageFilter{Purpose: "single-delete"}, 10, 0)
	if err != nil || len(listed) != 1 {
		t.Fatalf("find single-delete log = %+v, err=%v", listed, err)
	}
	if err := DeleteUsageRecord(ctx, db, listed[0].ID); err != nil {
		t.Fatalf("delete one usage log: %v", err)
	}
	if got := usageLogInventoryCount(t, ctx, db); got != 5 {
		t.Fatalf("inventory after single delete = %d, want 5", got)
	}
	assertUsageAnalyticsSnapshot(t, "single delete", baseline, takeUsageAnalyticsSnapshot(t, ctx, db))

	deleted, err := DeleteUsageByFilter(ctx, db, UsageFilter{ModelID: "model-a"})
	if err != nil {
		t.Fatalf("bulk delete model-a logs: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("bulk-deleted model-a logs = %d, want 2", deleted)
	}
	if got := usageLogInventoryCount(t, ctx, db); got != 3 {
		t.Fatalf("inventory after bulk delete = %d, want 3", got)
	}
	assertUsageAnalyticsSnapshot(t, "filtered bulk delete", baseline, takeUsageAnalyticsSnapshot(t, ctx, db))

	deleted, err = DeleteUsageByFilter(ctx, db, UsageFilter{})
	if err != nil {
		t.Fatalf("delete all usage logs: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted remaining logs = %d, want 3", deleted)
	}
	if got := usageLogInventoryCount(t, ctx, db); got != 0 {
		t.Fatalf("inventory after deleting all logs = %d, want 0", got)
	}
	assertUsageAnalyticsSnapshot(t, "delete all", baseline, takeUsageAnalyticsSnapshot(t, ctx, db))
}

func TestMessageFeedbackUsageMetadataSurvivesLogDeletion(t *testing.T) {
	db, ctx := openMessageFeedbackTestDB(t)
	seed := seedMessageFeedbackTest(t, ctx, db)

	channelID, err := MessageFeedbackChannelID(ctx, db, seed.answer1, seed.modelID)
	if err != nil || channelID != seed.channelID {
		t.Fatalf("channel before log deletion = %q, err=%v; want %q", channelID, err, seed.channelID)
	}
	if _, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: seed.answer1, ConversationID: seed.convID, UserID: "u1",
		ModelID: seed.modelID, ChannelID: channelID, Rating: MessageFeedbackDislike,
	}); err != nil {
		t.Fatalf("set feedback: %v", err)
	}
	before, err := AdminMessageFeedbackReportData(ctx, db, AdminMessageFeedbackFilter{}, 20, 0)
	if err != nil || len(before.Items) != 1 {
		t.Fatalf("feedback report before deletion = %+v, err=%v", before, err)
	}
	if !before.Items[0].Fallback || before.Items[0].ChannelID != seed.channelID {
		t.Fatalf("feedback metadata before deletion = %+v, want fallback on channel %q", before.Items[0], seed.channelID)
	}

	deleted, err := DeleteUsageByFilter(ctx, db, UsageFilter{})
	if err != nil || deleted != 1 {
		t.Fatalf("delete feedback usage log = %d, err=%v; want 1", deleted, err)
	}
	if got := usageLogInventoryCount(t, ctx, db); got != 0 {
		t.Fatalf("feedback usage log inventory = %d after deletion, want 0", got)
	}
	channelAfter, err := MessageFeedbackChannelID(ctx, db, seed.answer1, seed.modelID)
	if err != nil || channelAfter != channelID {
		t.Fatalf("channel after log deletion = %q, err=%v; want retained %q", channelAfter, err, channelID)
	}
	after, err := AdminMessageFeedbackReportData(ctx, db, AdminMessageFeedbackFilter{}, 20, 0)
	if err != nil || len(after.Items) != 1 {
		t.Fatalf("feedback report after deletion = %+v, err=%v", after, err)
	}
	if after.Items[0].Fallback != before.Items[0].Fallback || after.Items[0].ChannelID != before.Items[0].ChannelID {
		t.Fatalf("feedback usage metadata changed after log deletion: before=%+v after=%+v", before.Items[0], after.Items[0])
	}
}
