package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"time"
)

var ErrMixedKBEmbeddingModels = errors.New("selected knowledge bases use different embedding models")

// knowledgeBaseAccessPredicate extends the workspace visibility boundary with
// explicit personal-library shares. The first five arguments are the scoped
// visibility boundary (§workspace RBAC phase 2: private KBs stay with their
// creator and workspace admins); the sixth is the share recipient.
func knowledgeBaseAccessPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `(` + workspaceScopedVisibilityPredicate(alias) + ` OR (` +
		`COALESCE(` + prefix + `workspace_id,'')='' AND EXISTS (` +
		`SELECT 1 FROM knowledge_base_shares kb_access_share ` +
		`WHERE kb_access_share.kb_id=` + prefix + `id AND kb_access_share.user_id=?` +
		`)` +
		`))`
}

func knowledgeBaseAccessArgs(userID string) []any {
	return append(workspaceScopedVisibilityArgs(userID), userID)
}

// knowledgeBaseWritePredicate admits personal owners/collaborators, workspace
// admins (canonical owner plus admin-role members) and current workspace KB
// creators. Other non-guest members must pass both the member-level and
// per-library add-file permissions. Guests can never upload.
func knowledgeBaseWritePredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND (` + prefix + `user_id=? OR EXISTS (` +
		`SELECT 1 FROM knowledge_base_shares kb_write_share ` +
		`WHERE kb_write_share.kb_id=` + prefix + `id AND kb_write_share.user_id=? AND kb_write_share.role='write'` +
		`))) OR (COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces kb_write_workspace ` +
		`WHERE kb_write_workspace.id=` + prefix + `workspace_id AND (` +
		`kb_write_workspace.owner_id=? OR EXISTS (` +
		`SELECT 1 FROM workspace_members kb_write_admin ` +
		`WHERE kb_write_admin.workspace_id=kb_write_workspace.id AND kb_write_admin.user_id=? ` +
		`AND ` + isAdminRoleSQL("kb_write_admin.role") + ` ` +
		`) OR (` + prefix + `user_id=? AND EXISTS (` +
		`SELECT 1 FROM workspace_members kb_write_creator ` +
		`WHERE kb_write_creator.workspace_id=kb_write_workspace.id AND kb_write_creator.user_id=?` +
		`)) OR EXISTS (` +
		`SELECT 1 FROM workspace_members kb_write_member ` +
		`WHERE kb_write_member.workspace_id=kb_write_workspace.id AND kb_write_member.user_id=? ` +
		`AND ` + isCollaboratorRoleSQL("kb_write_member.role") + ` ` +
		`AND kb_write_member.can_add_kb_files=1 AND COALESCE((` +
		`SELECT permission.can_add_files FROM workspace_kb_member_permissions permission ` +
		`WHERE permission.kb_id=` + prefix + `id AND permission.user_id=?` +
		`),1)=1` +
		`)` +
		`)` +
		`)))`
}

func knowledgeBaseWriteArgs(userID string) []any {
	return []any{userID, userID, userID, userID, userID, userID, userID, userID}
}

// workspaceKnowledgeBaseContentDeletePredicate is the manage-any-content
// boundary of a workspace knowledge base: workspace admins (canonical owner
// plus admin-role members) and the library's current creator may delete or
// retry ANY document. Ordinary members delete only their own uploads (see
// knowledgeBaseDocumentMutationPredicate).
func workspaceKnowledgeBaseContentDeletePredicate(alias string) string {
	prefix := alias + "."
	return `COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (
		SELECT 1 FROM workspaces kb_delete_workspace
		 WHERE kb_delete_workspace.id=` + prefix + `workspace_id AND (
		   kb_delete_workspace.owner_id=? OR EXISTS (
		     SELECT 1 FROM workspace_members kb_delete_admin
		      WHERE kb_delete_admin.workspace_id=kb_delete_workspace.id
		        AND kb_delete_admin.user_id=?
		        AND ` + isAdminRoleSQL("kb_delete_admin.role") + `
		   ) OR (
		     ` + prefix + `user_id=? AND EXISTS (
		       SELECT 1 FROM workspace_members kb_delete_creator
		        WHERE kb_delete_creator.workspace_id=kb_delete_workspace.id
		          AND kb_delete_creator.user_id=?
		     )
		   )
		 )
	)`
}

func workspaceKnowledgeBaseDeleteArgs(userID string) []any {
	return []any{userID, userID, userID, userID}
}

func knowledgeBaseDeletePredicate(alias string) string {
	prefix := alias + "."
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND ` + prefix + `user_id=?) OR (
		COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (
		  SELECT 1 FROM workspaces kb_object_delete_workspace
		   WHERE kb_object_delete_workspace.id=` + prefix + `workspace_id AND (
		     kb_object_delete_workspace.owner_id=? OR EXISTS (
		       SELECT 1 FROM workspace_members kb_object_delete_admin
		        WHERE kb_object_delete_admin.workspace_id=kb_object_delete_workspace.id
		          AND kb_object_delete_admin.user_id=?
		          AND ` + isAdminRoleSQL("kb_object_delete_admin.role") + `
		     ) OR (
		       ` + prefix + `user_id=? AND EXISTS (
		         SELECT 1 FROM workspace_members kb_object_delete_creator
		          WHERE kb_object_delete_creator.workspace_id=kb_object_delete_workspace.id
		            AND kb_object_delete_creator.user_id=?
		       )
		     )
		   )
		)
	))`
}

func knowledgeBaseDeleteArgs(userID string) []any {
	return []any{userID, userID, userID, userID, userID}
}

// knowledgeBaseDocumentMutationPredicate protects document-level mutations.
// Workspace content: admins and the library creator manage every document;
// ordinary non-guest members may only mutate documents they uploaded
// themselves, and only while their member-level and per-library delete
// permissions allow it. Personal owners manage every document while write
// collaborators may only mutate documents they uploaded themselves.
func knowledgeBaseDocumentMutationPredicate(kbAlias, documentAlias string) string {
	return `((` + workspaceKnowledgeBaseContentDeletePredicate(kbAlias) + `) OR (` +
		`COALESCE(` + kbAlias + `.workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces document_delete_workspace` +
		` JOIN workspace_members document_delete_member ON document_delete_member.workspace_id=document_delete_workspace.id` +
		` WHERE document_delete_workspace.id=` + kbAlias + `.workspace_id` +
		`   AND document_delete_member.user_id=?` +
		`   AND ` + isCollaboratorRoleSQL("document_delete_member.role") +
		`   AND document_delete_member.can_delete_kb_content=1` +
		`   AND COALESCE((SELECT permission.can_delete_content` +
		`     FROM workspace_kb_member_permissions permission` +
		`    WHERE permission.kb_id=` + kbAlias + `.id AND permission.user_id=?),1)=1` +
		`   AND ` + documentAlias + `.uploaded_by_user_id=?` +
		`)) OR (` +
		`COALESCE(` + kbAlias + `.workspace_id,'')='' AND (` + kbAlias + `.user_id=? OR EXISTS (` +
		`SELECT 1 FROM knowledge_base_shares document_mutation_share ` +
		`WHERE document_mutation_share.kb_id=` + kbAlias + `.id AND document_mutation_share.user_id=? ` +
		`AND document_mutation_share.role='write' AND ` + documentAlias + `.uploaded_by_user_id=?` +
		`))` +
		`))`
}

func knowledgeBaseDocumentMutationArgs(userID string) []any {
	return []any{
		userID, userID, userID, userID, // manage-any (admin/creator)
		userID, userID, userID, // own-upload member path
		userID, userID, userID, // personal owner / write share
	}
}

// ListKBs returns the user's knowledge bases.
// CountStandaloneKBsByUser counts a user's standalone knowledge bases -- those
// not backing a project (§ user-group caps). Workspace KBs created by the user
// count while the user remains an authoritative workspace principal. Project
// libraries are created implicitly and remain governed by the project cap.
func CountStandaloneKBsByUser(ctx context.Context, db *sql.DB, userID string) (int, error) {
	return countStandaloneKBsByUser(ctx, db, userID)
}

