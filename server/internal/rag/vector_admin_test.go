package rag

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"aivory/server/internal/store"
	"aivory/server/internal/vector"
)

func TestVectorAdminAuditAndRebuildMissingVectors(t *testing.T) {
	ctx := context.Background()
	db := seedVectorAdminDB(t, ctx)
	defer db.Close()

	vec := &adminVectorStore{statuses: map[string]vector.ChunkVectorStatus{
		"ch1": {Exists: true, HasVector: true},
	}}
	svc := &Service{db: db, vec: vec}

	audit, err := svc.AuditVectorIndex(ctx)
	if err != nil {
		t.Fatalf("AuditVectorIndex: %v", err)
	}
	if audit.Total != 2 || audit.Present != 1 || audit.Missing != 1 || audit.Empty != 0 {
		t.Fatalf("unexpected audit: %+v", audit)
	}

	report, err := svc.RebuildMissingVectors(ctx, nil)
	if err != nil {
		t.Fatalf("RebuildMissingVectors: %v", err)
	}
	if report.Rebuilt != 1 || report.Failed != 0 {
		t.Fatalf("unexpected rebuild report: %+v", report)
	}
	if report.After == nil || report.After.Missing != 0 || report.After.Empty != 0 || report.After.Present != 2 {
		t.Fatalf("unexpected post-rebuild audit: %+v", report.After)
	}
	if len(vec.points) != 1 || vec.points[0].ChunkID != "ch2" || len(vec.points[0].Vector) == 0 {
		t.Fatalf("unexpected upserted points: %+v", vec.points)
	}
}

func seedVectorAdminDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "vector-admin.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','a@b.c','h','A','user')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`,
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d1','c1','f.txt','text/plain',10,'ready')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('ch1','d1','c1',1,'text','first chunk','aivory-local-embed')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('ch2','d1','c1',2,'text','second chunk','aivory-local-embed')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			_ = db.Close()
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	return db
}

type adminVectorStore struct {
	mu       sync.Mutex
	statuses map[string]vector.ChunkVectorStatus
	points   []vector.Point
}

func (v *adminVectorStore) Enabled() bool { return true }

func (v *adminVectorStore) Upsert(_ context.Context, _ int, points []vector.Point) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.points = append(v.points, points...)
	if v.statuses == nil {
		v.statuses = map[string]vector.ChunkVectorStatus{}
	}
	for _, p := range points {
		v.statuses[p.ChunkID] = vector.ChunkVectorStatus{Exists: true, HasVector: len(p.Vector) > 0}
	}
	return nil
}

func (*adminVectorStore) Search(context.Context, int, []float32, vector.Scope, int) ([]vector.Hit, error) {
	return nil, nil
}

func (*adminVectorStore) SearchKeyword(context.Context, int, string, vector.Scope, int) ([]vector.Hit, error) {
	return nil, nil
}

func (v *adminVectorStore) ExistingChunkIDs(context.Context, int, vector.Scope) (map[string]bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]bool{}
	for id, status := range v.statuses {
		out[id] = status.Exists
	}
	return out, nil
}

func (v *adminVectorStore) VectorChunkStatuses(context.Context, int, vector.Scope) (map[string]vector.ChunkVectorStatus, error) {
	return v.allVectorChunkStatuses(), nil
}

func (v *adminVectorStore) AllVectorChunkStatuses(context.Context, int) (map[string]vector.ChunkVectorStatus, error) {
	return v.allVectorChunkStatuses(), nil
}

func (v *adminVectorStore) allVectorChunkStatuses() map[string]vector.ChunkVectorStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]vector.ChunkVectorStatus{}
	for id, status := range v.statuses {
		out[id] = status
	}
	return out
}

func (*adminVectorStore) DeleteByDocument(context.Context, string) error {
	return nil
}

func (*adminVectorStore) DeleteByKB(context.Context, string) error {
	return nil
}

func (*adminVectorStore) DeleteByConversation(context.Context, string) error {
	return nil
}
