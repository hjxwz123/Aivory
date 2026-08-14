package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Conversation shares (§ public read-only sharing). A share freezes a
// cost-stripped snapshot of the active message path at share time and exposes it
// under a public token. Revoking deletes the row, so the link dies and no later
// private messages are ever reachable. At most one live share per conversation
// (enforced by a unique index) — re-sharing replaces the snapshot.

// Share is one public share record.
type Share struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	UserID         string          `json:"user_id"`
	Title          string          `json:"title"`
	Snapshot       json.RawMessage `json:"snapshot"`
	CreatedAt      int64           `json:"created_at"`
}

// CanManageConversationShare reports whether userID may publish or revoke a
// conversation. In a workspace, the canonical owner or the conversation creator
// while still a member may manage it; unrelated ordinary members do not qualify.
func CanManageConversationShare(ctx context.Context, db *sql.DB, convID, userID string) (bool, error) {
	var count int
	args := []any{convID}
	args = append(args, workspaceResourceManagerArgs(userID)...)
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		   FROM conversations c
		  WHERE c.id=? AND `+workspaceResourceManagerPredicate("c"), args...,
	).Scan(&count)
	return count > 0, err
}

// CreateShare atomically replaces any existing share for the conversation with
// a fresh snapshot. The INSERT ... SELECT repeats the management predicate in
// the write itself, so a handler check cannot be raced by a membership change
// and a direct store caller cannot bypass authorization. snapshot is opaque JSON
// built by the API's cost-stripped public message projection.
func CreateShare(ctx context.Context, db *sql.DB, userID, convID, title string, snapshot []byte) (*Share, error) {
	allowed, err := conversationSharingAllowedForUser(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	id := "sh_" + genToken() // §D1: 192-bit unguessable token (public capability URL)
	if len(snapshot) == 0 {
		snapshot = []byte("[]")
	}
	createdAt := time.Now().Unix()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var workspaceID string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if workspaceID != "" {
		if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
			return nil, err
		}
	}
	args := []any{id, userID, title, string(snapshot), createdAt, convID}
	args = append(args, workspaceResourceManagerArgs(userID)...)
	res, err := tx.ExecContext(ctx,
		`INSERT INTO conversation_shares(id, conversation_id, user_id, title, snapshot, created_at)
		 SELECT ?, c.id, ?, ?, ?, ?
		   FROM conversations c
		  WHERE c.id=? AND `+workspaceResourceManagerPredicate("c")+`
		 ON CONFLICT(conversation_id) DO UPDATE SET
		      id=excluded.id,
		      user_id=excluded.user_id,
		      title=excluded.title,
		      snapshot=excluded.snapshot,
		      created_at=excluded.created_at`,
		args...)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Share{
		ID: id, ConversationID: convID, UserID: userID, Title: title,
		Snapshot: json.RawMessage(snapshot), CreatedAt: createdAt,
	}, nil
}

// GetShareByConversation returns the live share only when userID is the
// current conversation creator-manager or canonical workspace owner. UserID on
// the returned Share records who most recently published it; it is not itself
// the access-control source.
func GetShareByConversation(ctx context.Context, db *sql.DB, convID, userID string) (*Share, error) {
	var s Share
	var snapshot string
	args := []any{convID}
	args = append(args, workspaceResourceManagerArgs(userID)...)
	err := db.QueryRowContext(ctx,
		`SELECT s.id, s.conversation_id, s.user_id, s.title, s.snapshot, s.created_at
		   FROM conversation_shares s
		   JOIN conversations c ON c.id=s.conversation_id
		  WHERE s.conversation_id=? AND `+workspaceResourceManagerPredicate("c")+`
		    AND `+validConversationSharePublisherPredicate("c", "s"), args...,
	).Scan(&s.ID, &s.ConversationID, &s.UserID, &s.Title, &snapshot, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	allowed, err := conversationSharingAllowedForUser(ctx, db, s.UserID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	s.Snapshot = json.RawMessage(snapshot)
	return &s, nil
}

// GetShareByToken returns a share by its public id for the unauthenticated
// public view. The publisher must still satisfy today's management boundary;
// this immediately invalidates legacy links created by ordinary workspace
// members before store-level authorization was introduced.
func GetShareByToken(ctx context.Context, db *sql.DB, token string) (*Share, error) {
	var s Share
	var snapshot string
	err := db.QueryRowContext(ctx,
		`SELECT s.id, s.conversation_id, s.user_id, s.title, s.snapshot, s.created_at
		   FROM conversation_shares s
		   JOIN conversations c ON c.id=s.conversation_id
		  WHERE s.id=? AND `+validConversationSharePublisherPredicate("c", "s"), token,
	).Scan(&s.ID, &s.ConversationID, &s.UserID, &s.Title, &snapshot, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	allowed, err := conversationSharingAllowedForUser(ctx, db, s.UserID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	s.Snapshot = json.RawMessage(snapshot)
	return &s, nil
}

// conversationSharingAllowedForUser re-resolves the publisher's current
// membership policy whenever a share is created or consumed. Existing rows are
// intentionally retained while permission is disabled, so restoring the
// capability makes the same public link live again. Administrators inherit the
// permissive policy from UserGroupPermissionStateForUser.
func conversationSharingAllowedForUser(ctx context.Context, db *sql.DB, userID string) (bool, error) {
	permissions, err := UserGroupPermissionsForUser(ctx, db, userID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return permissions.AllowSharing, nil
}

// DeleteShareByConversation revokes a share only when the same management
// predicate used by CreateShare is true at the moment of the DELETE. Deleting a
// missing share remains idempotent for an authorized manager.
func DeleteShareByConversation(ctx context.Context, db *sql.DB, convID, userID string) error {
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	args := []any{convID}
	args = append(args, workspaceResourceManagerArgs(userID)...)
	deleteQuery :=
		`DELETE FROM conversation_shares
		  WHERE conversation_id=? AND EXISTS (
		        SELECT 1 FROM conversations c
		         WHERE c.id=conversation_shares.conversation_id
		           AND ` + workspaceResourceManagerPredicate("c") + `
		  )`
	if workspaceID == "" {
		res, err := db.ExecContext(ctx, deleteQuery, args...)
		if err != nil {
			return err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if n > 0 {
			return nil
		}
		allowed, err := CanManageConversationShare(ctx, db, convID, userID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrNotFound
		}
		return nil
	}

	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, deleteQuery, args...)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n > 0 {
		return tx.Commit()
	}
	var allowed int
	managerArgs := []any{convID}
	managerArgs = append(managerArgs, workspaceResourceManagerArgs(userID)...)
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversations c
		  WHERE c.id=? AND `+workspaceResourceManagerPredicate("c"), managerArgs...,
	).Scan(&allowed); err != nil {
		return err
	}
	if allowed == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// validConversationSharePublisherPredicate invalidates a live/public share as
