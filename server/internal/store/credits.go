package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

const DefaultCreditPeriodSeconds int64 = 7 * 24 * 60 * 60

const (
	CreditLedgerTimedDebit     = "timed_debit"
	CreditLedgerPermanentDebit = "permanent_debit"
)

// CreditBalance is the authoritative live balance for a user's current group.
// Permanent may be negative when a completed request exceeded the preflight
// estimate; Available never drops below zero and prevents further paid use until
// a top-up or a future timed allowance covers that debt.
type CreditBalance struct {
	GroupID        string
	CycleAnchor    int64
	CycleStart     int64
	ResetsAt       int64
	PeriodSeconds  int
	Allowance      float64
	TimedUsed      float64
	TimedRemaining float64
	Permanent      float64
	Available      float64
}

type CreditDebit struct {
	Timed     float64
	Permanent float64
	Total     float64
}

// CreditCycleStart returns the user-anchored cycle containing now. A non-positive
// period means there is no timed allowance and therefore no reset boundary.
func CreditCycleStart(anchor int64, periodSeconds int, now int64) (start, resetsAt int64) {
	if anchor <= 0 || periodSeconds <= 0 {
		return 0, 0
	}
	period := int64(periodSeconds)
	start = anchor
	if now > anchor {
		start += ((now - anchor) / period) * period
	}
	return start, start + period
}

var ErrInvalidCreditAmount = errors.New("invalid credit amount")

func nextCreditCycleAnchor(current, now int64) int64 {
	if current >= now {
		return current + 1
	}
	return now
}