func countStandaloneKBsByUser(ctx context.Context, q commercialCapQueryer, userID string) (int, error) {
	var n int
	args := []any{userID}
	args = append(args, workspaceScopedVisibilityArgs(userID)...)
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM knowledge_bases
		  WHERE user_id=? AND (project_id IS NULL OR project_id='')
		    AND NOT EXISTS (
		      SELECT 1 FROM projects commercial_project_library
		       WHERE commercial_project_library.kb_id=knowledge_bases.id
		    )
		    AND `+workspaceScopedVisibilityPredicate("knowledge_bases"), args...).Scan(&n)
	return n, err
}

func ListKBs(ctx context.Context, db *sql.DB, userID string) ([]KnowledgeBase, error) {
	// Personal listing only — workspace KBs are isolated (§workspaces) and listed
	// via ListWorkspaceKBs. A project's dedicated library is managed exclusively
	// through the project API and must never appear as a standalone KB.
	rows, err := db.QueryContext(ctx,
		`SELECT k.id, k.user_id, k.name, k.description, k.embedding_model_id, k.embedding_dim, COALESCE(k.project_id, ''), k.created_at, COALESCE(k.workspace_id,''),
		        COALESCE(owner.name,''), CASE WHEN k.user_id=? THEN 'owner' ELSE COALESCE(s.role,'') END
		   FROM knowledge_bases k
		   LEFT JOIN knowledge_base_shares s ON s.kb_id=k.id AND s.user_id=?
		   LEFT JOIN users owner ON owner.id=k.user_id
		  WHERE COALESCE(k.workspace_id,'')='' AND (k.user_id=? OR s.user_id=?)
		    AND `+standaloneKnowledgeBasePredicate("k")+`
		  ORDER BY k.created_at DESC`, userID, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KnowledgeBase{}
	for rows.Next() {
		kb, err := scanKBWithAccess(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, kb)
	}
	return out, rows.Err()
}

// ListWorkspaceKBs lists a workspace's shared KBs without a caller boundary. It
// is reserved for administrator/maintenance views. User-facing callers must use
// ListWorkspaceKBsForUser.
func ListWorkspaceKBs(ctx context.Context, db *sql.DB, workspaceID string) ([]KnowledgeBase, error) {
	return listWorkspaceKBs(ctx, db, workspaceID, "")
}

// ListWorkspaceKBsForUser lists shared KBs only while userID is the canonical
// workspace owner or a current member.
func ListWorkspaceKBsForUser(ctx context.Context, db *sql.DB, workspaceID, userID string) ([]KnowledgeBase, error) {
	return listWorkspaceKBs(ctx, db, workspaceID, userID)
}

func listWorkspaceKBs(ctx context.Context, db *sql.DB, workspaceID, userID string) ([]KnowledgeBase, error) {
	q := `SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id, ''), created_at, COALESCE(workspace_id,''), COALESCE(is_public,1)
		 FROM knowledge_bases WHERE workspace_id=?
		   AND ` + standaloneKnowledgeBasePredicate("knowledge_bases")
	args := []any{workspaceID}
	if userID != "" {
		q += ` AND ` + workspaceScopedVisibilityPredicate("knowledge_bases")
		args = append(args, workspaceScopedVisibilityArgs(userID)...)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := db.QueryContext(ctx,
		q, args...)
	if err != nil {
		return nil, err
	}
	out := []KnowledgeBase{}
	for rows.Next() {
		kb, err := scanKB(rows)
		if err != nil {
			return nil, err
		}
		kb.AccessRole = "workspace"
		out = append(out, kb)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if userID != "" {
		for i := range out {
			if err := enrichKnowledgeBasePermissions(ctx, db, &out[i], userID); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// standaloneKnowledgeBasePredicate keeps project-owned libraries behind the
// project boundary. New rows carry project_id; the reverse projects.kb_id check
// also protects legacy libraries created before that marker was persisted.
func standaloneKnowledgeBasePredicate(alias string) string {
	return `COALESCE(` + alias + `.project_id,'')='' AND NOT EXISTS (
		SELECT 1 FROM projects standalone_project_library
		 WHERE standalone_project_library.kb_id=` + alias + `.id
	)`
}

// OwnedKBIDs filters ids down to the ones the user may retrieve from (§C1 — the
// retrieval scope must never include another user's KB). workspaceID steers the
// scope (§workspaces): ” admits only the user's PERSONAL KBs; set, it admits
// only THAT workspace's shared KBs — personal KBs are unusable inside a
// workspace and vice versa. On a DB error it fails closed (returns none).
func OwnedKBIDs(ctx context.Context, db *sql.DB, userID, workspaceID string, ids []string) []string {
	if len(ids) == 0 {
		return ids
	}
	ph := make([]string, len(ids))
	args := make([]any, 0, len(ids)+4)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	// Project libraries are attached through the conversation's project scope and
	// must never be accepted as optional KB selections, including the legacy
	// compatibility path used by older clients.
	scope := standaloneKnowledgeBasePredicate("knowledge_bases") + ` AND COALESCE(knowledge_bases.workspace_id,'')='' AND ` + knowledgeBaseAccessPredicate("knowledge_bases")
	if workspaceID != "" {
		scope = standaloneKnowledgeBasePredicate("knowledge_bases") + ` AND knowledge_bases.workspace_id=? AND ` + workspaceScopedVisibilityPredicate("knowledge_bases")
		args = append(args, workspaceID)
		args = append(args, workspaceScopedVisibilityArgs(userID)...)
	} else {
		args = append(args, knowledgeBaseAccessArgs(userID)...)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM knowledge_bases WHERE id IN (`+strings.Join(ph, ",")+`) AND `+scope, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	owned := make([]string, 0, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			owned = append(owned, id)
		}
	}
	return owned
}

// ResolveOwnedKBIDs strictly resolves an explicit knowledge-base selection.
// Unlike OwnedKBIDs, it returns database errors and rejects the whole selection
// when any id is missing, inaccessible, or belongs to a project. Returned ids
// are trimmed, deduplicated, and kept in the caller's order.
func ResolveOwnedKBIDs(ctx context.Context, db *sql.DB, userID, workspaceID string, ids []string) ([]string, error) {
	normalized := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, ErrNotFound
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return []string{}, nil
	}

	ph := make([]string, len(normalized))
	args := make([]any, 0, len(normalized)+4)
	for i, id := range normalized {
		ph[i] = "?"
		args = append(args, id)
	}
	scope := `COALESCE(knowledge_bases.workspace_id,'')='' AND ` + knowledgeBaseAccessPredicate("knowledge_bases")
	if workspaceID != "" {
		scope = `knowledge_bases.workspace_id=? AND ` + workspaceScopedVisibilityPredicate("knowledge_bases")
		args = append(args, workspaceID)
		args = append(args, workspaceScopedVisibilityArgs(userID)...)
	} else {
		args = append(args, knowledgeBaseAccessArgs(userID)...)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM knowledge_bases
		  WHERE id IN (`+strings.Join(ph, ",")+`)
		    AND `+standaloneKnowledgeBasePredicate("knowledge_bases")+`
		    AND `+scope,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	allowed := make(map[string]bool, len(normalized))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		allowed[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range normalized {
		if !allowed[id] {
			return nil, ErrNotFound
		}
	}
	return normalized, nil
}

// ValidateKBEmbeddingCompatibility rejects a selection whose indexes cannot be
// queried with one embedding vector. The caller must pass an already
// access-filtered list (for example OwnedKBIDs); both model identity and actual
// stored dimension are part of the vector-space signature.
func ValidateKBEmbeddingCompatibility(ctx context.Context, db *sql.DB, ids []string) error {
	if len(ids) < 2 {
		return nil
	}
	ph := make([]string, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT embedding_model_id, embedding_dim FROM knowledge_bases WHERE id IN (`+strings.Join(ph, ",")+`)`,
		args...,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	modelID := ""
	dim := 0
	seen := 0
	for rows.Next() {
		var currentModel string
		var currentDim int
		if err := rows.Scan(&currentModel, &currentDim); err != nil {
			return err
		}
		if seen == 0 {
			modelID, dim = currentModel, currentDim
		} else if currentModel != modelID || currentDim != dim {
			return ErrMixedKBEmbeddingModels
		}
		seen++
	}
	return rows.Err()
}

// GetKB reads one row through the authoritative personal/workspace scope. A
// workspace KB's original creator has no access after membership is revoked.
func GetKB(ctx context.Context, db *sql.DB, id, userID string) (*KnowledgeBase, error) {
	args := []any{id}
	args = append(args, knowledgeBaseAccessArgs(userID)...)
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id, ''), created_at, COALESCE(workspace_id,''), COALESCE(is_public,1) FROM knowledge_bases WHERE id=? AND `+knowledgeBaseAccessPredicate("knowledge_bases"), args...)
	kb, err := scanKB(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := enrichKnowledgeBaseForUser(ctx, db, &kb, userID); err != nil {
		return nil, err
	}
	return &kb, nil
}

// GetStandaloneKB returns one user-facing library without relying on whichever
// personal/workspace list the client currently has selected. Project-owned
// libraries remain reachable only through their project APIs, including legacy
// rows whose project_id marker is empty but are referenced by projects.kb_id.
func GetStandaloneKB(ctx context.Context, db *sql.DB, id, userID string) (*KnowledgeBase, error) {
	args := []any{id}
	args = append(args, knowledgeBaseAccessArgs(userID)...)
	var visible int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM knowledge_bases
		  WHERE id=?
		    AND `+knowledgeBaseAccessPredicate("knowledge_bases")+`
		    AND `+standaloneKnowledgeBasePredicate("knowledge_bases"), args...).Scan(&visible)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return GetKB(ctx, db, id, userID)
}

// ListKnowledgeBaseAccessUserIDs snapshots every user that can currently see a
// knowledge base. The visibility predicate must stay exact: workspace guests
// and members see shared libraries, while a private library remains with its
// creator plus workspace administrators. Handlers use this before and after a
// visibility change to revoke only principals that actually lost access.
func ListKnowledgeBaseAccessUserIDs(ctx context.Context, db *sql.DB, id string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT access_user_id FROM (
			SELECT k.user_id AS access_user_id
			  FROM knowledge_bases k WHERE k.id=?
			UNION ALL
			SELECT s.user_id AS access_user_id
			  FROM knowledge_base_shares s
			  JOIN knowledge_bases k ON k.id=s.kb_id
			 WHERE s.kb_id=? AND COALESCE(k.workspace_id,'')=''
			UNION ALL
			SELECT m.user_id AS access_user_id
			  FROM knowledge_bases k
			  JOIN workspace_members m ON m.workspace_id=k.workspace_id
			 WHERE k.id=? AND COALESCE(k.workspace_id,'')<>'' AND COALESCE(k.is_public,1)=1
			UNION ALL
			SELECT m.user_id AS access_user_id
			  FROM knowledge_bases k
			  JOIN workspace_members m ON m.workspace_id=k.workspace_id
			 WHERE k.id=? AND COALESCE(k.workspace_id,'')<>''
			   AND `+isAdminRoleSQL("m.role")+`
			UNION ALL
			SELECT w.owner_id AS access_user_id
			  FROM knowledge_bases k JOIN workspaces w ON w.id=k.workspace_id
			 WHERE k.id=? AND COALESCE(k.workspace_id,'')<>''
		) knowledge_base_access_users
		WHERE TRIM(access_user_id)<>''
		ORDER BY access_user_id`, id, id, id, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	userIDs := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return nil, ErrNotFound
	}
	return userIDs, nil
}

func enrichKnowledgeBaseForUser(ctx context.Context, q rowQueryer, kb *KnowledgeBase, userID string) error {
	if kb == nil || userID == "" {
		return nil
	}
	if kb.WorkspaceID != "" {
		if err := enrichKnowledgeBasePermissions(ctx, q, kb, userID); err != nil {
			return err
		}
	} else if kb.UserID == userID {
		kb.AccessRole = "owner"
		kb.CanShare = true
		kb.CanUpload = true
		kb.CanDelete = true
		kb.CanDeleteContent = true
	} else {
		if err := q.QueryRowContext(ctx,
			`SELECT role FROM knowledge_base_shares WHERE kb_id=? AND user_id=?`, kb.ID, userID,
		).Scan(&kb.AccessRole); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		kb.CanUpload = kb.AccessRole == "write"
	}
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(name,'') FROM users WHERE id=?`, kb.UserID,
	).Scan(&kb.OwnerName); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func enrichKnowledgeBasePermissions(ctx context.Context, q rowQueryer, kb *KnowledgeBase, userID string) error {
	if kb == nil || kb.WorkspaceID == "" || userID == "" {
		return nil
	}
	args := []any{kb.ID}
	args = append(args, knowledgeBaseWriteArgs(userID)...)
	args = append(args, kb.ID)
	args = append(args, workspaceKnowledgeBaseDeleteArgs(userID)...)
	args = append(args, kb.ID)
	args = append(args, knowledgeBaseDeleteArgs(userID)...)
	args = append(args, kb.ID)
	args = append(args, workspaceResourceManagerArgs(userID)...)
	var canUpload, canDeleteContent, canDelete, canManageMembers int
	err := q.QueryRowContext(ctx, `SELECT
		CASE WHEN EXISTS (SELECT 1 FROM knowledge_bases permission_upload WHERE permission_upload.id=? AND `+knowledgeBaseWritePredicate("permission_upload")+`) THEN 1 ELSE 0 END,
		CASE WHEN EXISTS (SELECT 1 FROM knowledge_bases permission_content WHERE permission_content.id=? AND `+workspaceKnowledgeBaseContentDeletePredicate("permission_content")+`) THEN 1 ELSE 0 END,
		CASE WHEN EXISTS (SELECT 1 FROM knowledge_bases permission_delete WHERE permission_delete.id=? AND `+knowledgeBaseDeletePredicate("permission_delete")+`) THEN 1 ELSE 0 END,
		CASE WHEN EXISTS (SELECT 1 FROM knowledge_bases permission_manage WHERE permission_manage.id=? AND `+workspaceResourceManagerPredicate("permission_manage")+`) THEN 1 ELSE 0 END`, args...).Scan(
		&canUpload, &canDeleteContent, &canDelete, &canManageMembers,
	)
	if err != nil {
		return err
	}
	kb.AccessRole = "workspace"
	kb.CanUpload = canUpload == 1
	kb.CanDeleteContent = canDeleteContent == 1
	kb.CanDelete = canDelete == 1
	kb.CanManageMembers = canManageMembers == 1
	return nil
}

