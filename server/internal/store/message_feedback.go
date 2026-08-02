package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MessageFeedbackLike            = "like"
	MessageFeedbackDislike         = "dislike"
	MessageFeedbackCommentMaxRunes = 500
	messageFeedbackHydrationBatch  = 500
	adminMessageFeedbackModelLimit = 200
	messageFeedbackBackfillKey     = "message_feedback_backfill_v1"
)

var messageFeedbackReasons = []string{
	"incorrect_fact",
	"not_answered",
	"instruction_ignored",
	"outdated",
	"citation_issue",
	"tool_file_issue",
	"incomplete",
	"poor_format",
	"unsafe",
	"other",
}

var messageFeedbackReasonSet = func() map[string]bool {
	out := make(map[string]bool, len(messageFeedbackReasons))
	for _, reason := range messageFeedbackReasons {
		out[reason] = true
	}
	return out
}()

// BackfillLegacyMessageFeedback attributes the former message-level feedback
// value to the conversation owner. It is safe to call repeatedly: existing
// per-user rows win, and the migration marker is updated in the same transaction
// as the inserted rows when ex is a transaction.
func BackfillLegacyMessageFeedback(ctx context.Context, ex RowExecer) (int64, error) {
	result, err := ex.ExecContext(ctx, `INSERT INTO message_feedback(
		id, message_id, conversation_id, user_id, workspace_id, model_id, channel_id,
		rating, reasons, comment, created_at, updated_at
	)
	SELECT 'mfb_legacy_' || m.id, m.id, m.conversation_id, c.user_id,
	       COALESCE(c.workspace_id,''), COALESCE(m.model_id,''),
	       COALESCE((SELECT us.channel_id FROM usage_stats us
	                 WHERE us.message_id=m.id AND us.purpose='chat' AND us.channel_id<>''
	                 ORDER BY us.source_log_id DESC LIMIT 1), ''),
	       m.feedback, '[]', '', m.created_at, m.created_at
	FROM messages m
	JOIN conversations c ON c.id=m.conversation_id
	WHERE m.role='assistant' AND m.feedback IN ('like','dislike')
	ON CONFLICT(message_id, user_id) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("backfill message feedback: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count backfilled message feedback: %w", err)
	}
	if _, err := ex.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, '1')
		ON CONFLICT(key) DO UPDATE SET value='1'`, messageFeedbackBackfillKey); err != nil {
		return 0, fmt.Errorf("mark message feedback backfill: %w", err)
	}
	return inserted, nil
}

// IsMessageFeedbackReason reports whether reason belongs to the stable API
// taxonomy. Keeping the allow-list in store lets handlers and tests share it.
func IsMessageFeedbackReason(reason string) bool {
	return messageFeedbackReasonSet[reason]
}

// NormalizeMessageFeedbackReasons validates and trims a unique, bounded reason
// list without changing its order.
func NormalizeMessageFeedbackReasons(reasons []string) ([]string, error) {
	if len(reasons) > len(messageFeedbackReasons) {
		return nil, fmt.Errorf("feedback reasons must contain at most %d values", len(messageFeedbackReasons))
	}
	out := make([]string, 0, len(reasons))
	seen := make(map[string]bool, len(reasons))
	for _, raw := range reasons {
		reason := strings.TrimSpace(raw)
		if !IsMessageFeedbackReason(reason) {
			return nil, fmt.Errorf("invalid feedback reason %q", reason)
		}
		if seen[reason] {
			return nil, fmt.Errorf("duplicate feedback reason %q", reason)
		}
		seen[reason] = true
		out = append(out, reason)
	}
	return out, nil
}

// MessageFeedback is one user's evaluation of one assistant message.
type MessageFeedback struct {
	ID             string   `json:"id"`
	MessageID      string   `json:"message_id"`
	ConversationID string   `json:"conversation_id"`
	UserID         string   `json:"user_id"`
	WorkspaceID    string   `json:"workspace_id"`
	ModelID        string   `json:"model_id"`
	ChannelID      string   `json:"channel_id"`
	Rating         string   `json:"rating"`
	Reasons        []string `json:"reasons"`
	Comment        string   `json:"comment"`
	CreatedAt      int64    `json:"created_at"`
	UpdatedAt      int64    `json:"updated_at"`
}

