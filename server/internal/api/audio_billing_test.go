package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

// testWAV builds a 16 kHz mono 16-bit PCM WAV holding `seconds` of silence.
// claimSeconds is what the header's data size advertises (0 = truthful), and
// extra chunks are inserted before "data" to exercise the chunk walk.
func testWAV(seconds, claimSeconds float64, byteRate uint32) []byte {
	const sampleRate, blockAlign = 16000, 2
	if byteRate == 0 {
		byteRate = sampleRate * blockAlign
	}
	dataLen := int(seconds * sampleRate * blockAlign)
	claimed := dataLen
	if claimSeconds > 0 {
		claimed = int(claimSeconds * sampleRate * blockAlign)
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+claimed))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&b, binary.LittleEndian, byteRate)
	_ = binary.Write(&b, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("LIST")
	_ = binary.Write(&b, binary.LittleEndian, uint32(3))
	b.WriteString("abc\x00") // odd-sized chunk + pad byte
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(claimed))
	b.Write(make([]byte, dataLen))
	return b.Bytes()
}

func TestWavDurationSecondsMeasuresPresentSamples(t *testing.T) {
	if got, ok := wavDurationSeconds(testWAV(2.5, 0, 0)); !ok || math.Abs(got-2.5) > 1e-9 {
		t.Fatalf("truthful WAV = %v/%v, want 2.5/true", got, ok)
	}
	// A header claiming less audio than is present still bills for what the
	// upstream will decode... and one claiming more cannot bill for absent bytes.
	if got, ok := wavDurationSeconds(testWAV(3, 10, 0)); !ok || math.Abs(got-3) > 1e-9 {
		t.Fatalf("over-claiming WAV = %v/%v, want 3/true", got, ok)
	}
	// A forged byte rate that would make long audio look short is rejected.
	if _, ok := wavDurationSeconds(testWAV(3, 0, 320000)); ok {
		t.Fatal("inconsistent byte rate must not be trusted")
	}
	if _, ok := wavDurationSeconds([]byte("OggS not a wav at all")); ok {
		t.Fatal("non-WAV data must not be measured")
	}
}

func TestBillableSecondsRoundsUpToWholeSeconds(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int64
	}{{0, 0}, {-1, 0}, {0.2, 1}, {3, 3}, {3.0000000001, 3}, {3.2, 4}} {
		if got := billableSeconds(tc.in); got != tc.want {
			t.Fatalf("billableSeconds(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestUpstreamAudioSecondsReadsReportedLength(t *testing.T) {
	if got := upstreamAudioSeconds([]byte(`{"text":"x","usage":{"type":"duration","seconds":6}}`)); got != 6 {
		t.Fatalf("usage seconds = %v, want 6", got)
	}
	if got := upstreamAudioSeconds([]byte(`{"text":"x","duration":4.5}`)); got != 4.5 {
		t.Fatalf("verbose duration = %v, want 4.5", got)
	}
	if got := upstreamAudioSeconds([]byte(`{"text":"x","usage":{"type":"tokens","input_tokens":9}}`)); got != 0 {
		t.Fatalf("token usage = %v, want 0", got)
	}
}

type audioBillingFixture struct {
	deps      Deps
	user      *store.User
	upstreams *atomic.Int32
}

// newAudioBillingFixture prices transcription at $0.01/s with 100 credits per
// USD (1 credit per second) unless settings override it, and gives the user
// `credits` permanent credits.
func newAudioBillingFixture(t *testing.T, credits float64, upstreamBody string, settings map[string]any) audioBillingFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "audio-billing.db"))
	t.Cleanup(func() { _ = db.Close() })
	permissions, err := json.Marshal(store.DefaultUserGroupPermissions())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,permissions) VALUES('voice-group','Voice',?)`, string(permissions))
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id,credits_permanent,credits_permanent_micros,credit_cycle_anchor)
		VALUES('voice-user','voice@example.test','h','user','active','voice-group',?,?,?)`,
		credits, int64(math.Round(credits*1e6)), time.Now().Unix()-60)

	base := map[string]any{
		"audio_transcribe_base_url":         "http://audio-upstream.test",
		"audio_transcribe_api_key":          "test-key",
		"audio_transcribe_price_per_second": 0.01,
		"credits_per_usd":                   100.0,
	}
	for key, value := range settings {
		base[key] = value
	}
	store.InvalidateConfig()
	for key, value := range base {
		if value == nil {
			continue
		}
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)

	upstreams := &atomic.Int32{}
	previous := audioHTTPClient
	audioHTTPClient = &http.Client{Transport: audioRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreams.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
			Request:    r,
		}, nil
	})}
	t.Cleanup(func() { audioHTTPClient = previous })
	return audioBillingFixture{
		deps:      Deps{DB: db, Cache: cache.NewMemory()},
		user:      &store.User{ID: "voice-user", Role: "user", Status: "active", GroupID: "voice-group"},
		upstreams: upstreams,
	}
}

