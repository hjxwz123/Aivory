package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aurelia/server/internal/store"
)

// listConversationsHandler returns the user's conversations with pagination.
// Query params: project_id, archived=only, limit (default 200, max 500), offset (default 0).
func listConversationsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	projectID := r.URL.Query().Get("project_id")
	// ?archived=only returns the archived chats (for the "Archived" view); the
	// default hides them.
	archivedFilter := "active"
	if r.URL.Query().Get("archived") == "only" {
		archivedFilter = "archived"
	}
	limit := 200
	if ls := r.URL.Query().Get("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}
	offset := 0
	if os := r.URL.Query().Get("offset"); os != "" {
		if n, err := strconv.Atoi(os); err == nil && n >= 0 {
			offset = n
		}
	}
	rows, err := store.ListConversations(r.Context(), d.DB, u.ID, projectID, archivedFilter, limit, offset)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	for i := range rows {
		stripServerConvFields(&rows[i])
	}
	writeJSON(w, 200, map[string]any{
		"conversations": rows,
		"limit":         limit,
		"offset":        offset,
		"has_more":      len(rows) == limit,
	})
}

// stripServerConvFields zeroes conversation fields that are server-internal and
// never read by the client. summary_blocks (the §4.7 compaction state) can be
// large and otherwise ships in every list row, wasting bandwidth and exposing
// summarised content. Mutates in place; the store layer keeps the real value.
func stripServerConvFields(c *store.Conversation) {
	c.SummaryBlocks = json.RawMessage("[]")
}

// searchHandler runs full-text search over the user's own conversation titles
// and message content (§ homepage search). Query param `q` (min 2 chars).
// Returns title hits + message hits (each with a snippet + message_id so the
// client can jump straight to the matching message).
func searchHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 {
		writeJSON(w, 200, map[string]any{"query": q, "titles": []store.SearchHit{}, "messages": []store.SearchHit{}})
		return
	}
	titles, messages, err := store.SearchConversations(r.Context(), d.DB, u.ID, q, 8, 40)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"query": q, "titles": titles, "messages": messages})
}

type createConversationReq struct {
	ModelID   string `json:"model_id"`
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
}

// createConversationHandler creates a fresh conversation.
func createConversationHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req createConversationReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if req.ModelID == "" {
		// Take default.
		if raw, err := store.GetSetting(d.DB, "default_model_id"); err == nil {
			_ = json.Unmarshal(raw, &req.ModelID)
		}
	}
	// Validate the project belongs to this user before attaching (don't trust
	// the client-supplied project_id).
	if req.ProjectID != "" {
		if _, err := store.GetProject(r.Context(), d.DB, req.ProjectID, u.ID); err != nil {
			writeError(w, 404, errors.New("project not found"))
			return
		}
	}
	conv, err := store.CreateConversation(r.Context(), d.DB, store.Conversation{
		UserID:    u.ID,
		ProjectID: req.ProjectID,
		Title:     strings.TrimSpace(req.Title),
		ModelID:   req.ModelID,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, conv)
}

// Import limits — bound the work a single import request can schedule.
const (
	importMaxConversations   = 1000
	importMaxMessagesPerConv = 10000
	importMaxContentBytes    = 200 * 1024
)

type importMessageReq struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Role     string `json:"role"`
	Content  string `json:"content"`
}

type importConversationReq struct {
	Title        string             `json:"title"`
	ModelID      string             `json:"model_id"`
	ActiveLeafID string             `json:"active_leaf_id"`
	Messages     []importMessageReq `json:"messages"`
}

