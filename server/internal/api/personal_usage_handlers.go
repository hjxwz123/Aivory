package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/store"
)

func meDailyUsageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx, userID := r.Context(), authUser(r).ID
	now, zone := time.Now(), store.SystemLocation(d.DB)
	start, end := store.CalendarDay(now, zone)
	config, err := store.GetDailyCheckinConfig(d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	claim, err := store.GetDailyCheckin(ctx, d.DB, userID, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var firstClaim string
	if err := d.DB.QueryRowContext(ctx, `SELECT COALESCE(MIN(day), '') FROM daily_checkins WHERE user_id=?`, userID).Scan(&firstClaim); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	balance, err := store.GetCreditBalance(ctx, d.DB, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	days, err := store.ListDailyUsage(ctx, d.DB, userID, now, zone)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"credits": creditsSnapshot(d, balance), "days": days, "time_zone": zone.String(), "next_day_at": end.Unix(),
		"checkin_enabled": config.Enabled, "checkin": claim, "first_checkin_date": firstClaim,
	})
}

func meCheckinHistoryHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	value := r.URL.Query().Get("month")
	month, err := time.Parse("2006-01", value)
	if err != nil || month.Year() < 1 || month.Year() >= 9999 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	claims, err := store.ListDailyCheckins(r.Context(), d.DB, authUser(r).ID, month)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, claims)
}

func claimDailyCheckinHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx, userID := r.Context(), authUser(r).ID
	now, zone := time.Now(), store.SystemLocation(d.DB)
	if expected := strings.TrimSpace(r.URL.Query().Get("date")); expected != "" && expected != now.In(zone).Format("2006-01-02") {
		writeError(w, http.StatusConflict, errors.New("daily_checkin_day_changed"))
		return
	}
	claim, awarded, err := store.ClaimDailyCheckin(ctx, d.DB, userID, now, zone)
	if errors.Is(err, store.ErrDailyCheckinDisabled) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	balance, err := store.GetCreditBalance(ctx, d.DB, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"checkin": claim, "awarded": awarded, "credits": creditsSnapshot(d, balance)})
}
