package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrStorageQuotaExceeded is returned by an atomic storage mutation when the
// billing principal's group cap would be exceeded.
var ErrStorageQuotaExceeded = errors.New("storage_quota_exceeded")

type storageQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// User storage accounting (§ user files page). Only non-image uploads count.
// A workspace is a shared container, so committed bytes are billed to its
// canonical owner even when a member uploaded or originally created the
// resource. Draft files remain private and are billed to their uploader until
// the message carrying them commits. Documents with a files twin are excluded
// because both rows point at the same physical bytes.

// UserStorageUsage returns the user's quota-relevant bytes.
func UserStorageUsage(ctx context.Context, db *sql.DB, userID string) (int64, error) {
	return userStorageUsage(ctx, db, userID)
}

func userStorageUsage(ctx context.Context, q storageQueryer, userID string) (int64, error) {
	var n sql.NullInt64
	err := q.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(size_bytes), 0) FROM (
				SELECT f.size_bytes
				  FROM files f
				  LEFT JOIN conversations storage_file_conversation
				    ON storage_file_conversation.id=f.conversation_id
				  LEFT JOIN workspaces storage_file_workspace
				    ON storage_file_workspace.id=storage_file_conversation.workspace_id
				 WHERE f.kind<>'image'
				   AND CASE
				     WHEN f.conversation_id IS NULL
				       OR COALESCE(storage_file_conversation.workspace_id,'')=''
				       OR f.draft=1
				     THEN f.user_id
				     ELSE COALESCE(storage_file_workspace.owner_id, f.user_id)
				   END=?
				UNION ALL
				SELECT d.size_bytes
				  FROM documents d
				  LEFT JOIN knowledge_bases storage_document_kb ON storage_document_kb.id=d.kb_id
				  LEFT JOIN conversations storage_document_conversation ON storage_document_conversation.id=d.conversation_id
				  LEFT JOIN workspaces storage_document_workspace
				    ON storage_document_workspace.id=CASE
				      WHEN COALESCE(storage_document_kb.workspace_id,'')<>'' THEN storage_document_kb.workspace_id
				      ELSE storage_document_conversation.workspace_id
				    END
				 WHERE CASE
				     WHEN COALESCE(storage_document_kb.workspace_id, storage_document_conversation.workspace_id, '')<>''
				     THEN COALESCE(storage_document_workspace.owner_id, storage_document_kb.user_id, storage_document_conversation.user_id, '')
				     ELSE COALESCE(storage_document_kb.user_id, storage_document_conversation.user_id, '')
				   END=?
				   AND NOT EXISTS (SELECT 1 FROM files storage_twin WHERE storage_twin.storage_path=d.storage_path)
			) storage_rows`, userID, userID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// StorageQuotaBytes resolves the user's group storage cap in bytes.
// 0 = unlimited (no group or a group without a cap).
func StorageQuotaBytes(ctx context.Context, db *sql.DB, userID string) (int64, error) {
	return storageQuotaBytes(ctx, db, userID)
}

func storageQuotaBytes(ctx context.Context, q storageQueryer, userID string) (int64, error) {
	var mb sql.NullInt64
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(g.max_storage_mb, 0)
		  FROM users u LEFT JOIN user_groups g ON g.id = u.group_id
		 WHERE u.id=?`, userID).Scan(&mb)
	if err != nil {
		return 0, err
	}
	return mb.Int64 * 1024 * 1024, nil
}

// CheckStorageQuota performs the upload-time preflight used before allocating
// disk space. Mutations that change billing ownership must additionally call
// enforceStorageQuotaTx while holding their database transaction.
func CheckStorageQuota(ctx context.Context, db *sql.DB, userID string, additionalBytes int64) error {
	return checkStorageQuotaForQueryer(ctx, db, userID, additionalBytes)
}

func checkStorageQuotaForQueryer(ctx context.Context, q storageQueryer, userID string, additionalBytes int64) error {
	if additionalBytes <= 0 {
		return nil
	}
	quota, err := storageQuotaBytes(ctx, q, userID)
	if err != nil {
		return err
	}
	if quota <= 0 {
		return nil
	}
	used, err := userStorageUsage(ctx, q, userID)
	if err != nil {
		return err
	}
	if additionalBytes > quota-used {
		return fmt.Errorf("%w: %d MB in use of %d MB; free up space and retry",
			ErrStorageQuotaExceeded, used/(1024*1024), quota/(1024*1024))
	}
	return nil
}

