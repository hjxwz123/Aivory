package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveOwnedKBIDsIsStrictAndOrdered(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "kb-selection.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES
		('u1','u1@example.test','h','user'),
		('u2','u2@example.test','h','user')`)
	exec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	exec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim)
		VALUES('emb-a','ch1','embedding','emb-a','Embedding A',3)`)
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
		('kb-a','u1','A','emb-a',3),
		('kb-b','u1','B','emb-a',3),
		('kb-other','u2','Other','emb-a',3),
		('kb-project','u1','Project Library','emb-a',3)`)
	exec(t, db, `INSERT INTO projects(id,user_id,name,kb_id) VALUES
		('project-1','u1','Project','kb-project')`)

	ids, err := ResolveOwnedKBIDs(ctx, db, "u1", "", []string{"kb-b", " kb-a ", "kb-b"})
	if err != nil || len(ids) != 2 || ids[0] != "kb-b" || ids[1] != "kb-a" {
		t.Fatalf("resolved ids=%v err=%v, want [kb-b kb-a]", ids, err)
	}

	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{name: "missing", ids: []string{"kb-missing"}},
		{name: "blank", ids: []string{" "}},
		{name: "unauthorized", ids: []string{"kb-other"}},
		{name: "partially unauthorized", ids: []string{"kb-a", "kb-other"}},
		{name: "project library", ids: []string{"kb-project"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ResolveOwnedKBIDs(ctx, db, "u1", "", tc.ids); !errors.Is(err, ErrNotFound) {
				t.Fatalf("error=%v, want ErrNotFound", err)
			}
		})
	}
}

func TestResolveOwnedKBIDsReturnsDatabaseErrors(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "kb-selection-error.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = ResolveOwnedKBIDs(context.Background(), db, "u1", "", []string{"kb-a"})
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%v, want database error", err)
	}
}
