package store

import (
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentAdminBansLeaveOneActiveAdmin(t *testing.T) {
	db := openAuthSecurityDB(t, "admin-ban.db")
	ids := createSecurityTestAdmins(t, db)
	runConcurrentAdminMutation(t, ids, func(id string) error {
		_, err := SetUserStatusGuarded(t.Context(), db, id, "banned")
		return err
	})
	assertSecurityTestActiveAdmins(t, db, 1)
}

func TestConcurrentAdminDemotionsLeaveOneAdmin(t *testing.T) {
	db := openAuthSecurityDB(t, "admin-demote.db")
	ids := createSecurityTestAdmins(t, db)
	runConcurrentAdminMutation(t, ids, func(id string) error {
		return SetUserRole(t.Context(), db, id, "user")
	})
	assertSecurityTestActiveAdmins(t, db, 1)
}

func createSecurityTestAdmins(t *testing.T, db *sql.DB) []string {
	t.Helper()
	ids := make([]string, 0, 2)
	for _, name := range []string{"admin-a", "admin-b"} {
		u, err := CreateUserWithRole(t.Context(), db, name+"@example.test", name, "hash", "admin")
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		ids = append(ids, u.ID)
	}
	return ids
}

func runConcurrentAdminMutation(t *testing.T, ids []string, mutate func(string) error) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes atomic.Int32
	var guarded atomic.Int32
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := mutate(id)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrLastAdmin):
				guarded.Add(1)
			default:
				t.Errorf("unexpected mutation error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 || guarded.Load() != 1 {
		t.Fatalf("successes=%d guarded=%d, want 1/1", successes.Load(), guarded.Load())
	}
}

func assertSecurityTestActiveAdmins(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != want {
		t.Fatalf("active admins = %d, want %d", active, want)
	}
}

func TestSetInitialPasswordHasOneConcurrentWinner(t *testing.T) {
	db := openAuthSecurityDB(t, "initial-password.db")
	u, err := CreateUserWithState(t.Context(), db, "oauth@example.test", "OAuth", "", "user", "active", false)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes atomic.Int32
	var rejected atomic.Int32
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := SetInitialPassword(t.Context(), db, u.ID, string(rune('a'+i)))
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, ErrPasswordAlreadySet) {
				rejected.Add(1)
			} else {
				t.Errorf("unexpected SetInitialPassword error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 || rejected.Load() != 15 {
		t.Fatalf("successes=%d rejected=%d, want 1/15", successes.Load(), rejected.Load())
	}
}