// enforceStorageQuotaTx serializes quota-changing mutations on the billing
// user's row, then evaluates current usage in that same transaction. Workspace
// callers must obtain the workspace mutation lock first, preserving the global
// workspace -> billing-user lock order.
func enforceStorageQuotaTx(ctx context.Context, tx *sql.Tx, userID string, additionalBytes int64) error {
	if additionalBytes <= 0 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET id=id WHERE id=?`, userID)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return checkStorageQuotaForQueryer(ctx, tx, userID, additionalBytes)
}

// StorageBillingUserForContainer resolves who pays for a newly-created
// document while also enforcing that accessUserID can currently access the KB
// or conversation. Personal documents are billed to the container owner;
// workspace documents are billed to the canonical workspace owner.
func StorageBillingUserForContainer(ctx context.Context, db *sql.DB, kbID, conversationID, accessUserID string) (string, error) {
	if (strings.TrimSpace(kbID) == "") == (strings.TrimSpace(conversationID) == "") {
		return "", ErrNotFound
	}
	var billingUserID string
	var err error
	if kbID != "" {
		args := []any{kbID}
		args = append(args, knowledgeBaseWriteArgs(accessUserID)...)
		err = db.QueryRowContext(ctx, `
			SELECT CASE WHEN COALESCE(k.workspace_id,'')<>''
			       THEN COALESCE(w.owner_id, k.user_id) ELSE k.user_id END
			  FROM knowledge_bases k
			  LEFT JOIN workspaces w ON w.id=k.workspace_id
			 WHERE k.id=? AND `+knowledgeBaseWritePredicate("k"), args...).Scan(&billingUserID)
	} else {
		args := []any{conversationID}
		args = append(args, conversationMemberMutationArgs(accessUserID)...)
		err = db.QueryRowContext(ctx, `
			SELECT CASE WHEN COALESCE(c.workspace_id,'')<>''
			       THEN COALESCE(w.owner_id, c.user_id) ELSE c.user_id END
			  FROM conversations c
			  LEFT JOIN workspaces w ON w.id=c.workspace_id
			 WHERE c.id=? AND `+conversationMemberMutationPredicate("c"), args...).Scan(&billingUserID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return billingUserID, nil
}

// StorageItemBillingUser returns the canonical billing principal for one row
// in the user file inventory, restricted to the caller's current visibility.
func StorageItemBillingUser(ctx context.Context, db *sql.DB, source, id, accessUserID string) (string, error) {
	var billingUserID string
	var err error
	switch source {
	case "file":
		args := []any{id, accessUserID, accessUserID}
		args = append(args, workspaceResourceAccessArgs(accessUserID)...)
		err = db.QueryRowContext(ctx, `
			SELECT CASE
			         WHEN f.conversation_id IS NULL OR COALESCE(c.workspace_id,'')='' OR f.draft=1
			         THEN f.user_id
			         ELSE COALESCE(w.owner_id, f.user_id)
			       END
			  FROM files f
			  LEFT JOIN conversations c ON c.id=f.conversation_id
			  LEFT JOIN workspaces w ON w.id=c.workspace_id
			 WHERE f.id=?
			   AND (f.conversation_id IS NULL AND f.user_id=? OR
			        f.conversation_id IS NOT NULL AND (f.user_id=? OR f.draft=0) AND `+conversationResourceAccessPredicate("c")+`)`,
			args...).Scan(&billingUserID)
	case "document":
		args := []any{id}
		args = append(args, documentUserAccessArgs(accessUserID)...)
		err = db.QueryRowContext(ctx, `
			SELECT CASE
			         WHEN COALESCE(k.workspace_id, c.workspace_id, '')<>''
			         THEN COALESCE(w.owner_id, k.user_id, c.user_id, '')
			         ELSE COALESCE(k.user_id, c.user_id, '')
			       END
			  FROM documents d
			  LEFT JOIN knowledge_bases k ON k.id=d.kb_id
			  LEFT JOIN conversations c ON c.id=d.conversation_id
			  LEFT JOIN workspaces w ON w.id=CASE
			    WHEN COALESCE(k.workspace_id,'')<>'' THEN k.workspace_id ELSE c.workspace_id END
			 WHERE d.id=? AND `+documentUserAccessPredicate("d"), args...).Scan(&billingUserID)
	default:
		return "", ErrNotFound
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return billingUserID, nil
}
