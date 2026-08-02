package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

const DefaultCreditPeriodSeconds int64 = 7 * 24 * 60 * 60
const CreditMicrosPerUnit int64 = 1_000_000

const (
	CreditLedgerTimedDebit     = "timed_debit"
	CreditLedgerPermanentDebit = "permanent_debit"

	CreditReservationReserved = "reserved"
	CreditReservationSettled  = "settled"
	CreditReservationReleased = "released"
)

var (
	ErrInvalidCreditAmount       = errors.New("invalid credit amount")
	ErrInsufficientCredits       = errors.New("insufficient credits")
	ErrCreditReservationReleased = errors.New("credit reservation already released")
	ErrCreditReservationSourceID = errors.New("credit reservation source id required")
	ErrCreditReservationConflict = errors.New("credit reservation source conflict")
)

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
	Reserved       float64
	Available      float64

	allowanceMicros      int64
	timedUsedMicros      int64
	timedRemainingMicros int64
	permanentMicros      int64
	reservedMicros       int64
	availableMicros      int64
}

type CreditDebit struct {
	Timed     float64
	Permanent float64
	Total     float64
}

type CreditReservation struct {
	ID           string
	UserID       string
	Amount       float64
	Actual       float64
	SourceType   string
	SourceID     string
	Status       string
	ExpiresAt    int64
	CreatedAt    int64
	UpdatedAt    int64
	amountMicros int64
}

func CreditsToMicros(amount float64) (int64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, ErrInvalidCreditAmount
	}
	scaled := amount * float64(CreditMicrosPerUnit)
	// float64(math.MaxInt64) rounds up to 2^63. Reject that boundary before
	// conversion; converting an out-of-range float to int64 is implementation-
	// dependent and can otherwise turn a huge positive balance negative.
	if math.IsInf(scaled, 0) || scaled >= float64(math.MaxInt64) {
		return 0, ErrInvalidCreditAmount
	}
	return int64(math.Round(scaled)), nil
}

func creditsFromMicros(amount int64) float64 {
	return float64(amount) / float64(CreditMicrosPerUnit)
}

func CreditCycleStart(anchor int64, periodSeconds int, now int64) (start, resetsAt int64) {
	if anchor <= 0 || periodSeconds <= 0 {
		return 0, 0
	}
	period := int64(periodSeconds)
	start = anchor
	if now > anchor {
		start += ((now - anchor) / period) * period
	}
	if start > math.MaxInt64-period {
		return start, math.MaxInt64
	}
	return start, start + period
}

func nextCreditCycleAnchor(current, now int64) int64 {
	if current >= now {
		if current == math.MaxInt64 {
			return now
		}
		return current + 1
	}
	return now
}

