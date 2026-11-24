package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

const DailyUsageDays = 365

type DailyUsage struct {
	Date    string  `json:"date"`
	Tokens  int64   `json:"tokens"`
	Credits float64 `json:"credits"`
}

// ListDailyUsage reads durable billing records. One indexed range per day
// keeps the history bounded and lets both databases use the same local dates.
func ListDailyUsage(ctx context.Context, db *sql.DB, userID string, now time.Time, zone *time.Location) ([]DailyUsage, error) {
	today, _ := CalendarDay(now, zone)
	first := today.AddDate(0, 0, 1-DailyUsageDays)
	days := make([]DailyUsage, DailyUsageDays)
	values := make([]string, len(days))
	args := make([]any, 0, len(days)*2+5)
	for i := range days {
		start, end := CalendarDay(first.AddDate(0, 0, i), zone)
		days[i].Date = start.Format(dateLayout)
		values[i] = "(CAST(? AS BIGINT), CAST(? AS BIGINT))"
		args = append(args, start.Unix(), end.Unix())
	}
	args = append(args, userID, userID, CreditLedgerTimedDebit, CreditLedgerPermanentDebit, CreditLedgerCheckinDebit)
	rows, err := db.QueryContext(ctx, `WITH days(start_time, end_time) AS (VALUES `+strings.Join(values, ",")+`)
		SELECT
		  (SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM billing_usage
		   WHERE user_id=? AND created_at>=days.start_time AND created_at<days.end_time),
		  (SELECT COALESCE(SUM(CAST(amount_micros AS DOUBLE PRECISION)),0) FROM credit_ledger
		   WHERE user_id=? AND created_at>=days.start_time AND created_at<days.end_time
		     AND kind IN (?,?,?) AND source_type<>'migration')
		FROM days ORDER BY start_time`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for i := 0; rows.Next(); i++ {
		var micros float64
		if err := rows.Scan(&days[i].Tokens, &micros); err != nil {
			return nil, err
		}
		days[i].Credits = micros / float64(CreditMicrosPerUnit)
	}
	return days, rows.Err()
}
