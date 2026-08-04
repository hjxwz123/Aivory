package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"testing"
)

func openMessageFeedbackTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "message-feedback.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, context.Background()
}

type feedbackSeed struct {
	channelID string
	modelID   string
	convID    string
	question1 string
	answer1   string
	question2 string
	answer2   string
}

func seedMessageFeedbackTest(t *testing.T, ctx context.Context, db *sql.DB) feedbackSeed {
	t.Helper()
	for _, user := range []struct{ id, email string }{{"u1", "one@example.test"}, {"u2", "two@example.test"}} {
		exec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES(?,?,?,'h','user','active')`, user.id, user.email, user.id)
	}
	primaryChannel, err := CreateChannel(ctx, db, "Configured feedback channel", "openai", "chat", "https://primary.example.test", "primary-secret")
	if err != nil {
		t.Fatalf("create configured channel: %v", err)
	}
	servedChannel, err := CreateChannel(ctx, db, "Fallback feedback channel", "openai", "chat", "https://fallback.example.test", "fallback-secret")
	if err != nil {
		t.Fatalf("create fallback channel: %v", err)
	}
	model, err := CreateModel(ctx, db, Model{
		ChannelID: primaryChannel.ID, FallbackChannelID: servedChannel.ID,
		RequestID: "quality-model", Label: "Quality Model",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	workspace, err := CreateWorkspace(ctx, db, "u1", "Feedback workspace")
	if err != nil {
		t.Fatalf("create feedback workspace: %v", err)
	}
	if err := JoinWorkspace(ctx, db, workspace.ID, "u2"); err != nil {
		t.Fatalf("join feedback workspace: %v", err)
	}
	conv, err := CreateConversation(ctx, db, Conversation{
		ID: "conv_feedback", UserID: "u1", WorkspaceID: workspace.ID,
		Title: "Feedback thread", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	blocks := func(values ...map[string]string) json.RawMessage {
		raw, _ := json.Marshal(values)
		return raw
	}
	question1, err := CreateMessage(ctx, db, Message{
		ID: "question_1", ConversationID: conv.ID, Role: "user",
		Blocks:      blocks(map[string]string{"kind": "text", "text": "What is two plus two?"}),
		Attachments: json.RawMessage(`[{"id":"file_1","filename":"notes.pdf"}]`),
	})
	if err != nil {
		t.Fatalf("create question 1: %v", err)
	}
	answer1, err := CreateMessage(ctx, db, Message{
		ID: "answer_1", ConversationID: conv.ID, ParentID: question1.ID, Role: "assistant",
		Provider: "openai", ModelID: model.ID, ModelLabel: model.Label,
		Blocks: blocks(
			map[string]string{"kind": "tool_call", "tool_name": "calculator"},
			map[string]string{"kind": "text", "text": "Two plus two is five."},
		),
		Citations:   json.RawMessage(`[{"id":"source_1","title":"Quality source","source":"kb"}]`),
		InputTokens: 12, OutputTokens: 7, CacheReadTokens: 2, Credits: 0.25, Cost: 0.01, GenMs: 320,
	})
	if err != nil {
		t.Fatalf("create answer 1: %v", err)
	}
	exec(t, db, `UPDATE messages SET gen_ms=320, credits=0.25 WHERE id=?`, answer1.ID)
	question2, err := CreateMessage(ctx, db, Message{
		ID: "question_2", ConversationID: conv.ID, ParentID: answer1.ID, Role: "user",
		Blocks: blocks(map[string]string{"kind": "text", "text": "Try again."}),
	})
	if err != nil {
		t.Fatalf("create question 2: %v", err)
	}
	answer2, err := CreateMessage(ctx, db, Message{
		ID: "answer_2", ConversationID: conv.ID, ParentID: question2.ID, Role: "assistant",
		Provider: "openai", ModelID: model.ID, ModelLabel: model.Label,
		Blocks:      blocks(map[string]string{"kind": "text", "text": "Two plus two is four."}),
		InputTokens: 8, OutputTokens: 6, Credits: 0.1, Cost: 0.005, GenMs: 180,
	})
	if err != nil {
		t.Fatalf("create answer 2: %v", err)
	}
	exec(t, db, `UPDATE messages SET gen_ms=180, credits=0.1 WHERE id=?`, answer2.ID)
	if err := LogUsage(ctx, db, UsageLog{
		UserID:         "u1",
		ConversationID: conv.ID,
		MessageID:      answer1.ID,
		ModelID:        model.ID,
		Purpose:        "chat",
		ChannelID:      servedChannel.ID,
		Fallback:       true,
		Status:         "ok",
	}); err != nil {
		t.Fatalf("log answer 1 usage: %v", err)
	}
	return feedbackSeed{servedChannel.ID, model.ID, conv.ID, question1.ID, answer1.ID, question2.ID, answer2.ID}
}

func TestMessageFeedbackUpsertPerUserAndClear(t *testing.T) {
	db, ctx := openMessageFeedbackTestDB(t)
	seed := seedMessageFeedbackTest(t, ctx, db)

	channelID, err := MessageFeedbackChannelID(ctx, db, seed.answer1, seed.modelID)
	if err != nil || channelID != seed.channelID {
		t.Fatalf("resolved channel = %q, err=%v; want %q", channelID, err, seed.channelID)
	}
	first, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: seed.answer1, ConversationID: seed.convID, UserID: "u1", ModelID: seed.modelID, ChannelID: channelID,
		Rating: MessageFeedbackDislike, Reasons: []string{"incorrect_fact", "citation_issue"}, Comment: "The result is wrong.",
	})
	if err != nil {
		t.Fatalf("create feedback: %v", err)
	}
	if first.ID == "" || first.Rating != MessageFeedbackDislike || len(first.Reasons) != 2 || first.Comment != "The result is wrong." {
		t.Fatalf("created feedback = %+v", first)
	}

	updated, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: seed.answer1, ConversationID: seed.convID, UserID: "u1", ModelID: seed.modelID,
		Rating: MessageFeedbackLike,
	})
	if err != nil {
		t.Fatalf("like must clear reasons/comment: %v", err)
	}
	if updated.ID != first.ID || updated.Rating != MessageFeedbackLike || len(updated.Reasons) != 0 || updated.Comment != "" {
		t.Fatalf("updated feedback = %+v", updated)
	}
	if _, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: seed.answer1, ConversationID: seed.convID, UserID: "u2", ModelID: seed.modelID,
		Rating: MessageFeedbackDislike, Reasons: []string{"poor_format"},
	}); err != nil {
		t.Fatalf("second user's feedback: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_feedback WHERE message_id=?`, seed.answer1).Scan(&count); err != nil || count != 2 {
		t.Fatalf("feedback row count=%d err=%v, want 2", count, err)
	}

	if cleared, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: seed.answer1, ConversationID: seed.convID, UserID: "u1", Rating: "",
	}); err != nil || cleared != nil {
		t.Fatalf("clear returned %+v, err=%v", cleared, err)
	}
	rows, err := ListMessageFeedbackForUser(ctx, db, "u2", []string{seed.answer1, seed.answer2, seed.answer1})
	if err != nil || len(rows) != 1 || rows[seed.answer1].Rating != MessageFeedbackDislike {
		t.Fatalf("u2 batch feedback=%+v err=%v", rows, err)
	}
	if _, err := GetMessageFeedbackForUser(ctx, db, seed.answer1, "u1"); err != ErrNotFound {
		t.Fatalf("cleared u1 feedback err=%v, want ErrNotFound", err)
	}
	var legacy string
	if err := db.QueryRowContext(ctx, `SELECT feedback FROM messages WHERE id=?`, seed.answer1).Scan(&legacy); err != nil || legacy != "" {
		t.Fatalf("legacy mirror=%q err=%v, want empty", legacy, err)
	}

	exec(t, db, `DELETE FROM messages WHERE id=?`, seed.answer1)
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_feedback WHERE message_id=?`, seed.answer1).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade feedback count=%d err=%v, want 0", count, err)
	}
}

func TestListMessageFeedbackForUserChunksLargeMessageSets(t *testing.T) {
	db, ctx := openMessageFeedbackTestDB(t)
	seed := seedMessageFeedbackTest(t, ctx, db)
	if _, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: seed.answer1, ConversationID: seed.convID, UserID: "u1", ModelID: seed.modelID,
		Rating: MessageFeedbackDislike, Reasons: []string{"incorrect_fact"},
	}); err != nil {
		t.Fatalf("set feedback: %v", err)
	}

	// Exceed SQLite's common 32,766-variable ceiling. The real message id sits
	// beyond the first batch so this also verifies that later batches are merged.
	ids := make([]string, 33_050)
	for i := range ids {
		ids[i] = fmt.Sprintf("missing_%05d", i)
	}
	ids[17_123] = seed.answer1
	feedback, err := ListMessageFeedbackForUser(ctx, db, "u1", ids)
	if err != nil {
		t.Fatalf("list chunked feedback: %v", err)
	}
	if len(feedback) != 1 || feedback[seed.answer1].Rating != MessageFeedbackDislike {
		t.Fatalf("chunked feedback=%+v", feedback)
	}
}

func TestMessageFeedbackReasonValidation(t *testing.T) {
	if _, err := NormalizeMessageFeedbackReasons([]string{"incorrect_fact", "incorrect_fact"}); err == nil {
		t.Fatal("duplicate reason accepted")
	}
	tooMany := make([]string, 11)
	for i := range tooMany {
		tooMany[i] = "other"
	}
	if _, err := NormalizeMessageFeedbackReasons(tooMany); err == nil {
		t.Fatal("more than ten reasons accepted")
	}
	if _, err := NormalizeMessageFeedbackReasons([]string{"unknown"}); err == nil {
		t.Fatal("unknown reason accepted")
	}
	db, ctx := openMessageFeedbackTestDB(t)
	seed := seedMessageFeedbackTest(t, ctx, db)
	if _, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: seed.answer1, ConversationID: seed.convID, UserID: "u1",
		Rating: MessageFeedbackLike, Reasons: []string{"unknown"},
	}); err == nil {
		t.Fatal("invalid reason on like accepted")
	}
}

func TestMessageFeedbackClearedWhenQuestionOrResponseIsEdited(t *testing.T) {
	db, ctx := openMessageFeedbackTestDB(t)
	seed := seedMessageFeedbackTest(t, ctx, db)
	sibling, err := CreateMessage(ctx, db, Message{
		ID: "answer_1_sibling", ConversationID: seed.convID, ParentID: seed.question1, Role: "assistant",
		Provider: "openai", ModelID: seed.modelID,
		Blocks: json.RawMessage(`[{"kind":"text","text":"A regenerated response."}]`),
	})
	if err != nil {
		t.Fatalf("create sibling response: %v", err)
	}
	set := func(messageID, userID string) {
		t.Helper()
		if _, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
			MessageID: messageID, ConversationID: seed.convID, UserID: userID, ModelID: seed.modelID,
			Rating: MessageFeedbackDislike, Reasons: []string{"incorrect_fact"},
		}); err != nil {
			t.Fatalf("set feedback for %s on %s: %v", userID, messageID, err)
		}
	}
	feedbackCount := func(stage, messageID string) int {
		t.Helper()
		var rows int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_feedback WHERE message_id=?`, messageID).Scan(&rows); err != nil {
			t.Fatalf("%s count: %v", stage, err)
		}
		return rows
	}
	assertCleared := func(stage, messageID string) {
		t.Helper()
		rows := feedbackCount(stage, messageID)
		var legacy string
		if err := db.QueryRowContext(ctx, `SELECT feedback FROM messages WHERE id=?`, messageID).Scan(&legacy); err != nil {
			t.Fatalf("%s legacy: %v", stage, err)
		}
		if rows != 0 || legacy != "" {
			t.Fatalf("%s rows=%d legacy=%q, want cleared", stage, rows, legacy)
		}
	}

	set(seed.answer1, "u1")
	set(seed.answer1, "u2")
	set(sibling.ID, "u1")
	if err := UpdateMessageContent(ctx, db, seed.answer1, json.RawMessage(`[{"kind":"text","text":"Edited response"}]`)); err != nil {
		t.Fatalf("edit response: %v", err)
	}
	assertCleared("assistant edit", seed.answer1)
	if got := feedbackCount("assistant edit sibling", sibling.ID); got != 1 {
		t.Fatalf("assistant edit cleared sibling feedback: got %d rows, want 1", got)
	}

	set(seed.answer1, "u1")
	set(seed.answer1, "u2")
	set(seed.answer2, "u1")
	if err := UpdateMessageContent(ctx, db, seed.question1, json.RawMessage(`[{"kind":"text","text":"Edited question"}]`)); err != nil {
		t.Fatalf("edit question: %v", err)
	}
	assertCleared("user edit answer", seed.answer1)
	assertCleared("user edit regenerated sibling", sibling.ID)
	if got := feedbackCount("user edit unrelated answer", seed.answer2); got != 1 {
		t.Fatalf("user edit cleared unrelated answer feedback: got %d rows, want 1", got)
	}
}

