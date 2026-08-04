package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestTwofaTicketHasOneConcurrentWinner(t *testing.T) {
	d, user, secret := newTwofaSecurityFixture(t, "ticket-race.db")
	ticket := issueTwofaTicket(d, user.ID, user.TokenVer, store.LoginMethodPassword)
	codes := []string{totpSecurityCode(t, secret, 0), totpSecurityCode(t, secret, -1)}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes atomic.Int32
	for _, code := range codes {
		code := code
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := completeTwofaSecurityLogin(d, ticket, code)
			if rec.Code == http.StatusOK {
				successes.Add(1)
			} else if rec.Code != http.StatusUnauthorized {
				t.Errorf("2FA completion status=%d body=%s", rec.Code, rec.Body.String())
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful ticket exchanges = %d, want 1", got)
	}
	var sessions int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("active sessions = %d, want 1", sessions)
	}
}

func TestTwofaReplayKeyUsesNormalizedCode(t *testing.T) {
	d, user, secret := newTwofaSecurityFixture(t, "normalized-code.db")
	code := totpSecurityCode(t, secret, 0)
	first := completeTwofaSecurityLogin(d, issueTwofaTicket(d, user.ID, user.TokenVer, store.LoginMethodPassword), code)
	if first.Code != http.StatusOK {
		t.Fatalf("first 2FA completion status=%d body=%s", first.Code, first.Body.String())
	}
	formatted := code[:3] + " " + code[3:]
	replay := completeTwofaSecurityLogin(d, issueTwofaTicket(d, user.ID, user.TokenVer, store.LoginMethodPassword), formatted)
	if replay.Code != http.StatusUnauthorized || !bytes.Contains(replay.Body.Bytes(), []byte(errTwofaCodeUsed.Error())) {
		t.Fatalf("formatted replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestTwofaTicketExpiresWhenTokenVersionChanges(t *testing.T) {
	d, user, secret := newTwofaSecurityFixture(t, "ticket-token-version.db")
	ticket := issueTwofaTicket(d, user.ID, user.TokenVer, store.LoginMethodPassword)
	if err := store.UpdateUserPassword(t.Context(), d.DB, user.ID, "new-password-hash"); err != nil {
		t.Fatalf("rotate password: %v", err)
	}
	rec := completeTwofaSecurityLogin(d, ticket, totpSecurityCode(t, secret, 0))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale ticket status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}
	var sessions int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("stale ticket created %d active sessions", sessions)
	}
}

func TestTwofaTicketExpiresWhenTwofaIsDisabled(t *testing.T) {
	d, user, secret := newTwofaSecurityFixture(t, "ticket-disabled.db")
	ticket := issueTwofaTicket(d, user.ID, user.TokenVer, store.LoginMethodPassword)
	if err := store.DisableUserTotp(t.Context(), d.DB, user.ID); err != nil {
		t.Fatalf("disable 2FA: %v", err)
	}
	rec := completeTwofaSecurityLogin(d, ticket, totpSecurityCode(t, secret, 0))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled-2FA ticket status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}
}

func newTwofaSecurityFixture(t *testing.T, dbName string) (Deps, *store.User, string) {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), dbName))
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c := cache.NewMemory()
	d := Deps{
		DB: db, Cache: c,
		Auth:   authsvc.New("twofa-security-test-secret-at-least-32", time.Hour, 24*time.Hour, c),
		Logger: log.New(io.Discard, "", 0),
	}
	user, err := store.CreateUser(t.Context(), db, "twofa@example.test", "TwoFA", "hash")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := authsvc.GenerateTotpSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserTotp(t.Context(), db, user.ID, secret, true); err != nil {
		t.Fatal(err)
	}
	user, err = store.FindUserByID(t.Context(), db, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d, user, secret
}

func completeTwofaSecurityLogin(d Deps, ticket, code string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"ticket":%q,"code":%q}`, ticket, code)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login/2fa", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	login2faHandler(d, rec, req)
	return rec
}

func totpSecurityCode(t *testing.T, secret string, offset int64) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30+offset))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	index := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[index]&0x7f) << 24) |
		(uint32(sum[index+1]) << 16) |
		(uint32(sum[index+2]) << 8) |
		uint32(sum[index+3])
	return fmt.Sprintf("%06d", value%1000000)
}
