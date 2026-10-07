package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func reorderTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "reorder.db"))
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES
		('u1','one@example.test','h','user','active'),
		('u2','two@example.test','h','user','active'),
		('guest','guest@example.test','h','user','active')`)
	now := time.Now().Unix()
	for index, id := range []string{"a", "b", "c", "d"} {
		mustExec(t, db, `INSERT INTO conversations(id,user_id,title,updated_at) VALUES(?,?,?,?)`, id, "u1", id, now-int64((index+1)*100))
	}
	return db
}

func reorderTestRequest(db *sql.DB, userID, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+id+"/reorder", strings.NewReader(body))
	ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": id})
	ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"})
	rec := httptest.NewRecorder()
	reorderConversationHandler(Deps{DB: db}, rec, req.WithContext(ctx))
	return rec
}

func reorderTestOrder(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM conversations WHERE id IN ('a','b','c','d') ORDER BY updated_at DESC,id DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestReorderConversationPersistsPosition(t *testing.T) {
	for _, test := range []struct {
		source, target, position string
		want                     []string
	}{
		{"c", "b", "before", []string{"a", "c", "b", "d"}},
		{"a", "c", "after", []string{"b", "c", "a", "d"}},
		{"d", "a", "before", []string{"d", "a", "b", "c"}},
		{"b", "d", "after", []string{"a", "c", "d", "b"}},
	} {
		t.Run(test.source+"_"+test.position+"_"+test.target, func(t *testing.T) {
			db := reorderTestDB(t)
			rec := reorderTestRequest(db, "u1", test.source, `{"target_id":"`+test.target+`","position":"`+test.position+`"}`)
			if rec.Code != 200 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := reorderTestOrder(t, db); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("order=%v want=%v", got, test.want)
			}
			var response struct {
				Conversations []store.ConversationTimestamp `json:"conversations"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Conversations) != 1 || response.Conversations[0].ID != test.source {
				t.Fatalf("unnecessary changes: %+v", response)
			}
			if response.Conversations[0].UpdatedAt >= time.Now().Unix() {
				t.Fatal("manual move must not outrank a subsequent message")
			}
			// Updating a conversation normally still returns it to the front.
			title := "continued"
			if _, err := store.UpdateConversation(context.Background(), db, "c", "u1", store.ConversationPatch{Title: &title}); err != nil {
				t.Fatal(err)
			}
			if got := reorderTestOrder(t, db); got[0] != "c" {
				t.Fatalf("continued conversation did not rise: %v", got)
			}
		})
	}
}

func TestReorderConversationSameSecond(t *testing.T) {
	db := reorderTestDB(t)
	mustExec(t, db, `UPDATE conversations SET updated_at=? WHERE id IN ('a','b','c')`, time.Now().Unix())
	rec := reorderTestRequest(db, "u1", "a", `{"target_id":"c","position":"before"}`)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := reorderTestOrder(t, db); !reflect.DeepEqual(got, []string{"a", "c", "b", "d"}) {
		t.Fatalf("order=%v", got)
	}
	listed, err := store.ListConversations(context.Background(), db, "u1", "", "active", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].ID != "a" || listed[1].ID != "c" || listed[2].ID != "b" {
		t.Fatalf("reload order=%+v", listed)
	}
}

func TestReorderConversationRejectsOtherScopes(t *testing.T) {
	for _, test := range []struct {
		name, setup, user, source, body string
		status                          int
	}{
		{"other-owner", `UPDATE conversations SET user_id='u2' WHERE id='b'`, "u1", "a", `{"target_id":"b","position":"before"}`, 404},
		{"unauthorized-source", "", "u2", "a", `{"target_id":"b","position":"before"}`, 404},
		{"starred", `UPDATE conversations SET starred=1 WHERE id='b'`, "u1", "a", `{"target_id":"b","position":"before"}`, 404},
		{"archived", `UPDATE conversations SET archived=1 WHERE id='b'`, "u1", "a", `{"target_id":"b","position":"before"}`, 404},
		{"inline", `UPDATE conversations SET inline_source_conv='a' WHERE id='b'`, "u1", "a", `{"target_id":"b","position":"before"}`, 404},
		{"self", "", "u1", "a", `{"target_id":"a","position":"before"}`, 400},
		{"invalid-position", "", "u1", "a", `{"target_id":"b","position":"sideways"}`, 400},
		{"missing-target", "", "u1", "a", `{"position":"before"}`, 400},
		{"streaming-source", `INSERT INTO messages(id,conversation_id,role,status) VALUES('m','a','assistant','streaming')`, "u1", "a", `{"target_id":"b","position":"after"}`, 409},
		{"streaming-target", `INSERT INTO messages(id,conversation_id,role,status) VALUES('m','b','assistant','streaming')`, "u1", "a", `{"target_id":"b","position":"after"}`, 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := reorderTestDB(t)
			if test.setup != "" {
				mustExec(t, db, test.setup)
			}
			before := reorderTestOrder(t, db)
			rec := reorderTestRequest(db, test.user, test.source, test.body)
			if rec.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.status, rec.Body.String())
			}
			if got := reorderTestOrder(t, db); !reflect.DeepEqual(got, before) {
				t.Fatalf("rejected request changed order: %v", got)
			}
		})
	}
}

