package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrConversationReorderConflict = errors.New("conversation order changed or a reply is in progress")

type ConversationTimestamp struct {
	ID        string `json:"id"`
	UpdatedAt int64  `json:"updated_at"`
}

// ReorderConversation persists a sidebar move using the existing updated_at
// order. Only the moved row and, when seconds collide, its immediate following
// neighbors need new timestamps. All writes commit together; future timestamps
// are avoided so a subsequent message can bring its conversation to the front.
func ReorderConversation(ctx context.Context, db *sql.DB, id, userID, targetID, position string) ([]ConversationTimestamp, error) {
	if id == "" || targetID == "" || id == targetID || (position != "before" && position != "after") {
		return nil, ErrConversationReorderConflict
	}
	conversation, err := GetConversation(ctx, db, id, userID)
	if err != nil {
		return nil, err
	}
	var tx *sql.Tx
	if conversation.WorkspaceID != "" {
		tx, err = beginWorkspaceMutationTx(ctx, db, conversation.WorkspaceID)
	} else {
		tx, err = db.BeginTx(ctx, nil)
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	// Acquire the SQLite write lock before reading the ordering or access rules.
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, id); err != nil {
		return nil, err
	}

	args := []any{conversation.WorkspaceID, conversation.ProjectID, conversation.ProjectID, boolInt(conversation.Starred)}
	args = append(args, conversationMemberMutationArgs(userID)...)
	rows, err := tx.QueryContext(ctx, `SELECT c.id, c.updated_at,
		EXISTS(SELECT 1 FROM messages m WHERE m.conversation_id=c.id AND m.status='streaming')
		FROM conversations c
		WHERE COALESCE(c.workspace_id,'')=? AND COALESCE(c.project_id,'')=?
		AND (?<>'' OR c.starred=?) AND c.archived=0 AND COALESCE(c.inline_source_conv,'')=''
		AND `+conversationMemberMutationPredicate("c")+`
		ORDER BY c.updated_at DESC, c.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	type entry struct {
		ConversationTimestamp
		streaming bool
	}
	entries := []entry{}
	sourceIndex, targetIndex := -1, -1
	for rows.Next() {
		var item entry
		if err := rows.Scan(&item.ID, &item.UpdatedAt, &item.streaming); err != nil {
			rows.Close()
			return nil, err
		}
		if item.ID == id {
			sourceIndex = len(entries)
		}
		if item.ID == targetID {
			targetIndex = len(entries)
		}
		entries = append(entries, item)
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return nil, readErr
	}
	if sourceIndex < 0 || targetIndex < 0 {
		return nil, ErrNotFound
	}
	if entries[sourceIndex].streaming || entries[targetIndex].streaming {
		return nil, ErrConversationReorderConflict
	}
	source := entries[sourceIndex]
	entries = append(entries[:sourceIndex], entries[sourceIndex+1:]...)
	if sourceIndex < targetIndex {
		targetIndex--
	}
	insertAt := targetIndex
	if position == "after" {
		insertAt++
	}
	updates := []ConversationTimestamp{}
	if insertAt == sourceIndex {
		return updates, tx.Commit()
	}
	entries = append(entries, entry{})
	copy(entries[insertAt+1:], entries[insertAt:])
	entries[insertAt] = source

	var timestamp int64
	if insertAt == 0 {
		timestamp = time.Now().Unix() - 1
	} else {
		upper := entries[insertAt-1].UpdatedAt
		timestamp = upper - 1
		if insertAt+1 < len(entries) {
			lower := entries[insertAt+1].UpdatedAt
			if upper-lower > 1 {
				timestamp = lower + (upper-lower)/2
			}
		}
	}
	if timestamp <= 0 {
		return nil, ErrConversationReorderConflict
	}
	updates = append(updates, ConversationTimestamp{ID: id, UpdatedAt: timestamp})
	// Repair only the adjacent run that has no free whole second. The remaining
	// history keeps its original timestamps and date buckets.
	for index := insertAt + 1; index < len(entries); index++ {
		item := entries[index]
		if item.UpdatedAt < timestamp {
			break
		}
		if item.streaming || timestamp <= 1 {
			return nil, ErrConversationReorderConflict
		}
		timestamp--
		updates = append(updates, ConversationTimestamp{ID: item.ID, UpdatedAt: timestamp})
	}
	for _, update := range updates {
		writeArgs := []any{update.UpdatedAt, update.ID}
		writeArgs = append(writeArgs, conversationMemberMutationArgs(userID)...)
		result, err := tx.ExecContext(ctx, `UPDATE conversations SET updated_at=?
			WHERE id=? AND `+conversationMemberMutationPredicate("conversations"), writeArgs...)
		if err != nil {
			return nil, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return nil, err
		} else if affected != 1 {
			return nil, ErrNotFound
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updates, nil
}
