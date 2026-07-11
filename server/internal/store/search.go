package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"aurelia/server/internal/envcfg"
)

// searchSnippetRadius is the ~rune padding on each side of a content-search
// snippet window; defaults to 64 when AURELIA_STORE_SEARCH_SNIPPET_RADIUS is unset.
var searchSnippetRadius = envcfg.Int("AURELIA_STORE_SEARCH_SNIPPET_RADIUS", 64)

// searchTextFromBlocks projects a message's blocks JSON down to the plain words
// the user typed or the assistant replied — i.e. only `text` blocks. Thinking,
// tool calls, citations, artifacts and image/document data are excluded. This is
// what gets stored in messages.search_text at write time, so content search scans
// a small column instead of LOWER()-ing the full blocks JSON (which also holds
// large extended-reasoning and tool text).
func searchTextFromBlocks(blocks json.RawMessage) string {
	if len(blocks) == 0 {
		return ""
	}
	var arr []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if json.Unmarshal(blocks, &arr) != nil {
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, b := range arr {
		if b.Kind == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// backfillSearchText populates messages.search_text for every existing row,
// keyset-paged over the primary key so each row is visited once. One-time
// migration; best-effort (errors ignored — a missed row just isn't searchable
// until next written).
func backfillSearchText(db *sql.DB) {
	batch := envcfg.Int("AURELIA_STORE_BATCH", 500)
	last := ""
	for {
		rows, err := db.Query(`SELECT id, blocks FROM messages WHERE id > ? ORDER BY id LIMIT ?`, last, batch)
		if err != nil {
			return
		}
		type row struct{ id, blocks string }
		buf := make([]row, 0, batch)
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.blocks); err != nil {
				continue
			}
			buf = append(buf, r)
		}
		rows.Close()
		if len(buf) == 0 {
			return
		}
		for _, r := range buf {
			st := searchTextFromBlocks(json.RawMessage(r.blocks))
			_, _ = db.Exec(`UPDATE messages SET search_text=? WHERE id=?`, st, r.id)
			last = r.id
		}
		if len(buf) < batch {
			return
		}
	}
}

// SearchHit is one result row — either a conversation whose title matched
// (MessageID empty) or a specific message whose content matched (MessageID +
// Snippet set so the client can jump to it).
type SearchHit struct {
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	MessageID      string `json:"message_id,omitempty"`
	Role           string `json:"role,omitempty"`
	Snippet        string `json:"snippet,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// likeEscape escapes the LIKE wildcards so a user query of "100%" or "a_b" is
// matched literally (paired with `ESCAPE '\'` in the query).
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// SearchConversations runs the user-scoped content + title search. Both queries
// are bounded to the caller's own non-archived, non-inline conversations, so the
// scan only ever touches one user's rows. Content search matches the small
// messages.search_text column (visible text only) rather than the full blocks
// JSON, so the scan corpus excludes thinking/tool/image data.
//
// workspaceID switches the scope (§workspaces): ” searches the caller's
// PERSONAL space only; set, it searches that workspace's shared conversations
// (all members' rows — the HANDLER must have validated membership).
func SearchConversations(ctx context.Context, db *sql.DB, userID, workspaceID, query string, titleLimit, msgLimit int) (titles []SearchHit, messages []SearchHit, err error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return []SearchHit{}, []SearchHit{}, nil
	}
	like := "%" + likeEscape(q) + "%"
	scope := `user_id=? AND COALESCE(workspace_id,'')=''`
	scopeArg := userID
	if workspaceID != "" {
		scope = `workspace_id=?`
		scopeArg = workspaceID
	}

	// Title hits.
	titles = []SearchHit{}
	tRows, err := db.QueryContext(ctx,
		`SELECT id, title, updated_at FROM conversations
		 WHERE `+scope+` AND archived=0 AND inline_source_conv='' AND LOWER(title) LIKE ? ESCAPE '\'
		 ORDER BY updated_at DESC LIMIT ?`, scopeArg, like, titleLimit)
	if err != nil {
		return nil, nil, err
	}
	for tRows.Next() {
		var h SearchHit
		if err := tRows.Scan(&h.ConversationID, &h.Title, &h.UpdatedAt); err != nil {
			tRows.Close()
			return nil, nil, err
		}
		titles = append(titles, h)
	}
	tRows.Close()
	if err := tRows.Err(); err != nil {
		return nil, nil, err
	}

	// Content hits — scan the small search_text column.
	messages = []SearchHit{}
	mScope := `c.user_id=? AND COALESCE(c.workspace_id,'')=''`
	if workspaceID != "" {
		mScope = `c.workspace_id=?`
	}
	mRows, err := db.QueryContext(ctx,
		`SELECT m.conversation_id, c.title, m.id, m.role, m.search_text, m.created_at, c.updated_at
		 FROM messages m JOIN conversations c ON m.conversation_id=c.id
		 WHERE `+mScope+` AND c.archived=0 AND c.inline_source_conv='' AND m.search_text<>'' AND LOWER(m.search_text) LIKE ? ESCAPE '\'
		 ORDER BY c.updated_at DESC, m.created_at DESC LIMIT ?`, scopeArg, like, msgLimit)
	if err != nil {
		return nil, nil, err
	}
	defer mRows.Close()
	for mRows.Next() {
		var h SearchHit
		var text string
		if err := mRows.Scan(&h.ConversationID, &h.Title, &h.MessageID, &h.Role, &text, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, nil, err
		}
		h.Snippet = buildSnippet(text, q, searchSnippetRadius)
		messages = append(messages, h)
	}
	if err := mRows.Err(); err != nil {
		return nil, nil, err
	}
	return titles, messages, nil
}

// buildSnippet returns a window of `text` centred on the first case-insensitive
// occurrence of `lowerQuery`, padded by ~radius runes on each side with ellipses.
// Rune-based so multibyte (CJK) text never splits mid-character.
func buildSnippet(text, lowerQuery string, radius int) string {
	flat := strings.Join(strings.Fields(text), " ") // collapse whitespace/newlines
	idx := strings.Index(strings.ToLower(flat), lowerQuery)
	runes := []rune(flat)
	if idx < 0 {
		if len(runes) <= radius*2 {
			return flat
		}
		return string(runes[:radius*2]) + "…"
	}
	// Convert byte offset to rune offset.
	matchRune := len([]rune(flat[:idx]))
	start := matchRune - radius
	if start < 0 {
		start = 0
	}
	end := matchRune + len([]rune(lowerQuery)) + radius
	if end > len(runes) {
		end = len(runes)
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out = out + "…"
	}
	return out
}
