package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrForkAttachmentsProcessing = errors.New("wait for attachment processing before forking this conversation")

// ForkMessagePathWithResources gives the new conversation independent resource
// records. Immutable storage bytes may be shared: the existing reference guard
// keeps them until the last file/document/artifact record has been deleted.
func ForkMessagePathWithResources(ctx context.Context, db *sql.DB, sourceID, targetID, userID, leafID string, source, copies []Message, copyVectors func(context.Context, string, map[string]string) error) (map[string]string, error) {
	files, err := ListFilesByConversationBranch(ctx, db, sourceID, userID, leafID)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`, sourceID).Scan(&workspaceID); err != nil {
		return nil, err
	}
	if workspaceID != "" {
		if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, sourceID); err != nil {
		return nil, err
	}
	args := append([]any{sourceID}, workspaceResourceAccessArgs(userID)...)
	var allowed int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations c WHERE c.id=? AND `+conversationResourceAccessPredicate("c"), args...).Scan(&allowed); err != nil {
		return nil, ErrNotFound
	}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id=? AND user_id=? AND COALESCE(workspace_id,'')=?`, targetID, userID, workspaceID).Scan(&allowed); err != nil {
		return nil, ErrNotFound
	}
	if len(source) != len(copies) || len(copies) == 0 {
		return nil, errors.New("empty fork path")
	}
	remap := map[string]string{sourceID: targetID}
	messageIDs := make([]string, len(source))
	for i := range source {
		messageIDs[i] = source[i].ID
		copies[i].ID = genID("msg")
		remap[source[i].ID] = copies[i].ID
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE id=? AND conversation_id=?`, source[i].ID, sourceID).Scan(&exists); err != nil {
			return nil, ErrNotFound
		}
	}
	fileIDs := []string{}
	for _, file := range files {
		if !file.Draft {
			fileIDs = append(fileIDs, file.ID)
		}
	}
	fileColumns := "id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft,branch_message_id,rel_path,created_at,vision_evidence,vision_evidence_key"
	fileRows := []map[string]any{}
	if len(fileIDs) > 0 {
		args := append([]any{sourceID}, anySlice(fileIDs)...)
		fileRows, err = forkRows(ctx, tx, `SELECT `+fileColumns+` FROM files WHERE conversation_id=? AND draft=0 AND id IN (`+idPlaceholders(len(fileIDs))+`)`, args...)
		if err != nil {
			return nil, err
		}
	}
	docColumns := "id,kb_id,conversation_id,filename,mime_type,size_bytes,status,error,chunk_count,storage_path,uploaded_by_user_id,ingest_updated_at,created_at"
	docRows, err := forkRows(ctx, tx, `SELECT `+docColumns+` FROM documents WHERE conversation_id=? AND (
	 NOT EXISTS (SELECT 1 FROM files f WHERE f.conversation_id=? AND f.storage_path=documents.storage_path)
	 OR EXISTS (SELECT 1 FROM files f WHERE f.conversation_id=? AND f.storage_path=documents.storage_path AND f.id IN (`+idPlaceholders(max(1, len(fileIDs)))+`)))`, append([]any{sourceID, sourceID, sourceID}, anySlice(forkNonEmptyIDs(fileIDs))...)...)
	if err != nil {
		return nil, err
	}
	artifactColumns := "id,message_id,filename,storage_path,mime_type,size_bytes,source,created_at"
	artifactRows, err := forkRowsForIDs(ctx, tx, `SELECT `+artifactColumns+` FROM artifacts WHERE message_id IN (%s)`, messageIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range fileRows {
		remap[forkString(row["id"])] = genID("file")
	}
	docIDs := []string{}
	for _, row := range docRows {
		if status := forkString(row["status"]); status != "ready" && status != "failed" {
			return nil, ErrForkAttachmentsProcessing
		}
		id := forkString(row["id"])
		docIDs = append(docIDs, id)
		remap[id] = genID("doc")
	}
	for _, row := range artifactRows {
		remap[forkString(row["id"])] = genID("art")
	}
	chunkColumns := "id,document_id,kb_id,conversation_id,seq,parent_id,chunk_type,content,image_ref,meta,embedding_model"
	chunkRows := []map[string]any{}
	chunkIDs := map[string]string{}
	if len(docIDs) > 0 {
		chunkRows, err = forkRowsForIDs(ctx, tx, `SELECT `+chunkColumns+` FROM chunks WHERE document_id IN (%s) ORDER BY seq,id`, docIDs)
		if err != nil {
			return nil, err
		}
		for _, row := range chunkRows {
			oldID := forkString(row["id"])
			newID := NewChunkID()
			remap[oldID] = newID
			chunkIDs[oldID] = newID
		}
	}
	var addedBytes int64
	for _, row := range fileRows {
		if forkString(row["kind"]) != "image" {
			n, _ := row["size_bytes"].(int64)
			addedBytes += n
		}
	}
	// Legacy documents without a files twin are billed as documents. Files
	// with shared immutable bytes still count as independent quota records.
	for _, row := range docRows {
		var hasTwin bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM files WHERE storage_path=?)`, row["storage_path"]).Scan(&hasTwin); err != nil {
			return nil, err
		}
		if !hasTwin {
			n, _ := row["size_bytes"].(int64)
			addedBytes += n
		}
	}
	billingUserID := userID
	if workspaceID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM workspaces WHERE id=?`, workspaceID).Scan(&billingUserID); err != nil {
			return nil, err
		}
	}
	if err := enforceStorageQuotaTx(ctx, tx, billingUserID, addedBytes); err != nil {
		return nil, err
	}
	for i := range copies {
		copies[i].Blocks = ForkRemapJSON(copies[i].Blocks, remap)
		copies[i].Raw = ForkRemapJSON(copies[i].Raw, remap)
		copies[i].Attachments = ForkRemapJSON(copies[i].Attachments, remap)
		copies[i].Citations = ForkRemapJSON(copies[i].Citations, remap)
	}
	if _, err := createMessagePathTx(ctx, tx, copies); err != nil {
		return nil, err
	}
	for _, group := range []struct {
		table, columns string
		rows           []map[string]any
	}{
		{"files", fileColumns, fileRows}, {"documents", docColumns, docRows}, {"artifacts", artifactColumns, artifactRows}, {"chunks", chunkColumns, chunkRows},
	} {
		for _, row := range group.rows {
			for _, column := range []string{"id", "conversation_id", "message_id", "document_id", "parent_id", "branch_message_id"} {
				if next, ok := remap[forkString(row[column])]; ok {
					row[column] = next
				}
			}
			if group.table == "files" {
				row["user_id"] = userID
				row["draft"] = 0
			}
			if group.table == "documents" {
				row["uploaded_by_user_id"] = userID
			}
			if group.table == "chunks" {
				row["meta"] = string(ForkRemapJSON([]byte(forkString(row["meta"])), remap))
			}
			columns := strings.Split(group.columns, ",")
			values := make([]any, len(columns))
			for i, column := range columns {
				values[i] = row[column]
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO `+group.table+`(`+group.columns+`) VALUES (`+idPlaceholders(len(columns))+`)`, values...); err != nil {
				return nil, err
			}
		}
	}
	// SQLite vectors have relational ownership and can be copied in the same
	// transaction. Qdrant points are copied by the API before delivering the fork.
	for oldID, newID := range chunkIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO vector_points(chunk_id,dimension,embedding) SELECT ?,dimension,embedding FROM vector_points WHERE chunk_id=?`, newID, oldID); err != nil {
			return nil, err
		}
	}
	// Keep the source conversation lock until the external index is copied;
	// deleting the original cannot remove its vectors in the middle of the fork.
	if copyVectors != nil && len(chunkIDs) > 0 {
		if err := copyVectors(ctx, sourceID, remap); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return remap, nil
}

func forkNonEmptyIDs(ids []string) []string {
	if len(ids) == 0 {
		return []string{""}
	}
	return ids
}
func forkString(value any) string {
	if value == nil {
		return ""
	}
	if b, ok := value.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(value)
}

func forkRows(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, column := range columns {
			row[column] = values[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Bound SQL variables for long message paths and large attachment collections.
func forkRowsForIDs(ctx context.Context, tx *sql.Tx, query string, ids []string) ([]map[string]any, error) {
	var rows []map[string]any
	for start := 0; start < len(ids); start += 400 {
		batch := ids[start:min(start+400, len(ids))]
		part, err := forkRows(ctx, tx, fmt.Sprintf(query, idPlaceholders(len(batch))), anySlice(batch)...)
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// Rewrite actual JSON strings, including download URLs inside Markdown/tool
// output, while preserving JSON escaping and provider-specific nested shapes.
func ForkRemapJSON(raw json.RawMessage, remap map[string]string) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	pairs := []string{}
	for oldID, newID := range remap {
		pairs = append(pairs, oldID, newID)
	}
	replace := strings.NewReplacer(pairs...)
	var rewrite func(any) any
	rewrite = func(value any) any {
		switch v := value.(type) {
		case string:
			return replace.Replace(v)
		case []any:
			for i := range v {
				v[i] = rewrite(v[i])
			}
		case map[string]any:
			for key, item := range v {
				v[key] = rewrite(item)
			}
		}
		return value
	}
	encoded, err := json.Marshal(rewrite(value))
	if err != nil {
		return raw
	}
	return encoded
}
