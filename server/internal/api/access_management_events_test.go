package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func assertAccessEventType(t *testing.T, conn *eventsConn, want string) {
	t.Helper()
	var event map[string]string
	if err := json.Unmarshal([]byte(waitEvent(t, conn.ch, 2*time.Second)), &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != want {
		t.Fatalf("event=%v, want type %q", event, want)
	}
}

func TestWorkspacePermissionUpdateNotifiesMemberAndManager(t *testing.T) {
	owner, member, deps, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	deps.Cache = eventsTestCache
	ownerConn := eventsHub.register(owner.ID)
	t.Cleanup(func() { eventsHub.unregister(owner.ID, ownerConn) })
	memberConn := eventsHub.register(member.ID)
	t.Cleanup(func() { eventsHub.unregister(member.ID, memberConn) })

	recorder := httptest.NewRecorder()
	updateWorkspaceMemberPermissionsHandler(deps, recorder, workspacePermissionRequest(
		t, http.MethodPatch, "/api/workspaces/x/members/y/permissions", owner,
		map[string]string{"id": workspaceID, "uid": member.ID}, store.WorkspaceMemberPermissions{},
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertAccessEventType(t, memberConn, "workspace.permissions_updated")
	assertAccessEventType(t, ownerConn, "workspace.permissions_updated")
}

func TestKnowledgeBaseShareUpdateNotifiesMemberAndOwner(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "kb-share-management-events.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES
		('event-share-owner','owner@example.test','Owner','h','admin','active'),
		('event-share-member','member@example.test','Member','h','admin','active')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('event-share-channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES
		('event-share-embedding','event-share-channel','embedding','embed','Embedding',1,3)`)
	kb, err := store.CreateKB(context.Background(), db, store.KnowledgeBase{
		ID: "event-share-kb", UserID: "event-share-owner", Name: "Shared library",
		EmbeddingModelID: "event-share-embedding", EmbeddingDim: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.FindUserByID(context.Background(), db, "event-share-owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerConn := eventsHub.register(owner.ID)
	t.Cleanup(func() { eventsHub.unregister(owner.ID, ownerConn) })
	memberConn := eventsHub.register("event-share-member")
	t.Cleanup(func() { eventsHub.unregister("event-share-member", memberConn) })

	req := httptest.NewRequest(http.MethodPut, "/api/kbs/"+kb.ID+"/shares", strings.NewReader(`{
		"user_id":"event-share-member","role":"read"
	}`))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, owner))
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": kb.ID}))
	recorder := httptest.NewRecorder()
	upsertKBShareHandler(Deps{DB: db, Cache: eventsTestCache}, recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertAccessEventType(t, memberConn, "knowledge_base.access_updated")
	assertAccessEventType(t, ownerConn, "knowledge_base.access_updated")
}
