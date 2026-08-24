package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
)

const captchaTestSecret = "test-secret-at-least-32-chars-long!!"

func captchaTestDeps() Deps {
	return Deps{
		Cache:  cache.NewMemory(),
		Config: config.Config{JWTSecret: captchaTestSecret},
	}
}

func captchaTestRequest(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.25:43110"
	req.Header.Set("User-Agent", "Aivory-Captcha-Test/1.0")
	return req
}

func validCaptchaTrace(answer float64) captchaInteraction {
	return captchaInteraction{
		Mode: "pointer",
		Points: []captchaTracePoint{
			{X: 0, T: 0},
			{X: answer * 0.35, T: 90},
			{X: answer * 0.78, T: 190},
			{X: answer, T: 310},
		},
	}
}

// TestCaptchaVerifyThenConsume covers the complete browser-bound flow: a valid
// drag mints a purpose-bound pass which the target endpoint accepts once.
func TestCaptchaVerifyThenConsume(t *testing.T) {
	d := captchaTestDeps()
	id := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	verifyReq := captchaTestRequest(http.MethodPost, "/api/public/captcha/verify", nil)
	state, err := json.Marshal(captchaChallengeState{
		Answer:   0.5,
		Purpose:  captchaPurposeRegister,
		Binding:  captchaClientBinding(d, verifyReq),
		IssuedAt: time.Now().Add(-500 * time.Millisecond).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Cache.Set("captcha:"+id, string(state), captchaChallengeCacheTTL)

	body, _ := json.Marshal(map[string]any{
		"id": id, "fraction": 0.5, "interaction": validCaptchaTrace(0.5),
	})
	verifyReq = captchaTestRequest(http.MethodPost, "/api/public/captcha/verify", body)
	rec := httptest.NewRecorder()
	captchaVerifyHandler(d, rec, verifyReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Token == "" {
		t.Fatalf("verify did not mint a token: %+v", result)
	}

	consumeReq := captchaTestRequest(http.MethodPost, "/api/auth/register", nil)
	if !consumeCaptchaPass(d, consumeReq, result.Token, captchaPurposeRegister) {
		t.Fatal("fresh pass was rejected")
	}
	if consumeCaptchaPass(d, consumeReq, result.Token, captchaPurposeRegister) {
		t.Fatal("captcha pass was accepted more than once")
	}
}

func TestCaptchaPassIsBoundAndTamperResistant(t *testing.T) {
	d := captchaTestDeps()
	original := captchaTestRequest(http.MethodPost, "/api/auth/register", nil)
	binding := captchaClientBinding(d, original)

	t.Run("wrong purpose burns pass", func(t *testing.T) {
		token := mintCaptchaPass(d, captchaPurposeRegister, binding)
		if consumeCaptchaPass(d, original, token, captchaPurposeLogin) {
			t.Fatal("registration pass was accepted for login")
		}
		if consumeCaptchaPass(d, original, token, captchaPurposeRegister) {
			t.Fatal("wrong-purpose attempt did not consume the pass")
		}
	})

	t.Run("different client burns pass", func(t *testing.T) {
		token := mintCaptchaPass(d, captchaPurposeRegister, binding)
		other := captchaTestRequest(http.MethodPost, "/api/auth/register", nil)
		other.RemoteAddr = "198.51.100.9:52000"
		other.Header.Set("User-Agent", "Different-Browser/2.0")
		if consumeCaptchaPass(d, other, token, captchaPurposeRegister) {
			t.Fatal("pass was accepted by another client")
		}
		if consumeCaptchaPass(d, original, token, captchaPurposeRegister) {
			t.Fatal("wrong-client attempt did not consume the pass")
		}
	})

	t.Run("tampered signature does not consume original", func(t *testing.T) {
		token := mintCaptchaPass(d, captchaPurposeRegister, binding)
		if consumeCaptchaPass(d, original, token+"x", captchaPurposeRegister) {
			t.Fatal("tampered pass was accepted")
		}
		if !consumeCaptchaPass(d, original, token, captchaPurposeRegister) {
			t.Fatal("signature rejection consumed the valid pass")
		}
	})

	t.Run("wrong signing key does not consume original", func(t *testing.T) {
		token := mintCaptchaPass(d, captchaPurposeRegister, binding)
		wrongKey := d
		wrongKey.Config.JWTSecret = "a-totally-different-secret-value-here"
		if consumeCaptchaPass(wrongKey, original, token, captchaPurposeRegister) {
			t.Fatal("pass signed by another key was accepted")
		}
		if !consumeCaptchaPass(d, original, token, captchaPurposeRegister) {
			t.Fatal("wrong-key rejection consumed the valid pass")
		}
	})

	if consumeCaptchaPass(d, original, "", captchaPurposeRegister) {
		t.Fatal("empty pass was accepted")
	}
}

func TestCaptchaInteractionValidation(t *testing.T) {
	valid := validCaptchaTrace(0.5)
	keyboard := validCaptchaTrace(0.5)
	keyboard.Mode = "keyboard"
	if !validCaptchaInteraction(valid, 0.5) || !validCaptchaInteraction(keyboard, 0.5) {
		t.Fatal("valid pointer or keyboard trace was rejected")
	}

	tests := []struct {
		name        string
		interaction captchaInteraction
		answer      float64
	}{
		{name: "unknown mode", interaction: captchaInteraction{Mode: "script", Points: valid.Points}, answer: 0.5},
		{name: "too few points", interaction: captchaInteraction{Mode: "pointer", Points: valid.Points[:2]}, answer: 0.5},
		{name: "instant movement", interaction: captchaInteraction{Mode: "pointer", Points: []captchaTracePoint{{X: 0, T: 0}, {X: 0.25, T: 20}, {X: 0.5, T: 40}}}, answer: 0.5},
		{name: "starts away from handle", interaction: captchaInteraction{Mode: "pointer", Points: []captchaTracePoint{{X: 0.2, T: 0}, {X: 0.35, T: 150}, {X: 0.5, T: 300}}}, answer: 0.5},
		{name: "reported answer differs from trace", interaction: valid, answer: 0.7},
		{name: "no meaningful movement", interaction: captchaInteraction{Mode: "pointer", Points: []captchaTracePoint{{X: 0, T: 0}, {X: 0.05, T: 150}, {X: 0.1, T: 300}}}, answer: 0.1},
		{name: "time moves backward", interaction: captchaInteraction{Mode: "pointer", Points: []captchaTracePoint{{X: 0, T: 0}, {X: 0.25, T: 200}, {X: 0.5, T: 190}}}, answer: 0.5},
		{name: "non-finite position", interaction: captchaInteraction{Mode: "pointer", Points: []captchaTracePoint{{X: 0, T: 0}, {X: 0.25, T: 150}, {X: math.NaN(), T: 300}}}, answer: 0.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if validCaptchaInteraction(tc.interaction, tc.answer) {
				t.Fatal("invalid interaction was accepted")
			}
		})
	}
}

func TestCaptchaChallengeCannotMoveBetweenClients(t *testing.T) {
	d := captchaTestDeps()
	id := base64.RawURLEncoding.EncodeToString([]byte("fedcba9876543210"))
	original := captchaTestRequest(http.MethodPost, "/api/public/captcha/verify", nil)
	state, _ := json.Marshal(captchaChallengeState{
		Answer:   0.5,
		Purpose:  captchaPurposeLogin,
		Binding:  captchaClientBinding(d, original),
		IssuedAt: time.Now().Add(-500 * time.Millisecond).UnixMilli(),
	})
	d.Cache.Set("captcha:"+id, string(state), captchaChallengeCacheTTL)

	other := captchaTestRequest(http.MethodPost, "/api/public/captcha/verify", nil)
	other.RemoteAddr = "203.0.113.15:44000"
	if _, ok := verifyPuzzleCaptcha(d, other, id, 0.5, validCaptchaTrace(0.5)); ok {
		t.Fatal("challenge was accepted from another client")
	}
	if _, ok := verifyPuzzleCaptcha(d, original, id, 0.5, validCaptchaTrace(0.5)); ok {
		t.Fatal("failed binding attempt did not consume challenge")
	}
}

func TestCaptchaPurposeParsing(t *testing.T) {
	tests := []struct {
		raw    string
		want   captchaPurpose
		wantOK bool
	}{
		{raw: "login", want: captchaPurposeLogin, wantOK: true},
		{raw: "register", want: captchaPurposeRegister, wantOK: true},
		{raw: "", want: captchaPurposeRegister, wantOK: true},
		{raw: "password-reset", want: "password-reset", wantOK: false},
	}
	for _, tc := range tests {
		got, ok := parseCaptchaPurpose(tc.raw)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("parseCaptchaPurpose(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}
