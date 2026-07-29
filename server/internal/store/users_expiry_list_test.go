package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListUsersBySearchExpiresOnlyReturnedPage(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "users-expiry-list.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_free','Free',1)`,
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_pro','Pro',0)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	expiresAt := time.Now().Unix() - 1
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,role,group_id,group_expires_at,previous_group_id,sort_order) VALUES('u_page_1','page1@example.test','hash','user','ug_pro',?,'ug_free',1)`,
		`INSERT INTO users(id,email,password_hash,role,group_id,group_expires_at,previous_group_id,sort_order) VALUES('u_page_2','page2@example.test','hash','user','ug_pro',?,'ug_free',2)`,
	} {
		if _, err := db.ExecContext(ctx, query, expiresAt); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	firstPage, err := ListUsersBySearch(ctx, db, "", 1, 0)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(firstPage) != 1 || firstPage[0].ID != "u_page_1" {
		t.Fatalf("first page = %+v, want u_page_1", firstPage)
	}
	if firstPage[0].GroupID != DefaultGroupID || firstPage[0].GroupExpiresAt != 0 || firstPage[0].PreviousGroupID != "" {
		t.Fatalf("returned expired user was not normalized: %+v", firstPage[0])
	}

	var groupID string
	var expiry int64
	if err := db.QueryRowContext(ctx, `SELECT group_id, group_expires_at FROM users WHERE id='u_page_2'`).Scan(&groupID, &expiry); err != nil {
		t.Fatalf("read second-page user: %v", err)
	}
	if groupID != "ug_pro" || expiry >= time.Now().Unix() {
		t.Fatalf("user outside returned page was changed: group=%q expiry=%d", groupID, expiry)
	}
}