func creditBalanceFrom(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID string, now int64, lockUser bool) (CreditBalance, error) {
	query := `SELECT u.group_id, COALESCE(u.credit_cycle_anchor,0),
	                 CASE WHEN COALESCE(u.credits_permanent_micros,0)=0 AND COALESCE(u.credits_permanent,0)<>0
	                      THEN CAST(ROUND(u.credits_permanent*1000000) AS BIGINT) ELSE COALESCE(u.credits_permanent_micros,0) END,
	                 CASE WHEN COALESCE(g.credit_allowance_micros,0)=0 AND COALESCE(g.credit_allowance,0)<>0
	                      THEN CAST(ROUND(g.credit_allowance*1000000) AS BIGINT) ELSE COALESCE(g.credit_allowance_micros,0) END,
	                 COALESCE(g.credit_period_seconds,0)
	            FROM users u LEFT JOIN user_groups g ON g.id=u.group_id WHERE u.id=?`
	if lockUser && usePostgres {
		query += ` FOR UPDATE OF u`
	}
	var balance CreditBalance
	if err := q.QueryRowContext(ctx, query, userID).Scan(
		&balance.GroupID, &balance.CycleAnchor, &balance.permanentMicros,
		&balance.allowanceMicros, &balance.PeriodSeconds,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CreditBalance{}, ErrNotFound
		}
		return CreditBalance{}, err
	}
	if balance.CycleAnchor <= 0 {
		balance.CycleAnchor = now
	}
	if balance.allowanceMicros > 0 && balance.PeriodSeconds > 0 {
		balance.CycleStart, balance.ResetsAt = CreditCycleStart(balance.CycleAnchor, balance.PeriodSeconds, now)
		if err := q.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(amount_micros),0) FROM credit_ledger
			  WHERE user_id=? AND group_id=? AND cycle_anchor=? AND cycle_start=? AND kind=?`,
			userID, balance.GroupID, balance.CycleAnchor, balance.CycleStart, CreditLedgerTimedDebit,
		).Scan(&balance.timedUsedMicros); err != nil {
			return CreditBalance{}, err
		}
		balance.timedRemainingMicros = balance.allowanceMicros - balance.timedUsedMicros
		if balance.timedRemainingMicros < 0 {
			balance.timedRemainingMicros = 0
		}
	}
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_micros),0) FROM credit_reservations
		  WHERE user_id=? AND status=? AND expires_at>?`,
		userID, CreditReservationReserved, now,
	).Scan(&balance.reservedMicros); err != nil {
		return CreditBalance{}, err
	}
	spendable := balance.timedRemainingMicros
	if balance.permanentMicros > 0 && spendable > math.MaxInt64-balance.permanentMicros {
		spendable = math.MaxInt64
	} else {
		spendable += balance.permanentMicros
	}
	if spendable < 0 {
		spendable = 0
	}
	balance.availableMicros = spendable - balance.reservedMicros
	if balance.availableMicros < 0 {
		balance.availableMicros = 0
	}
	balance.Allowance = creditsFromMicros(balance.allowanceMicros)
	balance.TimedUsed = creditsFromMicros(balance.timedUsedMicros)
	balance.TimedRemaining = creditsFromMicros(balance.timedRemainingMicros)
	balance.Permanent = creditsFromMicros(balance.permanentMicros)
	balance.Reserved = creditsFromMicros(balance.reservedMicros)
	balance.Available = creditsFromMicros(balance.availableMicros)
	return balance, nil
}

func GetCreditBalance(ctx context.Context, db *sql.DB, userID string) (CreditBalance, error) {
	return creditBalanceFrom(ctx, db, userID, time.Now().Unix(), false)
}