// soon as its publisher no longer satisfies today's management boundary. SQL
// aliases are trusted constants supplied by this file.
func validConversationSharePublisherPredicate(conversationAlias, shareAlias string) string {
	return `((COALESCE(` + conversationAlias + `.workspace_id,'')='' AND ` +
		shareAlias + `.user_id=` + conversationAlias + `.user_id) OR (` +
		`COALESCE(` + conversationAlias + `.workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces publisher_workspace ` +
		`WHERE publisher_workspace.id=` + conversationAlias + `.workspace_id AND (` +
		`publisher_workspace.owner_id=` + shareAlias + `.user_id OR (` +
		conversationAlias + `.user_id=` + shareAlias + `.user_id AND EXISTS (` +
		`SELECT 1 FROM workspace_members publisher_member ` +
		`WHERE publisher_member.workspace_id=publisher_workspace.id ` +
		`AND publisher_member.user_id=` + shareAlias + `.user_id` +
		`)` +
		`)` +
		`)` +
		`)` +
		`))`
}

// GetSharedFile loads a committed upload only when the database confirms that
// it belongs to the conversation frozen by a public share. Public handlers
// must additionally verify that the share snapshot contains a structured
// attachment reference; neither condition is sufficient by itself.
func GetSharedFile(ctx context.Context, db *sql.DB, id, convID string) (*File, error) {
	var f File
	var conversationID sql.NullString
	var draft int
	err := db.QueryRowContext(ctx,
		`SELECT id, user_id, conversation_id, filename, mime_type, size_bytes, storage_path, kind, draft, created_at
		   FROM files WHERE id=? AND conversation_id=? AND draft=0`, id, convID,
	).Scan(&f.ID, &f.UserID, &conversationID, &f.Filename, &f.MimeType, &f.SizeBytes, &f.StoragePath, &f.Kind, &draft, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f.ConversationID = conversationID.String
	f.Draft = draft != 0
	return &f, nil
}

// GetSharedArtifact loads a generated artifact only when its owning message is
// part of the conversation frozen by a public share. The conversation join is
// the authoritative ownership boundary for artifacts.
func GetSharedArtifact(ctx context.Context, db *sql.DB, id, convID string) (*Artifact, error) {
	var a Artifact
	err := db.QueryRowContext(ctx,
		`SELECT a.id, a.message_id, a.filename, a.storage_path, a.mime_type, a.size_bytes, a.created_at
		   FROM artifacts a
		   JOIN messages m ON m.id=a.message_id
		  WHERE a.id=? AND m.conversation_id=?`, id, convID,
	).Scan(&a.ID, &a.MessageID, &a.Filename, &a.StoragePath, &a.MimeType, &a.SizeBytes, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}
