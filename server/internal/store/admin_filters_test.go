package store

import (
	"context"
	"path/filepath"
	"testing"
)

// § admin filters: UsageFilter.Purpose (exact + "task" umbrella) and
// AdminFileFilter.UserQ (user_id exact OR email/name substring — the
// files page's search-based owner filter).
func TestUsageFilterPurpose(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "purpose.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','alice@x.io','h','Alice','user')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, p := range []string{"chat", "chat", "image", "task.title", "task.router", "embedding"} {
		if err := LogUsage(ctx, db, UsageLog{UserID: "u1", ModelID: "m1", Purpose: p}); err != nil {
			t.Fatalf("log usage %s: %v", p, err)
		}
	}

	count := func(f UsageFilter) int {
		rows, err := AdminUsageRecords(ctx, db, f, 50, 0)
		if err != nil {
			t.Fatalf("records: %v", err)
		}
		return len(rows)
	}
	if n := count(UsageFilter{}); n != 6 {
		t.Fatalf("no filter = %d rows, want 6", n)
	}
	if n := count(UsageFilter{Purpose: "chat"}); n != 2 {
		t.Fatalf("purpose=chat = %d rows, want 2", n)
	}
	if n := count(UsageFilter{Purpose: "task.title"}); n != 1 {
		t.Fatalf("purpose=task.title = %d rows, want 1", n)
	}
	// Umbrella: matches every task.* sub-purpose.
	if n := count(UsageFilter{Purpose: "task"}); n != 2 {
		t.Fatalf("purpose=task umbrella = %d rows, want 2", n)
	}
	if n := count(UsageFilter{Purpose: "embedding"}); n != 1 {
		t.Fatalf("purpose=embedding = %d rows, want 1", n)
	}
}

func TestAdminFilesUserQFilter(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "filesuserq.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name,role) VALUES
		('u1','alice@x.io','h','Alice','user'),
		('u2','bob@y.io','h','Bob','user')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO files(id,user_id,filename,mime_type,kind,size_bytes,storage_path) VALUES
		('f1','u1','a.pdf','application/pdf','pdf',10,'/tmp/a'),
		('f2','u2','b.pdf','application/pdf','pdf',10,'/tmp/b')`); err != nil {
		t.Fatalf("seed files: %v", err)
	}

	count := func(f AdminFileFilter) int {
		rows, err := ListAdminFiles(ctx, db, f, 50, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return len(rows)
	}
	if n := count(AdminFileFilter{}); n != 2 {
		t.Fatalf("no filter = %d, want 2", n)
	}
	// Email substring (case-insensitive).
	if n := count(AdminFileFilter{UserQ: "ALICE"}); n != 1 {
		t.Fatalf("userq=ALICE = %d, want 1", n)
	}
	// Name substring.
	if n := count(AdminFileFilter{UserQ: "bob"}); n != 1 {
		t.Fatalf("userq=bob = %d, want 1", n)
	}
	// Exact user_id.
	if n := count(AdminFileFilter{UserQ: "u1"}); n != 1 {
		t.Fatalf("userq=u1 = %d, want 1", n)
	}
	if n := count(AdminFileFilter{UserQ: "nobody"}); n != 0 {
		t.Fatalf("userq=nobody = %d, want 0", n)
	}
}
