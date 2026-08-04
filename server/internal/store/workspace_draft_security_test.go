package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestMigrateAddsDraftStorageIndexToLegacyFilesTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-files-draft.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("seed current schema: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE files DROP COLUMN draft`); err != nil {
		t.Fatalf("simulate legacy files table: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	if _, err := db.Exec(`SELECT draft FROM files WHERE 1=0`); err != nil {
		t.Fatalf("draft column missing after migration: %v", err)
	}
	rows, err := db.Query(`PRAGMA index_info('idx_files_storage_draft')`)
	if err != nil {
		t.Fatalf("inspect draft storage index: %v", err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var sequence, columnID int
		var name string
		if err := rows.Scan(&sequence, &columnID, &name); err != nil {
			t.Fatalf("scan draft storage index: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate draft storage index: %v", err)
	}
	if want := []string{"storage_path", "draft"}; !reflect.DeepEqual(columns, want) {
		t.Fatalf("draft storage index columns = %v, want %v", columns, want)
	}
}

// TestWorkspaceDraftFilesAreUploaderPrivate covers every store boundary used by
// the workspace conversation UI and attachment normalizer. Committed files stay
// collaborative; composer drafts remain visible only to their uploader.
func TestWorkspaceDraftFilesAreUploaderPrivate(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-drafts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, user := range []string{"owner", "member", "outsider"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES(?,?, 'h','user')`, user, user+"@example.test")
	}
	exec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws1','Shared','owner','invite')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','owner','owner')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','member','member')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('c1','owner','Shared chat','ws1')`)
	exec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	exec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('emb1','ch1','embedding','emb','Embedding',3)`)
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('kb1','owner','Shared KB','emb1',3,'ws1')`)
	insertFile := func(id, user string, draft int) {
		t.Helper()
		exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, id, user, "c1", id+".txt", "text/plain", 10, "/tmp/"+id, "text", draft, 1)
	}
	insertFile("owner-draft", "owner", 1)
	insertFile("owner-committed", "owner", 0)
	insertFile("member-draft", "member", 1)
	insertFile("member-committed", "member", 0)
	// A stale/malformed ownership row must not grant conversation access by
	// itself; every conversation-scoped list first checks owner/membership.
	insertFile("outsider-draft", "outsider", 1)
	// The RAG worker may finish ingesting before the composer sends. Both the
	// conversation document and a project auto-add KB twin must stay private
	// until the file row is committed.
	if _, err := CreateDocument(ctx, db, Document{ID: "doc-conv-draft", ConversationID: "c1", Filename: "owner-draft.txt", MimeType: "text/plain", SizeBytes: 10, Status: "ready", StoragePath: "/tmp/owner-draft"}); err != nil {
		t.Fatalf("create conversation draft doc: %v", err)
	}
	if err := CreateChunk(ctx, db, "doc-conv-draft", "", "c1", 0, "private conversation draft", "emb1"); err != nil {
		t.Fatalf("create conversation draft chunk: %v", err)
	}
	if _, err := CreateDocument(ctx, db, Document{ID: "doc-kb-draft", KBID: "kb1", Filename: "owner-draft.txt", MimeType: "text/plain", SizeBytes: 10, Status: "ready", StoragePath: "/tmp/owner-draft"}); err != nil {
		t.Fatalf("create KB draft twin: %v", err)
	}
	if err := CreateChunk(ctx, db, "doc-kb-draft", "kb1", "", 0, "private KB draft", "emb1"); err != nil {
		t.Fatalf("create KB draft chunk: %v", err)
	}

	ids := func(files []File) map[string]bool {
		got := make(map[string]bool, len(files))
		for _, f := range files {
			got[f.ID] = true
		}
		return got
	}
	ownerFiles, err := ListFilesByConversation(ctx, db, "c1", "owner")
	if err != nil {
		t.Fatalf("owner list: %v", err)
	}
	if got := ids(ownerFiles); !got["owner-draft"] || !got["owner-committed"] || !got["member-committed"] || got["member-draft"] {
		t.Fatalf("owner list = %#v; want own draft plus committed workspace files", got)
	}
	memberFiles, err := ListFilesByConversation(ctx, db, "c1", "member")
	if err != nil {
		t.Fatalf("member list: %v", err)
	}
	if got := ids(memberFiles); !got["member-draft"] || !got["owner-committed"] || !got["member-committed"] || got["owner-draft"] {
		t.Fatalf("member list = %#v; owner draft must be hidden", got)
	}
	outsiderFiles, err := ListFilesByConversation(ctx, db, "c1", "outsider")
	if err != nil {
		t.Fatalf("outsider list: %v", err)
	}
	if len(outsiderFiles) != 0 {
		t.Fatalf("outsider list = %#v; file ownership alone must not grant conversation access", ids(outsiderFiles))
	}
	ownerDocs, err := ListDocumentsForUser(ctx, db, "conversation", "c1", "owner")
	if err != nil {
		t.Fatalf("owner conversation docs: %v", err)
	}
	memberDocs, err := ListDocumentsForUser(ctx, db, "conversation", "c1", "member")
	if err != nil {
		t.Fatalf("member conversation docs: %v", err)
	}
	if len(ownerDocs) != 1 || ownerDocs[0].ID != "doc-conv-draft" || len(memberDocs) != 0 {
		t.Fatalf("conversation docs owner=%#v member=%#v; draft doc must be uploader-private", ownerDocs, memberDocs)
	}
	if _, err := GetDocument(ctx, db, "doc-conv-draft"); err != nil {
		t.Fatalf("admin/unscoped GetDocument(conversation draft) = %v; want visible", err)
	}
	if _, err := GetDocumentForUser(ctx, db, "doc-conv-draft", "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member GetDocument(conversation draft) = %v; want ErrNotFound", err)
	}
	if _, err := GetDocumentForUser(ctx, db, "doc-kb-draft", "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member GetDocument(KB draft) = %v; want ErrNotFound", err)
	}
	// Direct/legacy documents have no files twin. They keep the parent
	// conversation's existing visibility instead of becoming hidden forever.
	if _, err := CreateDocument(ctx, db, Document{ID: "doc-legacy", ConversationID: "c1", Filename: "legacy.txt", MimeType: "text/plain", Status: "ready", StoragePath: "/tmp/legacy"}); err != nil {
		t.Fatalf("create legacy document: %v", err)
	}
	memberDocs, err = ListDocumentsForUser(ctx, db, "conversation", "c1", "member")
	if err != nil || len(memberDocs) != 1 || memberDocs[0].ID != "doc-legacy" {
		t.Fatalf("member legacy docs=%#v err=%v; want visible direct document", memberDocs, err)
	}
	if _, err := GetDocumentForUser(ctx, db, "doc-legacy", "member"); err != nil {
		t.Fatalf("member GetDocument(legacy direct document) = %v; want visible", err)
	}
	if err := DeleteDocument(ctx, db, "doc-legacy"); err != nil {
		t.Fatalf("delete legacy document fixture: %v", err)
	}
	memberKBDocs, err := ListDocumentsForUser(ctx, db, "kb", "kb1", "member")
	if err != nil {
		t.Fatalf("member KB docs: %v", err)
	}
	if len(memberKBDocs) != 0 {
		t.Fatalf("member KB docs=%#v; auto-add draft twin must be hidden", memberKBDocs)
	}
	if got, err := ListChunksInScope(ctx, db, nil, "c1"); err != nil || len(got) != 0 {
		t.Fatalf("conversation RAG scope while draft: chunks=%#v err=%v; want empty", got, err)
	}
	if got, err := ListChunksInScope(ctx, db, []string{"kb1"}, ""); err != nil || len(got) != 0 {
		t.Fatalf("KB RAG scope while draft: chunks=%#v err=%v; want empty", got, err)
	}

	if _, err := GetFile(ctx, db, "owner-draft", "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member GetFile(owner draft) = %v; want ErrNotFound", err)
	}
	if f, err := GetFile(ctx, db, "owner-draft", "owner"); err != nil || f == nil || !f.Draft {
		t.Fatalf("owner GetFile(owner draft) = %#v, %v; want draft", f, err)
	}
	if f, err := GetFile(ctx, db, "owner-draft", ""); err != nil || f == nil || !f.Draft {
		t.Fatalf("admin/unscoped GetFile(owner draft) = %#v, %v; want draft", f, err)
	}
	if f, err := GetFile(ctx, db, "owner-committed", "member"); err != nil || f == nil || f.Draft {
		t.Fatalf("member GetFile(committed) = %#v, %v; want shared committed file", f, err)
	}
	// Committing the file makes both document twins collaborative again.
	exec(t, db, `UPDATE files SET draft=0 WHERE id='owner-draft'`)
	memberDocs, err = ListDocumentsForUser(ctx, db, "conversation", "c1", "member")
	if err != nil || len(memberDocs) != 1 || memberDocs[0].ID != "doc-conv-draft" {
		t.Fatalf("member conversation docs after commit=%#v err=%v; want shared", memberDocs, err)
	}
	memberKBDocs, err = ListDocumentsForUser(ctx, db, "kb", "kb1", "member")
	if err != nil || len(memberKBDocs) != 1 || memberKBDocs[0].ID != "doc-kb-draft" {
		t.Fatalf("member KB docs after commit=%#v err=%v; want shared", memberKBDocs, err)
	}
	if _, err := GetDocumentForUser(ctx, db, "doc-conv-draft", "member"); err != nil {
		t.Fatalf("member GetDocument(conversation committed) = %v; want shared", err)
	}
	if got, err := ListChunksInScope(ctx, db, nil, "c1"); err != nil || len(got) != 1 || got[0].DocumentID != "doc-conv-draft" {
		t.Fatalf("conversation RAG scope after commit: chunks=%#v err=%v; want committed chunk", got, err)
	}
	// Restore the draft state for the message-commit authorization assertions
	// below; the preceding transition is only the document visibility check.
	exec(t, db, `UPDATE files SET draft=1 WHERE id='owner-draft'`)
	if _, err := GetFile(ctx, db, "owner-committed", "outsider"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider GetFile(committed) = %v; want ErrNotFound", err)
	}

	attached, err := ConversationFilesByIDs(ctx, db, "c1", "member", []string{"owner-draft", "owner-committed", "member-draft"})
	if err != nil {
		t.Fatalf("member attachment lookup: %v", err)
	}
	if attached["owner-draft"].ID != "" || attached["owner-committed"].ID != "owner-committed" || attached["member-draft"].ID != "member-draft" {
		t.Fatalf("member attachment lookup = %#v; owner draft must be omitted", attached)
	}
	drafts, err := ListDraftFilesForConversationForUser(ctx, db, "c1", "member")
	if err != nil {
		t.Fatalf("member draft list: %v", err)
	}
	if got := ids(drafts); len(got) != 1 || !got["member-draft"] {
		t.Fatalf("member draft list = %#v; want only member-draft", got)
	}

	// The delete transaction has its own uploader predicate. Do not rely on the
	// preceding read gate to protect it from a direct or future caller.
	if err := DeleteConversationFileAndDocuments(ctx, db, "owner-draft", "c1", "member", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member delete(owner draft) = %v; want ErrNotFound", err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE id='owner-draft'`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("owner draft row after member delete: count=%d err=%v", remaining, err)
	}

	// A user message may commit its own draft, but an attachment id supplied by a
	// different workspace member must not transition the row.
	otherDraftAtt, _ := json.Marshal([]map[string]string{{"id": "owner-draft"}})
	if _, err := CreateMessage(ctx, db, Message{ID: "member-message", ConversationID: "c1", Role: "user", AuthorID: "member", Attachments: otherDraftAtt}); err != nil {
		t.Fatalf("member message: %v", err)
	}
	if f, err := GetFile(ctx, db, "owner-draft", "owner"); err != nil || f == nil || !f.Draft {
		t.Fatalf("owner draft after member message = %#v, %v; member must not commit it", f, err)
	}
	ownerDraftAtt, _ := json.Marshal([]map[string]string{{"id": "owner-draft"}})
	if _, err := CreateMessage(ctx, db, Message{ID: "owner-message", ConversationID: "c1", Role: "user", AuthorID: "owner", Attachments: ownerDraftAtt}); err != nil {
		t.Fatalf("owner message: %v", err)
	}
	if f, err := GetFile(ctx, db, "owner-draft", "owner"); err != nil || f == nil || f.Draft {
		t.Fatalf("owner draft after owner message = %#v, %v; want committed", f, err)
	}
}

// TestWorkspaceMembersCannotCommitDraftsConcurrently makes the ownership
// predicate observable under contention: no number of member-side message
// transactions may transition another user's draft.
func TestWorkspaceMembersCannotCommitDraftsConcurrently(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-drafts-race.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('owner','o@example.test','h','user')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('member','m@example.test','h','user')`)
	exec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws1','Shared','owner','invite')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','owner','owner')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','member','member')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('c1','owner','Shared','ws1')`)
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft) VALUES('owner-draft','owner','c1','x.txt','text/plain',1,'/tmp/x','text',1)`)
	attachments := json.RawMessage(`[{"id":"owner-draft"}]`)
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := CreateMessage(ctx, db, Message{ID: "member-race-" + string(rune('a'+i)), ConversationID: "c1", Role: "user", AuthorID: "member", Attachments: attachments})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("member race message: %v", err)
		}
	}
	var draft int
	if err := db.QueryRowContext(ctx, `SELECT draft FROM files WHERE id='owner-draft'`).Scan(&draft); err != nil {
		t.Fatalf("draft state: %v", err)
	}
	if draft != 1 {
		t.Fatalf("owner draft was committed by member race: draft=%d", draft)
	}
}
