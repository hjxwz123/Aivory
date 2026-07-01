package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMigrateDedupesSkillNamesAndEnforcesUnique(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatalf("apply base schema: %v", err)
	}
	legacy := []string{
		`DROP INDEX idx_skills_name_unique`,
		`INSERT INTO skills(id, name, description, instructions, sort_order, updated_at) VALUES('sk_keep', ' deck ', 'A', 'A', 0, 1)`,
		`INSERT INTO skills(id, name, description, instructions, sort_order, updated_at) VALUES('sk_dup', 'DECK', 'B', 'B', 1, 2)`,
		`INSERT INTO channels(id, name, type) VALUES('ch1', 'Channel', 'openai')`,
		`INSERT INTO models(id, channel_id, request_id, label) VALUES('m1', 'ch1', 'req', 'Model')`,
		`INSERT INTO model_skills(model_id, skill_id) VALUES('m1', 'sk_dup')`,
	}
	for _, q := range legacy {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("legacy seed %q: %v", q, err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var skillCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM skills WHERE lower(trim(name))='deck'`).Scan(&skillCount); err != nil {
		t.Fatalf("count skills: %v", err)
	}
	if skillCount != 1 {
		t.Fatalf("expected one normalized deck skill after migrate, got %d", skillCount)
	}

	var boundID string
	if err := db.QueryRowContext(ctx, `SELECT skill_id FROM model_skills WHERE model_id='m1'`).Scan(&boundID); err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if boundID != "sk_keep" {
		t.Fatalf("expected binding moved to sk_keep, got %q", boundID)
	}

	if _, err := CreateSkill(ctx, db, Skill{Name: "Deck", Description: "C", Instructions: "C", Enabled: true}); !errors.Is(err, ErrSkillNameExists) {
		t.Fatalf("expected ErrSkillNameExists, got %v", err)
	}
}

func TestMigrateRenamesHistoricalDuplicateNamesAndEnforcesUnique(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "unique-names.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatalf("apply base schema: %v", err)
	}
	for _, q := range []string{
		`DROP INDEX idx_channels_name_unique`,
		`DROP INDEX idx_models_channel_request_unique`,
		`DROP INDEX idx_projects_user_name_unique`,
		`DROP INDEX idx_kbs_user_name_unique`,
		`INSERT INTO users(id, email, password_hash, name) VALUES('u1', 'u1@example.com', 'hash', 'User')`,
		`INSERT INTO channels(id, name, type, sort_order, updated_at) VALUES('ch1', 'Main', 'openai', 0, 1)`,
		`INSERT INTO models(id, channel_id, kind, request_id, label, sort_order, updated_at) VALUES('em1', 'ch1', 'embedding', 'embed', 'Embed', 0, 1)`,
		`INSERT INTO projects(id, user_id, name, created_at, updated_at) VALUES('pr_keep', 'u1', 'Research', 1, 1)`,
		`INSERT INTO projects(id, user_id, name, created_at, updated_at) VALUES('pr_dup', 'u1', ' research ', 2, 2)`,
		`INSERT INTO knowledge_bases(id, user_id, name, embedding_model_id, embedding_dim, created_at) VALUES('kb_keep', 'u1', 'Library', 'em1', 3, 1)`,
		`INSERT INTO knowledge_bases(id, user_id, name, embedding_model_id, embedding_dim, created_at) VALUES('kb_dup', 'u1', ' library ', 'em1', 3, 2)`,
		`INSERT INTO models(id, channel_id, kind, request_id, label, sort_order, updated_at) VALUES('m_keep', 'ch1', 'chat', 'gpt-test', 'GPT test', 1, 2)`,
		`INSERT INTO models(id, channel_id, kind, request_id, label, sort_order, updated_at) VALUES('m_dup', 'ch1', 'chat', ' GPT-TEST ', 'GPT test duplicate', 2, 3)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("legacy seed %q: %v", q, err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	assertRowsRemainAndNamesUnique := func(table, where string) {
		t.Helper()
		var total, unique int
		query := `SELECT count(*), count(DISTINCT lower(trim(name))) FROM ` + table
		if where != "" {
			query += ` WHERE ` + where
		}
		if err := db.QueryRowContext(ctx, query).Scan(&total, &unique); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if total != 2 || unique != 2 {
			t.Fatalf("%s: expected two preserved rows with unique names, got total=%d unique=%d", table, total, unique)
		}
	}
	assertRowsRemainAndNamesUnique("projects", "user_id='u1'")
	assertRowsRemainAndNamesUnique("knowledge_bases", "user_id='u1'")

	var modelTotal, modelUnique int
	if err := db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT lower(trim(request_id))) FROM models WHERE channel_id='ch1' AND kind='chat'`).Scan(&modelTotal, &modelUnique); err != nil {
		t.Fatalf("count models: %v", err)
	}
	if modelTotal != 2 || modelUnique != 2 {
		t.Fatalf("models: expected two preserved rows with unique request ids, got total=%d unique=%d", modelTotal, modelUnique)
	}

	if _, err := CreateProject(ctx, db, Project{UserID: "u1", Name: "RESEARCH"}); !errors.Is(err, ErrProjectNameExists) {
		t.Fatalf("expected ErrProjectNameExists, got %v", err)
	}
	if _, err := CreateKB(ctx, db, KnowledgeBase{UserID: "u1", Name: "LIBRARY", EmbeddingModelID: "em1", EmbeddingDim: 3}); !errors.Is(err, ErrKBNameExists) {
		t.Fatalf("expected ErrKBNameExists, got %v", err)
	}
	if _, err := CreateModel(ctx, db, Model{ChannelID: "ch1", Kind: "chat", RequestID: "GPT-TEST", Label: "Duplicate", Enabled: true}); !errors.Is(err, ErrModelRequestExists) {
		t.Fatalf("expected ErrModelRequestExists, got %v", err)
	}
}
