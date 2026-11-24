package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
	_ "time/tzdata"
)

const SystemTimeZoneKey = "system_time_zone"
const dateLayout = "2006-01-02"

func NormalizeSystemTimeZone(raw json.RawMessage) (string, error) {
	var zone string
	if err := json.Unmarshal(raw, &zone); err != nil {
		return "", err
	}
	zone = strings.TrimSpace(zone)
	if zone == "" || zone == "Local" || len(zone) > 100 {
		return "", ErrInvalidCreditConfig
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return "", err
	}
	return zone, nil
}

func SystemLocation(db *sql.DB) *time.Location {
	if db != nil {
		if raw, err := GetSetting(db, SystemTimeZoneKey); err == nil {
			return systemLocation(raw)
		}
	}
	return time.UTC
}

func systemLocation(raw json.RawMessage) *time.Location {
	var zone string
	if json.Unmarshal(raw, &zone) == nil && zone != "" && zone != "Local" {
		if loc, err := time.LoadLocation(zone); err == nil {
			return loc
		}
	}
	return time.UTC
}

func systemLocationFrom(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) *time.Location {
	var raw string
	if q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, SystemTimeZoneKey).Scan(&raw) != nil {
		return time.UTC
	}
	return systemLocation(json.RawMessage(raw))
}

// CalendarDay returns the local date's start and next start. In locations with a
// daylight-saving transition the elapsed time need not be exactly 24 hours.
func CalendarDay(now time.Time, zone *time.Location) (time.Time, time.Time) {
	local := now.In(zone)
	year, month, day := local.Date()
	return civilMidnight(year, month, day, zone), civilMidnight(year, month, day+1, zone)
}

func civilMidnight(year int, month time.Month, day int, zone *time.Location) time.Time {
	date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	midnight := time.Date(year, month, day, 0, 0, 0, 0, zone)
	// A zone can move its clock forward across midnight. time.Date may choose a
	// time on the preceding date in that gap; the transition begins the new day.
	if midnight.In(zone).Format(dateLayout) != date.Format(dateLayout) {
		_, next := midnight.ZoneBounds()
		if !next.IsZero() && next.In(zone).Format(dateLayout) == date.Format(dateLayout) {
			return next.In(zone)
		}
	}
	return midnight
}

func calendarCreditCycle(anchor int64, periodSeconds int, now int64, zone *time.Location) (int64, int64) {
	days := periodSeconds / 86400
	if anchor <= 0 || days <= 0 || periodSeconds%86400 != 0 || zone == time.UTC {
		return CreditCycleStart(anchor, periodSeconds, now)
	}
	localAnchor := time.Unix(anchor, 0).In(zone)
	localNow := time.Unix(now, 0).In(zone)
	// Compare the date components in UTC to get a count of civil days.
	anchorDay := time.Date(localAnchor.Year(), localAnchor.Month(), localAnchor.Day(), 0, 0, 0, 0, time.UTC)
	nowDay := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
	offset := max(0, int((nowDay.Unix()-anchorDay.Unix())/86400)/days) * days
	start := localAnchor.AddDate(0, 0, offset)
	if offset > 0 && start.Unix() > now {
		offset -= days
		start = localAnchor.AddDate(0, 0, offset)
	}
	return start.Unix(), localAnchor.AddDate(0, 0, offset+days).Unix()
}
