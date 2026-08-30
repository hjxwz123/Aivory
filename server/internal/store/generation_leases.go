package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const generationInterruptedMessage = "Generation interrupted. Please try again."

// ConversationGenerationLease serializes one principal's normal append from a
// branch leaf. Other workspace members, explicit branch edits, and regenerations
// intentionally remain independent.
type ConversationGenerationLease struct {
	ConversationID string
	BranchKey      string
	PrincipalID    string
	OwnerToken     string
}

func generationBranchKey(parentID string) string {
	if parentID == "" {
		return "root:"
	}
	return "message:" + parentID
}

// TryAcquireConversationGenerationLease resolves the exact parent of a normal
// append and atomically claims that branch for one principal. It also rejects an
// append when that principal already has an assistant generating on the selected
// path. The relational database is authoritative so the same user cannot
// double-send from independent application replicas.
func TryAcquireConversationGenerationLease(
	ctx context.Context,
	db *sql.DB,
	conversationID, preferredParentID, principalID, ownerToken string,
	ttl time.Duration,
) (*ConversationGenerationLease, string, bool, error) {
	if db == nil {
		return nil, "", false, errors.New("database is not initialized")
	}
	conversationID = strings.TrimSpace(conversationID)
	preferredParentID = strings.TrimSpace(preferredParentID)
	principalID = strings.TrimSpace(principalID)
	ownerToken = strings.TrimSpace(ownerToken)
	if conversationID == "" || principalID == "" || ownerToken == "" {
		return nil, "", false, errors.New("conversation generation lease requires conversation, principal, and owner ids")
	}
	if ttl <= 0 {
		return nil, "", false, errors.New("conversation generation lease ttl must be positive")
	}

	now := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", false, err
	}
	defer func() { _ = tx.Rollback() }()

	// Every message insertion advances this row in its own transaction. Taking
	// the same write lock makes parent resolution + lease admission one atomic
	// decision relative to branch switches and concurrent message creation.
	lockResult, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, conversationID)
	if err != nil {
		return nil, "", false, err
	}
	if rows, rowsErr := lockResult.RowsAffected(); rowsErr != nil {
		return nil, "", false, rowsErr
	} else if rows != 1 {
		return nil, "", false, ErrNotFound
	}

	var activeLeafID, conversationOwnerID string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(active_leaf_id,''),user_id FROM conversations WHERE id=?`, conversationID,
	).Scan(&activeLeafID, &conversationOwnerID); err != nil {
		return nil, "", false, err
	}

	parentID := preferredParentID
	if parentID == "" {
		parentID = activeLeafID
	}
	parentValid := false
	if parentID != "" {
		var found string
		lookupErr := tx.QueryRowContext(ctx,
			`SELECT id FROM messages WHERE id=? AND conversation_id=?`, parentID, conversationID,
		).Scan(&found)
		if lookupErr == nil {
			parentValid = true
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, "", false, lookupErr
		}
	}
	if !parentValid && parentID != activeLeafID {
		parentID = activeLeafID
	}
	if !parentValid && parentID != "" {
		var found string
		lookupErr := tx.QueryRowContext(ctx,
			`SELECT id FROM messages WHERE id=? AND conversation_id=?`, parentID, conversationID,
		).Scan(&found)
		if lookupErr == nil {
			parentValid = true
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, "", false, lookupErr
		}
	}
	if !parentValid {
		parentID, err = deepestLeafFromTx(ctx, tx, conversationID, "")
		if err != nil {
			return nil, "", false, err
		}
	}

	// A server crash can leave a streaming placeholder without a live handler.
	// Once it is older than the maximum protected generation lifetime, settle it
	// before admitting a later append so it cannot keep rendering forever.
	staleBefore := now.Add(-ttl).Unix()
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages
		    SET status='error', stop_reason='generation_interrupted', error=?
		  WHERE conversation_id=? AND role='assistant' AND status='streaming' AND created_at<=?`,
		generationInterruptedMessage, conversationID, staleBefore,
	); err != nil {
		return nil, "", false, err
	}

	// Walk only the selected root-to-leaf path. A streaming sibling on another
	// branch remains independent and does not block this append.
	for current := parentID; current != ""; {
		var nextParent, role, status, authorID string
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(parent_id,''), role, status, COALESCE(author_id,'')
			   FROM messages WHERE id=? AND conversation_id=?`, current, conversationID,
		).Scan(&nextParent, &role, &status, &authorID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			return nil, "", false, err
		}
		ownedByPrincipal := authorID == principalID || authorID == "" && conversationOwnerID == principalID
		if role == "assistant" && status == "streaming" && ownedByPrincipal {
			if err := tx.Commit(); err != nil {
				return nil, "", false, err
			}
			return nil, parentID, false, nil
		}
		current = nextParent
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_generation_leases
		  WHERE conversation_id=? AND expires_at<=?`, conversationID, now.UnixNano(),
	); err != nil {
		return nil, "", false, err
	}
	branchKey := generationBranchKey(parentID)
	result, err := tx.ExecContext(ctx,
		`INSERT INTO conversation_generation_leases(conversation_id,branch_key,principal_id,owner_token,expires_at)
		 VALUES(?,?,?,?,?) ON CONFLICT(conversation_id,branch_key,principal_id) DO NOTHING`,
		conversationID, branchKey, principalID, ownerToken, now.Add(ttl).UnixNano(),
	)
	if err != nil {
		return nil, "", false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, "", false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", false, err
	}
	if rows != 1 {
		return nil, parentID, false, nil
	}
	lease := &ConversationGenerationLease{
		ConversationID: conversationID,
		BranchKey:      branchKey,
		PrincipalID:    principalID,
		OwnerToken:     ownerToken,
	}
	return lease, parentID, true, nil
}

// ReleaseConversationGenerationLease is owner-token guarded so an expired
// worker cannot remove a newer generation's replacement lease.
func ReleaseConversationGenerationLease(ctx context.Context, db *sql.DB, lease *ConversationGenerationLease) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if lease == nil || lease.ConversationID == "" || lease.BranchKey == "" || lease.PrincipalID == "" || lease.OwnerToken == "" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`DELETE FROM conversation_generation_leases
		  WHERE conversation_id=? AND branch_key=? AND principal_id=? AND owner_token=?`,
		lease.ConversationID, lease.BranchKey, lease.PrincipalID, lease.OwnerToken,
	)
	return err
}
