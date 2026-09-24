package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AiPPTDeck is one AI PPT generation owned by a user (§ AI PPT / Docmee API
// mode). It tracks the upstream task/ppt ids and the mirrored .pptx so the deck
// survives Docmee's short-lived download links.
type AiPPTDeck struct {
	ID           string  `json:"id"`
	UserID       string  `json:"user_id"`
	TaskID       string  `json:"task_id"`
	PptID        string  `json:"ppt_id"`
	Subject      string  `json:"subject"`
	SourceType   int     `json:"source_type"`
	Status       string  `json:"status"`
	Outline      string  `json:"outline"`
	TemplateID   string  `json:"template_id"`
	TemplateName string  `json:"template_name"`
	CoverURL     string  `json:"cover_url"`
	FileID       string  `json:"file_id"`
	Error        string  `json:"error"`
	Credits      float64 `json:"credits"`
	OptionsJSON  string  `json:"options_json"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
}

// AI PPT deck lifecycle. A deck is created before any content exists (draft),
// gains an outline, then either produces a mirrored file (ready) or records why
// it failed.
const (
	AiPPTDeckDraft        = "draft"
	AiPPTDeckOutlineReady = "outline_ready"
	AiPPTDeckGenerating   = "generating"
	AiPPTDeckReady        = "ready"
	AiPPTDeckFailed       = "failed"
)

const aiPPTDeckColumns = `id,user_id,task_id,ppt_id,subject,source_type,status,outline,
	template_id,template_name,cover_url,file_id,error,credits,options_json,created_at,updated_at`

func scanAiPPTDeck(row interface {
	Scan(...any) error
}) (*AiPPTDeck, error) {
	var d AiPPTDeck
	if err := row.Scan(&d.ID, &d.UserID, &d.TaskID, &d.PptID, &d.Subject, &d.SourceType, &d.Status,
		&d.Outline, &d.TemplateID, &d.TemplateName, &d.CoverURL, &d.FileID, &d.Error, &d.Credits,
		&d.OptionsJSON, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateAiPPTDeck inserts a new deck row, assigning an id when the caller left it
// blank.
func CreateAiPPTDeck(ctx context.Context, db *sql.DB, deck AiPPTDeck) (*AiPPTDeck, error) {
	if deck.ID == "" {
		deck.ID = genID("ppt")
	}
	now := time.Now().Unix()
	if deck.Status == "" {
		deck.Status = AiPPTDeckDraft
	}
	if deck.SourceType == 0 {
		deck.SourceType = 1
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO aippt_decks(`+aiPPTDeckColumns+`)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		deck.ID, deck.UserID, deck.TaskID, deck.PptID, deck.Subject, deck.SourceType, deck.Status,
		deck.Outline, deck.TemplateID, deck.TemplateName, deck.CoverURL, deck.FileID, deck.Error,
		deck.Credits, deck.OptionsJSON, now, now); err != nil {
		return nil, err
	}
	deck.CreatedAt, deck.UpdatedAt = now, now
	return &deck, nil
}

// GetAiPPTDeck loads one deck owned by userID. ErrNotFound covers both "missing"
// and "someone else's" so a guessed id cannot confirm existence.
func GetAiPPTDeck(ctx context.Context, db *sql.DB, id, userID string) (*AiPPTDeck, error) {
	deck, err := scanAiPPTDeck(db.QueryRowContext(ctx,
		`SELECT `+aiPPTDeckColumns+` FROM aippt_decks WHERE id=? AND user_id=?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return deck, nil
}

// ListAiPPTDecks returns the user's decks, newest first.
func ListAiPPTDecks(ctx context.Context, db *sql.DB, userID string, limit, offset int) ([]AiPPTDeck, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.QueryContext(ctx,
		`SELECT `+aiPPTDeckColumns+` FROM aippt_decks WHERE user_id=?
		  ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	decks := make([]AiPPTDeck, 0, limit)
	for rows.Next() {
		deck, err := scanAiPPTDeck(rows)
		if err != nil {
			return nil, err
		}
		decks = append(decks, *deck)
	}
	return decks, rows.Err()
}

// CountAiPPTDecks counts the user's decks so the list page can paginate.
func CountAiPPTDecks(ctx context.Context, db *sql.DB, userID string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM aippt_decks WHERE user_id=?`, userID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// UpdateAiPPTDeck writes back every mutable column of a deck the caller already
// owns. Ownership is part of the WHERE clause, so a stale row can never be
// written across users.
func UpdateAiPPTDeck(ctx context.Context, db *sql.DB, deck AiPPTDeck) error {
	res, err := db.ExecContext(ctx,
		`UPDATE aippt_decks SET task_id=?, ppt_id=?, subject=?, source_type=?, status=?, outline=?,
		        template_id=?, template_name=?, cover_url=?, file_id=?, error=?, credits=?,
		        options_json=?, updated_at=?
		  WHERE id=? AND user_id=?`,
		deck.TaskID, deck.PptID, deck.Subject, deck.SourceType, deck.Status, deck.Outline,
		deck.TemplateID, deck.TemplateName, deck.CoverURL, deck.FileID, deck.Error, deck.Credits,
		deck.OptionsJSON, time.Now().Unix(), deck.ID, deck.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAiPPTDeck removes one of the caller's decks.
func DeleteAiPPTDeck(ctx context.Context, db *sql.DB, id, userID string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM aippt_decks WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
