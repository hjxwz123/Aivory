package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestDeleteProjectHandlerDeletesConversationsOnlyWhenRequested(t *testing.T) {
	for _, test := range []struct {
		name                string
		query               string
		wantConversationRow bool
	}{
		{name: "detach conversations by default", wantConversationRow: true},
		{name: "delete conversations when selected", query: "?delete_conversations=true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMigrated(t, filepath.Join(t.TempDir(), "delete-project.db"))
			defer db.Close()
			mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status)
				VALUES('u1','u1@example.test','hash','admin','active')`)
			mustExec(t, db, `INSERT INTO projects(id,user_id,name,workspace_id)
				VALUES('project-1','u1','Project','')`)
			mustExec(t, db, `INSERT INTO conversations(id,user_id,project_id,title,workspace_id)
				VALUES('conversation-1','u1','project-1','Project conversation','')`)

			req := httptest.NewRequest(http.MethodDelete, "/api/projects/project-1"+test.query, nil)
			ctx := context.WithValue(req.Context(), userCtxKey{}, &store.User{
				ID: "u1", Role: "admin", Status: "active",
			})
			ctx = context.WithValue(ctx, pathCtxKey{}, map[string]string{"id": "project-1"})
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()

			deleteProjectHandler(Deps{DB: db}, rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("delete project status=%d body=%s", rec.Code, rec.Body.String())
			}
			var projectID sql.NullString
			err := db.QueryRow(`SELECT project_id FROM conversations WHERE id='conversation-1'`).Scan(&projectID)
			if test.wantConversationRow {
				if err != nil {
					t.Fatalf("read detached conversation: %v", err)
				}
				if projectID.Valid {
					t.Fatalf("detached conversation project_id=%q, want NULL", projectID.String)
				}
			} else if err != sql.ErrNoRows {
				t.Fatalf("deleted conversation lookup error=%v, want sql.ErrNoRows", err)
			}
		})
	}
}
