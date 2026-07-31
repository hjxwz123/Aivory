package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
)

var ErrInvalidBillingUsage = errors.New("invalid billing usage")

type TurnBillingCosts struct {
	Total float64
	Image float64
}

func billingCostToMicros(cost float64) (int64, error) {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return 0, ErrInvalidBillingUsage
	}
	scaled := cost * 1e6
	if math.IsInf(scaled, 0) || scaled >= float64(math.MaxInt64) {
		return 0, ErrInvalidBillingUsage
	}
	return int64(math.Round(scaled)), nil
}

// RecordBillingUsage stores provider consumption independently from usage_logs,
// which administrators may prune as analytics data. Zero-cost token/image rows
// are retained because they enforce deployment-wide daily limits.
func RecordBillingUsage(ctx context.Context, db *sql.DB, u UsageLog) error {
	if db == nil {
		return ErrInvalidBillingUsage
	}
	costMicros, err := billingCostToMicros(u.Cost)
	if err != nil || u.InputTokens < 0 || u.OutputTokens < 0 || u.ImagesCount < 0 {
		return ErrInvalidBillingUsage
	}
	if costMicros > 0 && strings.ToUpper(strings.TrimSpace(u.Currency)) != "USD" {
		return ErrInvalidBillingUsage
	}
	if costMicros == 0 && u.InputTokens == 0 && u.OutputTokens == 0 && u.ImagesCount == 0 {
		return nil
	}
	currency := strings.ToUpper(strings.TrimSpace(u.Currency))
	if currency == "" {
		currency = "USD"
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO billing_usage(id,user_id,conversation_id,message_id,model_id,purpose,cost_micros,images_count,input_tokens,output_tokens,currency,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		genID("bu"), nullable(u.UserID), u.ConversationID, u.MessageID, u.ModelID, u.Purpose,
		costMicros, u.ImagesCount, u.InputTokens, u.OutputTokens, currency, time.Now().Unix())
	return err
}

// TurnSideBillingCosts returns side-call cost and its image component in one
// authoritative read, preventing separate aggregates from observing different
// sets of rows or classifying the same image cost twice.
func TurnSideBillingCosts(ctx context.Context, db *sql.DB, messageID string) (TurnBillingCosts, error) {
	if db == nil || messageID == "" {
		return TurnBillingCosts{}, ErrInvalidBillingUsage
	}
	var totalMicros, imageMicros int64
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost_micros),0),
		        COALESCE(SUM(CASE WHEN purpose='image' THEN cost_micros ELSE 0 END),0)
		   FROM billing_usage WHERE message_id=? AND purpose<>'chat'`, messageID).
		Scan(&totalMicros, &imageMicros)
	if err != nil {
		return TurnBillingCosts{}, err
	}
	return TurnBillingCosts{Total: creditsFromMicros(totalMicros), Image: creditsFromMicros(imageMicros)}, nil
}

func BillingImagesSince(ctx context.Context, db *sql.DB, userID string, since int64) (int, error) {
	var used int
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(images_count),0) FROM billing_usage WHERE user_id=? AND created_at>=?`,
		userID, since).Scan(&used)
	return used, err
}

func BillingTokensSince(ctx context.Context, db *sql.DB, userID string, since int64) (int, error) {
	var used int
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM billing_usage WHERE user_id=? AND created_at>=?`,
		userID, since).Scan(&used)
	return used, err
}
