package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveConversationAppendParent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "append-parent.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	for _, id := range []string{"c1", "c2", "empty"} {
		exec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES(?, 'u1', 'T')`, id)
	}

	create := func(id, convID, parentID, role string, createdAt int64) {
		t.Helper()
		if _, err := CreateMessage(ctx, db, Message{
			ID: id, ConversationID: convID, ParentID: parentID, Role: role, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("create message %s: %v", id, err)
		}
	}
	create("old-root", "c1", "", "user", 10)
	create("old-answer", "c1", "old-root", "assistant", 20)
	create("old-leaf", "c1", "old-answer", "user", 30)
	create("new-root", "c1", "", "user", 40)
	create("new-answer-old", "c1", "new-root", "assistant", 50)
	create("new-answer-latest", "c1", "new-root", "assistant", 60)
	create("other-conversation-leaf", "c2", "", "assistant", 70)

	t.Run("valid leaf remains unchanged", func(t *testing.T) {
		got, repaired, err := ResolveConversationAppendParent(ctx, db, "c1", "old-leaf")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != "old-leaf" || repaired {
			t.Fatalf("got parent=%q repaired=%v, want old-leaf/false", got, repaired)
		}
	})

	t.Run("dangling leaf recovers deepest surviving branch", func(t *testing.T) {
		got, repaired, err := ResolveConversationAppendParent(ctx, db, "c1", "missing")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != "new-answer-latest" || !repaired {
			t.Fatalf("got parent=%q repaired=%v, want new-answer-latest/true", got, repaired)
		}
	})

	t.Run("cross conversation leaf is rejected", func(t *testing.T) {
		got, repaired, err := ResolveConversationAppendParent(ctx, db, "c1", "other-conversation-leaf")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != "new-answer-latest" || !repaired {
			t.Fatalf("got parent=%q repaired=%v, want new-answer-latest/true", got, repaired)
		}
	})

	t.Run("empty conversation stays root", func(t *testing.T) {
		got, repaired, err := ResolveConversationAppendParent(ctx, db, "empty", "")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != "" || repaired {
			t.Fatalf("got parent=%q repaired=%v, want empty/false", got, repaired)
		}

		got, repaired, err = ResolveConversationAppendParent(ctx, db, "empty", "missing")
		if err != nil {
			t.Fatalf("resolve dangling empty: %v", err)
		}
		if got != "" || !repaired {
			t.Fatalf("dangling empty got parent=%q repaired=%v, want empty/true", got, repaired)
		}
	})
}

func TestLatestAssistantInSubtreeRejectsInvalidStart(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "subtree-start.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','One')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c2','u1','Two')`)
	if _, err := CreateMessage(ctx, db, Message{ID: "c1-leaf", ConversationID: "c1", Role: "assistant"}); err != nil {
		t.Fatalf("create c1 message: %v", err)
	}
	if _, err := CreateMessage(ctx, db, Message{ID: "c2-leaf", ConversationID: "c2", Role: "assistant"}); err != nil {
		t.Fatalf("create c2 message: %v", err)
	}

	for _, start := range []string{"missing", "c2-leaf"} {
		t.Run(start, func(t *testing.T) {
			got, err := LatestAssistantInSubtree(ctx, db, "c1", start)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
			if got != "" {
				t.Fatalf("target = %q, want empty", got)
			}
		})
	}
}