// importConversationsHandler bulk-creates conversations + message trees from an
// external export (§ conversation import). It bypasses the orchestrator entirely
// — no model calls, no quota — and only stores chat history + titles. The client
// has already stripped images/files/usage/<details> blocks; the server just
// validates shape, caps sizes, and remaps the tree. Per-conversation failures are
// skipped (reported in `failed`) rather than aborting the whole import.
func importConversationsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req struct {
		Conversations []importConversationReq `json:"conversations"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if len(req.Conversations) == 0 {
		writeError(w, 400, errors.New("no conversations to import"))
		return
	}
	if len(req.Conversations) > importMaxConversations {
		writeError(w, 400, errors.New("too many conversations in one import"))
		return
	}
	// Default model so imported conversations can be continued.
	defaultModel := ""
	if raw, err := store.GetSetting(d.DB, "default_model_id"); err == nil {
		_ = json.Unmarshal(raw, &defaultModel)
	}
	ids := []string{}
	failed := 0
	for _, c := range req.Conversations {
		if len(c.Messages) == 0 || len(c.Messages) > importMaxMessagesPerConv {
			failed++
			continue
		}
		modelID := c.ModelID
		if modelID == "" {
			modelID = defaultModel
		}
		msgs := make([]store.ImportMessageInput, 0, len(c.Messages))
		for _, m := range c.Messages {
			content := m.Content
			if len(content) > importMaxContentBytes {
				content = strings.ToValidUTF8(content[:importMaxContentBytes], "")
			}
			msgs = append(msgs, store.ImportMessageInput{
				ClientID:       m.ID,
				ParentClientID: m.ParentID,
				Role:           m.Role,
				Content:        content,
			})
		}
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = "Imported chat"
		}
		convID, err := store.ImportConversation(r.Context(), d.DB, store.Conversation{
			UserID:  u.ID,
			Title:   title,
			ModelID: modelID,
		}, msgs, c.ActiveLeafID)
		if err != nil {
			failed++
			continue
		}
		ids = append(ids, convID)
	}
	writeJSON(w, 201, map[string]any{"imported": len(ids), "failed": failed, "conversation_ids": ids})
}

type createInlineThreadReq struct {
	MessageID string `json:"message_id"`
	Quote     string `json:"quote"`
}

// createInlineThreadHandler opens a sub-conversation anchored to a quoted
// excerpt of a message in the given source conversation (§ text-selection
// sub-conversations). It inherits the source's model and is hidden from the
// normal conversation list; the quote is injected as system context so the
// assistant stays scoped to the highlighted passage.
func createInlineThreadHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	srcID := pathParam(r, "id")
	src, err := store.GetConversation(r.Context(), d.DB, srcID, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var req createInlineThreadReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	quote := strings.TrimSpace(req.Quote)
	if quote == "" || req.MessageID == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	// Cap the quote so a runaway selection can't bloat the system prompt.
	if rs := []rune(quote); len(rs) > 4000 {
		quote = string(rs[:4000])
	}
	// The anchored message must belong to the source conversation.
	msg, err := store.GetMessage(r.Context(), d.DB, req.MessageID)
	if err != nil || msg.ConversationID != srcID {
		writeError(w, 404, errNotFound)
		return
	}
	title := quote
	if rs := []rune(title); len(rs) > 40 {
		title = strings.TrimSpace(string(rs[:40])) + "…"
	}
	conv, err := store.CreateConversation(r.Context(), d.DB, store.Conversation{
		UserID:           u.ID,
		ModelID:          src.ModelID,
		Provider:         src.Provider,
		Title:            title,
		InlineSourceConv: srcID,
		InlineParentID:   req.MessageID,
		InlineQuote:      quote,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, conv)
}

// listInlineThreadsHandler returns the sub-conversations anchored to a source
// conversation so the UI can render inline-thread markers on its messages.
func listInlineThreadsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	srcID := pathParam(r, "id")
	if _, err := store.GetConversation(r.Context(), d.DB, srcID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	rows, err := store.ListInlineThreads(r.Context(), d.DB, srcID, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// getConversationHandler reads one conversation + path messages.
func getConversationHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	conv, err := store.GetConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	msgs, _ := store.ListMessages(r.Context(), d.DB, conv.ID, conv.ActiveLeafID)
	// Optional reverse pagination over the active path: ?limit=N (&before=<id>)
	// returns the trailing window oldest-first. With NO limit the whole path is
	// returned and has_more=false — preserving the original (unpaginated) contract.
	before := r.URL.Query().Get("before")
	limit := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, perr := strconv.Atoi(l); perr == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	window, hasMore, nextBefore := paginatePath(msgs, before, limit)
	stripServerConvFields(conv)
	// Enrich with sibling indexes so the active-path load carries branch_count /
	// branch_index / siblings — without this the frontend never sees the
	// `< n/m >` branch picker on a fresh load or post-stream reconcile (§4.15).
	writeJSON(w, 200, map[string]any{
		"conversation": conv,
		"messages":     redactCost(enrichWithSiblings(d, r, window)),
		"has_more":     hasMore,
		"next_before":  nextBefore,
	})
}

// paginatePath returns the trailing window of an active path. When before!="" the
// path is first cut to everything strictly above that message id. When limit>0 the
// last `limit` messages are returned (oldest-first) with hasMore + the cursor
// (oldest returned id) for the next older page. limit<=0 returns the whole slice
// unchanged with hasMore=false — i.e. no pagination.
func paginatePath(msgs []store.Message, before string, limit int) (window []store.Message, hasMore bool, nextBefore string) {
	if before != "" {
		cut := -1
		for i, m := range msgs {
			if m.ID == before {
				cut = i
				break
			}
		}
		if cut < 0 {
			// Stale/foreign cursor (message deleted, branch switched, wrong path):
			// treat as exhausted rather than re-serving the latest window, which
			// would let the client loop re-requesting the same page.
			return []store.Message{}, false, ""
		}
		msgs = msgs[:cut]
	}
	if limit <= 0 {
		return msgs, false, ""
	}
	if len(msgs) > limit {
		hasMore = true
		msgs = msgs[len(msgs)-limit:]
	}
	if hasMore && len(msgs) > 0 {
		nextBefore = msgs[0].ID
	}
	return msgs, hasMore, nextBefore
}

// updateConversationHandler edits selected fields (title, project, archive…).
func updateConversationHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	var p store.ConversationPatch
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	// §C1: never let a user attach a KB they don't own — filter kb_ids to the
	// owned subset at write time (the orchestrator re-filters at read time too).
	if len(p.KBIDs) > 0 {
		var ids []string
		if json.Unmarshal(p.KBIDs, &ids) == nil {
			owned := store.OwnedKBIDs(r.Context(), d.DB, u.ID, ids)
			b, _ := json.Marshal(owned)
			p.KBIDs = b
		} else {
			p.KBIDs = json.RawMessage("[]")
		}
	}
	// Mirror the create path: a moved conversation must point at a project the
	// caller owns — don't trust a client-supplied project_id (an empty string
	// detaches, which is always allowed).
	if p.ProjectID != nil && *p.ProjectID != "" {
		if _, err := store.GetProject(r.Context(), d.DB, *p.ProjectID, u.ID); err != nil {
			writeError(w, 404, errors.New("project not found"))
			return
		}
	}
	conv, err := store.UpdateConversation(r.Context(), d.DB, id, u.ID, p)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	stripServerConvFields(conv)
	writeJSON(w, 200, conv)
}

// deleteConversationHandler removes a conversation.
func deleteConversationHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	children, err := store.DeleteConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	// Conversation uploads cascade-delete (documents.conversation_id ON DELETE
	// CASCADE); drop their vectors too — for the conversation and every inline
	// sub-conversation that was removed with it.
	for _, cid := range append([]string{id}, children...) {
		if err := d.RAG.OnConversationDeleted(r.Context(), cid); err != nil {
			d.Logger.Printf("rag: drop vectors for conversation %s: %v", cid, err)
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// listMessagesHandler returns either the active path or the full tree.
func listMessagesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if _, err := store.GetConversation(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "tree" {
		msgs, err := store.ListAllMessages(r.Context(), d.DB, id)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		// Enrich each message with sibling indexes so the frontend can render
		// branch pickers without a second roundtrip.
		writeJSON(w, 200, redactCost(enrichWithSiblings(d, r, msgs)))
		return
	}
	conv, _ := store.GetConversation(r.Context(), d.DB, id, u.ID)
	msgs, err := store.ListMessages(r.Context(), d.DB, id, conv.ActiveLeafID)
	if err != nil {
		writeError(w, 500, err)
		return
	}

	// §6.1 cursor reverse-pagination over the active path: ?before=<id>&limit=N
	// returns the trailing window oldest-first. Cursor metadata travels in
	// headers so the response stays a plain array (backward compatible).
	before := r.URL.Query().Get("before")
	limit := 30
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, perr := strconv.Atoi(l); perr == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	window, hasMore, nextBefore := paginatePath(msgs, before, limit)
	w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))
	if nextBefore != "" {
		w.Header().Set("X-Next-Before", nextBefore)
	}
	writeJSON(w, 200, redactCost(enrichWithSiblings(d, r, window)))
}

type enrichedMessage struct {
	store.Message
	BranchIndex int      `json:"branch_index"`
	BranchCount int      `json:"branch_count"`
	Siblings    []string `json:"siblings"`
}

func enrichWithSiblings(d Deps, r *http.Request, msgs []store.Message) []enrichedMessage {
	// Resolve all sibling lists in a single batch (one query per unique parent
	// slot) instead of issuing one query per message (N+1 pattern).
	siblingMap, _ := store.BatchSiblingsOf(r.Context(), d.DB, msgs)
	out := make([]enrichedMessage, 0, len(msgs))
	for _, m := range msgs {
		ids := siblingMap[m.ID]
		idx := 0
		for i, id := range ids {
			if id == m.ID {
				idx = i
				break
			}
		}
		out = append(out, enrichedMessage{
			Message:     m,
			BranchIndex: idx,
			BranchCount: len(ids),
			Siblings:    ids,
		})
	}
	return out
}

// redactCost zeroes the per-message cost/currency before a USER-facing response.
// Spend is admin-only (visible in /admin/usage); regular users never see it, and
// the API never returns it to them. Admin message-drilldown endpoints skip this.
func redactCost(ems []enrichedMessage) []enrichedMessage {
	for i := range ems {
		ems[i].Cost = 0
		ems[i].Currency = ""
		// `raw` is the provider-native exchange (tool I/O, retrieved RAG context,
		// provider plumbing) kept server-side for same-vendor replay — never meant
		// for end users. Strip it on every user-facing path; admin endpoints that
		// intentionally skip redaction still expose it.
		ems[i].Raw = nil
		ems[i].Attachments = backfillAttachmentURLs(ems[i].Attachments)
	}
	return ems
}

// backfillAttachmentURLs walks the attachments JSON blob and, for any item
// missing a `url`, inserts "/api/files/<id>". Older messages persisted before
// the upload endpoint started emitting URLs (or messages whose client never
// populated url) need this so the user-bubble image preview can render through
// the persistent download endpoint instead of a revoked blob: URL.
func backfillAttachmentURLs(raw json.RawMessage) json.RawMessage {
	if len(raw) < 2 {
		return raw
	}
	var atts []map[string]any
	if err := json.Unmarshal(raw, &atts); err != nil {
		return raw
	}
	changed := false
	for i := range atts {
		if url, _ := atts[i]["url"].(string); url == "" {
			if id, _ := atts[i]["id"].(string); id != "" {
				atts[i]["url"] = "/api/files/" + id
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(atts)
	if err != nil {
		return raw
	}
	return out
}

type setActiveLeafReq struct {
	LeafID string `json:"leaf_id"`
}

// setActiveLeafHandler updates conversations.active_leaf_id; the front-end
// passes the deepest descendant of the picked sibling so the UI renders the
// full branch.
func setActiveLeafHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	var req setActiveLeafReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if req.LeafID == "" {
		writeError(w, 400, errors.New("leaf_id required"))
		return
	}
	target, err := store.LatestAssistantInSubtree(r.Context(), d.DB, id, req.LeafID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	conv, err := store.UpdateConversation(r.Context(), d.DB, id, u.ID, store.ConversationPatch{ActiveLeafID: &target})
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	msgs, _ := store.ListMessages(r.Context(), d.DB, id, conv.ActiveLeafID)
	stripServerConvFields(conv)
	writeJSON(w, 200, map[string]any{
		"conversation": conv,
		"messages":     redactCost(enrichWithSiblings(d, r, msgs)),
	})
}

// forkConversationHandler copies the path ending at leaf_id into a brand new
// conversation, leaving the original intact. This implements §4.15's
// "fork to new conversation".
func forkConversationHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	conv, err := store.GetConversation(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	var body struct {
		LeafID string `json:"leaf_id"`
		Title  string `json:"title"`
	}
	_ = decodeJSON(r, &body)
	if body.LeafID == "" {
		body.LeafID = conv.ActiveLeafID
	}
	if body.LeafID == "" {
		writeError(w, 400, errors.New("leaf_id required"))
		return
	}
	path, err := store.ListMessages(r.Context(), d.DB, conv.ID, body.LeafID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = conv.Title + " (fork)"
	}
	newConv, err := store.CreateConversation(r.Context(), d.DB, store.Conversation{
		UserID:    u.ID,
		ProjectID: conv.ProjectID,
		Title:     title,
		Provider:  conv.Provider,
		ModelID:   conv.ModelID,
		KBIDs:     conv.KBIDs,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	parent := ""
	for _, m := range path {
		copied, err := store.CreateMessage(r.Context(), d.DB, store.Message{
			ConversationID: newConv.ID,
			ParentID:       parent,
			Role:           m.Role,
			Provider:       m.Provider,
			ModelID:        m.ModelID,
			Blocks:         m.Blocks,
			Raw:            m.Raw,
			StopReason:     m.StopReason,
			Attachments:    m.Attachments,
			Citations:      m.Citations,
			InputTokens:    m.InputTokens,
			OutputTokens:   m.OutputTokens,
			Cost:           m.Cost,
			Currency:       m.Currency,
			Status:         "complete",
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		parent = copied.ID
	}
	writeJSON(w, 201, newConv)
}

// promoteDocumentHandler moves a conversation document into the project KB.
func promoteDocumentHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	docID := pathParam(r, "docId")
	conv, err := store.GetConversation(r.Context(), d.DB, convID, u.ID)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	if conv.ProjectID == "" {
		writeError(w, 400, errors.New("conversation is not in a project"))
		return
	}
	p, err := store.GetProject(r.Context(), d.DB, conv.ProjectID, u.ID)
	if err != nil || p.KBID == "" {
		writeError(w, 400, errors.New("project has no knowledge base"))
		return
	}
	doc, err := store.GetDocument(r.Context(), d.DB, docID)
	if err != nil || doc.ConversationID != conv.ID {
		writeError(w, 404, errNotFound)
		return
	}
	// §C5: re-embed with the destination KB's locked embedder (not a raw chunk
	// move) so the promoted document is actually retrievable in the KB.
	if err := d.RAG.PromoteDocument(r.Context(), docID, p.KBID); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// listConversationDocsHandler returns the conversation-scoped documents and
// their ingest status, so the client can show upload/parse progress and block
// the first send until a just-attached file is 'ready' (§ chat uploads).
func listConversationDocsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	docs, _ := store.ListDocuments(r.Context(), d.DB, "conversation", convID)
	writeJSON(w, 200, docs)
}

// convFile is the shape returned by the conversation files drawer (§ conversation
// files): the authoritative set of files this conversation references, each with
// a download URL.
type convFile struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Kind      string `json:"kind"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt int64  `json:"created_at"`
	URL       string `json:"url"`
}

