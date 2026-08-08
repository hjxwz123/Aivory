package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const CreditAdjustmentReasonMaxRunes = 500

var (
	ErrInsufficientPermanentCredits = errors.New("insufficient permanent credits")
	ErrInvalidCreditNotification    = errors.New("invalid credit notification")
)

type PermanentCreditAdjustment struct {
	Before         float64
	After          float64
	Delta          float64
	NotificationID string
}

type CreditAdjustmentNotification struct {
	ID        string  `json:"id"`
	Direction string  `json:"direction"`
	Amount    float64 `json:"amount"`
	Reason    string  `json:"reason"`
	CreatedAt int64   `json:"created_at"`
}

// AdjustPermanentCredits atomically changes only the user's non-expiring
// balance. A requested notification is written in the same transaction, so a
// user can never be notified about an adjustment that was not committed.
func AdjustPermanentCredits(
	ctx context.Context,
	db *sql.DB,
	userID string,
	delta float64,
	notify bool,
	reason string,
) (PermanentCreditAdjustment, error) {
	if math.IsNaN(delta) || math.IsInf(delta, 0) || delta == 0 {
		return PermanentCreditAdjustment{}, ErrInvalidCreditAmount
	}
	amount := math.Abs(delta)
	amountMicros, err := CreditsToMicros(amount)
	if err != nil || amountMicros <= 0 {
		return PermanentCreditAdjustment{}, ErrInvalidCreditAmount
	}
	reason = strings.TrimSpace(reason)
	if notify && (reason == "" || utf8.RuneCountInString(reason) > CreditAdjustmentReasonMaxRunes) {
		return PermanentCreditAdjustment{}, ErrInvalidCreditNotification
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PermanentCreditAdjustment{}, err
	}
	defer func() { _ = tx.Rollback() }()

	current, err := permanentCreditsMicrosForUpdate(ctx, tx, userID)
	if err != nil {
		return PermanentCreditAdjustment{}, err
	}

	direction := "add"
	deltaMicros := amountMicros
	if delta < 0 {
		direction = "remove"
		deltaMicros = -amountMicros
		if current < amountMicros {
			return PermanentCreditAdjustment{}, ErrInsufficientPermanentCredits
		}
	} else if current > math.MaxInt64-amountMicros {
		return PermanentCreditAdjustment{}, ErrInvalidCreditAmount
	}

	next := current + deltaMicros
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET credits_permanent_micros=?, credits_permanent=CAST(? AS DOUBLE PRECISION)/1000000.0 WHERE id=?`,
		next, next, userID); err != nil {
		return PermanentCreditAdjustment{}, err
	}

	notificationID := ""
	if notify {
		notificationID = genID("can")
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credit_adjustment_notifications(id,user_id,direction,amount_micros,reason,created_at,claimed_at)
			 VALUES(?,?,?,?,?,?,0)`,
			notificationID, userID, direction, amountMicros, reason, time.Now().Unix()); err != nil {
			return PermanentCreditAdjustment{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return PermanentCreditAdjustment{}, err
	}
	return PermanentCreditAdjustment{
		Before:         creditsFromMicros(current),
		After:          creditsFromMicros(next),
		Delta:          creditsFromMicros(deltaMicros),
		NotificationID: notificationID,
	}, nil
}

func permanentCreditsMicrosForUpdate(ctx context.Context, tx *sql.Tx, userID string) (int64, error) {
	// SQLite transactions are deferred. This no-op update acquires the write
	// lock before reading, matching the existing top-up path's serialization.
	if !usePostgres {
		result, err := tx.ExecContext(ctx,
			`UPDATE users SET credits_permanent_micros=credits_permanent_micros WHERE id=?`, userID)
		if err != nil {
			return 0, err
		}
		if affected, err := result.RowsAffected(); err == nil && affected == 0 {
			return 0, ErrNotFound
		}
	}

	query := `SELECT CASE
	    WHEN COALESCE(credits_permanent_micros,0)=0 AND COALESCE(credits_permanent,0)<>0
	    THEN CAST(ROUND(credits_permanent*1000000) AS BIGINT)
	    ELSE COALESCE(credits_permanent_micros,0)
	 END FROM users WHERE id=?`
	if usePostgres {
		query += ` FOR UPDATE`
	}
	var current int64
	if err := tx.QueryRowContext(ctx, query, userID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return current, nil
}

// ClaimCreditAdjustmentNotification returns and permanently marks the oldest
// pending notice as claimed. This server-side claim provides once-only
// behavior across refreshes, tabs, and devices.
func ClaimCreditAdjustmentNotification(
	ctx context.Context,
	db *sql.DB,
	userID string,
) (*CreditAdjustmentNotification, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	query := `SELECT id,direction,amount_micros,reason,created_at
	            FROM credit_adjustment_notifications
	           WHERE user_id=? AND claimed_at=0
	           ORDER BY created_at,id
	           LIMIT 1`
	if usePostgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	var notice CreditAdjustmentNotification
	var amountMicros int64
	if err := tx.QueryRowContext(ctx, query, userID).Scan(
		&notice.ID, &notice.Direction, &amountMicros, &notice.Reason, &notice.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, err
	}

	result, err := tx.ExecContext(ctx,
		`UPDATE credit_adjustment_notifications SET claimed_at=? WHERE id=? AND claimed_at=0`,
		time.Now().Unix(), notice.ID)
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, errors.New("credit adjustment notification claim conflict")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	notice.Amount = creditsFromMicros(amountMicros)
	return &notice, nil
}
