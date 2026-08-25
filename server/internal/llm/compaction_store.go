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
// second return value is false for a dangling/cyclic tree, making merge cleanup
// fail closed rather than deleting a block whose branch reach is uncertain.
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
// where replacement is not. Only those inputs must remain stored after a fold;
// every other folded input is superseded by the coarse replacement on all paths
// that could render it.
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

func (t *compactionMessageTree) pathTo(nodeID string) ([]store.Message, bool) {
	if t == nil {
		return nil, false
	}
	seen := make(map[string]bool)
	reversed := make([]store.Message, 0)
	for current := nodeID; current != ""; {
		if seen[current] {
			return nil, false
		}
		seen[current] = true
		parentID, ok := t.parents[current]
		if !ok {
			return nil, false
		}
		reversed = append(reversed, store.Message{ID: current, ParentID: parentID})
		current = parentID
	}
	path := make([]store.Message, len(reversed))
	for i := range reversed {
		path[len(reversed)-1-i] = reversed[i]
	}
	return path, true
}

func (t *compactionMessageTree) frontiersUnchanged(before, after []SummaryBlock) bool {
	if t == nil {
		return false
	}
	hasChild := make(map[string]bool, len(t.parents))
	for _, parentID := range t.parents {
		if parentID != "" {
			hasChild[parentID] = true
		}
	}
	for id := range t.parents {
		// Only durable branch leaves matter. An internal historical prefix can lose
		// a housekeeping summary safely because its original messages become raw
		// context again; a sibling leaf must retain its exact summarized frontier.
		if hasChild[id] {
			continue
		}
		path, ok := t.pathTo(id)
		if !ok {
			return false
		}
		beforeFrontier := summarizedFrontier(filterBlocksForPath(before, path), path)
		afterFrontier := summarizedFrontier(filterBlocksForPath(after, path), path)
		if beforeFrontier != afterFrontier {
			return false
		}
	}
	return true
}

// mergeAndPersist folds over-budget path summaries into a coarser block when the
// path's summary tokens exceed budget, with at most one fold pipeline: it reads
// the current blocks, merges if needed, and CAS-writes. A pipeline may still use
// bounded map-reduce or one short-output retry for oversized sources. On
// contention it returns ok=false without starting a second fold.
func mergeAndPersist(ctx context.Context, db *sql.DB, task *TaskLLM, conv *store.Conversation, payerID, conversationModelID string, history []store.Message, budget int) ([]SummaryBlock, bool, error) {
	// Generate the optional coarse summary outside the write transaction, then
	// lock and CAS-persist below. Edit/delete take the same conversation lock.
	var curRaw string
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?", conv.ID).Scan(&curRaw); err != nil {
		return nil, false, nil
	}
	compactionExtraParams, _ := resolvedCompactionExtraParams(ctx, db, conversationModelID)
	compactionBlockTokenLimit := compactionSummaryBlockTokenLimit(db, compactionExtraParams)
	cur := loadSummaryBlocksForRequestWithTokenLimit(json.RawMessage(curRaw), compactionBlockTokenLimit)
	if summaryTokens(filterBlocksForPath(cur, history)) <= budget {
		return cur, true, nil // nothing to fold
	}
	tree, treeErr := loadCompactionMessageTree(ctx, db, conv.ID, history)
	if treeErr != nil {
		return cur, true, nil // optional housekeeping fails closed
	}
	merged, err := mergeIfOver(ctx, task, conv, payerID, conversationModelID, cur, history, tree, budget)
	if err != nil {
		return nil, false, err
	}
	if reflect.DeepEqual(merged, cur) {
		return cur, true, nil
	}
	tx, txErr := db.BeginTx(ctx, nil)
	if txErr != nil {
		return nil, false, nil
	}
	defer func() { _ = tx.Rollback() }()
	lockedRaw, lockErr := lockCompactionConversationTx(ctx, tx, conv.ID)
	if lockErr != nil || lockedRaw != curRaw {
		return nil, false, nil
	}
	lockedTree, treeErr := loadCompactionMessageTree(ctx, tx, conv.ID, history)
	if treeErr != nil || !tree.sameTopology(lockedTree) {
		return nil, false, nil
	}
	encoded, _ := json.Marshal(merged)
	res, err := tx.ExecContext(ctx, "UPDATE conversations SET summary_blocks=? WHERE id=?", string(encoded), conv.ID)
	if err != nil {
		return nil, false, nil
	}
	if n, _ := res.RowsAffected(); n == 1 {
		if err := tx.Commit(); err != nil {
			return nil, false, nil
		}
		return merged, true, nil
	}
	return nil, false, nil // contended — let a later turn fold
}

