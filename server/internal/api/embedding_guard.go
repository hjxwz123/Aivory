package api

import (
	"context"
	"database/sql"
	"strings"
)

// Embedding-model reference guards.
//
// A model used as the RAG embedding backend is referenced from two places:
// the global settings key `embedding_model_id` (the write-once lock — plain
// JSON text in `settings`, NO foreign key) and each
// `knowledge_bases.embedding_model_id` (a real FK to models, so the database
// itself refuses to delete a KB-locked row, but only with an opaque FK error).
// The settings reference has no FK protection at all: deleting the model's
// CHANNEL cascaded (models.channel_id ON DELETE CASCADE) straight through the
// locked embedding model and left the global lock dangling with no API way to
// repair it. These helpers turn both reference paths into explicit 409s
// before the destructive write happens.

// modelIDsInChannel returns every model id owned by a channel — the rows a
// channel delete would cascade to.
func modelIDsInChannel(ctx context.Context, db *sql.DB, channelID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM models WHERE channel_id=?`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// knowledgeBasesLocking maps each given model id to the names of the
// knowledge bases whose locked embedding model it is. Purely for diagnostics
// + deciding whether a KB reference exists; the caller treats "any names" as
// in-use.
func knowledgeBasesLocking(ctx context.Context, db *sql.DB, modelIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(modelIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(modelIDs))
	for _, id := range modelIDs {
		args = append(args, id)
	}
	query := `SELECT embedding_model_id, name FROM knowledge_bases WHERE embedding_model_id IN (` +
		strings.TrimSuffix(strings.Repeat("?,", len(modelIDs)), ",") + `)`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var modelID, name string
		if err := rows.Scan(&modelID, &name); err != nil {
			return nil, err
		}
		out[modelID] = append(out[modelID], name)
	}
	return out, rows.Err()
}

// guardEmbeddingModelsDeletion refuses to destroy the given model rows while
// any of them is referenced as an embedding model. The global settings lock
// keeps its existing errEmbeddingModelLocked semantics (model-delete tests and
// the AdminDocuments locale already match on that code); knowledge-base locks
// surface as the new errEmbeddingModelInUse.
func guardEmbeddingModelsDeletion(ctx context.Context, d Deps, modelIDs []string) error {
	if len(modelIDs) == 0 {
		return nil
	}
	lockedID, err := lockedEmbeddingModelID(d)
	if err != nil {
		return err
	}
	for _, id := range modelIDs {
		if id == lockedID {
			return errEmbeddingModelLocked
		}
	}
	kbs, err := knowledgeBasesLocking(ctx, d.DB, modelIDs)
	if err != nil {
		return err
	}
	for id, names := range kbs {
		if len(names) > 0 {
			if d.Logger != nil {
				d.Logger.Printf("admin: refusing delete — embedding model %s still locked by knowledge bases %v", id, names)
			}
			return errEmbeddingModelInUse
		}
	}
	return nil
}

// guardEmbeddingModelUpdate reports whether vector-identity fields of model
// id may change: false (→ errEmbeddingModelLocked) while the id is the global
// settings lock OR any KB lists it as its locked embedding model. Global
// identity changes strand every chunk/Qdrant collection built with the old
// vectors — regardless of WHICH row wrote the lock.
func guardEmbeddingModelUpdate(ctx context.Context, d Deps, id string) (bool, error) {
	lockedID, err := lockedEmbeddingModelID(d)
	if err != nil {
		return false, err
	}
	if lockedID != "" && lockedID == id {
		return true, nil
	}
	kbs, err := knowledgeBasesLocking(ctx, d.DB, []string{id})
	if err != nil {
		return false, err
	}
	return len(kbs[id]) > 0, nil
}
