package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrInvalidKnowledgeBaseShare = errors.New("invalid knowledge base share")

func validateKnowledgeBaseShareRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "read" && role != "write" {
		return "", ErrInvalidKnowledgeBaseShare
	}
	return role, nil
}

// ListKnowledgeBaseShares is owner-only and deliberately rejects workspace and
// project libraries. Those containers already have their own membership model.
func ListKnowledgeBaseShares(ctx context.Context, db *sql.DB, kbID, ownerID string) ([]KnowledgeBaseShare, error) {
	var allowed int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM knowledge_bases k
		WHERE k.id=? AND k.user_id=? AND COALESCE(k.workspace_id,'')='' AND `+standaloneKnowledgeBasePredicate("k"), kbID, ownerID).Scan(&allowed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.kb_id, s.user_id, s.role, COALESCE(u.name,''), u.email,
		       COALESCE(u.settings,''), s.created_at, s.updated_at
		  FROM knowledge_base_shares s JOIN users u ON u.id=s.user_id
		 WHERE s.kb_id=? ORDER BY LOWER(u.name), LOWER(u.email), s.user_id`, kbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shares := []KnowledgeBaseShare{}
	for rows.Next() {
		var share KnowledgeBaseShare
		var settings string
		if err := rows.Scan(&share.KBID, &share.UserID, &share.Role, &share.Name, &share.Email, &settings, &share.CreatedAt, &share.UpdatedAt); err != nil {
			return nil, err
		}
		share.AvatarURL = avatarFromSettings(settings)
		shares = append(shares, share)
	}
	return shares, rows.Err()
}

// CanRevokeKnowledgeBaseShare performs the side-effect-free authorization
// check required before the API installs generation tombstones. The target
// share must still exist, and only the owner of a standalone personal library
// may revoke it; a failed DELETE must never interrupt another user's turn.
func CanRevokeKnowledgeBaseShare(ctx context.Context, db *sql.DB, kbID, ownerID, userID string) (bool, error) {
	var allowed int
	err := db.QueryRowContext(ctx, `SELECT 1
		FROM knowledge_base_shares s JOIN knowledge_bases k ON k.id=s.kb_id
		WHERE s.kb_id=? AND s.user_id=? AND k.user_id=?
		  AND COALESCE(k.workspace_id,'')='' AND `+standaloneKnowledgeBasePredicate("k"),
		kbID, userID, ownerID,
	).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return allowed == 1, nil
}

