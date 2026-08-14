package store

import (
	"context"
	"database/sql"
	"strings"
)

// ScrubAccessRevokedGeneration removes every provider-derived field from one
// assistant turn after its generation authority has been revoked.
// The full identity tuple prevents a stale or forged message id from changing
// another conversation, another user's turn, or a non-assistant message.
//
// This update intentionally accepts both streaming and already-finalized rows:
// a provider can ignore context cancellation and race a complete final write
// after the first scrub. Reapplying the scrub after the provider returns makes
// the stopped state durable without affecting any other turn.
func ScrubAccessRevokedGeneration(
	ctx context.Context,
	db *sql.DB,
	messageID, conversationID, userID string,
) (bool, error) {
	messageID = strings.TrimSpace(messageID)
	conversationID = strings.TrimSpace(conversationID)
	userID = strings.TrimSpace(userID)
	if messageID == "" || conversationID == "" || userID == "" {
		return false, ErrNotFound
	}

	res, err := db.ExecContext(ctx,
		`UPDATE messages
		    SET blocks='[]', raw=NULL, citations='[]', stop_reason='stopped',
		        input_tokens=0, context_tokens=0, output_tokens=0,
		        cache_read_tokens=0, cache_write_tokens=0,
		        cost=0, credits=0, status='stopped', error='', gen_ms=0,
		        verify='', search_text=''
		  WHERE id=? AND conversation_id=? AND role='assistant'
		    AND COALESCE(author_id,'')=?`,
		messageID, conversationID, userID,
	)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// ScrubKnowledgeBaseRevokedGeneration is retained as the knowledge-base
// specific name used by existing callers and tests.
func ScrubKnowledgeBaseRevokedGeneration(
	ctx context.Context,
	db *sql.DB,
	messageID, conversationID, userID string,
) (bool, error) {
	return ScrubAccessRevokedGeneration(ctx, db, messageID, conversationID, userID)
}
