package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestAdminUsageRecords exercises the per-record usage list: ordering, the
// deleted-conversation flag, user/model/time filters, the count+cost summary,
// and both delete paths.
func TestAdminUsageRecords(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','alice@x.com','h','user')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u2','bob@y.com','h','user')`)
	if _, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "Hello"}); err != nil {
		t.Fatalf("conv: %v", err)
	}
	ins := `INSERT INTO usage_logs(user_id,conversation_id,model_id,purpose,input_tokens,output_tokens,cost,currency,created_at) VALUES(?,?,?,?,?,?,?,?,?)`
	exec(t, db, ins, "u1", "c1", "m1", "chat", 10, 20, 0.1, "USD", 1000)                                                              // existing conv
	exec(t, db, ins, "u1", "cGONE", "m1", "task.title", 5, 5, 0.2, "USD", 2000)                                                       // conv was deleted
	exec(t, db, `INSERT INTO usage_logs(user_id,model_id,purpose,cost,currency,created_at) VALUES('u2','m2','image',0.3,'USD',3000)`) // no conv

	all, err := AdminUsageRecords(ctx, db, UsageFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d records, want 3", len(all))
	}
	// Newest first (created_at DESC): image(3000), task.title(2000), chat(1000).
	if all[0].Purpose != "image" || all[2].Purpose != "chat" {
		t.Errorf("order wrong: %s … %s", all[0].Purpose, all[2].Purpose)
	}
	byPurpose := map[string]AdminUsageRecord{}
	for _, r := range all {
		byPurpose[r.Purpose] = r
	}
	if !byPurpose["task.title"].ConversationDeleted {
		t.Error("row referencing a missing conversation should be flagged deleted")
	}
	if byPurpose["chat"].ConversationDeleted {
		t.Error("row with an existing conversation must NOT be flagged deleted")
	}
	if byPurpose["image"].ConversationDeleted {
		t.Error("row with no conversation must NOT be flagged deleted")
	}
	if byPurpose["chat"].UserEmail != "alice@x.com" {
		t.Errorf("email join wrong: %q", byPurpose["chat"].UserEmail)
	}

	// Filter by user (email substring).
	if rows, _ := AdminUsageRecords(ctx, db, UsageFilter{UserQ: "alice"}, 50, 0); len(rows) != 2 {
		t.Errorf("user filter: got %d, want 2", len(rows))
	}
	// Filter by model.
	if rows, _ := AdminUsageRecords(ctx, db, UsageFilter{ModelID: "m2"}, 50, 0); len(rows) != 1 || rows[0].Purpose != "image" {
		t.Errorf("model filter wrong: %+v", rows)
	}
	// Filter by time window (since 2500 → only the 3000 row).
	if rows, _ := AdminUsageRecords(ctx, db, UsageFilter{Since: 2500}, 50, 0); len(rows) != 1 {
		t.Errorf("since filter: got %d, want 1", len(rows))
	}

	// Count + cost over all.
	n, cost, err := AdminUsageCount(ctx, db, UsageFilter{})
	if err != nil || n != 3 || cost < 0.59 || cost > 0.61 {
		t.Errorf("count/cost = %d / %.4f (err %v), want 3 / 0.6", n, cost, err)
	}

	// Delete a single record.
	chatID := byPurpose["chat"].ID
	if err := DeleteUsageRecord(ctx, db, chatID); err != nil {
		t.Fatalf("delete one: %v", err)
	}
	if err := DeleteUsageRecord(ctx, db, chatID); err != ErrNotFound {
		t.Errorf("re-delete should be ErrNotFound, got %v", err)
	}
	// Bulk delete by filter (model m1 → the remaining task.title row).
	del, err := DeleteUsageByFilter(ctx, db, UsageFilter{ModelID: "m1"})
	if err != nil || del != 1 {
		t.Errorf("bulk delete = %d (err %v), want 1", del, err)
	}
	if rows, _ := AdminUsageRecords(ctx, db, UsageFilter{}, 50, 0); len(rows) != 1 || rows[0].Purpose != "image" {
		t.Errorf("after deletes, want only the image row; got %+v", rows)
	}
}