func (f audioBillingFixture) transcribe(t *testing.T, filename string, audio []byte, durationMS string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatal(err)
	}
	if durationMS != "" {
		_ = writer.WriteField("duration_ms", durationMS)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/audio/transcriptions", bytes.NewReader(body.Bytes()))
	req.Header.Set("content-type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, f.user))
	rec := httptest.NewRecorder()
	transcribeAudioHandler(f.deps, rec, req)
	return rec
}

func (f audioBillingFixture) available(t *testing.T) float64 {
	t.Helper()
	balance, err := store.GetCreditBalance(context.Background(), f.deps.DB, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	return balance.Available
}

func (f audioBillingFixture) usage(t *testing.T) (rows int, credits, cost float64) {
	t.Helper()
	if err := f.deps.DB.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(credits),0), COALESCE(SUM(cost),0) FROM usage_logs WHERE user_id=? AND purpose=?`,
		f.user.ID, audioUsagePurpose,
	).Scan(&rows, &credits, &cost); err != nil {
		t.Fatal(err)
	}
	return rows, credits, cost
}

func TestAudioTranscriptionChargesPerBilledSecond(t *testing.T) {
	f := newAudioBillingFixture(t, 100, `{"text":"hello"}`, nil)
	rec := f.transcribe(t, "voice.wav", testWAV(2.5, 0, 0), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 2.5 s rounds up to 3 billed seconds × $0.01 × 100 credits/USD.
	if got := f.available(t); math.Abs(got-97) > 1e-9 {
		t.Fatalf("available = %v, want 97", got)
	}
	rows, credits, cost := f.usage(t)
	if rows != 1 || math.Abs(credits-3) > 1e-9 || math.Abs(cost-0.03) > 1e-9 {
		t.Fatalf("usage rows=%d credits=%v cost=%v, want 1/3/0.03", rows, credits, cost)
	}
}

func TestAudioTranscriptionRefusesWithoutCreditsBeforeUpstream(t *testing.T) {
	f := newAudioBillingFixture(t, 2, `{"text":"hello"}`, nil)
	rec := f.transcribe(t, "voice.wav", testWAV(2.5, 0, 0), "")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), `"insufficient_credits"`) {
		t.Fatalf("status=%d body=%s, want 402 insufficient_credits", rec.Code, rec.Body.String())
	}
	if f.upstreams.Load() != 0 {
		t.Fatal("an unaffordable clip must not reach the paid upstream")
	}
	if got := f.available(t); math.Abs(got-2) > 1e-9 {
		t.Fatalf("available = %v, want the untouched 2", got)
	}
}

func TestAudioTranscriptionTruesUpNonWAVAgainstUpstreamLength(t *testing.T) {
	f := newAudioBillingFixture(t, 100, `{"text":"hi","usage":{"type":"duration","seconds":6}}`, nil)
	rec := f.transcribe(t, "voice.webm", []byte("compressed audio"), "4200")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The recorder reported 4.2 s, the upstream 6 s: the larger length is billed.
	if got := f.available(t); math.Abs(got-94) > 1e-9 {
		t.Fatalf("available = %v, want 94", got)
	}
}

func TestAudioTranscriptionIsFreeWithoutPriceOrForAdmins(t *testing.T) {
	t.Run("no price", func(t *testing.T) {
		f := newAudioBillingFixture(t, 5, `{"text":"hi"}`, map[string]any{"audio_transcribe_price_per_second": 0})
		if rec := f.transcribe(t, "voice.wav", testWAV(30, 0, 0), ""); rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := f.available(t); math.Abs(got-5) > 1e-9 {
			t.Fatalf("available = %v, want 5", got)
		}
		// Free calls are still visible to administrators.
		if rows, credits, _ := f.usage(t); rows != 1 || credits != 0 {
			t.Fatalf("usage rows=%d credits=%v, want one free row", rows, credits)
		}
	})
	t.Run("credits disabled", func(t *testing.T) {
		f := newAudioBillingFixture(t, 0, `{"text":"hi"}`, map[string]any{"credits_per_usd": 0})
		if rec := f.transcribe(t, "voice.wav", testWAV(30, 0, 0), ""); rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("admin", func(t *testing.T) {
		f := newAudioBillingFixture(t, 0, `{"text":"hi"}`, nil)
		mustExec(t, f.deps.DB, `UPDATE users SET role='admin' WHERE id=?`, f.user.ID)
		f.user.Role = "admin"
		if rec := f.transcribe(t, "voice.wav", testWAV(30, 0, 0), ""); rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestAudioCapabilitiesReportCallerPrice(t *testing.T) {
	f := newAudioBillingFixture(t, 0, `{}`, nil)
	read := func(user *store.User) float64 {
		req := httptest.NewRequest(http.MethodGet, "/api/audio/capabilities", nil)
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
		rec := httptest.NewRecorder()
		audioCapabilitiesHandler(f.deps, rec, req)
		var body struct {
			Enabled          bool    `json:"enabled"`
			CreditsPerMinute float64 `json:"credits_per_minute"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Enabled {
			t.Fatalf("capabilities = %s (%v)", rec.Body.String(), err)
		}
		return body.CreditsPerMinute
	}
	if got := read(f.user); got != 60 {
		t.Fatalf("user credits_per_minute = %v, want 60", got)
	}
	if got := read(&store.User{ID: "admin", Role: "admin"}); got != 0 {
		t.Fatalf("admin credits_per_minute = %v, want 0", got)
	}
}
