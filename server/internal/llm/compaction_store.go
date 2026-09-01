// Transactional compaction state validation, branch topology, and maintenance.
package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

func manualCompactionSnapshotCurrentTx(ctx context.Context, tx *sql.Tx, convID, leafID string) (bool, error) {
	var currentLeaf string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(active_leaf_id,'') FROM conversations WHERE id=?`, convID,
	).Scan(&currentLeaf); err != nil {
		return false, err
	}
	if currentLeaf != leafID {
		return false, nil
	}
	var inFlight int
	streamingCutoff := protectedStreamingCutoffUnix()
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM messages
		  WHERE conversation_id=? AND role='assistant' AND status='streaming' AND created_at>?`,
		convID, streamingCutoff,
	).Scan(&inFlight); err != nil {
		return false, err
	}
	return inFlight == 0, nil
}

// automaticCompactionSnapshotCurrentTx verifies that the message which
// scheduled an inline/queued compaction still belongs to the active branch.
// The caller already holds the conversation row lock, so active_leaf_id cannot
// move between this check and the summary write. Descendants are accepted: the
// normal case has an assistant placeholder/reply below the scheduling user row.
func automaticCompactionSnapshotCurrentTx(ctx context.Context, tx *sql.Tx, convID, expectedMessageID string) (bool, error) {
	var activeLeafID string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(active_leaf_id,'') FROM conversations WHERE id=?`, convID,
	).Scan(&activeLeafID); err != nil {
		return false, err
	}
	if strings.TrimSpace(activeLeafID) == "" || strings.TrimSpace(expectedMessageID) == "" {
		return false, nil
	}
	tree, err := loadCompactionMessageTree(ctx, tx, convID, nil)
	if err != nil {
		return false, err
	}
	onPath, valid := tree.ancestorOf(expectedMessageID, activeLeafID)
	return valid && onPath, nil
}

// readSummaryRaw reads the conversation's current summary_blocks JSON (or "[]").
func readSummaryRaw(ctx context.Context, db *sql.DB, convID string) (string, error) {
	var raw string
	err := db.QueryRowContext(ctx, "SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?", convID).Scan(&raw)
	return raw, err
}

// readSummaryRawAfterPersistence reconciles an uncertain transaction result.
// Commit may return an error even though the database made the write durable;
// use a short read-only window detached from request cancellation so the caller
// can adopt that durable frontier instead of reporting failure or replaying it.
func readSummaryRawAfterPersistence(ctx context.Context, db *sql.DB, convID string) (string, error) {
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compactionPersistenceVerifyTimeout)
	defer cancel()
	return readSummaryRaw(verifyCtx, db, convID)
}

type compactionTxQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func lockCompactionConversationTx(ctx context.Context, tx *sql.Tx, convID string) (string, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, convID); err != nil {
		return "", err
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, convID).Scan(&raw); err != nil {
		return "", err
	}
	return raw, nil
}

// messagesStillCurrentTx verifies every prompt-bearing field and parent link
// while the caller holds the conversation row lock shared with edit/delete.
// Query failures fail
// CLOSED: advancing the frontier without proving the snapshot current can
// resurrect stale or deleted content permanently.
func messagesStillCurrentTx(ctx context.Context, queryer compactionTxQueryer, convID string, msgs []store.Message) (bool, error) {
	chunkSize := envcfg.Int("AIVORY_LLM_CHUNK_SIZE", 400)
	if chunkSize <= 0 {
		chunkSize = 400
	}
	for start := 0; start < len(msgs); start += chunkSize {
		end := start + chunkSize
		if end > len(msgs) {
			end = len(msgs)
		}
		chunk := msgs[start:end]
		want := make(map[string]store.Message, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		args = append(args, convID)
		ph := make([]string, len(chunk))
		for i, m := range chunk {
			want[m.ID] = m
			ph[i] = "?"
			args = append(args, m.ID)
		}
		query := "SELECT id, COALESCE(parent_id,''), blocks, COALESCE(raw,''), COALESCE(attachments,'[]'), COALESCE(citations,'[]') FROM messages WHERE conversation_id=? AND id IN (" + strings.Join(ph, ",") + ")"
		rows, err := queryer.QueryContext(ctx, query, args...)
		if err != nil {
			return false, err
		}
		seen := 0
		for rows.Next() {
			var id, parentID, blocks, raw, attachments, citations string
			if err := rows.Scan(&id, &parentID, &blocks, &raw, &attachments, &citations); err != nil {
				rows.Close()
				return false, err
			}
			m, ok := want[id]
			if !ok || parentID != m.ParentID || blocks != string(m.Blocks) || raw != string(m.Raw) ||
				!compactionSnapshotJSONEqual(attachments, m.Attachments) || !compactionSnapshotJSONEqual(citations, m.Citations) {
				rows.Close()
				return false, nil
			}
			seen++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		rows.Close()
		if seen != len(chunk) {
			return false, nil
		}
	}
	return true, nil
}

func compactionSnapshotJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "[]"
	}
	return string(raw)
}

func compactionSnapshotJSONEqual(stored string, snapshot json.RawMessage) bool {
	want := compactionSnapshotJSON(snapshot)
	if stored == want {
		return true
	}
	var storedValue, snapshotValue any
	if json.Unmarshal([]byte(stored), &storedValue) != nil || json.Unmarshal([]byte(want), &snapshotValue) != nil {
		return false
	}
	return reflect.DeepEqual(storedValue, snapshotValue)
}

// compactionMessageTree is the immutable parent-link snapshot used to decide
// whether a summary block is still required by a sibling branch. Summary block
// anchors alone cannot answer that question: an unrelated off-path block says
// nothing about which shared ancestor ranges its branch can actually see.
type compactionMessageTree struct {
	parents map[string]string
	pathIDs map[string]bool
}

func loadCompactionMessageTree(ctx context.Context, queryer compactionTxQueryer, convID string, history []store.Message) (*compactionMessageTree, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT id, COALESCE(parent_id,'') FROM messages WHERE conversation_id=?`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parents := make(map[string]string)
	for rows.Next() {
		var id, parentID string
		if err := rows.Scan(&id, &parentID); err != nil {
			return nil, err
		}
		parents[id] = parentID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return newCompactionMessageTree(parents, history)
}

func newCompactionMessageTree(parents map[string]string, history []store.Message) (*compactionMessageTree, error) {
	pathIDs := make(map[string]bool, len(history))
	for _, message := range history {
		if _, ok := parents[message.ID]; !ok {
			return nil, fmt.Errorf("compaction path message %q missing from message tree", message.ID)
		}
		pathIDs[message.ID] = true
	}
	return &compactionMessageTree{parents: parents, pathIDs: pathIDs}, nil
}

func (t *compactionMessageTree) sameTopology(other *compactionMessageTree) bool {
	if t == nil || other == nil || len(t.parents) != len(other.parents) {
		return false
	}
	for id, parentID := range t.parents {
		otherParentID, ok := other.parents[id]
		if !ok || otherParentID != parentID {
			return false
		}
	}
	return true
}

// ancestorOf reports whether ancestorID is on nodeID's real parent chain. The
// second return value is false for a dangling/cyclic tree, making replacement
// cleanup fail closed rather than deleting a block whose branch reach is uncertain.
func (t *compactionMessageTree) ancestorOf(ancestorID, nodeID string) (bool, bool) {
	if t == nil || ancestorID == "" || nodeID == "" {
		return false, false
	}
	if _, ok := t.parents[ancestorID]; !ok {
		return false, false
	}
	seen := make(map[string]bool)
	for current := nodeID; current != ""; {
		if seen[current] {
			return false, false
		}
		seen[current] = true
		if current == ancestorID {
			return true, true
		}
		parentID, ok := t.parents[current]
		if !ok {
			return false, false
		}
		current = parentID
	}
	return false, true
}

// neededOutsideReplacement reports whether block is visible on a sibling path
// where replacement is not. Only those inputs must remain stored; every other
// input is superseded by the replacement on all paths that could render it.
func (t *compactionMessageTree) neededOutsideReplacement(block, replacement SummaryBlock) (bool, bool) {
	containsReplacement, ok := t.ancestorOf(block.AnchorMessageID, replacement.AnchorMessageID)
	if !ok || !containsReplacement {
		return false, false
	}
	for id := range t.parents {
		if t.pathIDs[id] {
			continue
		}
		seesBlock, validBlockPath := t.ancestorOf(block.AnchorMessageID, id)
		if !validBlockPath {
			return false, false
		}
		if !seesBlock {
			continue
		}
		seesReplacement, validReplacementPath := t.ancestorOf(replacement.AnchorMessageID, id)
		if !validReplacementPath {
			return false, false
		}
		if !seesReplacement {
			return true, true
		}
	}
	return false, true
}

func summaryBlockRangeKey(block SummaryBlock) string {
	return block.FromMessageID + "\x00" + block.AnchorMessageID
}

// installContinuationReplacement replaces the active path's prior continuation
// blocks with the newly generated state while retaining blocks still needed by
// sibling branches. This is part of the normal one-request replacement path,
// not the removed legacy summary-fold maintenance pass.
func installContinuationReplacement(blocks, pathBlocks []SummaryBlock, replacement SummaryBlock, tree *compactionMessageTree) []SummaryBlock {
	if tree == nil || len(pathBlocks) == 0 {
		return append(append([]SummaryBlock{}, blocks...), replacement)
	}
	pathRanges := make(map[string]bool, len(pathBlocks))
	for _, block := range pathBlocks {
		pathRanges[summaryBlockRangeKey(block)] = true
	}
	next := make([]SummaryBlock, 0, len(blocks)+1)
	for _, block := range blocks {
		if !pathRanges[summaryBlockRangeKey(block)] {
			next = append(next, block)
			continue
		}
		neededOutside, ok := tree.neededOutsideReplacement(block, replacement)
		if !ok || neededOutside {
			next = append(next, block)
		}
	}
	return append(next, replacement)
}

// summaryTokens sums the token estimate across blocks.
func summaryTokens(blocks []SummaryBlock) int {
	t := 0
	for _, b := range blocks {
		// Tokens is derived, persisted data and may be stale or forged by an older
		// backup. Budget decisions must follow the text that will actually be sent.
		t += estimateTokens(strings.TrimSpace(b.Text))
	}
	return t
}
