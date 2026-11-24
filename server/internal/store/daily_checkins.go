package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"time"
)

const DailyCheckinKey = "daily_checkin"

var ErrDailyCheckinDisabled = errors.New("daily_checkin_disabled")

type DailyCheckinConfig struct {
	Enabled bool    `json:"enabled"`
	Mode    string  `json:"mode"`
	Pool    string  `json:"pool"`
	Fixed   float64 `json:"fixed"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
}

type DailyCheckin struct {
	Date      string  `json:"date"`
	Credits   float64 `json:"credits"`
	ExpiresAt int64   `json:"expires_at"`
}

func NormalizeDailyCheckin(raw json.RawMessage) (DailyCheckinConfig, error) {
	var cfg *DailyCheckinConfig
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg == nil {
		return DailyCheckinConfig{}, ErrInvalidCreditConfig
	}
	validAmount := func(amount float64) bool {
		return amount >= 0.01 && amount <= 1_000_000 && math.Round(amount*100)/100 == amount
	}
	if cfg.Pool != "timed" && cfg.Pool != "permanent" {
		return DailyCheckinConfig{}, ErrInvalidCreditConfig
	}
	switch cfg.Mode {
	case "fixed":
		if validAmount(cfg.Fixed) {
			return *cfg, nil
		}
	case "random":
		if validAmount(cfg.Min) && validAmount(cfg.Max) && cfg.Min <= cfg.Max {
			return *cfg, nil
		}
	}
	return DailyCheckinConfig{}, ErrInvalidCreditConfig
}

func GetDailyCheckinConfig(db *sql.DB) (DailyCheckinConfig, error) {
	raw, err := GetSetting(db, DailyCheckinKey)
	if errors.Is(err, sql.ErrNoRows) {
		return DailyCheckinConfig{}, nil
	}
	if err != nil {
		return DailyCheckinConfig{}, err
	}
	return NormalizeDailyCheckin(raw)
}

func GetDailyCheckin(ctx context.Context, q RowExecer, userID string, start, end time.Time) (*DailyCheckin, error) {
	var claim DailyCheckin
	var micros int64
	err := q.QueryRowContext(ctx, `SELECT day, amount_micros, expires_at FROM daily_checkins
		WHERE user_id=? AND (day=? OR (claimed_at>=? AND claimed_at<?)) ORDER BY claimed_at DESC LIMIT 1`,
		userID, start.Format(dateLayout), start.Unix(), end.Unix()).Scan(&claim.Date, &micros, &claim.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	claim.Credits = creditsFromMicros(micros)
	return &claim, nil
}

func ListDailyCheckins(ctx context.Context, db *sql.DB, userID string, month time.Time) ([]DailyCheckin, error) {
	rows, err := db.QueryContext(ctx, `SELECT day, amount_micros, expires_at FROM daily_checkins
		WHERE user_id=? AND day>=? AND day<? ORDER BY day`, userID, month.Format(dateLayout), month.AddDate(0, 1, 0).Format(dateLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claims := make([]DailyCheckin, 0)
	for rows.Next() {
		var claim DailyCheckin
		var micros int64
		if err := rows.Scan(&claim.Date, &micros, &claim.ExpiresAt); err != nil {
			return nil, err
		}
		claim.Credits = creditsFromMicros(micros)
		claims = append(claims, claim)
	}
	return claims, rows.Err()
}

// ClaimDailyCheckin grants a reward and records the claim in one transaction.
// Lock the user in the same order as credit settlement before updating either pool.
func ClaimDailyCheckin(ctx context.Context, db *sql.DB, userID string, now time.Time, zone *time.Location) (*DailyCheckin, bool, error) {
	start, end := CalendarDay(now, zone)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `UPDATE users SET credits_permanent_micros=credits_permanent_micros WHERE id=?`, userID); err != nil {
		return nil, false, err
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, DailyCheckinKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrDailyCheckinDisabled
	}
	if err != nil {
		return nil, false, err
	}
	cfg, err := NormalizeDailyCheckin(json.RawMessage(raw))
	if err != nil {
		return nil, false, err
	}
	if !cfg.Enabled {
		return nil, false, ErrDailyCheckinDisabled
	}
	if claim, err := GetDailyCheckin(ctx, tx, userID, start, end); err != nil || claim != nil {
		return claim, false, err
	}
	amount := cfg.Fixed
	if cfg.Mode == "random" {
		minCents, maxCents := int64(math.Round(cfg.Min*100)), int64(math.Round(cfg.Max*100))
		roll, err := rand.Int(rand.Reader, big.NewInt(maxCents-minCents+1))
		if err != nil {
			return nil, false, err
		}
		amount = float64(minCents+roll.Int64()) / 100
	}
	micros, err := CreditsToMicros(amount)
	if err != nil {
		return nil, false, err
	}
	claim := &DailyCheckin{Date: start.Format(dateLayout), Credits: amount}
	var remaining int64
	if cfg.Pool == "timed" {
		claim.ExpiresAt, remaining = end.Unix(), micros
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO daily_checkins(user_id, day, claimed_at, amount_micros, remaining_micros, expires_at)
		VALUES(?,?,?,?,?,?)`, userID, claim.Date, now.Unix(), micros, remaining, claim.ExpiresAt); err != nil {
		return nil, false, err
	}
	if cfg.Pool == "permanent" {
		if err := addPermanentCreditsMicrosTx(ctx, tx, userID, micros); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return claim, true, nil
}

func debitTimedCheckinCredits(ctx context.Context, tx *sql.Tx, userID string, micros, now int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT day, remaining_micros FROM daily_checkins
		WHERE user_id=? AND expires_at>? AND remaining_micros>0 ORDER BY expires_at, day`, userID, now)
	if err != nil {
		return err
	}
	type debit struct {
		day    string
		micros int64
	}
	var debits []debit
	for micros > 0 && rows.Next() {
		var entry debit
		if err := rows.Scan(&entry.day, &entry.micros); err != nil {
			_ = rows.Close()
			return err
		}
		entry.micros = min(micros, entry.micros)
		micros -= entry.micros
		debits = append(debits, entry)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if micros != 0 {
		return ErrInsufficientCredits
	}
	for _, entry := range debits {
		if _, err := tx.ExecContext(ctx, `UPDATE daily_checkins SET remaining_micros=remaining_micros-?
			WHERE user_id=? AND day=?`, entry.micros, userID, entry.day); err != nil {
			return err
		}
	}
	return nil
}
