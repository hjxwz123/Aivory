package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

type recordedEmail struct {
	to      string
	purpose string
}

type recordingCodeMailer struct {
	mu    sync.Mutex
	calls []recordedEmail
	sent  chan struct{}
}

func newRecordingCodeMailer() *recordingCodeMailer {
	return &recordingCodeMailer{sent: make(chan struct{}, 8)}
}

func (m *recordingCodeMailer) SendCode(to, _ string, purpose string) error {
	m.mu.Lock()
	m.calls = append(m.calls, recordedEmail{to: to, purpose: purpose})
	m.mu.Unlock()
	select {
	case m.sent <- struct{}{}:
	default:
	}
	return nil
}

func (m *recordingCodeMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func newEmailCooldownDeps(t *testing.T) (Deps, *recordingCodeMailer) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "email-cooldown.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	store.InvalidateConfig()
	mailer := newRecordingCodeMailer()
	return Deps{
		DB:     db,
		Cache:  cache.NewMemory(),
		Mailer: mailer,
		Logger: log.New(io.Discard, "", 0),
	}, mailer
}

func runAuthJSONHandler(t *testing.T, d Deps, h handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h(d, rec, req)
	return rec
}

func readCooldownResponse(t *testing.T, rec *httptest.ResponseRecorder) (string, int) {
	t.Helper()
	var body struct {
		Error      string `json:"error"`
		RetryAfter int    `json:"retry_after"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return body.Error, body.RetryAfter
}

func TestReserveEmailSendScopesByNormalizedRecipientAndPurpose(t *testing.T) {
	d := Deps{Cache: cache.NewMemory()}
	retry, allowed := reserveEmailSend(d, " User@Example.Test ", "verify")
	if !allowed || retry != 120 {
		t.Fatalf("first reservation = allowed %v retry %d, want true/120", allowed, retry)
	}
	retry, allowed = reserveEmailSend(d, "user@example.test", "verify")
	if allowed || retry < 119 || retry > 120 {
		t.Fatalf("duplicate reservation = allowed %v retry %d, want false/119..120", allowed, retry)
	}
	if _, allowed = reserveEmailSend(d, "user@example.test", "reset"); !allowed {
		t.Fatal("a different purpose must use a separate cooldown")
	}
	if _, allowed = reserveEmailSend(d, "other@example.test", "verify"); !allowed {
		t.Fatal("a different recipient must use a separate cooldown")
	}
}

func TestReserveEmailSendFailsClosedWithoutCache(t *testing.T) {
	retry, allowed := reserveEmailSend(Deps{}, "user@example.test", "verify")
	if allowed || retry != 120 {
		t.Fatalf("reservation without cache = allowed %v retry %d, want false/120", allowed, retry)
	}
}

func TestReserveEmailSendAllowsOnlyOneConcurrentRequest(t *testing.T) {
	d := Deps{Cache: cache.NewMemory()}
	const callers = 32
	start := make(chan struct{})
	results := make(chan bool, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, allowed := reserveEmailSend(d, "same@example.test", "reset")
			results <- allowed
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	allowedCount := 0
	for allowed := range results {
		if allowed {
			allowedCount++
		}
	}
	if allowedCount != 1 {
		t.Fatalf("allowed %d concurrent requests, want exactly 1", allowedCount)
	}
}

func TestForgotAndResetResendShareCooldownAndRetryAfter(t *testing.T) {
	d, mailer := newEmailCooldownDeps(t)
	if _, err := store.CreateUser(context.Background(), d.DB, "user@example.test", "User", "hash"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	first := runAuthJSONHandler(t, d, forgotPasswordHandler, "/api/auth/forgot-password", `{"email":"User@Example.Test"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	_, retry := readCooldownResponse(t, first)
	if retry != 120 || mailer.count() != 1 {
		t.Fatalf("first retry=%d sends=%d, want 120/1", retry, mailer.count())
	}

	second := runAuthJSONHandler(t, d, sendCodeHandler, "/api/auth/send-code", `{"email":"user@example.test","purpose":"reset"}`)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	code, retry := readCooldownResponse(t, second)
	if code != errEmailCooldown.Error() || retry < 119 || retry > 120 {
		t.Fatalf("second error=%q retry=%d", code, retry)
	}
	if second.Header().Get("Retry-After") != strconv.Itoa(retry) {
		t.Fatalf("Retry-After=%q, body retry_after=%d", second.Header().Get("Retry-After"), retry)
	}
	if mailer.count() != 1 {
		t.Fatalf("duplicate request sent %d emails, want 1", mailer.count())
	}
}

func TestUnknownRecipientGetsTheSameCooldownWithoutSending(t *testing.T) {
	d, mailer := newEmailCooldownDeps(t)
	first := runAuthJSONHandler(t, d, forgotPasswordHandler, "/api/auth/forgot-password", `{"email":"missing@example.test"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	_, retry := readCooldownResponse(t, first)
	if retry != 120 || mailer.count() != 0 {
		t.Fatalf("unknown recipient retry=%d sends=%d, want 120/0", retry, mailer.count())
	}
	second := runAuthJSONHandler(t, d, forgotPasswordHandler, "/api/auth/forgot-password", `{"email":"missing@example.test"}`)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	code, retry := readCooldownResponse(t, second)
	if code != errEmailCooldown.Error() || retry < 119 || retry > 120 {
		t.Fatalf("second error=%q retry=%d", code, retry)
	}
}

func TestRegistrationEmailSharesVerifyCooldownWithResend(t *testing.T) {
	d, mailer := newEmailCooldownDeps(t)
	if _, err := store.CreateUserWithRole(context.Background(), d.DB, "admin@example.test", "Admin", "hash", "admin"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := store.SetSetting(d.DB, "email_verification_required", true); err != nil {
		t.Fatalf("enable email verification: %v", err)
	}
	store.InvalidateConfig()

	register := runAuthJSONHandler(t, d, registerHandler, "/api/auth/register", `{"email":"new@example.test","password":"password123","name":"New User"}`)
	if register.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", register.Code, register.Body.String())
	}
	_, retry := readCooldownResponse(t, register)
	if retry != 120 {
		t.Fatalf("register retry_after=%d, want 120", retry)
	}
	select {
	case <-mailer.sent:
	case <-time.After(time.Second):
		t.Fatal("registration verification email was not dispatched")
	}

	resend := runAuthJSONHandler(t, d, sendCodeHandler, "/api/auth/send-code", `{"email":"new@example.test","purpose":"verify"}`)
	if resend.Code != http.StatusTooManyRequests {
		t.Fatalf("resend status=%d body=%s", resend.Code, resend.Body.String())
	}
	if mailer.count() != 1 {
		t.Fatalf("verify resend sent %d emails during cooldown, want 1", mailer.count())
	}
}

func TestSendCodeRejectsUnknownPurpose(t *testing.T) {
	d, mailer := newEmailCooldownDeps(t)
	rec := runAuthJSONHandler(t, d, sendCodeHandler, "/api/auth/send-code", `{"email":"user@example.test","purpose":"other"}`)
	if rec.Code != http.StatusBadRequest || mailer.count() != 0 {
		t.Fatalf("status=%d sends=%d body=%s", rec.Code, mailer.count(), rec.Body.String())
	}
}

func TestCORSMiddlewareExposesRetryAfter(t *testing.T) {
	h := corsMiddleware([]string{"https://app.example.test"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/send-code", nil)
	req.Header.Set("Origin", "https://app.example.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "Retry-After" {
		t.Fatalf("Access-Control-Expose-Headers=%q, want Retry-After", got)
	}
}