// mergeIfOver folds the oldest current-path blocks into one coarser block when
// the path exceeds budget; off-path blocks are preserved untouched. One append
// performs at most one fold. The fold has a strict compression target below, so
// repeated near-lossless rewrites cannot create several expensive calls in one
// logical compaction operation.
func mergeIfOver(ctx context.Context, task *TaskLLM, conv *store.Conversation, payerID, conversationModelID string, blocks []SummaryBlock, history []store.Message, tree *compactionMessageTree, budget int) ([]SummaryBlock, error) {
	pathBlocks := filterBlocksForPath(blocks, history)
	pathTokens := summaryTokens(pathBlocks)
	if pathTokens <= budget || len(pathBlocks) < 2 {
		return blocks, nil
	}
	merged, err := mergeOldestBlocksWithModel(ctx, task, conv, payerID, conversationModelID, pathBlocks, budget)
	if err != nil {
		return blocks, err
	}
	foldCount := compactionFoldCount(len(pathBlocks))
	// A task-model failure returns pathBlocks unchanged. Treat that as no fold;
	// otherwise cleanup would delete source blocks despite having no replacement.
	if len(merged) != len(pathBlocks)-foldCount+1 || len(merged) == 0 {
		return blocks, nil
	}
	replacement := merged[0]
	if replacement.FromMessageID != pathBlocks[0].FromMessageID ||
		replacement.AnchorMessageID != pathBlocks[foldCount-1].AnchorMessageID {
		return blocks, nil
	}

	// Remove a folded input only when the real message tree proves the coarse
	// replacement is visible on every branch that could render that input. A
	// branch that split before replacement's anchor keeps its shared-prefix block.
	foldedSet := make(map[string]bool, foldCount)
	for _, b := range pathBlocks[:foldCount] {
		neededOutside, ok := tree.neededOutsideReplacement(b, replacement)
		if !ok {
			return blocks, nil
		}
		if neededOutside {
			continue
		}
		foldedSet[summaryBlockRangeKey(b)] = true
	}
	if len(foldedSet) == 0 {
		return blocks, nil
	}
	rebuilt := make([]SummaryBlock, 0, len(blocks))
	for _, b := range blocks {
		if !foldedSet[summaryBlockRangeKey(b)] {
			rebuilt = append(rebuilt, b)
		}
	}
	next := append(rebuilt, replacement)
	nextPath := filterBlocksForPath(next, history)
	// A valid fold must make measurable progress without growing durable state,
	// changing any branch's summarized frontier, or replacing old blocks with an
	// equal/larger summary. Rejecting dubious output is safe: the original blocks
	// remain immutable and a later turn may retry.
	if len(next) > len(blocks) || len(nextPath) >= len(pathBlocks) ||
		summaryTokens(nextPath) >= pathTokens || !tree.frontiersUnchanged(blocks, next) {
		return blocks, nil
	}
	return next, nil
}

func summaryBlockRangeKey(block SummaryBlock) string {
	return block.FromMessageID + "\x00" + block.AnchorMessageID
}

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