// GetKBByName returns a user's KB by case-insensitive, trimmed name.
func GetKBByName(ctx context.Context, db *sql.DB, userID, name string) (*KnowledgeBase, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id, ''), created_at, COALESCE(workspace_id,''), COALESCE(is_public,1)
		 FROM knowledge_bases
		 WHERE user_id=? AND COALESCE(workspace_id,'')=''
		   AND lower(trim(name))=lower(trim(?)) LIMIT 1`,
		userID, name)
	kb, err := scanKB(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &kb, nil
}

func scanKB(s scanner) (KnowledgeBase, error) {
	var kb KnowledgeBase
	if err := s.Scan(&kb.ID, &kb.UserID, &kb.Name, &kb.Description, &kb.EmbeddingModelID, &kb.EmbeddingDim, &kb.ProjectID, &kb.CreatedAt, &kb.WorkspaceID, &kb.IsPublic); err != nil {
		return kb, err
	}
	return kb, nil
}

func scanKBWithAccess(s scanner) (KnowledgeBase, error) {
	var kb KnowledgeBase
	if err := s.Scan(&kb.ID, &kb.UserID, &kb.Name, &kb.Description, &kb.EmbeddingModelID, &kb.EmbeddingDim, &kb.ProjectID, &kb.CreatedAt, &kb.WorkspaceID, &kb.OwnerName, &kb.AccessRole); err != nil {
		return kb, err
	}
	kb.CanShare = kb.AccessRole == "owner"
	kb.CanUpload = kb.AccessRole == "owner" || kb.AccessRole == "write"
	kb.CanDelete = kb.AccessRole == "owner"
	kb.CanDeleteContent = kb.AccessRole == "owner"
	return kb, nil
}

// CreateKB inserts a row.
func CreateKB(ctx context.Context, db *sql.DB, kb KnowledgeBase) (*KnowledgeBase, error) {
	if kb.ID == "" {
		kb.ID = genID("kb")
	}
	kb.Name = strings.TrimSpace(kb.Name)
	kb.Description = strings.TrimSpace(kb.Description)
	var pid any
	if kb.ProjectID == "" {
		pid = nil
	} else {
		pid = kb.ProjectID
	}
	now := time.Now().Unix()
	var err error
	if kb.WorkspaceID == "" {
		_, err = db.ExecContext(ctx,
			`INSERT INTO knowledge_bases(id, user_id, name, description, embedding_model_id, embedding_dim, project_id, created_at, workspace_id, is_public) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			kb.ID, kb.UserID, kb.Name, kb.Description, kb.EmbeddingModelID, kb.EmbeddingDim, pid, now, kb.WorkspaceID, boolInt(kb.IsPublic))
	} else {
		tx, txErr := beginWorkspaceMutationTx(ctx, db, kb.WorkspaceID)
		if txErr != nil {
			return nil, txErr
		}
		defer tx.Rollback() //nolint:errcheck
		var result sql.Result
		result, err = tx.ExecContext(ctx,
			`INSERT INTO knowledge_bases(id, user_id, name, description, embedding_model_id, embedding_dim, project_id, created_at, workspace_id, is_public)
			 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			   FROM workspaces create_workspace
			  WHERE create_workspace.id=?
			    AND `+workspaceAcceptsResourceCreationPredicate("create_workspace")+`
				    AND `+workspaceMemberCapabilityPredicate("create_workspace", "can_create_kb"),
			kb.ID, kb.UserID, kb.Name, kb.Description, kb.EmbeddingModelID, kb.EmbeddingDim, pid, now, kb.WorkspaceID, boolInt(kb.IsPublic),
			kb.WorkspaceID, kb.UserID, kb.UserID, kb.UserID)
		if err != nil {
			if isUniqueIndexErr(err, "idx_kbs_user_name_unique", "knowledge_bases.user_id") {
				return nil, ErrKBNameExists
			}
			return nil, err
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if affected != 1 {
			return nil, ErrNotFound
		}
		created, scanErr := scanKB(tx.QueryRowContext(ctx,
			`SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id,''), created_at, COALESCE(workspace_id,''), COALESCE(is_public,1)
			   FROM knowledge_bases WHERE id=?`, kb.ID))
		if scanErr != nil {
			return nil, scanErr
		}
		if err := enrichKnowledgeBaseForUser(ctx, tx, &created, kb.UserID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &created, nil
	}
	if err != nil {
		if isUniqueIndexErr(err, "idx_kbs_user_name_unique", "knowledge_bases.user_id") {
			return nil, ErrKBNameExists
		}
		return nil, err
	}
	return GetKB(ctx, db, kb.ID, kb.UserID)
}

// CreateKBWithLimit atomically re-evaluates the creator's standalone-KB cap
// and inserts a KB. Project libraries must use CreateKB because their lifecycle
// is governed by the project cap rather than this standalone resource cap.
func CreateKBWithLimit(ctx context.Context, db *sql.DB, kb KnowledgeBase, maxKBs int) (*KnowledgeBase, error) {
	if kb.ID == "" {
		kb.ID = genID("kb")
	}
	kb.Name = strings.TrimSpace(kb.Name)
	kb.Description = strings.TrimSpace(kb.Description)
	var pid any
	if kb.ProjectID == "" {
		pid = nil
	} else {
		pid = kb.ProjectID
	}
	now := time.Now().Unix()

	var tx *sql.Tx
	var err error
	if kb.WorkspaceID == "" {
		tx, err = db.BeginTx(ctx, nil)
	} else {
		tx, err = beginWorkspaceMutationTx(ctx, db, kb.WorkspaceID)
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	if kb.WorkspaceID != "" {
		if err := validateWorkspaceResourceCreationTx(ctx, tx, kb.WorkspaceID, kb.UserID, "can_create_kb"); err != nil {
			return nil, err
		}
	}
	if err := lockCommercialCapUserTx(ctx, tx, kb.UserID); err != nil {
		return nil, err
	}
	if maxKBs > 0 && kb.ProjectID == "" {
		n, err := countStandaloneKBsByUser(ctx, tx, kb.UserID)
		if err != nil {
			return nil, err
		}
		if n >= maxKBs {
			return nil, ErrKBLimitExceeded
		}
	}

	if kb.WorkspaceID == "" {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO knowledge_bases(id, user_id, name, description, embedding_model_id, embedding_dim, project_id, created_at, workspace_id, is_public) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			kb.ID, kb.UserID, kb.Name, kb.Description, kb.EmbeddingModelID, kb.EmbeddingDim, pid, now, kb.WorkspaceID, boolInt(kb.IsPublic))
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx,
			`INSERT INTO knowledge_bases(id, user_id, name, description, embedding_model_id, embedding_dim, project_id, created_at, workspace_id, is_public)
			 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			   FROM workspaces create_workspace
			  WHERE create_workspace.id=?
			    AND `+workspaceAcceptsResourceCreationPredicate("create_workspace")+`
				    AND `+workspaceMemberCapabilityPredicate("create_workspace", "can_create_kb"),
			kb.ID, kb.UserID, kb.Name, kb.Description, kb.EmbeddingModelID, kb.EmbeddingDim, pid, now, kb.WorkspaceID, boolInt(kb.IsPublic),
			kb.WorkspaceID, kb.UserID, kb.UserID, kb.UserID)
		if err == nil {
			if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
				return nil, rowsErr
			} else if affected != 1 {
				return nil, ErrNotFound
			}
		}
	}
	if err != nil {
		if isUniqueIndexErr(err, "idx_kbs_user_name_unique", "knowledge_bases.user_id") {
			return nil, ErrKBNameExists
		}
		return nil, err
	}
	created, err := scanKB(tx.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id,''), created_at, COALESCE(workspace_id,''), COALESCE(is_public,1)
		   FROM knowledge_bases WHERE id=?`, kb.ID))
	if err != nil {
		return nil, err
	}
	if err := enrichKnowledgeBaseForUser(ctx, tx, &created, kb.UserID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateKBVisibility flips a workspace knowledge base between private and
// workspace-shared (§workspace RBAC phase 2). Only the library creator or a
// workspace admin may change it; personal libraries have no visibility scope.
// The change re-authorizes inside the workspace-membership transaction.
func UpdateKBVisibility(ctx context.Context, db *sql.DB, kbID, userID string, isPublic bool) (*KnowledgeBase, error) {
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(workspace_id,'') FROM knowledge_bases WHERE id=?`, kbID,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if workspaceID == "" {
		return nil, ErrNotFound
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	updateArgs := []any{boolInt(isPublic), kbID}
	updateArgs = append(updateArgs, workspaceResourceManagerArgs(userID)...)
	res, err := tx.ExecContext(ctx,
		`UPDATE knowledge_bases SET is_public=?
		  WHERE id=? AND COALESCE(workspace_id,'')<>'' AND `+workspaceResourceManagerPredicate("knowledge_bases"),
		updateArgs...)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, userID, AuditResourceVisibilityChanged,
		"knowledge_base", kbID, map[string]any{"is_public": isPublic}); err != nil {
		return nil, err
	}
	var updated KnowledgeBase
	if err := tx.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id, ''), created_at, COALESCE(workspace_id,''), COALESCE(is_public,1)
		   FROM knowledge_bases WHERE id=?`, kbID,
	).Scan(&updated.ID, &updated.UserID, &updated.Name, &updated.Description,
		&updated.EmbeddingModelID, &updated.EmbeddingDim, &updated.ProjectID,
		&updated.CreatedAt, &updated.WorkspaceID, &updated.IsPublic); err != nil {
		return nil, err
	}
	if err := enrichKnowledgeBaseForUser(ctx, tx, &updated, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &updated, nil
}

// SetKBEmbeddingDim corrects the stored vector width for a KB. Called during
// ingest when the embedding model's actual output dimension differs from what
// was configured, so retrieval resolves the same (real) dim and hits the right
// Qdrant collection.
func SetKBEmbeddingDim(ctx context.Context, db *sql.DB, kbID string, dim int) error {
	_, err := db.ExecContext(ctx, `UPDATE knowledge_bases SET embedding_dim=? WHERE id=?`, dim, kbID)
	return err
}

// DeleteKB removes the KB and cascades to documents/chunks. It also removes
// the deleted KB's ID from the kb_ids JSON array in all conversations so stale
// references don't cause retrieval errors (§ FIX-5).
// storageRoots are optional for backwards-compatible store callers. Physical
// cleanup is skipped when they are omitted; API handlers should pass the
// configured upload and artifact roots explicitly.
func DeleteKB(ctx context.Context, db *sql.DB, id, userID string, storageRoots ...string) error {
	workspaceID, err := knowledgeBaseWorkspaceID(ctx, db, id)
	if err != nil {
		return err
	}
	tx, err := beginKnowledgeBaseMutationTx(ctx, db, id, workspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var mutationDB kbMutationDB = tx

	// Collect the KB's document files BEFORE the delete so we can remove them
	// from disk afterwards — the DB rows cascade away (documents → chunks via
	// FK ON DELETE CASCADE), but the stored files on disk would otherwise be
	// orphaned. Best-effort; a query error just skips disk cleanup.
	var diskPaths []string
	if rows, qerr := mutationDB.QueryContext(ctx, `SELECT storage_path FROM documents WHERE kb_id=? AND storage_path<>''`, id); qerr == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && p != "" {
				diskPaths = append(diskPaths, p)
			}
		}
		rows.Close()
	}
	// Personal owner, workspace owner, or the KB creator while they remain a
	// member. Other workspace members may query and upload, but cannot destroy a
	// shared library. The manager check is part of the DELETE so a concurrent kick
	// cannot be bypassed by the handler.
	args := []any{id}
	args = append(args, knowledgeBaseDeleteArgs(userID)...)
	res, err := mutationDB.ExecContext(ctx,
		`DELETE FROM knowledge_bases
		  WHERE id=?
		    AND `+standaloneKnowledgeBasePredicate("knowledge_bases")+`
		    AND `+knowledgeBaseDeletePredicate("knowledge_bases"),
		args...,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	// Clean up kb_ids references in conversations. kb_ids is stored as a JSON
	// TEXT array in both SQLite and Postgres. We use json_each to rebuild the
	// array without the deleted KB's ID (raw query — not in query files because
	// this dialect-switch logic would be awkward in sqlc templates).
	if IsPostgres() {
		// Postgres: use json_agg + json_array_elements_text to filter the array.
		_, _ = mutationDB.ExecContext(ctx, `
			UPDATE conversations
			SET kb_ids = COALESCE(
				(SELECT json_agg(value ORDER BY ordinality)
				 FROM json_array_elements_text(kb_ids::json) WITH ORDINALITY
				 WHERE value != $1),
				'[]'::json
			)::text
			WHERE kb_ids LIKE '%' || $1 || '%'
		`, id)
	} else {
		// SQLite: use json_each + json_group_array to rebuild without the deleted ID.
		_, _ = mutationDB.ExecContext(ctx, `
			UPDATE conversations
			SET kb_ids = (
				SELECT COALESCE(json_group_array(value), '[]')
				FROM json_each(kb_ids)
				WHERE value != ?
			)
			WHERE json_type(kb_ids) = 'array' AND kb_ids LIKE '%' || ? || '%'
		`, id, id)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// The KB row (and, via cascade, its documents + chunks) is gone — now remove
	// local document files only when no remaining DB row still references the same
	// storage path. S3/OSS cleanup is orchestrated by the API layer.
	for _, p := range diskPaths {
		ref, rerr := StoragePathReferenced(context.Background(), db, p)
		if rerr != nil {
			log.Printf("delete kb %s: check storage refs for %q: %v", id, p, rerr)
			continue
		}
		if ref {
			continue
		}
		if rmErr := removeLocalStoragePath(p, storageRoots...); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Printf("delete kb %s: remove file %q: %v", id, p, rmErr)
		}
	}
	return nil
}

type kbMutationDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func knowledgeBaseWorkspaceID(ctx context.Context, db *sql.DB, id string) (string, error) {
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(workspace_id,'') FROM knowledge_bases WHERE id=?`, id,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return workspaceID, nil
}