func creditBalanceFrom(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID string, now int64, lockUser bool) (CreditBalance, error) {
	query := `SELECT u.group_id, COALESCE(u.credit_cycle_anchor,0), COALESCE(u.credits_permanent,0),
	                 COALESCE(g.credit_allowance,0), COALESCE(g.credit_period_seconds,0)
	            FROM users u LEFT JOIN user_groups g ON g.id=u.group_id WHERE u.id=?`
	if lockUser && usePostgres {
		query += ` FOR UPDATE OF u`
	}
	var balance CreditBalance
	if err := q.QueryRowContext(ctx, query, userID).Scan(
		&balance.GroupID, &balance.CycleAnchor, &balance.Permanent,
		&balance.Allowance, &balance.PeriodSeconds,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CreditBalance{}, ErrNotFound
		}
		return CreditBalance{}, err
	}
	if balance.CycleAnchor <= 0 {
		balance.CycleAnchor = now
	}
	if balance.Allowance > 0 && balance.PeriodSeconds > 0 {
		balance.CycleStart, balance.ResetsAt = CreditCycleStart(balance.CycleAnchor, balance.PeriodSeconds, now)
		if err := q.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(amount),0) FROM credit_ledger
			  WHERE user_id=? AND group_id=? AND cycle_anchor=? AND cycle_start=? AND kind=?`,
			userID, balance.GroupID, balance.CycleAnchor, balance.CycleStart, CreditLedgerTimedDebit,
		).Scan(&balance.TimedUsed); err != nil {
			return CreditBalance{}, err
		}
		balance.TimedRemaining = balance.Allowance - balance.TimedUsed
		if balance.TimedRemaining < 0 {
			balance.TimedRemaining = 0
		}
	}
	balance.Available = balance.TimedRemaining + balance.Permanent
	if balance.Available < 0 {
		balance.Available = 0
	}
	return balance, nil
}

func GetCreditBalance(ctx context.Context, db *sql.DB, userID string) (CreditBalance, error) {
	return creditBalanceFrom(ctx, db, userID, time.Now().Unix(), false)
}

// DebitCredits serializes every debit on the user row, consumes the current
// anchored timed allowance first, and records both portions in the billing
// ledger. The permanent balance may become negative when actual provider usage
// exceeds a preflight estimate; this records the full liability instead of
// silently granting the excess request for free.
func DebitCredits(ctx context.Context, db *sql.DB, userID string, amount float64, sourceType, sourceID string) (CreditDebit, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return CreditDebit{}, ErrInvalidCreditAmount
	}
	if amount == 0 {
		return CreditDebit{}, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return CreditDebit{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	balance, err := creditBalanceFrom(ctx, tx, userID, now, true)
	if err != nil {
		return CreditDebit{}, err
	}
	if balance.CycleAnchor <= 0 {
		balance.CycleAnchor = now
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET credit_cycle_anchor=? WHERE id=? AND COALESCE(credit_cycle_anchor,0)<=0`,
		balance.CycleAnchor, userID); err != nil {
		return CreditDebit{}, err
	}

	debit := CreditDebit{Total: amount}
	debit.Timed = math.Min(amount, balance.TimedRemaining)
	debit.Permanent = amount - debit.Timed
	if debit.Timed > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credit_ledger(id,user_id,group_id,cycle_anchor,cycle_start,kind,amount,source_type,source_id,created_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?)`,
			genID("cl"), userID, balance.GroupID, balance.CycleAnchor, balance.CycleStart,
			CreditLedgerTimedDebit, debit.Timed, sourceType, sourceID, now); err != nil {
			return CreditDebit{}, err
		}
	}
	if debit.Permanent > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET credits_permanent=COALESCE(credits_permanent,0)-? WHERE id=?`,
			debit.Permanent, userID); err != nil {
			return CreditDebit{}, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credit_ledger(id,user_id,group_id,cycle_anchor,cycle_start,kind,amount,source_type,source_id,created_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?)`,
			genID("cl"), userID, balance.GroupID, balance.CycleAnchor, balance.CycleStart,
			CreditLedgerPermanentDebit, debit.Permanent, sourceType, sourceID, now); err != nil {
			return CreditDebit{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CreditDebit{}, err
	}
	return debit, nil
}

// migrateCreditLedger preserves the current timed balance of pre-ledger
// deployments. Existing accounts keep their current fixed-window boundary once;
// every later group change receives a new per-user anchor.
func migrateCreditLedger(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE user_groups SET credit_period_seconds=? WHERE credit_allowance>0 AND credit_period_seconds<=0`,
		DefaultCreditPeriodSeconds); err != nil {
		return err
	}

	now := time.Now().Unix()
	rows, err := tx.QueryContext(ctx,
		`SELECT u.id, u.group_id, COALESCE(g.credit_allowance,0), COALESCE(g.credit_period_seconds,0)
		   FROM users u LEFT JOIN user_groups g ON g.id=u.group_id
		  WHERE COALESCE(u.credit_cycle_anchor,0)<=0`)
	if err != nil {
		return err
	}
	type legacyUser struct {
		id, groupID string
		allowance   float64
		period      int
	}
	var users []legacyUser
	for rows.Next() {
		var u legacyUser
		if err := rows.Scan(&u.id, &u.groupID, &u.allowance, &u.period); err != nil {
			rows.Close()
			return err
		}
		users = append(users, u)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, u := range users {
		period := int64(u.period)
		if u.allowance <= 0 || period <= 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET credit_cycle_anchor=? WHERE id=?`, now, u.id); err != nil {
				return err
			}
			continue
		}
		anchor := (now / period) * period
		var used float64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(credits),0) FROM usage_logs WHERE user_id=? AND created_at>=?`,
			u.id, anchor).Scan(&used); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET credit_cycle_anchor=? WHERE id=?`, anchor, u.id); err != nil {
			return err
		}
		if used > 0 {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO credit_ledger(id,user_id,group_id,cycle_anchor,cycle_start,kind,amount,source_type,source_id,created_at)
				 VALUES(?,?,?,?,?,?,?,?,?,?)`,
				genID("cl"), u.id, u.groupID, anchor, anchor, CreditLedgerTimedDebit, used, "migration", "usage_logs", now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