func compactionFoldCount(blockCount int) int {
	foldCount := blockCount / 2
	if foldCount < 2 {
		foldCount = 2
	}
	if foldCount > blockCount {
		foldCount = blockCount
	}
	return foldCount
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

// mergeOldestBlocks folds the oldest half of the path's summary blocks into one
// coarser (level+1) block to move the total toward budget. Level records the
// fold depth (provenance); it grows by one per genuine fold — bounded, because
// every fold strictly reduces the block count (see the half floor below).
func mergeOldestBlocks(ctx context.Context, task *TaskLLM, conv *store.Conversation, payerID string, blocks []SummaryBlock, budget int) ([]SummaryBlock, error) {
	return mergeOldestBlocksWithModel(ctx, task, conv, payerID, conv.ModelID, blocks, budget)
}

func mergeOldestBlocksWithModel(ctx context.Context, task *TaskLLM, conv *store.Conversation, payerID, conversationModelID string, blocks []SummaryBlock, budget int) ([]SummaryBlock, error) {
	if len(blocks) < 2 {
		return blocks, nil
	}
	// Fold at least TWO blocks: merging N blocks into one reduces the count by
	// N-1, so a "fold" of a single block (len 2-3 → half 1) would reduce nothing —
	// it just lossily rewrites that block via the task model, and since the total
	// stays over budget the same block gets re-paraphrased (level bumped, cache
	// prefix churned, one wasted call) on every subsequent appending turn.
	half := compactionFoldCount(len(blocks))
	oldest := blocks[:half]
	rest := blocks[half:]
	oldTokens := summaryTokens(oldest)
	if oldTokens <= 1 {
		return blocks, nil
	}
	// Require a real fold, not a near-lossless rewrite. Aim for half the source
	// and never accept more than 60%. When the untouched tail leaves less room,
	// honor that tighter budget so one successful fold normally brings the path
	// below summary_merge_max_tokens.
	target := max(1, oldTokens*defaultSummaryMergeTargetPercent/100)
	maxAccepted := max(1, oldTokens*defaultSummaryMergeMaxPercent/100)
	if available := budget - summaryTokens(rest); available > 0 {
		target = min(target, available)
		maxAccepted = min(maxAccepted, available)
	}
	configuredOutputCap := min(compactionSummaryOutputCap(target, budget), maxAccepted)
	if configuredOutputCap <= 0 {
		return blocks, nil
	}
	target = min(target, configuredOutputCap)
	maxLevel := 1
	for _, b := range oldest {
		if b.Level > maxLevel {
			maxLevel = b.Level
		}
	}
	source := summaryInputsText(summaryBlocksToInputs(oldest))
	text := ""
	if task != nil {
		requestMaxTokens := compactionRequestMaxTokens(task.db)
		var taskErr error
		text, taskErr = summarizeCompactionText(
			ctx, task, conv, source, payerID, conversationModelID, compactionPrompt(task.db),
			compactionReduceInstruction, target, configuredOutputCap, requestMaxTokens,
		)
		if terminalErr := terminalCompactionTaskError(ctx, taskErr); terminalErr != nil {
			return blocks, terminalErr
		}
		if taskErr != nil {
			// Folding is optional housekeeping. Preserve all immutable source blocks
			// unless every bounded map/reduce request produced an acceptable summary.
			return blocks, nil
		}
	}
	if strings.TrimSpace(text) == "" {
		if task == nil {
			parts := make([]string, 0, len(oldest))
			for _, b := range oldest {
				parts = append(parts, b.Text)
			}
			text = clipToTokens(strings.Join(parts, " "), target)
		}
	}
	if strings.TrimSpace(text) == "" {
		// Folding is optional housekeeping. On model failure retain the original
		// immutable blocks instead of clipping away the tail of the conversation.
		return blocks, nil
	}
	outputCap := configuredOutputCap
	if task != nil {
		outputCap = effectiveCompactionOutputCap(compactionRequestMaxTokens(task.db), outputCap)
	}
	text = clipToTokens(strings.TrimSpace(text), outputCap)
	if tokens := estimateTokens(text); tokens >= oldTokens || tokens > maxAccepted {
		return blocks, nil
	}
	coarse := SummaryBlock{
		Level:           maxLevel + 1,
		AnchorMessageID: oldest[len(oldest)-1].AnchorMessageID,
		FromMessageID:   oldest[0].FromMessageID,
		Text:            strings.TrimSpace(text),
		Tokens:          estimateTokens(text),
		Media:           mergeCompactionMediaRefs(oldest),
	}
	return append([]SummaryBlock{coarse}, rest...), nil
}
