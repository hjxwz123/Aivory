package store

import (
	"context"
	"testing"
	"time"
)

func TestBillingUsageSurvivesAnalyticsDeletionAndAggregatesOnce(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash) VALUES('billing-user','billing@example.test','hash')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	rows := []UsageLog{
		{UserID: "billing-user", MessageID: "billing-message", ModelID: "image", Purpose: "image", ImagesCount: 2, Cost: 0.3, Currency: "USD"},
		{UserID: "billing-user", MessageID: "billing-message", ModelID: "task", Purpose: "task.tool_route", InputTokens: 10, OutputTokens: 5, Cost: 0.2, Currency: "USD"},
		{UserID: "billing-user", MessageID: "billing-message", ModelID: "chat", Purpose: "chat", InputTokens: 20, OutputTokens: 10, Cost: 1.0, Currency: "USD"},
	}
	for _, row := range rows {
		if err := LogUsage(ctx, db, row); err != nil {
			t.Fatalf("log usage: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id='billing-user'`); err != nil {
		t.Fatalf("delete analytics: %v", err)
	}
	costs, err := TurnSideBillingCosts(ctx, db, "billing-message")
	if err != nil {
		t.Fatalf("turn billing costs: %v", err)
	}
	if costs.Total != 0.5 || costs.Image != 0.3 {
		t.Fatalf("turn side costs = %+v, want total .5 image .3", costs)
	}
	dayStart := time.Now().Add(-time.Hour).Unix()
	images, err := BillingImagesSince(ctx, db, "billing-user", dayStart)
	if err != nil || images != 2 {
		t.Fatalf("billing images = %d, %v, want 2", images, err)
	}
	tokens, err := BillingTokensSince(ctx, db, "billing-user", dayStart)
	if err != nil || tokens != 45 {
		t.Fatalf("billing tokens = %d, %v, want 45", tokens, err)
	}
}

func TestBillingUsageRejectsNonUSDProviderCost(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash) VALUES('billing-user','billing@example.test','hash')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	err := RecordBillingUsage(context.Background(), db, UsageLog{
		UserID: "billing-user", Purpose: "chat", Cost: 1, Currency: "EUR",
	})
	if err != ErrInvalidBillingUsage {
		t.Fatalf("non-USD billing error = %v, want %v", err, ErrInvalidBillingUsage)
	}
}
