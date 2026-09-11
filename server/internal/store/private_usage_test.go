package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPrivateUsageRedactsDiagnosticsAndPreservesAnonymousTitle(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "private-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash) VALUES('private-user','private@example.test','hash')`)
	ctx := context.Background()
	if err := LogUsageAnalytics(ctx, db, UsageLog{UserID: "private-user", MessageID: "private_test", ConversationID: "accidental-conversation", ModelID: "model", Purpose: "chat", Status: "error", Error: "provider echoed sensitive prompt", RequestBody: "private prompt and base64", RequestHeaders: "private metadata", RequestURL: "https://example.test/secret", RequestMethod: "POST"}); err != nil {
		t.Fatal(err)
	}
	rows, err := AdminUsageRecords(ctx, db, UsageFilter{Purpose: "chat"}, 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d error=%v", len(rows), err)
	}
	row := rows[0]
	if row.ConversationTitle != "匿名对话" || row.ConversationID != "" || row.ConversationDeleted || row.Error != "provider_request_failed" || row.RequestBody != "" || row.RequestHeaders != "" || row.RequestURL != "" || row.RequestMethod != "" {
		t.Fatalf("unsafe private log: %+v", row)
	}
}
