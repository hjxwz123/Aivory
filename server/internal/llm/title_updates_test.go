package llm

import (
	"context"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestPersistGeneratedTitleNotifiesAfterCommit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "title-update.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u@example.test','h','user')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','New conversation')`); err != nil {
		t.Fatalf("conversation: %v", err)
	}

	var notifiedUser, notifiedConversation string
	o := &Orchestrator{
		db: db,
		onConversationUpdated: func(userID, conversationID string) {
			notifiedUser = userID
			notifiedConversation = conversationID
		},
	}
	if ok := o.persistGeneratedTitle(context.Background(), "c1", "u1", "Database connection pooling"); !ok {
		t.Fatal("title update should succeed")
	}

	conv, err := store.GetConversation(context.Background(), db, "c1", "u1")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.Title != "Database connection pooling" {
		t.Fatalf("title = %q", conv.Title)
	}
	if notifiedUser != "u1" || notifiedConversation != "c1" {
		t.Fatalf("notification = (%q, %q), want (u1, c1)", notifiedUser, notifiedConversation)
	}
}

func TestPersistGeneratedTitleDoesNotNotifyOnFailedCommit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "missing-title-update.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	notified := false
	o := &Orchestrator{
		db: db,
		onConversationUpdated: func(_, _ string) {
			notified = true
		},
	}
	if ok := o.persistGeneratedTitle(context.Background(), "missing", "u1", "Never committed"); ok {
		t.Fatal("missing conversation must fail to persist")
	}
	if notified {
		t.Fatal("failed title update must not notify clients")
	}
}
