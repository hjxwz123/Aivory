package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openAuthSecurityDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func TestCreateInitialAdminAllowsOneConcurrentWinner(t *testing.T) {
	db := openAuthSecurityDB(t, "setup.db")
	const attempts = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes atomic.Int32
	errs := make(chan error, attempts)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := CreateInitialAdmin(
				t.Context(), db, fmt.Sprintf("admin-%d@example.test", i), "Admin", "hash",
			)
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, ErrAlreadyInitialized) {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("unexpected setup error: %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful setup attempts = %d, want 1", got)
	}
	var users, admins, claims int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN role='admin' THEN 1 ELSE 0 END),0) FROM users`).Scan(&users, &admins); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='_internal_setup_complete'`).Scan(&claims); err != nil {
		t.Fatalf("count setup claims: %v", err)
	}
	if users != 1 || admins != 1 || claims != 1 {
		t.Fatalf("users=%d admins=%d setup claims=%d, want 1/1/1", users, admins, claims)
	}
}

func TestRotateRefreshTokenHasOneConcurrentWinner(t *testing.T) {
	db := openAuthSecurityDB(t, "refresh.db")
	user, err := CreateUser(t.Context(), db, "user@example.test", "User", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	createdAt := time.Now().Add(-2 * time.Hour).Unix()
	if err := SaveRefreshToken(t.Context(), db, "old-jti", user.ID, time.Now().Add(time.Hour), SessionMeta{CreatedAt: createdAt}); err != nil {
		t.Fatalf("save old refresh token: %v", err)
	}

	const attempts = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes atomic.Int32
	errs := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := RotateRefreshToken(
				t.Context(), db, "old-jti", user.ID, user.TokenVer,
				fmt.Sprintf("new-jti-%d", i), time.Now().Add(24*time.Hour), SessionMeta{},
			)
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, ErrInvalidRefreshToken) {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("unexpected rotation error: %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful refresh rotations = %d, want 1", got)
	}
	var active int
	var inherited int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(created_at),0) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&active, &inherited); err != nil {
		t.Fatalf("read active refresh tokens: %v", err)
	}
	if active != 1 || inherited != createdAt {
		t.Fatalf("active tokens=%d inherited created_at=%d, want 1/%d", active, inherited, createdAt)
	}
}