// UpsertKnowledgeBaseShare resolves the target from a complete account email.
// Accepting an opaque user id here would let API callers bypass the exact-email
// discovery boundary and disclose another account's identity in the response.
func UpsertKnowledgeBaseShare(ctx context.Context, db *sql.DB, kbID, ownerID, targetEmail, role string) (*KnowledgeBaseShare, error) {
	role, err := validateKnowledgeBaseShareRole(role)
	if err != nil {
		return nil, ErrInvalidKnowledgeBaseShare
	}
	targetEmail, err = NormalizeUserEmail(targetEmail)
	if err != nil {
		return nil, ErrInvalidKnowledgeBaseShare
	}
	tx, err := beginKnowledgeBaseMutationTx(ctx, db, kbID, "")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO knowledge_base_shares(kb_id,user_id,role,created_at,updated_at)
		SELECT k.id, target.id, ?, ?, ?
		  FROM knowledge_bases k
		  JOIN users target ON LOWER(TRIM(target.email))=? AND target.status='active'
		 WHERE k.id=? AND k.user_id=? AND target.id<>?
		   AND COALESCE(k.workspace_id,'')='' AND `+standaloneKnowledgeBasePredicate("k")+`
		ON CONFLICT(kb_id,user_id) DO UPDATE SET role=excluded.role, updated_at=excluded.updated_at`,
		role, now, now, targetEmail, kbID, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	var share KnowledgeBaseShare
	var settings string
	err = tx.QueryRowContext(ctx, `
		SELECT s.kb_id,s.user_id,s.role,COALESCE(u.name,''),u.email,COALESCE(u.settings,''),s.created_at,s.updated_at
		  FROM knowledge_base_shares s JOIN users u ON u.id=s.user_id
		 WHERE s.kb_id=? AND LOWER(TRIM(u.email))=?`, kbID, targetEmail).
		Scan(&share.KBID, &share.UserID, &share.Role, &share.Name, &share.Email, &settings, &share.CreatedAt, &share.UpdatedAt)
	if err != nil {
		return nil, err
	}
	share.AvatarURL = avatarFromSettings(settings)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &share, nil
}

func DeleteKnowledgeBaseShare(ctx context.Context, db *sql.DB, kbID, ownerID, userID string) error {
	tx, err := beginKnowledgeBaseMutationTx(ctx, db, kbID, "")
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `DELETE FROM knowledge_base_shares
		WHERE kb_id=? AND user_id=? AND EXISTS (
			SELECT 1 FROM knowledge_bases k WHERE k.id=knowledge_base_shares.kb_id
			AND k.user_id=? AND COALESCE(k.workspace_id,'')='' AND `+standaloneKnowledgeBasePredicate("k")+`
		)`, kbID, userID, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	// A personal share can be attached only to the collaborator's personal
	// conversations. Remove the revoked id in the same transaction so the next
	// turn cannot inherit a stale selection and fail after the owner has already
	// received a successful revoke response.
	if IsPostgres() {
		_, err = tx.ExecContext(ctx, `
			UPDATE conversations
			SET kb_ids = COALESCE(
				(SELECT json_agg(value ORDER BY ordinality)
				 FROM json_array_elements_text(kb_ids::json) WITH ORDINALITY
				 WHERE value != $1),
				'[]'::json
			)::text
			WHERE user_id=$2 AND COALESCE(workspace_id,'')=''
			  AND kb_ids LIKE '%' || $1 || '%'
		`, kbID, userID)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE conversations
			SET kb_ids = (
				SELECT COALESCE(json_group_array(value), '[]')
				FROM json_each(kb_ids)
				WHERE value != ?
			)
			WHERE user_id=? AND COALESCE(workspace_id,'')=''
			  AND json_type(kb_ids)='array' AND kb_ids LIKE '%' || ? || '%'
		`, kbID, userID, kbID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SearchKnowledgeBaseShareCandidates returns only display identity fields and
// only after proving the caller owns a shareable personal knowledge base. User
// discovery is deliberately exact-email-only: an empty, partial, name-based,
// or otherwise invalid query must never degrade into an account directory.
func SearchKnowledgeBaseShareCandidates(ctx context.Context, db *sql.DB, kbID, ownerID, search string, limit int) ([]KnowledgeBaseShare, error) {
	// Account emails are unique. Keep the database boundary at one row even if
	// legacy case variants exist, so this endpoint can never become a directory.
	if limit <= 0 || limit > 1 {
		limit = 1
	}
	var allowed int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM knowledge_bases k
		WHERE k.id=? AND k.user_id=? AND COALESCE(k.workspace_id,'')='' AND `+standaloneKnowledgeBasePredicate("k"), kbID, ownerID).Scan(&allowed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	email, err := NormalizeUserEmail(search)
	if err != nil {
		return []KnowledgeBaseShare{}, nil
	}
	query := `SELECT u.id,COALESCE(u.name,''),u.email,COALESCE(u.settings,''),COALESCE(s.role,'')
		FROM users u LEFT JOIN knowledge_base_shares s ON s.kb_id=? AND s.user_id=u.id
		WHERE u.id<>? AND u.status='active' AND LOWER(TRIM(u.email))=?
		ORDER BY CASE WHEN s.user_id IS NULL THEN 1 ELSE 0 END, LOWER(u.name), LOWER(u.email)
		LIMIT ?`
	rows, err := db.QueryContext(ctx, query, kbID, ownerID, email, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []KnowledgeBaseShare{}
	for rows.Next() {
		var item KnowledgeBaseShare
		var settings string
		if err := rows.Scan(&item.UserID, &item.Name, &item.Email, &settings, &item.Role); err != nil {
			return nil, err
		}
		item.KBID = kbID
		item.AvatarURL = avatarFromSettings(settings)
		result = append(result, item)
	}
	return result, rows.Err()
}