func TestAdminMessageFeedbackReportAggregatesAndFilters(t *testing.T) {
	db, ctx := openMessageFeedbackTestDB(t)
	seed := seedMessageFeedbackTest(t, ctx, db)
	set := func(user, message, rating string, reasons []string, comment string) {
		t.Helper()
		if _, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
			MessageID: message, ConversationID: seed.convID, UserID: user, ModelID: seed.modelID, ChannelID: seed.channelID,
			Rating: rating, Reasons: reasons, Comment: comment,
		}); err != nil {
			t.Fatalf("set %s %s feedback: %v", user, message, err)
		}
	}
	set("u1", seed.answer1, MessageFeedbackDislike, []string{"incorrect_fact", "citation_issue"}, "Wrong arithmetic")
	set("u2", seed.answer1, MessageFeedbackLike, nil, "")
	set("u1", seed.answer2, MessageFeedbackLike, nil, "")
	exec(t, db, `UPDATE models SET label='Quality Model Renamed' WHERE id=?`, seed.modelID)

	report, err := AdminMessageFeedbackReportData(ctx, db, AdminMessageFeedbackFilter{
		Rating: MessageFeedbackDislike,
		Reason: "incorrect_fact",
	}, 20, 0)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if report.Summary.Total != 3 || report.Summary.Likes != 2 || report.Summary.Dislikes != 1 {
		t.Fatalf("summary = %+v, want unfiltered 3/2/1", report.Summary)
	}
	if math.Abs(report.Summary.PositiveRate-2.0/3.0) > 1e-9 || report.Summary.AssistantMessages != 2 || report.Summary.Coverage != 1 {
		t.Fatalf("summary rates = %+v", report.Summary)
	}
	if report.Total != 1 || len(report.Items) != 1 {
		t.Fatalf("filtered total/items=%d/%d, want 1/1", report.Total, len(report.Items))
	}
	item := report.Items[0]
	if item.QuestionText != "What is two plus two?" || item.ResponseText != "Two plus two is five." || item.ChannelID != seed.channelID || item.ModelLabel != "Quality Model" {
		t.Fatalf("item content/metadata = %+v", item)
	}
	if !item.HasTools || !item.HasFiles || !item.HasRAG || item.GenMs != 320 || item.InputTokens != 12 || item.OutputTokens != 7 {
		t.Fatalf("item diagnostics = %+v", item)
	}
	if len(item.ToolNames) != 1 || item.ToolNames[0] != "calculator" || len(item.FileNames) != 1 || item.FileNames[0] != "notes.pdf" || len(item.CitationTitles) != 1 || item.CitationTitles[0] != "Quality source" {
		t.Fatalf("item bounded metadata = %+v", item)
	}
	if len(report.ByModel) != 1 || report.ByModel[0].ModelLabel != "Quality Model Renamed" || report.ByModel[0].Total != 3 || report.ByModel[0].TopReason != "incorrect_fact" || report.ByModel[0].SampleSufficient {
		t.Fatalf("by_model = %+v", report.ByModel)
	}

	empty, err := AdminMessageFeedbackReportData(ctx, db, AdminMessageFeedbackFilter{ModelID: "missing"}, 20, 0)
	if err != nil || empty.Total != 0 || empty.Summary.Total != 0 || empty.Summary.AssistantMessages != 0 || len(empty.Items) != 0 {
		t.Fatalf("missing model report=%+v err=%v", empty, err)
	}
}

func TestMessageFeedbackLegacyBackfill(t *testing.T) {
	db, ctx := openMessageFeedbackTestDB(t)
	seed := seedMessageFeedbackTest(t, ctx, db)
	exec(t, db, `UPDATE messages SET feedback='like' WHERE id=?`, seed.answer2)
	exec(t, db, `DELETE FROM settings WHERE key='message_feedback_backfill_v1'`)
	if err := Migrate(db); err != nil {
		t.Fatalf("rerun migrate: %v", err)
	}
	feedback, err := GetMessageFeedbackForUser(ctx, db, seed.answer2, "u1")
	if err != nil || feedback.Rating != MessageFeedbackLike || feedback.ModelID != seed.modelID {
		t.Fatalf("legacy feedback=%+v err=%v", feedback, err)
	}
}