func TestReorderConversationWorkspaceMembership(t *testing.T) {
	db := reorderTestDB(t)
	ctx := context.Background()
	workspace, err := store.CreateWorkspace(ctx, db, "u1", "Reorder")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"u2", "guest"} {
		if err := store.JoinWorkspace(ctx, db, workspace.ID, user); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, db, `UPDATE workspace_members SET role='guest' WHERE workspace_id=? AND user_id='guest'`, workspace.ID)
	mustExec(t, db, `UPDATE conversations SET workspace_id=?,is_public=1 WHERE id IN ('a','b')`, workspace.ID)
	if rec := reorderTestRequest(db, "guest", "a", `{"target_id":"b","position":"after"}`); rec.Code != 404 {
		t.Fatalf("guest status=%d %s", rec.Code, rec.Body.String())
	}
	if rec := reorderTestRequest(db, "u2", "a", `{"target_id":"b","position":"after"}`); rec.Code != 200 {
		t.Fatalf("member status=%d %s", rec.Code, rec.Body.String())
	}
	if rec := reorderTestRequest(db, "u1", "a", `{"target_id":"c","position":"after"}`); rec.Code != 404 {
		t.Fatalf("cross-space status=%d", rec.Code)
	}
	mustExec(t, db, `UPDATE conversations SET is_public=0 WHERE id='b'`)
	if rec := reorderTestRequest(db, "u2", "a", `{"target_id":"b","position":"before"}`); rec.Code != 404 {
		t.Fatalf("private target status=%d", rec.Code)
	}
	mustExec(t, db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id='u2'`, workspace.ID)
	if rec := reorderTestRequest(db, "u2", "a", `{"target_id":"b","position":"before"}`); rec.Code != 404 {
		t.Fatalf("revoked member status=%d", rec.Code)
	}
}

func TestReorderConversationProjectScope(t *testing.T) {
	db := reorderTestDB(t)
	mustExec(t, db, `INSERT INTO projects(id,user_id,name) VALUES('project','u1','Project')`)
	mustExec(t, db, `UPDATE conversations SET project_id='project' WHERE id IN ('a','b')`)
	mustExec(t, db, `UPDATE conversations SET starred=1 WHERE id='a'`)
	if rec := reorderTestRequest(db, "u1", "b", `{"target_id":"a","position":"before"}`); rec.Code != 200 {
		t.Fatalf("project star status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := reorderTestOrder(t, db); got[0] != "b" {
		t.Fatalf("project order=%v", got)
	}
	if rec := reorderTestRequest(db, "u1", "b", `{"target_id":"c","position":"after"}`); rec.Code != 404 {
		t.Fatalf("cross-project status=%d", rec.Code)
	}
}

func TestReorderConversationCollisionWithStreamingRowIsAtomic(t *testing.T) {
	db := reorderTestDB(t)
	now := time.Now().Unix()
	mustExec(t, db, `UPDATE conversations SET updated_at=? WHERE id IN ('a','b','c')`, now)
	mustExec(t, db, `INSERT INTO messages(id,conversation_id,role,status) VALUES('m','b','assistant','streaming')`)
	before := reorderTestOrder(t, db)
	rec := reorderTestRequest(db, "u1", "a", `{"target_id":"c","position":"before"}`)
	if rec.Code != 409 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := reorderTestOrder(t, db); !reflect.DeepEqual(got, before) {
		t.Fatalf("partial writes: %v", got)
	}
	var minimum int64
	if err := db.QueryRow(`SELECT MIN(updated_at) FROM conversations WHERE id IN ('a','b','c')`).Scan(&minimum); err != nil {
		t.Fatal(err)
	}
	if minimum != now {
		t.Fatalf("collision rejection changed a timestamp: %d", minimum)
	}
}