// beginKnowledgeBaseMutationTx serializes personal-library share changes with
// every write that depends on those shares. Workspace libraries keep using the
// workspace membership lock because membership and per-library permissions are
// their authoritative access boundary.
func beginKnowledgeBaseMutationTx(ctx context.Context, db *sql.DB, kbID, workspaceID string) (*sql.Tx, error) {
	if workspaceID != "" {
		return beginWorkspaceMutationTx(ctx, db, workspaceID)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	var ownerID string
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id FROM knowledge_bases WHERE id=? AND COALESCE(workspace_id,'')=''`, kbID,
	).Scan(&ownerID); err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Keep the same user -> resource lock order as DeleteUser. Besides avoiding
	// a Postgres deadlock, this prevents a write/share change from committing
	// after the owner account and its personal library have been deleted.
	if err := lockKnowledgeBaseOwnerTx(ctx, tx, ownerID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := lockPersonalKnowledgeBaseTx(ctx, tx, kbID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func lockKnowledgeBaseOwnerTx(ctx context.Context, tx *sql.Tx, ownerID string) error {
	res, err := tx.ExecContext(ctx, `UPDATE users SET id=id WHERE id=? AND status='active'`, ownerID)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// lockPersonalKnowledgeBaseTx is the non-workspace-library serialization point.
// A collaborator mutation that gets this lock before a role downgrade/revoke
// may finish; one that gets it afterwards observes the new share state and
// fails closed. Project libraries also use the row lock, but their separate
// project boundary still decides whether the mutation is authorized.
func lockPersonalKnowledgeBaseTx(ctx context.Context, tx *sql.Tx, kbID string) error {
	res, err := tx.ExecContext(ctx, `UPDATE knowledge_bases SET id=id
		WHERE id=? AND COALESCE(workspace_id,'')=''`, kbID)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// ListDocuments lists documents for either a KB or a conversation. Scope is
// "kb" or "conversation" — empty returns all the user's own (joined via FK).
// ConversationHasReadyDocs reports whether a conversation has at least one
// successfully-ingested (retrievable) document — used to decide whether to run
// inline RAG even when no knowledge base is bound (§C/§4.11-B chat uploads).
func ConversationHasReadyDocs(ctx context.Context, db *sql.DB, convID string) bool {
	var n int
	_ = db.QueryRowContext(ctx,
		`SELECT 1 FROM documents d
		  WHERE d.conversation_id=? AND d.status='ready'
		    AND NOT EXISTS (
		      SELECT 1 FROM files draft_visibility_file
		       WHERE draft_visibility_file.storage_path=d.storage_path
		         AND draft_visibility_file.draft=1
		    )
		  LIMIT 1`, convID).Scan(&n)
	return n == 1
}

// ConversationDocReady reports whether a conversation-scoped document with this
// filename has finished RAG ingestion (status=ready). Used to skip re-sending a
// PDF as a slow native `document` block when its text is already retrievable via
// RAG (§4.6 / §perf — native PDF processing is minutes for a large file).
func ConversationDocReady(ctx context.Context, db *sql.DB, convID, filename string) bool {
	if convID == "" || filename == "" {
		return false
	}
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM documents d
		  WHERE d.conversation_id=? AND d.filename=? AND d.status='ready'
		    AND NOT EXISTS (
		      SELECT 1 FROM files draft_visibility_file
		       WHERE draft_visibility_file.storage_path=d.storage_path
		         AND draft_visibility_file.draft=1
		    )
		  LIMIT 1`,
		convID, filename).Scan(&n)
	return err == nil && n == 1
}

// ConversationHasPendingDocs reports whether a conversation has a document still
// being ingested (pending/parsing/embedding). Used by admin/maintenance views and
// old safety checks; normal sends validate the attached document ids directly.
func ConversationHasPendingDocs(ctx context.Context, db *sql.DB, convID string) bool {
	var n int
	_ = db.QueryRowContext(ctx,
		`SELECT 1 FROM documents d
		  WHERE d.conversation_id=? AND d.status IN ('pending','parsing','embedding')
		    AND NOT EXISTS (
		      SELECT 1 FROM files draft_visibility_file
		       WHERE draft_visibility_file.storage_path=d.storage_path
		         AND draft_visibility_file.draft=1
		    )
		  LIMIT 1`, convID).Scan(&n)
	return n == 1
}

