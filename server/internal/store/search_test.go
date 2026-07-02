package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestSearchConversations covers the content + title search end to end: a word
// that appears only inside a message body (not the title) is found, scoped to the
// owning user, with a snippet built around the match — and a word that lives only
// in a non-text (thinking) block is NOT matched.
func TestSearchConversations(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u2','x@y.z','h','user')`)

	if _, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "Compiler notes"}); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	// Another user's conversation with the same keyword — must NOT leak.
	if _, err := CreateConversation(ctx, db, Conversation{ID: "c2", UserID: "u2", Title: "secret quokka stuff"}); err != nil {
		t.Fatalf("create conv2: %v", err)
	}

	blocks := func(s string) json.RawMessage {
		b, _ := json.Marshal([]map[string]string{{"kind": "text", "text": s}})
		return b
	}
	if _, err := CreateMessage(ctx, db, Message{ID: "m1", ConversationID: "c1", Role: "user", Blocks: blocks("tell me about the quokka please")}); err != nil {
		t.Fatalf("create msg: %v", err)
	}
	if _, err := CreateMessage(ctx, db, Message{ID: "m2", ConversationID: "c1", Role: "assistant", Blocks: blocks("a quokka is a small marsupial")}); err != nil {
		t.Fatalf("create msg2: %v", err)
	}

	// Content match: "quokka" is in messages but not in c1's title.
	titles, msgs, err := SearchConversations(ctx, db, "u1", "", "quokka", 8, 40)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(titles) != 0 {
		t.Fatalf("expected 0 title hits for u1, got %d", len(titles))
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 message hits, got %d", len(msgs))
	}
	for _, h := range msgs {
		if h.ConversationID != "c1" {
			t.Fatalf("hit leaked from another conversation: %+v", h)
		}
		if !strings.Contains(strings.ToLower(h.Snippet), "quokka") {
			t.Fatalf("snippet missing the match: %q", h.Snippet)
		}
	}

	// Title match path.
	titles2, _, err := SearchConversations(ctx, db, "u1", "", "compiler", 8, 40)
	if err != nil {
		t.Fatalf("search title: %v", err)
	}
	if len(titles2) != 1 || titles2[0].ConversationID != "c1" {
		t.Fatalf("expected 1 title hit c1, got %+v", titles2)
	}

	// Cross-user isolation: u1 must not see u2's "quokka" title conversation.
	if _, msgsX, _ := SearchConversations(ctx, db, "u1", "", "secret", 8, 40); len(msgsX) != 0 {
		t.Fatalf("u1 saw u2 content: %+v", msgsX)
	}

	// Only visible text is searched — a word that appears solely in a thinking
	// block (or any non-text block) must NOT populate search_text, so it never
	// produces a hit.
	if _, err := CreateMessage(ctx, db, Message{ID: "m3", ConversationID: "c1", Role: "assistant", Blocks: json.RawMessage(`[{"kind":"thinking","text":"internal numbat reasoning"}]`)}); err != nil {
		t.Fatalf("create m3: %v", err)
	}
	if _, hidden, _ := SearchConversations(ctx, db, "u1", "", "numbat", 8, 40); len(hidden) != 0 {
		t.Fatalf("a thinking-only word leaked into results: %+v", hidden)
	}
	// The same word in a real text block IS found.
	if _, err := CreateMessage(ctx, db, Message{ID: "m4", ConversationID: "c1", Role: "user", Blocks: blocks("what is a numbat")}); err != nil {
		t.Fatalf("create m4: %v", err)
	}
	if _, shown, _ := SearchConversations(ctx, db, "u1", "", "numbat", 8, 40); len(shown) != 1 {
		t.Fatalf("expected 1 visible-text hit, got %d", len(shown))
	}

	// Backfill: a legacy row whose search_text was never set becomes searchable.
	exec(t, db, `INSERT INTO messages(id,conversation_id,role,blocks,search_text) VALUES('m5','c1','user','[{"kind":"text","text":"legacy wombat fact"}]','')`)
	if _, pre, _ := SearchConversations(ctx, db, "u1", "", "wombat", 8, 40); len(pre) != 0 {
		t.Fatalf("expected no hit before backfill, got %d", len(pre))
	}
	backfillSearchText(db)
	if _, post, _ := SearchConversations(ctx, db, "u1", "", "wombat", 8, 40); len(post) != 1 {
		t.Fatalf("expected 1 hit after backfill, got %d", len(post))
	}
}