// SetMessageFeedbackForUser upserts one user's rating. An empty rating deletes
// that user's row. messages.feedback is updated in the same transaction as a
// compatibility mirror; user-facing reads overwrite it from this table.
func SetMessageFeedbackForUser(ctx context.Context, db *sql.DB, feedback MessageFeedback) (*MessageFeedback, error) {
	feedback.Rating = strings.TrimSpace(feedback.Rating)
	feedback.Comment = strings.TrimSpace(feedback.Comment)
	if feedback.MessageID == "" || feedback.ConversationID == "" || feedback.UserID == "" {
		return nil, errors.New("message, conversation, and user are required")
	}
	if feedback.Rating != "" && feedback.Rating != MessageFeedbackLike && feedback.Rating != MessageFeedbackDislike {
		return nil, errors.New("feedback must be 'like', 'dislike', or empty")
	}
	if utf8.RuneCountInString(feedback.Comment) > MessageFeedbackCommentMaxRunes {
		return nil, fmt.Errorf("feedback comment exceeds %d characters", MessageFeedbackCommentMaxRunes)
	}
	reasons, err := NormalizeMessageFeedbackReasons(feedback.Reasons)
	if err != nil {
		return nil, err
	}
	feedback.Reasons = reasons
	if feedback.Rating != MessageFeedbackDislike {
		feedback.Reasons = []string{}
		feedback.Comment = ""
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if feedback.Rating == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_feedback WHERE message_id=? AND user_id=?`, feedback.MessageID, feedback.UserID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET feedback='' WHERE id=?`, feedback.MessageID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	if feedback.ID == "" {
		feedback.ID = genID("mfb")
	}
	now := time.Now().Unix()
	reasonsJSON, _ := json.Marshal(feedback.Reasons)
	_, err = tx.ExecContext(ctx, `INSERT INTO message_feedback(
		id, message_id, conversation_id, user_id, workspace_id, model_id, channel_id,
		rating, reasons, comment, created_at, updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(message_id, user_id) DO UPDATE SET
		conversation_id=excluded.conversation_id,
		workspace_id=excluded.workspace_id,
		model_id=excluded.model_id,
		channel_id=excluded.channel_id,
		rating=excluded.rating,
		reasons=excluded.reasons,
		comment=excluded.comment,
		updated_at=excluded.updated_at`,
		feedback.ID, feedback.MessageID, feedback.ConversationID, feedback.UserID,
		feedback.WorkspaceID, feedback.ModelID, feedback.ChannelID, feedback.Rating,
		string(reasonsJSON), feedback.Comment, now, now)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET feedback=? WHERE id=?`, feedback.Rating, feedback.MessageID); err != nil {
		return nil, err
	}
	stored, err := getMessageFeedback(ctx, tx, feedback.MessageID, feedback.UserID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stored, nil
}

type feedbackRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getMessageFeedback(ctx context.Context, q feedbackRowQuerier, messageID, userID string) (*MessageFeedback, error) {
	row := q.QueryRowContext(ctx, `SELECT id, message_id, conversation_id, user_id,
		workspace_id, model_id, channel_id, rating, reasons, comment, created_at, updated_at
		FROM message_feedback WHERE message_id=? AND user_id=?`, messageID, userID)
	feedback, err := scanMessageFeedback(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return feedback, err
}

// GetMessageFeedbackForUser returns one user's current rating for a message.
func GetMessageFeedbackForUser(ctx context.Context, db *sql.DB, messageID, userID string) (*MessageFeedback, error) {
	return getMessageFeedback(ctx, db, messageID, userID)
}

func scanMessageFeedback(s scanner) (*MessageFeedback, error) {
	var feedback MessageFeedback
	var reasons string
	if err := s.Scan(&feedback.ID, &feedback.MessageID, &feedback.ConversationID, &feedback.UserID,
		&feedback.WorkspaceID, &feedback.ModelID, &feedback.ChannelID, &feedback.Rating,
		&reasons, &feedback.Comment, &feedback.CreatedAt, &feedback.UpdatedAt); err != nil {
		return nil, err
	}
	feedback.Reasons = []string{}
	_ = json.Unmarshal([]byte(reasons), &feedback.Reasons)
	if feedback.Reasons == nil {
		feedback.Reasons = []string{}
	}
	return &feedback, nil
}

// ListMessageFeedbackForUser batch-loads ratings for response hydration. It is
// deliberately independent of the conversation path cache because feedback is
// per-user and can change without changing message content.
func ListMessageFeedbackForUser(ctx context.Context, db *sql.DB, userID string, messageIDs []string) (map[string]MessageFeedback, error) {
	out := make(map[string]MessageFeedback)
	if userID == "" || len(messageIDs) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(messageIDs))
	seen := make(map[string]bool, len(messageIDs))
	for _, id := range messageIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return out, nil
	}
	for start := 0; start < len(ids); start += messageFeedbackHydrationBatch {
		end := start + messageFeedbackHydrationBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, 1, len(batch)+1)
		args[0] = userID
		for i, id := range batch {
			placeholders[i] = "?"
			args = append(args, id)
		}
		rows, err := db.QueryContext(ctx, `SELECT id, message_id, conversation_id, user_id,
			workspace_id, model_id, channel_id, rating, reasons, comment, created_at, updated_at
			FROM message_feedback WHERE user_id=? AND message_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			feedback, err := scanMessageFeedback(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[feedback.MessageID] = *feedback
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MessageFeedbackChannelID resolves the actual channel that served the most
// recent successful chat request, falling back to the model's configured
// channel for legacy messages without a durable usage fact.
func MessageFeedbackChannelID(ctx context.Context, db *sql.DB, messageID, modelID string) (string, error) {
	var channelID string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(
			(SELECT us.channel_id FROM usage_stats us
			 WHERE us.message_id=? AND us.purpose='chat' AND us.channel_id<>''
			 ORDER BY us.source_log_id DESC LIMIT 1),
		(SELECT mo.channel_id FROM models mo WHERE mo.id=?), '')`, messageID, modelID).Scan(&channelID)
	return channelID, err
}

// AdminMessageFeedbackFilter uses Since+ModelID for the generated-response
// cohort behind summary, by-model rows, and items. Rating+Reason intentionally
// narrow only the diagnostic items and top-level paginated total, so selecting
// "dislike" does not make the overall positive-rate KPI read as zero.
type AdminMessageFeedbackFilter struct {
	Since   int64
	Rating  string
	ModelID string
	Reason  string
}

func (filter AdminMessageFeedbackFilter) aggregateWhere() (string, []any) {
	conditions := []string{"reply.role='assistant'"}
	args := []any{}
	if filter.Since > 0 {
		conditions = append(conditions, "reply.created_at>=?")
		args = append(args, filter.Since)
	}
	if filter.ModelID != "" {
		conditions = append(conditions, "f.model_id=?")
		args = append(args, filter.ModelID)
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func (filter AdminMessageFeedbackFilter) itemWhere() (string, []any) {
	where, args := filter.aggregateWhere()
	conditions := []string{}
	if filter.Rating != "" {
		conditions = append(conditions, "f.rating=?")
		args = append(args, filter.Rating)
	}
	if filter.Reason != "" {
		conditions = append(conditions, "CAST(f.reasons AS TEXT) LIKE ?")
		args = append(args, `%"`+filter.Reason+`"%`)
	}
	if len(conditions) > 0 {
		where += " AND " + strings.Join(conditions, " AND ")
	}
	return where, args
}

type AdminMessageFeedbackSummary struct {
	Total             int     `json:"total"`
	Likes             int     `json:"likes"`
	Dislikes          int     `json:"dislikes"`
	PositiveRate      float64 `json:"positive_rate"`
	RatedMessages     int     `json:"rated_messages"`
	AssistantMessages int     `json:"assistant_messages"`
	Coverage          float64 `json:"coverage"`
}

type AdminMessageFeedbackByModel struct {
	ModelID          string  `json:"model_id"`
	ModelLabel       string  `json:"model_label"`
	Total            int     `json:"total"`
	Likes            int     `json:"likes"`
	Dislikes         int     `json:"dislikes"`
	PositiveRate     float64 `json:"positive_rate"`
	TopReason        string  `json:"top_reason"`
	SampleSufficient bool    `json:"sample_sufficient"`
}

type AdminMessageFeedbackItem struct {
	ID                  string   `json:"id"`
	MessageID           string   `json:"message_id"`
	ConversationID      string   `json:"conversation_id"`
	ConversationTitle   string   `json:"conversation_title"`
	ConversationOwnerID string   `json:"conversation_owner_id"`
	QuestionID          string   `json:"question_id"`
	QuestionText        string   `json:"question"`
	ResponseText        string   `json:"response"`
	UserID              string   `json:"user_id"`
	UserEmail           string   `json:"user_email"`
	UserName            string   `json:"user_name"`
	WorkspaceID         string   `json:"workspace_id"`
	WorkspaceName       string   `json:"workspace_name"`
	ModelID             string   `json:"model_id"`
	ModelLabel          string   `json:"model_label"`
	ChannelID           string   `json:"channel_id"`
	ChannelName         string   `json:"channel_name"`
	Provider            string   `json:"provider"`
	Rating              string   `json:"rating"`
	Reasons             []string `json:"reasons"`
	Comment             string   `json:"comment"`
	GenMs               int64    `json:"gen_ms"`
	InputTokens         int      `json:"input_tokens"`
	OutputTokens        int      `json:"output_tokens"`
	CacheReadTokens     int      `json:"cache_read_tokens"`
	CacheWriteTokens    int      `json:"cache_write_tokens"`
	Credits             float64  `json:"credits"`
	Cost                float64  `json:"cost"`
	Currency            string   `json:"currency"`
	HasTools            bool     `json:"has_tools"`
	HasFiles            bool     `json:"has_files"`
	HasRAG              bool     `json:"has_rag"`
	Fallback            bool     `json:"fallback"`
	ToolNames           []string `json:"tool_names"`
	FileNames           []string `json:"file_names"`
	CitationTitles      []string `json:"citation_titles"`
	Status              string   `json:"status"`
	Error               string   `json:"error,omitempty"`
	MessageCreatedAt    int64    `json:"message_created_at"`
	CreatedAt           int64    `json:"created_at"`
	UpdatedAt           int64    `json:"updated_at"`
}

type AdminMessageFeedbackReport struct {
	Summary AdminMessageFeedbackSummary   `json:"summary"`
	ByModel []AdminMessageFeedbackByModel `json:"by_model"`
	Items   []AdminMessageFeedbackItem    `json:"items"`
	Total   int                           `json:"total"`
	Limit   int                           `json:"limit"`
	Offset  int                           `json:"offset"`
}

// AdminMessageFeedbackReportData returns dashboard aggregates and the matching
// newest-first examples in one bounded response.
func AdminMessageFeedbackReportData(ctx context.Context, db *sql.DB, filter AdminMessageFeedbackFilter, limit, offset int) (AdminMessageFeedbackReport, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	report := AdminMessageFeedbackReport{ByModel: []AdminMessageFeedbackByModel{}, Items: []AdminMessageFeedbackItem{}, Limit: limit, Offset: offset}
	aggregateWhere, aggregateArgs := filter.aggregateWhere()
	itemWhere, itemFilterArgs := filter.itemWhere()

	err := db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN f.rating='like' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN f.rating='dislike' THEN 1 ELSE 0 END),0),
		COUNT(DISTINCT f.message_id)
		FROM message_feedback f JOIN messages reply ON reply.id=f.message_id`+aggregateWhere, aggregateArgs...).
		Scan(&report.Summary.Total, &report.Summary.Likes, &report.Summary.Dislikes, &report.Summary.RatedMessages)
	if err != nil {
		return report, err
	}
	if report.Summary.Total > 0 {
		report.Summary.PositiveRate = float64(report.Summary.Likes) / float64(report.Summary.Total)
	}
	assistantConditions := []string{"role='assistant'"}
	assistantArgs := []any{}
	if filter.Since > 0 {
		assistantConditions = append(assistantConditions, "created_at>=?")
		assistantArgs = append(assistantArgs, filter.Since)
	}
	if filter.ModelID != "" {
		assistantConditions = append(assistantConditions, "model_id=?")
		assistantArgs = append(assistantArgs, filter.ModelID)
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE `+strings.Join(assistantConditions, " AND "), assistantArgs...).Scan(&report.Summary.AssistantMessages)
	if err != nil {
		return report, err
	}
	if report.Summary.AssistantMessages > 0 {
		report.Summary.Coverage = float64(report.Summary.RatedMessages) / float64(report.Summary.AssistantMessages)
	}

	reasonSums := make([]string, 0, len(messageFeedbackReasons))
	for _, reason := range messageFeedbackReasons {
		reasonSums = append(reasonSums, `COALESCE(SUM(CASE WHEN CAST(f.reasons AS TEXT) LIKE '%"`+reason+`"%' THEN 1 ELSE 0 END),0)`)
	}
	modelQuery := `SELECT f.model_id,
			COALESCE(NULLIF(MAX(model.label),''), NULLIF(MAX(reply.model_label),''), f.model_id),
		COUNT(*),
		COALESCE(SUM(CASE WHEN f.rating='like' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN f.rating='dislike' THEN 1 ELSE 0 END),0), ` + strings.Join(reasonSums, ", ") + `
		FROM message_feedback f
		JOIN messages reply ON reply.id=f.message_id
		LEFT JOIN models model ON model.id=f.model_id` + aggregateWhere + `
		GROUP BY f.model_id
		ORDER BY COALESCE(SUM(CASE WHEN f.rating='dislike' THEN 1 ELSE 0 END),0) DESC, COUNT(*) DESC, f.model_id
		LIMIT ?`
	// Keep the combined dashboard response bounded even on installations that
	// retain feedback for a large, frequently-changing model catalog.
	modelArgs := append(append([]any{}, aggregateArgs...), adminMessageFeedbackModelLimit)
	rows, err := db.QueryContext(ctx, modelQuery, modelArgs...)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var model AdminMessageFeedbackByModel
		counts := make([]int, len(messageFeedbackReasons))
		scanArgs := []any{&model.ModelID, &model.ModelLabel, &model.Total, &model.Likes, &model.Dislikes}
		for i := range counts {
			scanArgs = append(scanArgs, &counts[i])
		}
		if err := rows.Scan(scanArgs...); err != nil {
			rows.Close()
			return report, err
		}
		if model.Total > 0 {
			model.PositiveRate = float64(model.Likes) / float64(model.Total)
		}
		model.SampleSufficient = model.Total >= 20
		maxCount := 0
		for i, count := range counts {
			if count > maxCount {
				maxCount = count
				model.TopReason = messageFeedbackReasons[i]
			}
		}
		report.ByModel = append(report.ByModel, model)
	}
	if err := rows.Close(); err != nil {
		return report, err
	}
	if err := rows.Err(); err != nil {
		return report, err
	}

	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_feedback f
		JOIN messages reply ON reply.id=f.message_id`+itemWhere, itemFilterArgs...).Scan(&report.Total); err != nil {
		return report, err
	}
	itemArgs := append(append([]any{}, itemFilterArgs...), limit, offset)
	itemRows, err := db.QueryContext(ctx, `SELECT
		f.id, f.message_id, f.conversation_id, COALESCE(conv.title,''), conv.user_id,
		COALESCE(question.id,''), COALESCE(question.search_text,''), COALESCE(reply.search_text,''),
		f.user_id, COALESCE(usr.email,''), COALESCE(usr.name,''),
		f.workspace_id, COALESCE(ws.name,''), f.model_id,
		COALESCE(NULLIF(reply.model_label,''), NULLIF(model.label,''), f.model_id),
		f.channel_id, COALESCE(channel.name,''), COALESCE(reply.provider,''),
		f.rating, f.reasons, f.comment,
		reply.gen_ms, reply.input_tokens, reply.output_tokens, reply.cache_read_tokens, reply.cache_write_tokens,
		reply.credits, reply.cost, reply.currency,
		COALESCE(question.attachments,'[]'), COALESCE(reply.attachments,'[]'), COALESCE(reply.blocks,'[]'), COALESCE(reply.citations,'[]'),
			CASE WHEN EXISTS(SELECT 1 FROM usage_stats us
			                 WHERE us.message_id=f.message_id AND us.purpose='chat'
			                   AND (us.fallback=1 OR us.ttft_fallback_model<>'')) THEN 1 ELSE 0 END,
		reply.status, COALESCE(reply.error,''), reply.created_at, f.created_at, f.updated_at
		FROM message_feedback f
		JOIN messages reply ON reply.id=f.message_id
		JOIN conversations conv ON conv.id=f.conversation_id
		LEFT JOIN messages question ON question.id=reply.parent_id AND question.role='user'
		LEFT JOIN users usr ON usr.id=f.user_id
		LEFT JOIN workspaces ws ON ws.id=f.workspace_id
		LEFT JOIN models model ON model.id=f.model_id
		LEFT JOIN channels channel ON channel.id=f.channel_id`+itemWhere+`
		ORDER BY f.updated_at DESC, f.id DESC LIMIT ? OFFSET ?`, itemArgs...)
	if err != nil {
		return report, err
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var item AdminMessageFeedbackItem
		var reasons, questionAttachments, responseAttachments, blocks, citations string
		var fallback int
		if err := itemRows.Scan(
			&item.ID, &item.MessageID, &item.ConversationID, &item.ConversationTitle, &item.ConversationOwnerID,
			&item.QuestionID, &item.QuestionText, &item.ResponseText,
			&item.UserID, &item.UserEmail, &item.UserName,
			&item.WorkspaceID, &item.WorkspaceName, &item.ModelID, &item.ModelLabel,
			&item.ChannelID, &item.ChannelName, &item.Provider,
			&item.Rating, &reasons, &item.Comment,
			&item.GenMs, &item.InputTokens, &item.OutputTokens, &item.CacheReadTokens, &item.CacheWriteTokens,
			&item.Credits, &item.Cost, &item.Currency,
			&questionAttachments, &responseAttachments, &blocks, &citations,
			&fallback, &item.Status, &item.Error, &item.MessageCreatedAt, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return report, err
		}
		item.Reasons = []string{}
		_ = json.Unmarshal([]byte(reasons), &item.Reasons)
		item.HasFiles = jsonArrayHasItems(questionAttachments) || jsonArrayHasItems(responseAttachments)
		item.HasTools = messageBlocksHaveTools(blocks)
		item.HasRAG = messageHasRAGCitation(citations)
		item.Fallback = fallback == 1
		item.ToolNames = feedbackToolNames(blocks)
		item.FileNames = appendUniqueFeedbackMetadata(feedbackAttachmentNames(questionAttachments), feedbackAttachmentNames(responseAttachments)...)
		item.CitationTitles = feedbackCitationTitles(citations)
		report.Items = append(report.Items, item)
	}
	return report, itemRows.Err()
}

func jsonArrayHasItems(raw string) bool {
	var values []json.RawMessage
	return json.Unmarshal([]byte(raw), &values) == nil && len(values) > 0
}

func messageBlocksHaveTools(raw string) bool {
	var blocks []struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal([]byte(raw), &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		if block.Kind == "tool_call" || block.Kind == "tool_output" {
			return true
		}
	}
	return false
}

func messageHasRAGCitation(raw string) bool {
	var citations []struct {
		Source string `json:"source"`
	}
	if json.Unmarshal([]byte(raw), &citations) != nil {
		return false
	}
	for _, citation := range citations {
		if citation.Source == "kb" || citation.Source == "rag" {
			return true
		}
	}
	return false
}

const feedbackMetadataLimit = 20

func feedbackToolNames(raw string) []string {
	var blocks []struct {
		Kind     string `json:"kind"`
		ToolName string `json:"tool_name"`
	}
	if json.Unmarshal([]byte(raw), &blocks) != nil {
		return []string{}
	}
	names := []string{}
	for _, block := range blocks {
		if block.Kind == "tool_call" || block.Kind == "tool_output" {
			names = appendUniqueFeedbackMetadata(names, block.ToolName)
		}
	}
	return names
}

func feedbackAttachmentNames(raw string) []string {
	var attachments []struct {
		Filename string `json:"filename"`
		Title    string `json:"title"`
		Name     string `json:"name"`
	}
	if json.Unmarshal([]byte(raw), &attachments) != nil {
		return []string{}
	}
	names := []string{}
	for _, attachment := range attachments {
		name := attachment.Filename
		if name == "" {
			name = attachment.Title
		}
		if name == "" {
			name = attachment.Name
		}
		names = appendUniqueFeedbackMetadata(names, name)
	}
	return names
}

func feedbackCitationTitles(raw string) []string {
	var citations []struct {
		Title string `json:"title"`
	}
	if json.Unmarshal([]byte(raw), &citations) != nil {
		return []string{}
	}
	titles := []string{}
	for _, citation := range citations {
		titles = appendUniqueFeedbackMetadata(titles, citation.Title)
	}
	return titles
}

func appendUniqueFeedbackMetadata(current []string, values ...string) []string {
	if len(current) >= feedbackMetadataLimit {
		return current
	}
	seen := make(map[string]bool, len(current)+len(values))
	for _, value := range current {
		seen[value] = true
	}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		current = append(current, value)
		if len(current) == feedbackMetadataLimit {
			break
		}
	}
	return current
}