func TestPasswordResetCannotRaceInARefreshSuccessor(t *testing.T) {
	db := openAuthSecurityDB(t, "reset-refresh-race.db")
	for i := 0; i < 20; i++ {
		user, err := CreateUser(t.Context(), db, fmt.Sprintf("race-%d@example.test", i), "Race", "old-hash")
		if err != nil {
			t.Fatalf("iteration %d create user: %v", i, err)
		}
		oldJTI := fmt.Sprintf("old-%d", i)
		if err := SaveRefreshToken(t.Context(), db, oldJTI, user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
			t.Fatalf("iteration %d save refresh: %v", i, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var rotateErr, resetErr error
		go func() {
			defer wg.Done()
			<-start
			_, rotateErr = RotateRefreshToken(
				t.Context(), db, oldJTI, user.ID, user.TokenVer,
				fmt.Sprintf("successor-%d", i), time.Now().Add(time.Hour), SessionMeta{},
			)
		}()
		go func() {
			defer wg.Done()
			<-start
			resetErr = UpdateUserPassword(t.Context(), db, user.ID, "new-hash")
		}()
		close(start)
		wg.Wait()

		if rotateErr != nil && !errors.Is(rotateErr, ErrInvalidRefreshToken) {
			t.Fatalf("iteration %d rotate: %v", i, rotateErr)
		}
		if resetErr != nil {
			t.Fatalf("iteration %d reset: %v", i, resetErr)
		}
		var active, tokenVer int
		if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&active); err != nil {
			t.Fatalf("iteration %d count active refresh: %v", i, err)
		}
		if err := db.QueryRow(`SELECT token_ver FROM users WHERE id=?`, user.ID).Scan(&tokenVer); err != nil {
			t.Fatalf("iteration %d read token version: %v", i, err)
		}
		if active != 0 || tokenVer != user.TokenVer+1 {
			t.Fatalf("iteration %d active refresh=%d token_ver=%d", i, active, tokenVer)
		}
	}
}

func TestRevokeSessionCannotMissConcurrentRefreshSuccessor(t *testing.T) {
	db := openAuthSecurityDB(t, "revoke-refresh-race.db")
	for i := 0; i < 20; i++ {
		user, err := CreateUser(t.Context(), db, fmt.Sprintf("revoke-race-%d@example.test", i), "Race", "hash")
		if err != nil {
			t.Fatalf("iteration %d create user: %v", i, err)
		}
		oldJTI := fmt.Sprintf("revoke-old-%d", i)
		if err := SaveRefreshToken(t.Context(), db, oldJTI, user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
			t.Fatalf("iteration %d save refresh: %v", i, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var rotateErr, revokeErr error
		var revoked bool
		go func() {
			defer wg.Done()
			<-start
			_, rotateErr = RotateRefreshToken(
				t.Context(), db, oldJTI, user.ID, user.TokenVer,
				fmt.Sprintf("revoke-successor-%d", i), time.Now().Add(time.Hour), SessionMeta{},
			)
		}()
		go func() {
			defer wg.Done()
			<-start
			revoked, revokeErr = RevokeUserSession(t.Context(), db, user.ID, oldJTI)
		}()
		close(start)
		wg.Wait()

		if rotateErr != nil && !errors.Is(rotateErr, ErrInvalidRefreshToken) {
			t.Fatalf("iteration %d rotate: %v", i, rotateErr)
		}
		if revokeErr != nil || !revoked {
			t.Fatalf("iteration %d revoke=(%v,%v), want true,nil", i, revoked, revokeErr)
		}
		var active int
		if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&active); err != nil {
			t.Fatalf("iteration %d count active: %v", i, err)
		}
		if active != 0 {
			t.Fatalf("iteration %d active refresh tokens=%d, want 0", i, active)
		}
	}
}

func TestRevokeOtherSessionsCannotMissConcurrentRefreshSuccessor(t *testing.T) {
	db := openAuthSecurityDB(t, "revoke-others-refresh-race.db")
	for i := 0; i < 20; i++ {
		user, err := CreateUser(t.Context(), db, fmt.Sprintf("revoke-others-%d@example.test", i), "Race", "hash")
		if err != nil {
			t.Fatalf("iteration %d create user: %v", i, err)
		}
		keepJTI := fmt.Sprintf("keep-%d", i)
		oldJTI := fmt.Sprintf("other-old-%d", i)
		if err := SaveRefreshToken(t.Context(), db, keepJTI, user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
			t.Fatalf("iteration %d save keep session: %v", i, err)
		}
		if err := SaveRefreshToken(t.Context(), db, oldJTI, user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
			t.Fatalf("iteration %d save other session: %v", i, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var rotateErr, revokeErr error
		go func() {
			defer wg.Done()
			<-start
			_, rotateErr = RotateRefreshToken(
				t.Context(), db, oldJTI, user.ID, user.TokenVer,
				fmt.Sprintf("other-successor-%d", i), time.Now().Add(time.Hour), SessionMeta{},
			)
		}()
		go func() {
			defer wg.Done()
			<-start
			revokeErr = RevokeOtherUserSessions(t.Context(), db, user.ID, keepJTI)
		}()
		close(start)
		wg.Wait()

		if rotateErr != nil && !errors.Is(rotateErr, ErrInvalidRefreshToken) {
			t.Fatalf("iteration %d rotate: %v", i, rotateErr)
		}
		if revokeErr != nil {
			t.Fatalf("iteration %d revoke others: %v", i, revokeErr)
		}
		var active, kept int
		if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN jti=? THEN 1 ELSE 0 END),0) FROM refresh_tokens WHERE user_id=? AND revoked=0`, keepJTI, user.ID).Scan(&active, &kept); err != nil {
			t.Fatalf("iteration %d count active: %v", i, err)
		}
		if active != 1 || kept != 1 {
			t.Fatalf("iteration %d active=%d kept=%d, want 1/1", i, active, kept)
		}
	}
}

func TestRefreshRotationPreservesStableSessionID(t *testing.T) {
	db := openAuthSecurityDB(t, "stable-session-id.db")
	user, err := CreateUser(t.Context(), db, "stable-session@example.test", "Stable", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRefreshToken(t.Context(), db, "initial-jti", user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
		t.Fatal(err)
	}
	sessionID, err := RotateRefreshToken(
		t.Context(), db, "initial-jti", user.ID, user.TokenVer,
		"rotated-jti", time.Now().Add(time.Hour), SessionMeta{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "initial-jti" {
		t.Fatalf("session id=%q, want initial-jti", sessionID)
	}
	sessions, err := ListUserSessions(t.Context(), db, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "initial-jti" {
		t.Fatalf("sessions=%+v, want one stable family", sessions)
	}
}

func TestConsumedRefreshJTIReplayFailsWithoutRevivingOrRevokingSuccessor(t *testing.T) {
	db := openAuthSecurityDB(t, "refresh-replay-policy.db")
	user, err := CreateUser(t.Context(), db, "refresh-replay@example.test", "Replay", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRefreshToken(t.Context(), db, "replay-old", user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateRefreshToken(t.Context(), db, "replay-old", user.ID, user.TokenVer, "replay-new", time.Now().Add(time.Hour), SessionMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateRefreshToken(t.Context(), db, "replay-old", user.ID, user.TokenVer, "replay-third", time.Now().Add(time.Hour), SessionMeta{}); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("replayed refresh error=%v, want ErrInvalidRefreshToken", err)
	}
	valid, err := IsRefreshSessionValid(t.Context(), db, user.ID, "replay-old")
	if err != nil || !valid {
		t.Fatalf("successor family valid=(%v,%v), want true,nil", valid, err)
	}
}

func TestSetUserStatusCannotRaceInARefreshSuccessor(t *testing.T) {
	db := openAuthSecurityDB(t, "status-refresh-race.db")
	for i := 0; i < 20; i++ {
		user, err := CreateUser(t.Context(), db, fmt.Sprintf("status-race-%d@example.test", i), "Race", "hash")
		if err != nil {
			t.Fatalf("iteration %d create user: %v", i, err)
		}
		oldJTI := fmt.Sprintf("status-old-%d", i)
		if err := SaveRefreshToken(t.Context(), db, oldJTI, user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
			t.Fatalf("iteration %d save refresh: %v", i, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var rotateErr, statusErr error
		go func() {
			defer wg.Done()
			<-start
			_, rotateErr = RotateRefreshToken(
				t.Context(), db, oldJTI, user.ID, user.TokenVer,
				fmt.Sprintf("status-successor-%d", i), time.Now().Add(time.Hour), SessionMeta{},
			)
		}()
		go func() {
			defer wg.Done()
			<-start
			statusErr = SetUserStatus(t.Context(), db, user.ID, "banned")
		}()
		close(start)
		wg.Wait()

		if rotateErr != nil && !errors.Is(rotateErr, ErrInvalidRefreshToken) {
			t.Fatalf("iteration %d rotate: %v", i, rotateErr)
		}
		if statusErr != nil {
			t.Fatalf("iteration %d status: %v", i, statusErr)
		}
		var active, tokenVer int
		var status string
		if err := db.QueryRow(`SELECT status, token_ver FROM users WHERE id=?`, user.ID).Scan(&status, &tokenVer); err != nil {
			t.Fatalf("iteration %d read user: %v", i, err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&active); err != nil {
			t.Fatalf("iteration %d count active: %v", i, err)
		}
		if status != "banned" || tokenVer != user.TokenVer+1 || active != 0 {
			t.Fatalf("iteration %d status=%q token_ver=%d active=%d", i, status, tokenVer, active)
		}
	}
}

func TestRevokeAllSessionsCannotMissConcurrentRefreshSuccessor(t *testing.T) {
	db := openAuthSecurityDB(t, "revoke-all-refresh-race.db")
	for i := 0; i < 20; i++ {
		user, err := CreateUser(t.Context(), db, fmt.Sprintf("revoke-all-%d@example.test", i), "Race", "hash")
		if err != nil {
			t.Fatalf("iteration %d create user: %v", i, err)
		}
		oldJTI := fmt.Sprintf("revoke-all-old-%d", i)
		if err := SaveRefreshToken(t.Context(), db, oldJTI, user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
			t.Fatalf("iteration %d save refresh: %v", i, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var rotateErr, revokeErr error
		go func() {
			defer wg.Done()
			<-start
			_, rotateErr = RotateRefreshToken(
				t.Context(), db, oldJTI, user.ID, user.TokenVer,
				fmt.Sprintf("revoke-all-successor-%d", i), time.Now().Add(time.Hour), SessionMeta{},
			)
		}()
		go func() {
			defer wg.Done()
			<-start
			revokeErr = RevokeAllUserSessions(t.Context(), db, user.ID)
		}()
		close(start)
		wg.Wait()

		if rotateErr != nil && !errors.Is(rotateErr, ErrInvalidRefreshToken) {
			t.Fatalf("iteration %d rotate: %v", i, rotateErr)
		}
		if revokeErr != nil {
			t.Fatalf("iteration %d revoke all: %v", i, revokeErr)
		}
		var active int
		if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&active); err != nil {
			t.Fatalf("iteration %d count active: %v", i, err)
		}
		if active != 0 {
			t.Fatalf("iteration %d active refresh tokens=%d, want 0", i, active)
		}
	}
}

func TestConcurrentCurrentPasswordChangesHaveOneWinner(t *testing.T) {
	db := openAuthSecurityDB(t, "concurrent-current-password.db")
	user, err := CreateUser(t.Context(), db, "password-race@example.test", "Password Race", "old-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRefreshToken(t.Context(), db, "password-race-session", user.ID, time.Now().Add(time.Hour), SessionMeta{}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, newHash := range []string{"new-hash-a", "new-hash-b"} {
		newHash := newHash
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- UpdateUserPasswordIfCurrent(t.Context(), db, user.ID, "old-hash", newHash)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded, stale := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrPasswordChanged):
			stale++
		default:
			t.Fatalf("concurrent password change error: %v", err)
		}
	}
	if succeeded != 1 || stale != 1 {
		t.Fatalf("password changes succeeded=%d stale=%d, want 1/1", succeeded, stale)
	}
	var tokenVer, activeRefresh int
	if err := db.QueryRow(`SELECT token_ver FROM users WHERE id=?`, user.ID).Scan(&tokenVer); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&activeRefresh); err != nil {
		t.Fatal(err)
	}
	if tokenVer != user.TokenVer+1 || activeRefresh != 0 {
		t.Fatalf("token_ver=%d active_refresh=%d, want %d/0", tokenVer, activeRefresh, user.TokenVer+1)
	}
}
