package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func groupUsersRequest(groupID string, query url.Values) *http.Request {
	target := "/api/admin/user-groups/" + groupID + "/users"
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	return req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": groupID}))
}

func decodeGroupUsersPage(t *testing.T, recorder *httptest.ResponseRecorder) struct {
	Users []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	} `json:"users"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
} {
	t.Helper()
	var page struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Name  string `json:"name"`
			Role  string `json:"role"`
		} `json:"users"`
		Total  int `json:"total"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode users page: %v body=%s", err, recorder.Body.String())
	}
	return page
}

func TestAdminGroupUsersSearchPaginationAndBounds(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-group-users.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO user_groups(id,name,features) VALUES('group-page','Paged','[]')`)
	for index := 0; index < 5; index++ {
		mustExec(t, db, `INSERT INTO users(
			id,email,name,password_hash,role,status,group_id,sort_order
		) VALUES(?,?,?,?, 'user','active','group-page',?)`,
			fmt.Sprintf("page-user-%d", index),
			fmt.Sprintf("person-%d@example.test", index),
			fmt.Sprintf("Member %d", index), "h", index)
	}
	deps := Deps{DB: db}

	second := httptest.NewRecorder()
	listUserGroupUsersAdmin(deps, second, groupUsersRequest("group-page", url.Values{
		"limit": {"2"}, "offset": {"2"},
	}))
	if second.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	page := decodeGroupUsersPage(t, second)
	if page.Total != 5 || page.Limit != 2 || page.Offset != 2 || len(page.Users) != 2 || page.Users[0].ID != "page-user-2" {
		t.Fatalf("second page=%+v users=%v", page, page.Users)
	}
	var raw map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	first, _ := raw["users"].([]any)[0].(map[string]any)
	for _, privateField := range []string{"settings", "totp_enabled", "credits_permanent", "permissions"} {
		if _, present := first[privateField]; present {
			t.Fatalf("group user summary leaked %q: %s", privateField, second.Body.String())
		}
	}

	searched := httptest.NewRecorder()
	listUserGroupUsersAdmin(deps, searched, groupUsersRequest("group-page", url.Values{
		"search": {"PERSON-4@EXAMPLE"}, "limit": {"500"}, "offset": {"-9"},
	}))
	if searched.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", searched.Code, searched.Body.String())
	}
	page = decodeGroupUsersPage(t, searched)
	if page.Total != 1 || page.Limit != 100 || page.Offset != 0 || len(page.Users) != 1 || page.Users[0].ID != "page-user-4" {
		t.Fatalf("search page=%+v users=%v", page, page.Users)
	}
}

func TestAdminGroupUsersExpiresMembershipBeforeCounting(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-group-users-expiry.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO user_groups(id,name,features) VALUES('group-expiring','Expiring','[]')`)
	mustExec(t, db, `INSERT INTO users(
		id,email,name,password_hash,role,status,group_id,group_expires_at,previous_group_id
	) VALUES('expired-member','expired@example.test','Expired','h','user','active','group-expiring',?,'ug_free')`, time.Now().Unix()-1)

	recorder := httptest.NewRecorder()
	listUserGroupUsersAdmin(Deps{DB: db}, recorder, groupUsersRequest("group-expiring", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expiry status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	page := decodeGroupUsersPage(t, recorder)
	if page.Total != 0 || len(page.Users) != 0 {
		t.Fatalf("expired member remained in group page: %+v", page)
	}
	var groupID string
	if err := db.QueryRow(`SELECT group_id FROM users WHERE id='expired-member'`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if groupID != store.DefaultGroupID {
		t.Fatalf("expired member group=%q, want %q", groupID, store.DefaultGroupID)
	}
}

func TestAdminGroupUsersMissingGroup(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-group-users-missing.db"))
	t.Cleanup(func() { _ = db.Close() })
	recorder := httptest.NewRecorder()
	listUserGroupUsersAdmin(Deps{DB: db}, recorder, groupUsersRequest("missing", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing group status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