// listConversationFilesHandler returns every file currently attached to the
// conversation (what the model actually sees / stages), for the files drawer.
func listConversationFilesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	files, err := store.ListFilesByConversation(r.Context(), d.DB, convID, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	out := make([]convFile, 0, len(files))
	for _, f := range files {
		out = append(out, convFile{
			ID: f.ID, Filename: f.Filename, Kind: f.Kind, MimeType: f.MimeType,
			SizeBytes: f.SizeBytes, CreatedAt: f.CreatedAt, URL: "/api/files/" + f.ID,
		})
	}
	writeJSON(w, 200, out)
}

// deleteConversationFileHandler removes a file from the conversation's referenced
// set (§ conversation files). It detaches the file (so the sandbox no longer
// stages it) and deletes the conversation-scoped RAG document(s) of the same
// name (chunks + vectors), so future turns no longer reference it. The file row
// survives so a historical message that uploaded it can still be downloaded.
func deleteConversationFileHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	convID := pathParam(r, "id")
	fileID := pathParam(r, "fileId")
	if _, err := store.GetConversation(r.Context(), d.DB, convID, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	f, err := store.GetFile(r.Context(), d.DB, fileID, u.ID)
	if err != nil || f == nil || f.ConversationID != convID {
		writeError(w, 404, errNotFound)
		return
	}
	if err := store.DetachFileFromConversation(r.Context(), d.DB, fileID, convID, u.ID); err != nil {
		writeError(w, 500, err)
		return
	}
	// Drop the conversation-scoped RAG document(s) of the same name so retrieval
	// stops referencing this file. Best-effort: a vector/chunk hiccup must not
	// fail the detach the user already performed.
	if docs, derr := store.ListDocuments(r.Context(), d.DB, "conversation", convID); derr == nil {
		for _, doc := range docs {
			if doc.Filename != f.Filename {
				continue
			}
			if d.RAG != nil {
				_ = d.RAG.OnDocumentDeleted(r.Context(), doc.ID)
			}
			_ = store.DeleteChunksByDocument(r.Context(), d.DB, doc.ID)
			_ = store.DeleteDocument(r.Context(), d.DB, doc.ID)
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// stopHandler signals a generation cancel for the conversation.
func stopHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	if _, err := store.GetConversation(r.Context(), d.DB, id, u.ID); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	d.Cache.Publish("conv:"+id+":stop", "1")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
