package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	LoginMethodPassword     = "password"
	LoginMethodPassword2FA  = "password_2fa"
	LoginMethodOAuth        = "oauth"
	LoginMethodOAuth2FA     = "oauth_2fa"
	loginHistoryDefaultPage = 50
	loginHistoryMaxPage     = 200
)

// LoginHistory is one immutable successful-login audit event. It is separate
// from refresh_tokens because session rotation and logout must not erase it.
type LoginHistory struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	LoginAt   int64  `json:"login_at"`
	IP        string `json:"ip"`
	Location  string `json:"location"`
	UserAgent string `json:"user_agent"`
	Method    string `json:"method"`
}

// RecordLoginHistory persists a successful sign-in. Callers should invoke it
// only after a real session has been minted, never during refresh rotation.
func RecordLoginHistory(ctx context.Context, db *sql.DB, userID, method string, meta SessionMeta) (*LoginHistory, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("login history user id required")
	}
	switch method {
	case LoginMethodPassword, LoginMethodPassword2FA, LoginMethodOAuth, LoginMethodOAuth2FA:
	default:
		return nil, errors.New("invalid login method")
	}
	row := &LoginHistory{
		ID:        genID("lh"),
		UserID:    userID,
		LoginAt:   time.Now().Unix(),
		IP:        truncateLoginHistoryText(strings.TrimSpace(meta.IP), 128),
		Location:  truncateLoginHistoryText(strings.TrimSpace(meta.Location), 256),
		UserAgent: truncateLoginHistoryText(strings.TrimSpace(meta.UserAgent), 1024),
		Method:    method,
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO login_histories(id,user_id,login_at,ip,location,user_agent,method) VALUES(?,?,?,?,?,?,?)`,
		row.ID, row.UserID, row.LoginAt, row.IP, row.Location, row.UserAgent, row.Method,
	)
	if err != nil {
		return nil, err
	}
	return row, nil
}

func truncateLoginHistoryText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

// ListLoginHistoriesForUser returns newest-first successful-login events.
func ListLoginHistoriesForUser(ctx context.Context, db *sql.DB, userID string, limit, offset int) ([]LoginHistory, error) {
	if limit <= 0 {
		limit = loginHistoryDefaultPage
	}
	if limit > loginHistoryMaxPage {
		limit = loginHistoryMaxPage
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id,user_id,login_at,ip,location,user_agent,method
		 FROM login_histories WHERE user_id=? ORDER BY login_at DESC,id DESC LIMIT ? OFFSET ?`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]LoginHistory, 0)
	for rows.Next() {
		var row LoginHistory
		if err := rows.Scan(&row.ID, &row.UserID, &row.LoginAt, &row.IP, &row.Location, &row.UserAgent, &row.Method); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func CountLoginHistoriesForUser(ctx context.Context, db *sql.DB, userID string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM login_histories WHERE user_id=?`, userID).Scan(&count)
	return count, err
}
