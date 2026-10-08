package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type SiteNotification struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	Enabled     bool   `json:"enabled"`
	Version     string `json:"version"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	Unread      bool   `json:"unread"`
	ShouldPopup bool   `json:"should_popup"`
}

type SiteNotificationPage struct {
	Notifications []SiteNotification `json:"notifications"`
	Total         int                `json:"total"`
}

func ListSiteNotifications(ctx context.Context, db *sql.DB, userID string, admin bool, search string, limit, offset int) (SiteNotificationPage, error) {
	page := SiteNotificationPage{Notifications: []SiteNotification{}}
	where := "1=1"
	args := []any{userID}
	if !admin {
		where += " AND n.enabled=1"
	}
	if search = strings.TrimSpace(search); search != "" {
		where += " AND lower(n.title) LIKE ? ESCAPE '\\'"
		search = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(strings.ToLower(search))
		args = append(args, "%"+search+"%")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM site_notifications n WHERE "+where, args[1:]...).Scan(&page.Total); err != nil {
		return page, err
	}
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, `SELECT n.id,n.title,n.enabled,n.version,n.created_at,n.updated_at,
 CASE WHEN COALESCE(s.read_version,'')<>n.version THEN 1 ELSE 0 END,
 CASE WHEN COALESCE(s.dismissed_version,'')<>n.version THEN 1 ELSE 0 END
 FROM site_notifications n LEFT JOIN site_notification_states s ON s.notification_id=n.id AND s.user_id=?
 WHERE `+where+` ORDER BY n.updated_at DESC,n.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var n SiteNotification
		if err := rows.Scan(&n.ID, &n.Title, &n.Enabled, &n.Version, &n.CreatedAt, &n.UpdatedAt, &n.Unread, &n.ShouldPopup); err != nil {
			return page, err
		}
		page.Notifications = append(page.Notifications, n)
	}
	return page, rows.Err()
}

func GetSiteNotification(ctx context.Context, db *sql.DB, id string, admin bool) (*SiteNotification, error) {
	var n SiteNotification
	query := `SELECT id,title,body,enabled,version,created_at,updated_at FROM site_notifications WHERE id=?`
	if !admin {
		query += " AND enabled=1"
	}
	err := db.QueryRowContext(ctx, query, id).Scan(&n.ID, &n.Title, &n.Body, &n.Enabled, &n.Version, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

func SaveSiteNotification(ctx context.Context, db *sql.DB, id, title, body string, enabled bool) (*SiteNotification, error) {
	now := time.Now().Unix()
	version := genID("nv")
	var err error
	if id == "" {
		id = genID("ntf")
		_, err = db.ExecContext(ctx, `INSERT INTO site_notifications(id,title,body,enabled,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, title, body, boolInt(enabled), version, now, now)
	} else {
		var result sql.Result
		result, err = db.ExecContext(ctx, `UPDATE site_notifications SET title=?,body=?,enabled=?,version=?,updated_at=? WHERE id=?`, title, body, boolInt(enabled), version, now, id)
		if err == nil {
			if count, _ := result.RowsAffected(); count == 0 {
				return nil, ErrNotFound
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return GetSiteNotification(ctx, db, id, true)
}

func DeleteSiteNotification(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM site_notifications WHERE id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

// Compare the displayed version inside the write, so a concurrent edit cannot
// accidentally mark the new content as read or suppress its popup.
func ReadSiteNotification(ctx context.Context, db *sql.DB, userID, id, version string, read, dismiss bool) error {
	dismissed := ""
	if dismiss {
		dismissed = version
	}
	readVersion := ""
	if read {
		readVersion = version
	}
	result, err := db.ExecContext(ctx, `INSERT INTO site_notification_states(user_id,notification_id,read_version,dismissed_version)
 SELECT ?,id,?,? FROM site_notifications WHERE id=? AND version=? AND enabled=1
 ON CONFLICT(user_id,notification_id) DO UPDATE SET
 read_version=CASE WHEN excluded.read_version<>'' THEN excluded.read_version ELSE site_notification_states.read_version END,
 dismissed_version=CASE WHEN excluded.dismissed_version<>'' THEN excluded.dismissed_version ELSE site_notification_states.dismissed_version END`, userID, readVersion, dismissed, id, version)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}
