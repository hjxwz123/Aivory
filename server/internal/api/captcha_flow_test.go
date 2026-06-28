package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"aurelia/server/internal/cache"
	"aurelia/server/internal/config"
)

// TestCaptchaVerifyThenConsume reproduces the backend captcha path the register
// handler relies on: a verified slider mints a stateless signed pass that
// register then verifies. The pass is HMAC-signed (not cache-backed), so it must
// validate without any shared cache — which is what fixes "captcha_failed" when
// the API restarts or runs across replicas between /captcha/verify and register.
func TestCaptchaVerifyThenConsume(t *testing.T) {
	d := Deps{
		Cache:  cache.NewMemory(),
		Config: config.Config{JWTSecret: "test-secret-at-least-32-chars-long!!"},
	}
	// Simulate a challenge minted by captchaHandler (gap fraction 0.5).
	d.Cache.Set("captcha:test-id", "0.500000", 5*time.Minute)

	body, _ := json.Marshal(map[string]any{"id": "test-id", "fraction": 0.5})
	rec := httptest.NewRecorder()
	captchaVerifyHandler(d, rec, httptest.NewRequest("POST", "/api/public/captcha/verify", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("verify status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var vr struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &vr); err != nil {
		t.Fatalf("decode verify resp: %v", err)
	}
	if !vr.OK || vr.Token == "" {
		t.Fatalf("verify did not mint a token: %+v", vr)
	}

	// A fresh, well-signed token validates (this is what register does) — and it
	// validates statelessly, with no surviving cache entry from the verify call.
	if !consumeCaptchaPass(d, vr.Token) {
		t.Fatal("consumeCaptchaPass=false for a fresh signed token → register would 400 captcha_failed")
	}
	// Tampered / empty / wrong-secret tokens are rejected.
	if consumeCaptchaPass(d, vr.Token+"x") {
		t.Fatal("tampered token accepted")
	}
	if consumeCaptchaPass(d, "") {
		t.Fatal("empty token accepted")
	}
	bad := Deps{Config: config.Config{JWTSecret: "a-totally-different-secret-value-here"}}
	if consumeCaptchaPass(bad, vr.Token) {
		t.Fatal("token forged under a different secret was accepted")
	}
}