// ConversationDocumentStatuses returns the status for conversation-scoped
// documents by id. The message handler uses it as a server-side guard so a stale
// or hand-written client cannot start generation before the attached document's
// RAG ingest is actually ready.
func ConversationDocumentStatuses(ctx context.Context, db *sql.DB, convID string, docIDs []string) (map[string]string, error) {
	docIDs = cleanIDs(docIDs)
	out := make(map[string]string, len(docIDs))
	if convID == "" || len(docIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(docIDs)+1)
	args = append(args, convID)
	for _, id := range docIDs {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, status FROM documents WHERE conversation_id=? AND id IN (`+idPlaceholders(len(docIDs))+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, err
		}
		out[id] = status
	}
	return out, rows.Err()
}

// ConversationDocumentStatusesForFiles returns the conversation-scoped document
// statuses that were created from the given file ids. It lets the message handler
// protect older clients that know only the file id, not the newer document_id.
func ConversationDocumentStatusesForFiles(ctx context.Context, db *sql.DB, convID string, fileIDs []string) (map[string][]string, error) {
	fileIDs = cleanIDs(fileIDs)
	out := make(map[string][]string, len(fileIDs))
	if convID == "" || len(fileIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(fileIDs)+1)
	args = append(args, convID)
	for _, id := range fileIDs {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `
SELECT f.id, d.status
FROM files f
JOIN documents d ON d.storage_path=f.storage_path AND d.conversation_id=f.conversation_id
WHERE f.conversation_id=? AND f.id IN (`+idPlaceholders(len(fileIDs))+`)
ORDER BY f.id, d.created_at ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fileID, status string
		if err := rows.Scan(&fileID, &status); err != nil {
			return nil, err
		}
		out[fileID] = append(out[fileID], status)
	}
	return out, rows.Err()
}

// ConversationFileKinds returns the SERVER-side kind (image | sheet | pdf | doc |
// code | text | other) for each given file id in a conversation. The send
// preflight uses it to decide whether a file is expected to have a RAG document
// at all: the client's attachment.kind is untrusted and can drift from the
// server's classification (e.g. an .xlsx whose OOXML MIME "officedocument"
// substring makes a browser-side heuristic call it 'doc' while the backend files
// it as 'sheet' with no document row). Gating on the server kind prevents that
// drift from 409-ing an otherwise valid turn.
func ConversationFileKinds(ctx context.Context, db *sql.DB, convID string, fileIDs []string) (map[string]string, error) {
	fileIDs = cleanIDs(fileIDs)
	out := make(map[string]string, len(fileIDs))
	if convID == "" || len(fileIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(fileIDs)+1)
	args = append(args, convID)
	for _, id := range fileIDs {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, kind FROM files WHERE conversation_id=? AND id IN (`+idPlaceholders(len(fileIDs))+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, err
		}
		out[id] = kind
	}
	return out, rows.Err()
}

// ListIncompleteDocuments returns documents stuck in a non-terminal state —
// used at boot to requeue ingest jobs lost to a restart (the queue is in-memory).
func ListIncompleteDocuments(ctx context.Context, db *sql.DB) ([]Document, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(kb_id,''), COALESCE(conversation_id,''), filename, mime_type, size_bytes, status, error, chunk_count, storage_path, created_at
		   FROM documents WHERE status IN ('pending','parsing','embedding') ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.KBID, &d.ConversationID, &d.Filename, &d.MimeType, &d.SizeBytes, &d.Status, &d.Error, &d.ChunkCount, &d.StoragePath, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TouchDocumentIngest refreshes the persisted heartbeat only while a document
// is in a non-terminal state. A late heartbeat can therefore never make a ready
// or failed document look active again.
func TouchDocumentIngest(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE documents SET ingest_updated_at=? WHERE id=? AND status IN ('pending','parsing','embedding')`,
		time.Now().Unix(), id)
	return err
}

// ClaimStaleIncompleteDocuments atomically claims abandoned ingest rows for a
// watchdog pass. Pending rows get a longer queue-wait allowance than active
// parsing/embedding rows. Multiple API replicas may list the same stale ids,
// but only one can advance each heartbeat and enqueue it.
func ClaimStaleIncompleteDocuments(ctx context.Context, db *sql.DB, pendingCutoff, activeCutoff int64) ([]Document, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, status FROM documents
		 WHERE (status='pending' AND ingest_updated_at<=?)
		    OR (status IN ('parsing','embedding') AND ingest_updated_at<=?)
		 ORDER BY ingest_updated_at ASC, created_at ASC`, pendingCutoff, activeCutoff)
	if err != nil {
		return nil, err
	}
	candidates := []Document{}
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.Status); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, d)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	claimed := make([]Document, 0, len(candidates))
	for _, d := range candidates {
		cutoff := activeCutoff
		if d.Status == "pending" {
			cutoff = pendingCutoff
		}
		res, err := db.ExecContext(ctx,
			`UPDATE documents SET status='pending', error='', ingest_updated_at=?
			 WHERE id=? AND status=? AND ingest_updated_at<=?`,
			now, d.ID, d.Status, cutoff)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			claimed = append(claimed, d)
		}
	}
	return claimed, nil
}

func ListDocuments(ctx context.Context, db *sql.DB, scope, parentID string) ([]Document, error) {
	return listDocumentsScoped(ctx, db, scope, parentID, "")
}

// ListDocumentsForUser applies the draft visibility rule used by user-facing
// document views. A document created from a composer file is visible to every
// authorized workspace member after that file is committed, but remains
// uploader-private while any matching file row is still draft=1. Documents with
// no file twin are legacy/direct document uploads and retain their existing
// conversation/KB authorization at the handler layer.
func ListDocumentsForUser(ctx context.Context, db *sql.DB, scope, parentID, userID string) ([]Document, error) {
	return listDocumentsScoped(ctx, db, scope, parentID, userID)
}

// ListKBDocumentsForUser adds server-side filename/uploader filters and computes
// the caller-relative delete capability for every returned document.
func ListKBDocumentsForUser(ctx context.Context, db *sql.DB, kbID, userID, search, uploadedByUserID string) ([]Document, error) {
	kb, err := GetStandaloneKB(ctx, db, kbID, userID)
	if err != nil {
		return nil, err
	}
	q := `SELECT d.id,COALESCE(d.kb_id,''),COALESCE(d.conversation_id,''),d.filename,d.mime_type,d.size_bytes,d.status,d.error,d.chunk_count,d.storage_path,
	             COALESCE(d.uploaded_by_user_id,''),COALESCE(u.name,''),COALESCE(u.email,''),d.created_at
	        FROM documents d LEFT JOIN users u ON u.id=d.uploaded_by_user_id
	       WHERE d.kb_id=? AND ` + documentUserAccessPredicate("d")
	args := []any{kbID}
	args = append(args, documentUserAccessArgs(userID)...)
	if search = strings.TrimSpace(search); search != "" {
		q += ` AND LOWER(d.filename) LIKE ?`
		args = append(args, "%"+strings.ToLower(search)+"%")
	}
	if uploadedByUserID = strings.TrimSpace(uploadedByUserID); uploadedByUserID != "" {
		q += ` AND d.uploaded_by_user_id=?`
		args = append(args, uploadedByUserID)
	}
	q += ` ORDER BY d.created_at DESC`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := []Document{}
	for rows.Next() {
		var doc Document
		if err := scanDocumentValues(rows, &doc); err != nil {
			return nil, err
		}
		if kb.WorkspaceID != "" {
			doc.CanDelete = kb.CanDeleteContent
		} else {
			doc.CanDelete = kb.CanDeleteContent || (kb.AccessRole == "write" && doc.UploadedByUserID == userID)
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func ListKBDocumentUploadersForUser(ctx context.Context, db *sql.DB, kbID, userID string) ([]KnowledgeBaseUploader, error) {
	if _, err := GetStandaloneKB(ctx, db, kbID, userID); err != nil {
		return nil, err
	}
	args := []any{kbID}
	args = append(args, documentUserAccessArgs(userID)...)
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT d.uploaded_by_user_id,COALESCE(u.name,''),COALESCE(u.email,''),COALESCE(u.settings,'')
		FROM documents d LEFT JOIN users u ON u.id=d.uploaded_by_user_id
		WHERE d.kb_id=? AND trim(d.uploaded_by_user_id)<>'' AND `+documentUserAccessPredicate("d")+`
		ORDER BY COALESCE(u.name,''),COALESCE(u.email,''),d.uploaded_by_user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []KnowledgeBaseUploader{}
	for rows.Next() {
		var item KnowledgeBaseUploader
		var settings string
		if err := rows.Scan(&item.UserID, &item.Name, &item.Email, &settings); err != nil {
			return nil, err
		}
		item.AvatarURL = avatarFromSettings(settings)
		items = append(items, item)
	}
	return items, rows.Err()
}

func documentContainerAccessPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `kb_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM knowledge_bases document_kb ` +
		`WHERE document_kb.id=` + prefix + `kb_id AND ` + knowledgeBaseAccessPredicate("document_kb") +
		`)) OR (COALESCE(` + prefix + `conversation_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM conversations document_conversation ` +
		`WHERE document_conversation.id=` + prefix + `conversation_id AND ` + conversationResourceAccessPredicate("document_conversation") +
		`)))`
}

func documentContainerAccessArgs(userID string) []any {
	args := knowledgeBaseAccessArgs(userID)
	return append(args, workspaceResourceAccessArgs(userID)...)
}

func documentDraftVisibilityPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `(
		NOT EXISTS (
			SELECT 1 FROM files draft_visibility_file
			 WHERE draft_visibility_file.storage_path=` + prefix + `storage_path
		)
		OR EXISTS (
			SELECT 1 FROM files visible_file
			 WHERE visible_file.storage_path=` + prefix + `storage_path
			   AND (visible_file.draft=0 OR visible_file.user_id=?)
		)
	)`
}

func documentUserAccessPredicate(alias string) string {
	return `(` + documentContainerAccessPredicate(alias) + ` AND ` + documentDraftVisibilityPredicate(alias) + `)`
}

func documentUserAccessArgs(userID string) []any {
	args := documentContainerAccessArgs(userID)
	return append(args, userID)
}

func listDocumentsScoped(ctx context.Context, db *sql.DB, scope, parentID, userID string) ([]Document, error) {
	q := `SELECT d.id, COALESCE(d.kb_id,''), COALESCE(d.conversation_id,''), d.filename, d.mime_type, d.size_bytes, d.status, d.error, d.chunk_count, d.storage_path,
	             COALESCE(d.uploaded_by_user_id,''), COALESCE(u.name,''), COALESCE(u.email,''), d.created_at
	        FROM documents d LEFT JOIN users u ON u.id=d.uploaded_by_user_id WHERE `
	args := []any{}
	switch scope {
	case "kb":
		q += `d.kb_id=?`
		args = append(args, parentID)
	case "conversation":
		q += `d.conversation_id=?`
		args = append(args, parentID)
	default:
		return nil, errors.New("unknown scope")
	}
	if strings.TrimSpace(userID) != "" {
		q += ` AND ` + documentUserAccessPredicate("d")
		args = append(args, documentUserAccessArgs(userID)...)
	}
	q += ` ORDER BY d.created_at DESC`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		var d Document
		if err := scanDocumentValues(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDocument returns one row.
func GetDocument(ctx context.Context, db *sql.DB, id string) (*Document, error) {
	return getDocumentScoped(ctx, db, id, "")
}

// GetDocumentForUser is the single-row counterpart to ListDocumentsForUser.
// It is intended for user-facing mutation endpoints (retry, promote, rename,
// delete) so an ID guess cannot operate on another uploader's draft twin.
func GetDocumentForUser(ctx context.Context, db *sql.DB, id, userID string) (*Document, error) {
	return getDocumentScoped(ctx, db, id, userID)
}

func getDocumentScoped(ctx context.Context, db *sql.DB, id, userID string) (*Document, error) {
	q := `SELECT d.id, COALESCE(d.kb_id,''), COALESCE(d.conversation_id,''), d.filename, d.mime_type, d.size_bytes, d.status, d.error, d.chunk_count, d.storage_path,
	             COALESCE(d.uploaded_by_user_id,''), COALESCE(u.name,''), COALESCE(u.email,''), d.created_at
	        FROM documents d LEFT JOIN users u ON u.id=d.uploaded_by_user_id WHERE d.id=?`
	args := []any{id}
	if strings.TrimSpace(userID) != "" {
		q += ` AND ` + documentUserAccessPredicate("d")
		args = append(args, documentUserAccessArgs(userID)...)
	}
	var d Document
	err := scanDocumentValues(db.QueryRowContext(ctx, q, args...), &d)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateDocument inserts a placeholder doc with status=pending. Either kbID or
// conversationID must be set; the other should be "" so the column stays null.
func CreateDocument(ctx context.Context, db *sql.DB, d Document) (*Document, error) {
	kbID, convID, now, err := prepareDocument(&d)
	if err != nil {
		return nil, err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO documents(id, kb_id, conversation_id, filename, mime_type, size_bytes, status, storage_path, uploaded_by_user_id, ingest_updated_at, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, kbID, convID, d.Filename, d.MimeType, d.SizeBytes, d.Status, d.StoragePath, d.UploadedByUserID, now, now)
	if err != nil {
		return nil, err
	}
	return GetDocument(ctx, db, d.ID)
}

// CreateDocumentForUser binds document creation to an authorized KB or
// conversation in the INSERT itself. It closes the upload-time gap between a
// handler's container lookup and persistence after a concurrent kick/leave.
func CreateDocumentForUser(ctx context.Context, db *sql.DB, d Document, userID string) (*Document, error) {
	kbID, convID, now, err := prepareDocument(&d)
	if err != nil {
		return nil, err
	}
	if d.SizeBytes < 0 {
		return nil, errors.New("document size cannot be negative")
	}
	scope, parentID := "conversation", d.ConversationID
	if d.KBID != "" {
		scope, parentID = "kb", d.KBID
	}
	workspaceID, err := documentParentWorkspaceID(ctx, db, scope, parentID)
	if err != nil {
		return nil, err
	}
	var tx *sql.Tx
	if d.KBID != "" {
		tx, err = beginKnowledgeBaseMutationTx(ctx, db, d.KBID, workspaceID)
	} else if workspaceID != "" {
		tx, err = beginWorkspaceMutationTx(ctx, db, workspaceID)
	} else {
		tx, err = db.BeginTx(ctx, nil)
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var mutationDB documentMutationDB = tx

	var hasFileTwin int
	if strings.TrimSpace(d.StoragePath) != "" {
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM files WHERE storage_path=?`, d.StoragePath,
		).Scan(&hasFileTwin); err != nil {
			return nil, err
		}
	}
	if hasFileTwin == 0 {
		billingUserID, err := documentBillingUserTx(ctx, tx, scope, parentID, userID)
		if err != nil {
			return nil, err
		}
		if err := enforceStorageQuotaTx(ctx, tx, billingUserID, d.SizeBytes); err != nil {
			return nil, err
		}
	}
	d.UploadedByUserID = userID
	baseArgs := []any{d.ID, kbID, convID, d.Filename, d.MimeType, d.SizeBytes, d.Status, d.StoragePath, d.UploadedByUserID, now, now}
	var res sql.Result
	if d.KBID != "" {
		args := append(baseArgs, d.KBID)
		args = append(args, knowledgeBaseWriteArgs(userID)...)
		res, err = mutationDB.ExecContext(ctx,
			`INSERT INTO documents(id, kb_id, conversation_id, filename, mime_type, size_bytes, status, storage_path, uploaded_by_user_id, ingest_updated_at, created_at)
			 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			   FROM knowledge_bases document_create_kb
			  WHERE document_create_kb.id=? AND `+knowledgeBaseWritePredicate("document_create_kb")+
				workspaceDocumentCreationPredicate("document_create_kb", workspaceID),
			appendWorkspaceDocumentCreationArgs(args, workspaceID, userID)...)
	} else {
		args := append(baseArgs, d.ConversationID)
		// Conversation documents are attachments, not merely visible content.
		// A guest can read a shared conversation but must never create an
		// attachment in it, including through a direct store caller.
		args = append(args, conversationMemberMutationArgs(userID)...)
		res, err = mutationDB.ExecContext(ctx,
			`INSERT INTO documents(id, kb_id, conversation_id, filename, mime_type, size_bytes, status, storage_path, uploaded_by_user_id, ingest_updated_at, created_at)
			 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			   FROM conversations document_create_conversation
			  WHERE document_create_conversation.id=? AND `+conversationMemberMutationPredicate("document_create_conversation")+
				workspaceDocumentCreationPredicate("document_create_conversation", workspaceID),
			appendWorkspaceDocumentCreationArgs(args, workspaceID, userID)...)
	}
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	created, err := scanDocument(mutationDB.QueryRowContext(ctx,
		`SELECT d.id, COALESCE(d.kb_id,''), COALESCE(d.conversation_id,''), d.filename, d.mime_type, d.size_bytes, d.status, d.error, d.chunk_count, d.storage_path,
		        COALESCE(d.uploaded_by_user_id,''), COALESCE(u.name,''), COALESCE(u.email,''), d.created_at
		   FROM documents d LEFT JOIN users u ON u.id=d.uploaded_by_user_id WHERE d.id=? AND `+documentUserAccessPredicate("d"),
		append([]any{d.ID}, documentUserAccessArgs(userID)...)...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if d.KBID != "" {
		args := []any{created.ID, d.KBID}
		args = append(args, knowledgeBaseDocumentMutationArgs(userID)...)
		args = append(args, userID)
		var canDelete int
		if err := mutationDB.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS (
			SELECT 1 FROM documents created_document
			JOIN knowledge_bases created_document_kb ON created_document_kb.id=created_document.kb_id
			WHERE created_document.id=? AND created_document.kb_id=?
			  AND `+knowledgeBaseDocumentMutationPredicate("created_document_kb", "created_document")+`
			  AND `+documentDraftVisibilityPredicate("created_document")+`
		) THEN 1 ELSE 0 END`, args...).Scan(&canDelete); err != nil {
			return nil, err
		}
		created.CanDelete = canDelete == 1
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &created, nil
}

func workspaceDocumentCreationPredicate(alias, workspaceID string) string {
	if workspaceID == "" {
		return ""
	}
	return ` AND EXISTS (
		SELECT 1 FROM workspaces document_create_workspace
		 WHERE document_create_workspace.id=` + alias + `.workspace_id
		   AND ` + workspaceAcceptsResourceCreationPredicate("document_create_workspace") + `
	)`
}

func appendWorkspaceDocumentCreationArgs(args []any, workspaceID, userID string) []any {
	if workspaceID == "" {
		return args
	}
	return append(args, userID)
}

func documentBillingUserTx(ctx context.Context, tx *sql.Tx, scope, parentID, accessUserID string) (string, error) {
	var query string
	args := []any{parentID}
	switch scope {
	case "kb":
		query = `SELECT CASE WHEN COALESCE(k.workspace_id,'')<>''
			THEN COALESCE(w.owner_id, k.user_id) ELSE k.user_id END
			FROM knowledge_bases k LEFT JOIN workspaces w ON w.id=k.workspace_id
			WHERE k.id=? AND ` + knowledgeBaseWritePredicate("k")
	case "conversation":
		query = `SELECT CASE WHEN COALESCE(c.workspace_id,'')<>''
			THEN COALESCE(w.owner_id, c.user_id) ELSE c.user_id END
			FROM conversations c LEFT JOIN workspaces w ON w.id=c.workspace_id
			WHERE c.id=? AND ` + conversationMemberMutationPredicate("c")
	default:
		return "", ErrNotFound
	}
	if scope == "kb" {
		args = append(args, knowledgeBaseWriteArgs(accessUserID)...)
	} else {
		args = append(args, conversationMemberMutationArgs(accessUserID)...)
	}
	var billingUserID string
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&billingUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return billingUserID, nil
}

type documentMutationDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func documentParentWorkspaceID(ctx context.Context, db *sql.DB, scope, parentID string) (string, error) {
	var query string
	switch scope {
	case "kb":
		query = `SELECT COALESCE(workspace_id,'') FROM knowledge_bases WHERE id=?`
	case "conversation":
		query = `SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`
	default:
		return "", ErrNotFound
	}
	var workspaceID string
	if err := db.QueryRowContext(ctx, query, parentID).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return workspaceID, nil
}

func documentWorkspaceID(ctx context.Context, db *sql.DB, id, scope, parentID string) (string, error) {
	var query string
	switch scope {
	case "kb":
		query = `SELECT COALESCE(k.workspace_id,'')
			FROM documents d JOIN knowledge_bases k ON k.id=d.kb_id
			WHERE d.id=? AND d.kb_id=?`
	case "conversation":
		query = `SELECT COALESCE(c.workspace_id,'')
			FROM documents d JOIN conversations c ON c.id=d.conversation_id
			WHERE d.id=? AND d.conversation_id=?`
	default:
		return "", ErrNotFound
	}
	var workspaceID string
	if err := db.QueryRowContext(ctx, query, id, parentID).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return workspaceID, nil
}

func scanDocument(s scanner) (Document, error) {
	var d Document
	err := scanDocumentValues(s, &d)
	return d, err
}

func scanDocumentValues(s scanner, d *Document) error {
	return s.Scan(
		&d.ID, &d.KBID, &d.ConversationID, &d.Filename, &d.MimeType,
		&d.SizeBytes, &d.Status, &d.Error, &d.ChunkCount, &d.StoragePath,
		&d.UploadedByUserID, &d.UploadedByName, &d.UploadedByEmail, &d.CreatedAt,
	)
}

func prepareDocument(d *Document) (kbID, convID any, now int64, err error) {
	if d.ID == "" {
		d.ID = genID("doc")
	}
	if d.Status == "" {
		d.Status = "pending"
	}
	if d.KBID != "" {
		kbID = d.KBID
	}
	if d.ConversationID != "" {
		convID = d.ConversationID
	}
	if kbID == nil && convID == nil {
		return nil, nil, 0, errors.New("document must belong to a kb or a conversation")
	}
	if kbID != nil && convID != nil {
		return nil, nil, 0, errors.New("document cannot belong to both a kb and a conversation")
	}
	return kbID, convID, time.Now().Unix(), nil
}

// UpdateDocumentStatus moves the document along the pipeline state machine.
func UpdateDocumentStatus(ctx context.Context, db *sql.DB, id, status, errMsg string, chunkCount int) error {
	_, err := db.ExecContext(ctx,
		`UPDATE documents SET status=?, error=?, chunk_count=?, ingest_updated_at=? WHERE id=?`,
		status, errMsg, chunkCount, time.Now().Unix(), id)
	return err
}

// RetryDocumentForUser atomically consumes a failed conversation document.
func RetryDocumentForUser(ctx context.Context, db *sql.DB, id, conversationID, userID string) error {
	return retryScopedDocumentForUser(ctx, db, id, "conversation", conversationID, userID)
}

// RetryKBDocumentForUser atomically consumes a failed knowledge-base document.
// Both retry entry points share the same access and workspace-membership fence.
func RetryKBDocumentForUser(ctx context.Context, db *sql.DB, id, kbID, userID string) error {
	return retryScopedDocumentForUser(ctx, db, id, "kb", kbID, userID)
}

func retryScopedDocumentForUser(ctx context.Context, db *sql.DB, id, scope, parentID, userID string) error {
	workspaceID, err := documentWorkspaceID(ctx, db, id, scope, parentID)
	if err != nil {
		return err
	}
	containerColumn := ""
	switch scope {
	case "conversation":
		containerColumn = "conversation_id"
	case "kb":
		containerColumn = "kb_id"
	default:
		return ErrNotFound
	}
	args := []any{time.Now().Unix(), id, parentID}
	args = append(args, documentUserAccessArgs(userID)...)
	q := `UPDATE documents
		SET status='pending', error='', chunk_count=0, ingest_updated_at=?
		WHERE id=? AND ` + containerColumn + `=? AND status='failed'
		  AND ` + documentUserAccessPredicate("documents")
	if scope == "kb" {
		// Viewing a shared document is not permission to mutate its ingest state.
		// Bind the owner/manager or own-upload writer rule to the UPDATE so a stale
		// handler check cannot be raced by a share-role or membership change.
		q += ` AND EXISTS (
			SELECT 1 FROM knowledge_bases document_retry_kb
			 WHERE document_retry_kb.id=documents.kb_id AND ` +
			knowledgeBaseDocumentMutationPredicate("document_retry_kb", "documents") + `
		)`
		args = append(args, knowledgeBaseDocumentMutationArgs(userID)...)
	} else {
		// A failed attachment remains readable to guests, but retrying parsing
		// changes shared state and therefore requires reply/write authority.
		q += ` AND EXISTS (
			SELECT 1 FROM conversations document_retry_conversation
			 WHERE document_retry_conversation.id=documents.conversation_id AND ` +
			conversationMemberMutationPredicate("document_retry_conversation") + `
		)`
		args = append(args, conversationMemberMutationArgs(userID)...)
	}
	if workspaceID == "" && scope != "kb" {
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if n != 1 {
			return ErrNotFound
		}
		return nil
	}
	var tx *sql.Tx
	if scope == "kb" {
		tx, err = beginKnowledgeBaseMutationTx(ctx, db, parentID, workspaceID)
	} else {
		tx, err = beginWorkspaceMutationTx(ctx, db, workspaceID)
	}
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

// RenameDocument updates just the filename of a document.
func RenameDocument(ctx context.Context, db *sql.DB, id, filename string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE documents SET filename=? WHERE id=?`, filename, id)
	return err
}

// RenameDocumentForUser atomically binds the mutation to the expected container
// and the caller's current access. A guessed document id in another KB or
// conversation is indistinguishable from a missing row.
func RenameDocumentForUser(ctx context.Context, db *sql.DB, id, scope, parentID, userID, filename string) error {
	workspaceID, err := documentWorkspaceID(ctx, db, id, scope, parentID)
	if err != nil {
		return err
	}
	q := `UPDATE documents SET filename=? WHERE id=?`
	args := []any{filename, id}
	switch scope {
	case "kb":
		q += ` AND kb_id=?`
		q += ` AND EXISTS (
			SELECT 1 FROM knowledge_bases document_rename_kb
			 WHERE document_rename_kb.id=documents.kb_id AND ` +
			knowledgeBaseDocumentMutationPredicate("document_rename_kb", "documents") + `
		)`
	case "conversation":
		q += ` AND conversation_id=?`
	default:
		return ErrNotFound
	}
	args = append(args, parentID)
	if scope == "kb" {
		args = append(args, knowledgeBaseDocumentMutationArgs(userID)...)
	} else {
		q += ` AND EXISTS (
			SELECT 1 FROM conversations document_rename_conversation
			 WHERE document_rename_conversation.id=documents.conversation_id AND ` +
			conversationMemberMutationPredicate("document_rename_conversation") + `
		)`
		args = append(args, conversationMemberMutationArgs(userID)...)
	}
	q += ` AND ` + documentUserAccessPredicate("documents")
	args = append(args, documentUserAccessArgs(userID)...)
	if workspaceID == "" && scope != "kb" {
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return nil
	}
	var tx *sql.Tx
	if scope == "kb" {
		tx, err = beginKnowledgeBaseMutationTx(ctx, db, parentID, workspaceID)
	} else {
		tx, err = beginWorkspaceMutationTx(ctx, db, workspaceID)
	}
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

// DeleteDocument removes the row.
func DeleteDocument(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM documents WHERE id=?", id)
	return err
}

// DeleteDocumentForUser is the user-scoped counterpart to DeleteDocument.
func DeleteDocumentForUser(ctx context.Context, db *sql.DB, id, scope, parentID, userID string) error {
	workspaceID, err := documentWorkspaceID(ctx, db, id, scope, parentID)
	if err != nil {
		return err
	}
	q := `DELETE FROM documents WHERE id=?`
	args := []any{id}
	switch scope {
	case "kb":
		q += ` AND kb_id=?`
		args = append(args, parentID)
		q += ` AND EXISTS (
			SELECT 1 FROM knowledge_bases document_delete_kb
			 WHERE document_delete_kb.id=documents.kb_id AND ` +
			knowledgeBaseDocumentMutationPredicate("document_delete_kb", "documents") + `
		) AND ` + documentDraftVisibilityPredicate("documents")
		args = append(args, knowledgeBaseDocumentMutationArgs(userID)...)
		args = append(args, userID)
	case "conversation":
		q += ` AND conversation_id=?`
		args = append(args, parentID)
		q += ` AND EXISTS (
			SELECT 1 FROM conversations document_delete_conversation
			 WHERE document_delete_conversation.id=documents.conversation_id AND ` +
			conversationMemberMutationPredicate("document_delete_conversation") + `
		)`
		args = append(args, conversationMemberMutationArgs(userID)...)
		q += ` AND ` + documentUserAccessPredicate("documents")
		args = append(args, documentUserAccessArgs(userID)...)
	default:
		return ErrNotFound
	}
	if workspaceID == "" && scope != "kb" {
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return nil
	}
	var tx *sql.Tx
	if scope == "kb" {
		tx, err = beginKnowledgeBaseMutationTx(ctx, db, parentID, workspaceID)
	} else {
		tx, err = beginWorkspaceMutationTx(ctx, db, workspaceID)
	}
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

// PromoteDocumentToKB switches a conversation-temp doc into a knowledge base
// without re-embedding (used by "add to project library").
// DeleteChunksByDocument removes a document's chunk rows. Used when re-embedding
// a document on promote (§C5) — its vectors are dropped separately via the
// vector store.
func DeleteChunksByDocument(ctx context.Context, db *sql.DB, docID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chunks WHERE document_id=?`, docID)
	return err
}

func PromoteDocumentToKB(ctx context.Context, db *sql.DB, docID, kbID, userID string) error {
	var sourceWorkspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(c.workspace_id,'')
		   FROM documents d JOIN conversations c ON c.id=d.conversation_id
		  WHERE d.id=?`, docID,
	).Scan(&sourceWorkspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	destinationWorkspaceID, err := knowledgeBaseWorkspaceID(ctx, db, kbID)
	if err != nil {
		return err
	}
	if sourceWorkspaceID != destinationWorkspaceID {
		return ErrNotFound
	}
	tx, err := beginKnowledgeBaseMutationTx(ctx, db, kbID, destinationWorkspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	args := []any{kbID, docID}
	args = append(args, documentUserAccessArgs(userID)...)
	args = append(args, conversationMemberMutationArgs(userID)...)
	args = append(args, kbID, destinationWorkspaceID)
	args = append(args, knowledgeBaseWriteArgs(userID)...)
	res, err := tx.ExecContext(ctx,
		`UPDATE documents
		    SET kb_id=?, conversation_id=NULL
		  WHERE id=? AND conversation_id IS NOT NULL
		    AND `+documentUserAccessPredicate("documents")+`
		    AND EXISTS (
		      SELECT 1 FROM conversations promotion_conversation
		       WHERE promotion_conversation.id=documents.conversation_id AND `+conversationMemberMutationPredicate("promotion_conversation")+`
		    )
		    AND EXISTS (
		      SELECT 1 FROM knowledge_bases promotion_kb
		       WHERE promotion_kb.id=?
		         AND COALESCE(promotion_kb.workspace_id,'')=?
		         AND `+knowledgeBaseWritePredicate("promotion_kb")+`
		    )`, args...)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chunks SET kb_id=?, conversation_id=NULL WHERE document_id=?`, kbID, docID); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateChunk inserts a single text chunk (back-compat convenience wrapper).
func CreateChunk(ctx context.Context, db *sql.DB, docID, kbID, convID string, seq int, content string, embeddingModel string) error {
	_, err := CreateChunkFull(ctx, db, ChunkInsert{
		DocumentID: docID, KBID: kbID, ConversationID: convID,
		Seq: seq, ChunkType: "text", Content: content,
		EmbeddingModel: embeddingModel,
	})
	return err
}

// ChunkInsert is the full insert shape, supporting the small-to-big layout
// (§4.11-C-2: parent rows carry section context, children carry vectors) and
// image-caption chunks referencing the original image.
type ChunkInsert struct {
	// ID, when set, is used verbatim (so a batched insert can pre-resolve
	// parent→child references); empty means "generate one".
	ID             string
	DocumentID     string
	KBID           string
	ConversationID string
	Seq            int
	ParentID       string
	ChunkType      string // text | parent | table | image_caption
	Content        string
	ImageRef       string
	EmbeddingModel string
}

// sanitizeChunkText strips NUL bytes and invalid UTF-8 from parsed text. Postgres
// TEXT columns reject these (SQLSTATE 22021 "invalid byte sequence for encoding
// UTF8: 0x00") and binary documents (docx/pdf/xls) routinely carry them, which
// otherwise fails the whole ingest.
func sanitizeChunkText(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "")
}

// CreateChunkFull inserts a chunk row and returns its id.
func CreateChunkFull(ctx context.Context, db *sql.DB, c ChunkInsert) (string, error) {
	id := genID("ch")
	c.Content = sanitizeChunkText(c.Content)
	var kb, conv, parent, imgRef any
	if c.KBID != "" {
		kb = c.KBID
	}
	if c.ConversationID != "" {
		conv = c.ConversationID
	}
	if c.ParentID != "" {
		parent = c.ParentID
	}
	if c.ImageRef != "" {
		imgRef = c.ImageRef
	}
	if c.ChunkType == "" {
		c.ChunkType = "text"
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO chunks(id, document_id, kb_id, conversation_id, seq, parent_id, chunk_type, content, image_ref, meta, embedding_model) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?)`,
		id, c.DocumentID, kb, conv, c.Seq, parent, c.ChunkType, c.Content, imgRef, c.EmbeddingModel)
	return id, err
}

// NewChunkID returns a fresh chunk id, so callers can pre-resolve parent→child
// references before a batched insert.
func NewChunkID() string { return genID("ch") }

// CreateChunksBatch inserts many chunks in ONE transaction — a single commit
// instead of one autonomous INSERT (and, on SQLite, one fsync) per chunk, which
// is the dominant cost when indexing a large document. Each chunk's ID must be
// pre-set (NewChunkID) and parents must precede the children that reference them.
// Rolls back on the first error.
func CreateChunksBatch(ctx context.Context, db *sql.DB, chunks []ChunkInsert) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO chunks(id, document_id, kb_id, conversation_id, seq, parent_id, chunk_type, content, image_ref, meta, embedding_model) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		id := c.ID
		if id == "" {
			id = genID("ch")
		}
		var kb, conv, parent, imgRef any
		if c.KBID != "" {
			kb = c.KBID
		}
		if c.ConversationID != "" {
			conv = c.ConversationID
		}
		if c.ParentID != "" {
			parent = c.ParentID
		}
		if c.ImageRef != "" {
			imgRef = c.ImageRef
		}
		ct := c.ChunkType
		if ct == "" {
			ct = "text"
		}
		if _, err := stmt.ExecContext(ctx, id, c.DocumentID, kb, conv, c.Seq, parent, ct, sanitizeChunkText(c.Content), imgRef, c.EmbeddingModel); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetChunkContent returns one chunk's content — used for small-to-big parent
// expansion at retrieval time (§4.11-C-2).
func GetChunkContent(ctx context.Context, db *sql.DB, id string) (string, error) {
	var content string
	err := db.QueryRowContext(ctx, `SELECT content FROM chunks WHERE id=?`, id).Scan(&content)
	return content, err
}

// Chunk is a denormalised chunk row used by the retrieve engine.
type Chunk struct {
	ID             string
	DocumentID     string
	KBID           string
	ConversationID string
	Seq            int
	ParentID       string
	ChunkType      string
	Content        string
	ImageRef       string
	Meta           json.RawMessage
	EmbeddingModel string
	Filename       string // joined from documents
}

// EmbeddedChunk is the admin-maintenance view of a child chunk that should have
// a vector point in Qdrant. KB chunks carry the KB's locked embedding_dim; plain
// conversation chunks leave it 0 and the RAG service resolves it from the model.
type EmbeddedChunk struct {
	ID             string
	DocumentID     string
	KBID           string
	ConversationID string
	Seq            int
	ParentID       string
	ChunkType      string
	Content        string
	EmbeddingModel string
	Filename       string
	EmbeddingDim   int
}

func ListEmbeddedChildChunks(ctx context.Context, db *sql.DB) ([]EmbeddedChunk, error) {
	rows, err := db.QueryContext(ctx, `
SELECT
  c.id,
  c.document_id,
  COALESCE(c.kb_id,''),
  COALESCE(c.conversation_id,''),
  c.seq,
  COALESCE(c.parent_id,''),
  c.chunk_type,
  c.content,
  c.embedding_model,
  d.filename,
  COALESCE(k.embedding_dim,0)
FROM chunks c
JOIN documents d ON d.id = c.document_id
LEFT JOIN knowledge_bases k ON k.id = c.kb_id
WHERE c.chunk_type <> 'parent'
  AND COALESCE(c.embedding_model,'') <> ''
ORDER BY c.embedding_model, COALESCE(k.embedding_dim,0), c.document_id, c.seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EmbeddedChunk{}
	for rows.Next() {
		var ch EmbeddedChunk
		if err := rows.Scan(&ch.ID, &ch.DocumentID, &ch.KBID, &ch.ConversationID, &ch.Seq, &ch.ParentID, &ch.ChunkType, &ch.Content, &ch.EmbeddingModel, &ch.Filename, &ch.EmbeddingDim); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// ListChunksInScope returns chunks whose kb_id ∈ kbIDs OR conversation_id =
// convID. Filename is joined for citation rendering.
func ListChunksInScope(ctx context.Context, db *sql.DB, kbIDs []string, convID string) ([]Chunk, error) {
	// The kb-scope and conv-scope legs are UNION ALL'd rather than OR'd: a chunk
	// has either kb_id OR conversation_id (promoting a conv doc to a KB nulls the
	// other), so the legs are disjoint (no duplicates) and each can use its own
	// index (idx_chunks_kb / idx_chunks_conv) — an `OR` across the two columns
	// would force a full scan.
	const cols = `c.id, c.document_id, COALESCE(c.kb_id,''), COALESCE(c.conversation_id,''), c.seq, COALESCE(c.parent_id,''), c.chunk_type, c.content, COALESCE(c.image_ref,''), c.meta, c.embedding_model, d.filename`
	// A document twin can be indexed before its composer file is submitted. Do
	// not let that pre-commit row enter either a conversation or project-KB RAG
	// scope; once the file transaction flips draft=0, the same chunks become
	// visible to the authorized shared scope automatically.
	const from = ` FROM chunks c JOIN documents d ON d.id = c.document_id
		 WHERE d.status='ready'
		   AND NOT EXISTS (
		     SELECT 1 FROM files draft_visibility_file
		      WHERE draft_visibility_file.storage_path=d.storage_path
		        AND draft_visibility_file.draft=1
		   ) AND `
	legs := []string{}
	args := []any{}
	if len(kbIDs) > 0 {
		ph := []string{}
		for _, id := range kbIDs {
			ph = append(ph, "?")
			args = append(args, id)
		}
		legs = append(legs, `SELECT `+cols+from+`c.kb_id IN (`+strings.Join(ph, ",")+`)`)
	}
	if convID != "" {
		legs = append(legs, `SELECT `+cols+from+`c.conversation_id=?`)
		args = append(args, convID)
	}
	if len(legs) == 0 {
		return nil, nil
	}
	// Deterministic DOCUMENT ORDER: full-text injection, map-reduce summarisation
	// and cross-document comparison all assume scope is in document order, but
	// UNION ALL guarantees no ordering (Postgres especially). Order by the output
	// columns document_id (2) then seq (5) — positional refs are portable across
	// SQLite/Postgres — so each doc's chunks stay contiguous and in sequence.
	q := strings.Join(legs, " UNION ALL ") + " ORDER BY 2, 5"
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chunk{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var ch Chunk
		var meta string
		if err := rows.Scan(&ch.ID, &ch.DocumentID, &ch.KBID, &ch.ConversationID, &ch.Seq, &ch.ParentID, &ch.ChunkType, &ch.Content, &ch.ImageRef, &meta, &ch.EmbeddingModel, &ch.Filename); err != nil {
			return nil, err
		}
		ch.Meta = json.RawMessage(meta)
		out = append(out, ch)
	}
	return out, rows.Err()
}
