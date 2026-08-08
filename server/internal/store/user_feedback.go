package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const UserFeedbackDescriptionMaxRunes = 2000

// UserFeedback is a product issue report submitted from a conversation row.
// Screenshot is deliberately omitted from JSON list responses; administrators
// fetch it through a separate authenticated endpoint only when opening detail.
type UserFeedback struct {
	ID                string `json:"id"`
	UserID            string `json:"user_id"`
	UserEmail         string `json:"user_email,omitempty"`
	UserName          string `json:"user_name,omitempty"`
	MessageID         string `json:"message_id"`
	ConversationID    string `json:"conversation_id"`
	ConversationTitle string `json:"conversation_title"`
	Description       string `json:"description"`
	PagePath          string `json:"page_path"`
	UserAgent         string `json:"user_agent"`
	ViewportWidth     int    `json:"viewport_width"`
	ViewportHeight    int    `json:"viewport_height"`
	Screenshot        []byte `json:"-"`
	ScreenshotMIME    string `json:"screenshot_mime"`
	ScreenshotWidth   int    `json:"screenshot_width"`
	ScreenshotHeight  int    `json:"screenshot_height"`
	ScreenshotSize    int64  `json:"screenshot_size"`
	HasScreenshot     bool   `json:"has_screenshot"`
	CreatedAt         int64  `json:"created_at"`
}

type AdminUserFeedbackPage struct {
	Items  []UserFeedback `json:"items"`
	Total  int64          `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// CreateUserFeedback persists a fully validated report.
func CreateUserFeedback(ctx context.Context, db *sql.DB, feedback UserFeedback) (*UserFeedback, error) {
	feedback.Description = strings.TrimSpace(feedback.Description)
	if feedback.Description == "" {
		return nil, fmt.Errorf("feedback description is required")
	}
	if len([]rune(feedback.Description)) > UserFeedbackDescriptionMaxRunes {
		return nil, fmt.Errorf("feedback description must be at most %d characters", UserFeedbackDescriptionMaxRunes)
	}
	if feedback.ID == "" {
		feedback.ID = genID("uf")
	}
	if feedback.CreatedAt == 0 {
		feedback.CreatedAt = time.Now().Unix()
	}
	_, err := db.ExecContext(ctx, `INSERT INTO user_feedback(
		id,user_id,message_id,conversation_id,conversation_title,description,page_path,user_agent,
		viewport_width,viewport_height,screenshot,screenshot_mime,screenshot_width,screenshot_height,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		feedback.ID, feedback.UserID, nullableString(feedback.MessageID), nullableString(feedback.ConversationID),
		feedback.ConversationTitle, feedback.Description, feedback.PagePath, feedback.UserAgent,
		feedback.ViewportWidth, feedback.ViewportHeight, nullableBytes(feedback.Screenshot), feedback.ScreenshotMIME,
		feedback.ScreenshotWidth, feedback.ScreenshotHeight, feedback.CreatedAt)
	if err != nil {
		return nil, err
	}
	feedback.ScreenshotSize = int64(len(feedback.Screenshot))
	feedback.HasScreenshot = len(feedback.Screenshot) > 0
	return &feedback, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// ListUserFeedbackAdmin returns support-triage metadata without screenshot
// bytes. Search is intentionally broad across reporter, description and thread.
func ListUserFeedbackAdmin(ctx context.Context, db *sql.DB, search string, limit, offset int) (*AdminUserFeedbackPage, error) {
	search = strings.ToLower(strings.TrimSpace(search))
	where := ""
	args := []any{}
	if search != "" {
		where = ` WHERE LOWER(COALESCE(u.email,'')) LIKE ?
			OR LOWER(COALESCE(u.name,'')) LIKE ?
			OR LOWER(f.description) LIKE ?
			OR LOWER(f.conversation_title) LIKE ?
			OR LOWER(f.page_path) LIKE ?
			OR LOWER(f.id) LIKE ?`
		needle := "%" + search + "%"
		args = append(args, needle, needle, needle, needle, needle, needle)
	}
	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_feedback f
		LEFT JOIN users u ON u.id=f.user_id`+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := db.QueryContext(ctx, `SELECT
		f.id,f.user_id,COALESCE(u.email,''),COALESCE(u.name,''),COALESCE(f.message_id,''),
		COALESCE(f.conversation_id,''),f.conversation_title,f.description,f.page_path,f.user_agent,
		f.viewport_width,f.viewport_height,f.screenshot_mime,f.screenshot_width,f.screenshot_height,
		CASE WHEN f.screenshot IS NULL THEN 0 ELSE length(f.screenshot) END,f.created_at
		FROM user_feedback f LEFT JOIN users u ON u.id=f.user_id`+where+`
		ORDER BY f.created_at DESC,f.id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UserFeedback, 0)
	for rows.Next() {
		var item UserFeedback
		if err := rows.Scan(
			&item.ID, &item.UserID, &item.UserEmail, &item.UserName, &item.MessageID,
			&item.ConversationID, &item.ConversationTitle, &item.Description, &item.PagePath, &item.UserAgent,
			&item.ViewportWidth, &item.ViewportHeight, &item.ScreenshotMIME, &item.ScreenshotWidth,
			&item.ScreenshotHeight, &item.ScreenshotSize, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		item.HasScreenshot = item.ScreenshotSize > 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &AdminUserFeedbackPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// GetUserFeedbackScreenshot returns the validated raster bytes for one report.
func GetUserFeedbackScreenshot(ctx context.Context, db *sql.DB, id string) ([]byte, string, error) {
	var data []byte
	var mime string
	err := db.QueryRowContext(ctx, `SELECT screenshot,screenshot_mime FROM user_feedback
		WHERE id=? AND screenshot IS NOT NULL`, id).Scan(&data, &mime)
	if err == sql.ErrNoRows {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", ErrNotFound
	}
	return data, mime, nil
}