func debitCreditsTx(ctx context.Context, tx *sql.Tx, userID string, amountMicros int64, sourceType, sourceID string, now int64) (CreditDebit, error) {
	if amountMicros < 0 {
		return CreditDebit{}, ErrInvalidCreditAmount
	}
	if amountMicros == 0 {
		return CreditDebit{}, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users
		    SET credits_permanent_micros=CASE
		            WHEN COALESCE(credits_permanent_micros,0)=0 AND COALESCE(credits_permanent,0)<>0
		            THEN CAST(ROUND(credits_permanent*1000000) AS BIGINT)
		            ELSE credits_permanent_micros
		        END
		  WHERE id=?`, userID); err != nil {
		return CreditDebit{}, err
	}
	balance, err := creditBalanceFrom(ctx, tx, userID, now, true)
	if err != nil {
		return CreditDebit{}, err
	}
	if amountMicros > balance.availableMicros {
		return CreditDebit{}, ErrInsufficientCredits
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET credit_cycle_anchor=? WHERE id=? AND COALESCE(credit_cycle_anchor,0)<=0`,
		balance.CycleAnchor, userID); err != nil {
		return CreditDebit{}, err
	}
	timedMicros := amountMicros
	if timedMicros > balance.timedRemainingMicros {
		timedMicros = balance.timedRemainingMicros
	}
	permanentMicros := amountMicros - timedMicros
	if timedMicros > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credit_ledger(id,user_id,group_id,cycle_anchor,cycle_start,kind,amount,amount_micros,source_type,source_id,created_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			genID("cl"), userID, balance.GroupID, balance.CycleAnchor, balance.CycleStart,
			CreditLedgerTimedDebit, creditsFromMicros(timedMicros), timedMicros, sourceType, sourceID, now); err != nil {
			return CreditDebit{}, err
		}
	}
	if permanentMicros > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users
			    SET credits_permanent_micros=COALESCE(credits_permanent_micros,0)-?,
			        credits_permanent=CAST(COALESCE(credits_permanent_micros,0)-? AS DOUBLE PRECISION)/1000000.0
			  WHERE id=?`,
			permanentMicros, permanentMicros, userID); err != nil {
			return CreditDebit{}, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credit_ledger(id,user_id,group_id,cycle_anchor,cycle_start,kind,amount,amount_micros,source_type,source_id,created_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			genID("cl"), userID, balance.GroupID, balance.CycleAnchor, balance.CycleStart,
			CreditLedgerPermanentDebit, creditsFromMicros(permanentMicros), permanentMicros, sourceType, sourceID, now); err != nil {
			return CreditDebit{}, err
		}
	}
	return CreditDebit{
		Timed: creditsFromMicros(timedMicros), Permanent: creditsFromMicros(permanentMicros), Total: creditsFromMicros(amountMicros),
	}, nil
}

func DebitCredits(ctx context.Context, db *sql.DB, userID string, amount float64, sourceType, sourceID string) (CreditDebit, error) {
	amountMicros, err := CreditsToMicros(amount)
	if err != nil {
		return CreditDebit{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return CreditDebit{}, err
	}
	defer func() { _ = tx.Rollback() }()
	debit, err := debitCreditsTx(ctx, tx, userID, amountMicros, sourceType, sourceID, time.Now().Unix())
	if err != nil {
		return CreditDebit{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreditDebit{}, err
	}
	return debit, nil
}

func ReserveCredits(ctx context.Context, db *sql.DB, userID string, amount float64, sourceType, sourceID string, ttl time.Duration) (*CreditReservation, error) {
	if sourceID == "" {
		return nil, ErrCreditReservationSourceID
	}
	amountMicros, err := CreditsToMicros(amount)
	if err != nil || amountMicros <= 0 {
		return nil, ErrInvalidCreditAmount
	}
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users
		    SET credits_permanent_micros=CASE
		            WHEN COALESCE(credits_permanent_micros,0)=0 AND COALESCE(credits_permanent,0)<>0
		            THEN CAST(ROUND(credits_permanent*1000000) AS BIGINT)
		            ELSE credits_permanent_micros
		        END
		  WHERE id=?`, userID); err != nil {
		return nil, err
	}
	if existing, err := creditReservationBySource(ctx, tx, sourceType, sourceID, usePostgres); err == nil {
		if existing.UserID != userID || existing.amountMicros != amountMicros || existing.Status == CreditReservationReleased {
			return nil, ErrCreditReservationConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	balance, err := creditBalanceFrom(ctx, tx, userID, now, true)
	if err != nil {
		return nil, err
	}
	if amountMicros > balance.availableMicros {
		return nil, ErrInsufficientCredits
	}
	reservation := &CreditReservation{
		ID: genID("cr"), UserID: userID, Amount: creditsFromMicros(amountMicros), amountMicros: amountMicros,
		SourceType: sourceType, SourceID: sourceID, Status: CreditReservationReserved,
		ExpiresAt: now + int64(ttl/time.Second), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO credit_reservations(id,user_id,amount_micros,actual_micros,source_type,source_id,status,expires_at,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		reservation.ID, reservation.UserID, amountMicros, 0, sourceType, sourceID, reservation.Status,
		reservation.ExpiresAt, now, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return reservation, nil
}

func creditReservationBySource(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceType, sourceID string, lock bool) (*CreditReservation, error) {
	query := `SELECT id,user_id,amount_micros,actual_micros,source_type,source_id,status,expires_at,created_at,updated_at
	            FROM credit_reservations WHERE source_type=? AND source_id=?`
	if lock && usePostgres {
		query += ` FOR UPDATE`
	}
	var r CreditReservation
	var actualMicros int64
	if err := q.QueryRowContext(ctx, query, sourceType, sourceID).Scan(
		&r.ID, &r.UserID, &r.amountMicros, &actualMicros, &r.SourceType, &r.SourceID, &r.Status,
		&r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Amount = creditsFromMicros(r.amountMicros)
	r.Actual = creditsFromMicros(actualMicros)
	return &r, nil
}

func SettleCreditReservation(ctx context.Context, db *sql.DB, sourceType, sourceID string, actual float64) (CreditDebit, error) {
	actualMicros, err := CreditsToMicros(actual)
	if err != nil {
		return CreditDebit{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return CreditDebit{}, err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := creditReservationBySource(ctx, tx, sourceType, sourceID, true)
	if err != nil {
		return CreditDebit{}, err
	}
	if r.Status == CreditReservationSettled {
		if err := tx.Commit(); err != nil {
			return CreditDebit{}, err
		}
		return CreditDebit{Total: r.Actual}, nil
	}
	if r.Status == CreditReservationReleased {
		return CreditDebit{}, ErrCreditReservationReleased
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`UPDATE credit_reservations SET status='settling', actual_micros=?, updated_at=? WHERE id=? AND status=?`,
		actualMicros, now, r.ID, CreditReservationReserved); err != nil {
		return CreditDebit{}, err
	}
	debit, err := debitCreditsTx(ctx, tx, r.UserID, actualMicros, sourceType, sourceID, now)
	if err != nil {
		return CreditDebit{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE credit_reservations SET status=?, actual_micros=?, updated_at=? WHERE id=?`,
		CreditReservationSettled, actualMicros, now, r.ID); err != nil {
		return CreditDebit{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreditDebit{}, err
	}
	return debit, nil
}

func ReleaseCreditReservation(ctx context.Context, db *sql.DB, sourceType, sourceID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE credit_reservations SET status=?, updated_at=? WHERE source_type=? AND source_id=? AND status=?`,
		CreditReservationReleased, time.Now().Unix(), sourceType, sourceID, CreditReservationReserved)
	return err
}

func migrateCreditLedger(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET credits_permanent_micros=CAST(ROUND(COALESCE(credits_permanent,0)*1000000) AS BIGINT)
		  WHERE credits_permanent_micros=0 AND COALESCE(credits_permanent,0)<>0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE user_groups SET credit_allowance_micros=CAST(ROUND(COALESCE(credit_allowance,0)*1000000) AS BIGINT)
		  WHERE credit_allowance_micros=0 AND COALESCE(credit_allowance,0)<>0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE credit_ledger SET amount_micros=CAST(ROUND(COALESCE(amount,0)*1000000) AS BIGINT)
		  WHERE amount_micros=0 AND COALESCE(amount,0)<>0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE user_groups SET credit_period_seconds=? WHERE credit_allowance_micros>0 AND credit_period_seconds<=0`,
		DefaultCreditPeriodSeconds); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET quota_cycle_anchor=CASE WHEN credit_cycle_anchor>0 THEN credit_cycle_anchor ELSE ? END
		  WHERE COALESCE(quota_cycle_anchor,0)<=0`, now); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT u.id, u.group_id, COALESCE(g.credit_allowance_micros,0), COALESCE(g.credit_period_seconds,0)
		   FROM users u LEFT JOIN user_groups g ON g.id=u.group_id
		  WHERE COALESCE(u.credit_cycle_anchor,0)<=0`)
	if err != nil {
		return err
	}
	type legacyUser struct {
		id, groupID string
		allowance   int64
		period      int
	}
	var users []legacyUser
	for rows.Next() {
		var u legacyUser
		if err := rows.Scan(&u.id, &u.groupID, &u.allowance, &u.period); err != nil {
			_ = rows.Close()
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
			if _, err := tx.ExecContext(ctx, `UPDATE users SET credit_cycle_anchor=?, quota_cycle_anchor=? WHERE id=?`, now, now, u.id); err != nil {
				return err
			}
			continue
		}
		anchor := (now / period) * period
		var used float64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(credits),0) FROM usage_stats WHERE user_id=? AND created_at>=?`,
			u.id, anchor).Scan(&used); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET credit_cycle_anchor=?, quota_cycle_anchor=? WHERE id=?`, anchor, anchor, u.id); err != nil {
			return err
		}
		usedMicros, err := CreditsToMicros(used)
		if err != nil {
			return err
		}
		if usedMicros > 0 {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO credit_ledger(id,user_id,group_id,cycle_anchor,cycle_start,kind,amount,amount_micros,source_type,source_id,created_at)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
				genID("cl"), u.id, u.groupID, anchor, anchor, CreditLedgerTimedDebit,
				creditsFromMicros(usedMicros), usedMicros, "migration", "usage_stats", now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
