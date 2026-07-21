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

func TestAdminFilesTypeFilterAndPaginationCount(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "filetypes.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','files@x.io','h','Files','user')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO files(id,user_id,filename,mime_type,kind,size_bytes,storage_path,created_at) VALUES
		('pdf-ext','u1','REPORT.PDF','application/octet-stream','pdf',10,'/tmp/pdf-ext',1),
		('pdf-mime','u1','download','application/pdf','pdf',10,'/tmp/pdf-mime',2),
		('document','u1','proposal.docx','application/octet-stream','doc',10,'/tmp/document',3),
		('presentation','u1','deck.bin','application/vnd.openxmlformats-officedocument.presentationml.presentation','doc',10,'/tmp/presentation',4),
		('spreadsheet','u1','BOOK.XLSX','application/octet-stream','sheet',10,'/tmp/spreadsheet',5),
		('csv','u1','rows.csv','text/csv','sheet',10,'/tmp/csv',6),
		('image','u1','opaque.bin','image/png','image',10,'/tmp/image',7),
		('text','u1','main.go','application/octet-stream','code',10,'/tmp/text',8),
		('other','u1','archive.bin','application/octet-stream','other',10,'/tmp/other',9)`); err != nil {
		t.Fatalf("seed files: %v", err)
	}

	for _, tc := range []struct {
		fileType string
		want     int
	}{
		{fileType: "all", want: 9},
		{fileType: "pdf", want: 2},
		{fileType: "document", want: 1},
		{fileType: "presentation", want: 1},
		{fileType: "spreadsheet", want: 2},
		{fileType: "image", want: 1},
		{fileType: "text", want: 1},
		{fileType: "other", want: 1},
		{fileType: "not-a-real-type", want: 9},
	} {
		t.Run(tc.fileType, func(t *testing.T) {
			filter := AdminFileFilter{Type: tc.fileType}
			total, err := CountAdminFiles(ctx, db, filter)
			if err != nil {
				t.Fatalf("count: %v", err)
			}
			rows, err := ListAdminFiles(ctx, db, filter, 50, 0)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if total != tc.want || len(rows) != tc.want {
				t.Fatalf("type=%q total=%d rows=%d, want %d", tc.fileType, total, len(rows), tc.want)
			}
		})
	}

	// Count must describe the complete filtered result, independently of the
	// current page size/offset used by ListAdminFiles.
	filter := AdminFileFilter{Type: "PDF"}
	total, err := CountAdminFiles(ctx, db, filter)
	if err != nil {
		t.Fatalf("count paginated pdf: %v", err)
	}
	first, err := ListAdminFiles(ctx, db, filter, 1, 0)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	second, err := ListAdminFiles(ctx, db, filter, 1, 1)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if total != 2 || len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Fatalf("pdf pagination total=%d first=%v second=%v", total, first, second)
	}
}

func TestAdminFilesTypeFilterExtensionAndMIMEMatrix(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "file-type-matrix.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name,role) VALUES(?,?,?,?,?)`,
		"u1", "matrix@x.io", "h", "Matrix", "user"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	tests := []struct {
		name     string
		filename string
		mimeType string
		wantType string
	}{
		{name: "docm extension", filename: "case-docm.DOCM", mimeType: "application/octet-stream", wantType: "document"},
		{name: "dot extension", filename: "case-dot.dot", mimeType: "application/octet-stream", wantType: "document"},
		{name: "dotm extension", filename: "case-dotm.dotm", mimeType: "application/octet-stream", wantType: "document"},
		{name: "dotx extension", filename: "case-dotx.dotx", mimeType: "application/octet-stream", wantType: "document"},
		{name: "pot extension", filename: "case-pot.pot", mimeType: "application/octet-stream", wantType: "presentation"},
		{name: "potm extension", filename: "case-potm.potm", mimeType: "application/octet-stream", wantType: "presentation"},
		{name: "potx extension", filename: "case-potx.potx", mimeType: "application/octet-stream", wantType: "presentation"},
		{name: "pps extension", filename: "case-pps.pps", mimeType: "application/octet-stream", wantType: "presentation"},
		{name: "ppsm extension", filename: "case-ppsm.ppsm", mimeType: "application/octet-stream", wantType: "presentation"},
		{name: "ppsx extension", filename: "case-ppsx.ppsx", mimeType: "application/octet-stream", wantType: "presentation"},
		{name: "pptm extension", filename: "case-pptm.pptm", mimeType: "application/octet-stream", wantType: "presentation"},
		{name: "xlsb extension", filename: "case-xlsb.XLSB", mimeType: "application/octet-stream", wantType: "spreadsheet"},
		{name: "xlt extension", filename: "case-xlt.xlt", mimeType: "application/octet-stream", wantType: "spreadsheet"},
		{name: "xltm extension", filename: "case-xltm.xltm", mimeType: "application/octet-stream", wantType: "spreadsheet"},
		{name: "xltx extension", filename: "case-xltx.xltx", mimeType: "application/octet-stream", wantType: "spreadsheet"},
		{name: "jsonl extension", filename: "case-jsonl.jsonl", mimeType: "application/octet-stream", wantType: "text"},
		{name: "cfg extension", filename: "case-cfg.cfg", mimeType: "application/octet-stream", wantType: "text"},
		{name: "mjs extension", filename: "case-mjs.mjs", mimeType: "application/octet-stream", wantType: "text"},
		{name: "vue extension", filename: "case-vue.vue", mimeType: "application/octet-stream", wantType: "text"},
		{name: "fish extension", filename: "case-fish.fish", mimeType: "application/octet-stream", wantType: "text"},
		{name: "tex extension", filename: "case-tex.tex", mimeType: "application/octet-stream", wantType: "text"},
		{name: "htm extension", filename: "case-htm.htm", mimeType: "application/octet-stream", wantType: "text"},
		{name: "html extension", filename: "case-html.html", mimeType: "application/octet-stream", wantType: "text"},
		{name: "docm MIME", filename: "mime-docm", mimeType: "application/vnd.ms-word.document.macroEnabled.12", wantType: "document"},
		{name: "dotx MIME", filename: "mime-dotx", mimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.template", wantType: "document"},
		{name: "pptm MIME", filename: "mime-pptm", mimeType: "application/vnd.ms-powerpoint.presentation.macroEnabled.12", wantType: "presentation"},
		{name: "ppsx MIME", filename: "mime-ppsx", mimeType: "application/vnd.openxmlformats-officedocument.presentationml.slideshow", wantType: "presentation"},
		{name: "xlsb MIME", filename: "mime-xlsb", mimeType: "application/vnd.ms-excel.sheet.binary.macroEnabled.12", wantType: "spreadsheet"},
		{name: "xltx MIME", filename: "mime-xltx", mimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.template", wantType: "spreadsheet"},
		{name: "JSON MIME", filename: "mime-json", mimeType: "application/json; charset=utf-8", wantType: "text"},
		{name: "JavaScript MIME", filename: "mime-javascript-only", mimeType: "application/javascript", wantType: "text"},
		{name: "XML MIME", filename: "mime-xml", mimeType: "application/xml", wantType: "text"},
	}

	for i, tc := range tests {
		id := "matrix-file-" + tc.filename
		if _, err := db.ExecContext(ctx, `INSERT INTO files(id,user_id,filename,mime_type,kind,size_bytes,storage_path,created_at) VALUES(?,?,?,?,?,?,?,?)`,
			id, "u1", tc.filename, tc.mimeType, "other", 10, "/tmp/"+id, i+1); err != nil {
			t.Fatalf("seed %s: %v", tc.name, err)
		}
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filter := AdminFileFilter{Search: tc.filename, Type: tc.wantType}
			total, err := CountAdminFiles(ctx, db, filter)
			if err != nil {
				t.Fatalf("count: %v", err)
			}
			rows, err := ListAdminFiles(ctx, db, filter, 10, 0)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if total != 1 || len(rows) != 1 || rows[0].Filename != tc.filename {
				t.Fatalf("filename=%q type=%q total=%d rows=%v, want one match", tc.filename, tc.wantType, total, rows)
			}
		})
	}
}
