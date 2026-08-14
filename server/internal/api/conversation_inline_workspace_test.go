package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestCreateInlineThreadKeepsWorkspaceBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		canPrivate bool
		wantPublic bool
	}{
		{name: "private when allowed", canPrivate: true, wantPublic: false},
		{name: "public when private conversations are denied", canPrivate: false, wantPublic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigrated(t, filepath.Join(t.TempDir(), "inline-workspace.db"))
			defer db.Close()
			for _, userID := range []string{"workspace-owner", "source-creator", "thread-creator"} {
				mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`, userID, userID+"@example.test")
			}
			workspace, err := store.CreateWorkspace(t.Context(), db, "workspace-owner", "Inline threads")
			if err != nil {
				t.Fatalf("create workspace: %v", err)
			}
			for _, userID := range []string{"source-creator", "thread-creator"} {
				if err := store.JoinWorkspace(t.Context(), db, workspace.ID, userID); err != nil {
					t.Fatalf("join %s: %v", userID, err)
				}
			}
			if !tc.canPrivate {
				permissions := store.WorkspaceMemberPermissions{
					CanCreateProjects:  true,
					CanCreateKB:        true,
					CanAddKBFiles:      true,
					CanDeleteKBContent: true,
				}
				if _, err := store.UpdateWorkspaceMemberPermissions(
					t.Context(), db, workspace.ID, "workspace-owner", "thread-creator", permissions,
				); err != nil {
					t.Fatalf("disable private conversations: %v", err)
				}
			}

			source, err := store.CreateConversation(t.Context(), db, store.Conversation{
				ID: "workspace-source", UserID: "source-creator", WorkspaceID: workspace.ID,
				Title: "Shared source", ModelID: "model-1", IsPublic: true,
			})
			if err != nil {
				t.Fatalf("create source: %v", err)
			}
			message, err := store.CreateMessage(t.Context(), db, store.Message{
				ID: "source-message", ConversationID: source.ID, Role: "assistant",
				Blocks: json.RawMessage(`[{"kind":"text","text":"workspace-only quotation"}]`),
			})
			if err != nil {
				t.Fatalf("create source message: %v", err)
			}

			req := httptest.NewRequest(
				http.MethodPost,
				"/api/conversations/"+source.ID+"/inline-threads",
				strings.NewReader(`{"message_id":"`+message.ID+`","quote":"workspace-only quotation"}`),
			)
			ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": source.ID})
			ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "thread-creator", Role: "user", Status: "active"})
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()
			createInlineThreadHandler(Deps{DB: db}, rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create inline status=%d body=%s", rec.Code, rec.Body.String())
			}
			var created store.Conversation
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatalf("decode created inline thread: %v", err)
			}
			if created.WorkspaceID != workspace.ID || created.IsPublic != tc.wantPublic {
				t.Fatalf("created inline metadata=%+v, want workspace=%q public=%v", created, workspace.ID, tc.wantPublic)
			}
			if personal, err := store.ListConversations(t.Context(), db, "thread-creator", "", "active", 20, 0); err != nil {
				t.Fatalf("list personal conversations: %v", err)
			} else if len(personal) != 0 {
				t.Fatalf("workspace inline thread leaked into personal conversations: %+v", personal)
			}
			if _, err := store.GetConversation(t.Context(), db, created.ID, "thread-creator"); err != nil {
				t.Fatalf("thread creator cannot read inline thread: %v", err)
			}
			_, ownerErr := store.GetConversation(t.Context(), db, created.ID, "workspace-owner")
			if tc.wantPublic && ownerErr != nil {
				t.Fatalf("workspace owner cannot read forced-public inline thread: %v", ownerErr)
			}
			if !tc.wantPublic && !errors.Is(ownerErr, store.ErrNotFound) {
				t.Fatalf("workspace owner read private inline thread error=%v, want ErrNotFound", ownerErr)
			}

			if err := store.RemoveWorkspaceMember(t.Context(), db, workspace.ID, "thread-creator"); err != nil {
				t.Fatalf("remove thread creator: %v", err)
			}
			if _, err := store.GetConversation(t.Context(), db, created.ID, "thread-creator"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("removed member read error=%v, want ErrNotFound", err)
			}
			if tc.wantPublic {
				if _, err := store.GetConversation(t.Context(), db, created.ID, "workspace-owner"); err != nil {
					t.Fatalf("forced-public thread disappeared for remaining workspace owner: %v", err)
				}
			}
		})
	}
}
