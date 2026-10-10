package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// maxAudioBytes caps an upload at the Whisper API's 25 MiB limit.
const maxAudioBytes = 25 * 1024 * 1024

var audioHTTPClient = &http.Client{Timeout: envcfg.Dur("AIVORY_API_AUDIO_TRANSCRIPTION_UPSTREAM_HTTP_TIMEOUT", 120*time.Second)}

// Env-overridable defaults (§ config-reference); each falls back to the
// original hardcoded value when its AIVORY_* variable is unset.
var (
	audioTranscriptionUserRateLimit            = envcfg.Int("AIVORY_API_AUDIO_TRANSCRIPTION_USER_RATE_LIMIT", 20)
	transcriptionUpstreamResponseReadCap       = envcfg.Int64("AIVORY_API_TRANSCRIPTION_UPSTREAM_RESPONSE_READ_CAP", 1<<20)
	transcriptionUpstreamErrorTruncationLength = 240
)

// transcribeAudioHandler accepts an audio blob (multipart field "file") and
// forwards it to an OpenAI-compatible /v1/audio/transcriptions endpoint using
// the admin-configured voice settings (base URL + API key + model). Returns
// {"text": "..."}. Voice config lives in admin settings (live-reloaded):
//
//	audio_transcribe_base_url  — default https://api.openai.com
//	audio_transcribe_api_key   — required
//	audio_transcribe_model     — default whisper-1
func transcribeAudioHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	watcher, err := startCapabilityAccessWatcher(
		d, r.Context(), u.ID, errVoiceGroupPermission,
		func(permissions store.UserGroupPermissions) bool { return permissions.AllowVoiceTranscription },
	)
	if err != nil {
		if isCapabilityDenied(err, errVoiceGroupPermission) {
			writeError(w, http.StatusForbidden, errVoiceGroupPermission)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	defer watcher.Close()
	r = r.WithContext(watcher.Context())

	base := settingString(d, "audio_transcribe_base_url", "https://api.openai.com")
	key := settingString(d, "audio_transcribe_api_key", "")
	model := settingString(d, "audio_transcribe_model", "whisper-1")
	if key == "" {
		writeError(w, 400, errors.New("voice transcription is not configured — set it in Admin → Voice"))
		return
	}
	// §D6: per-user rate limit — each call buffers up to 25 MiB and burns the
	// admin's transcription spend.
	if !rateLimitUser(d, u.ID, "audio", audioTranscriptionUserRateLimit, time.Minute) {
		writeError(w, 429, errors.New("transcription rate limit exceeded — try again shortly"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAudioBytes+(1<<20))
	if err := r.ParseMultipartForm(maxAudioBytes + 1024); err != nil {
		writeError(w, 400, err)
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, errors.New("audio file required (field 'file')"))
		return
	}
	defer file.Close()
	// Read through a capped reader so an oversized upload can't balloon memory.
	audio, err := io.ReadAll(io.LimitReader(file, maxAudioBytes+1))
	if err != nil {
		writeError(w, 400, errors.New("audio file could not be read"))
		return
	}
	if len(audio) == 0 || len(audio) > maxAudioBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("audio upload is empty or exceeds the size limit"))
		return
	}

	// Measure actual audio samples on the server before reserving credit.
	// Client-reported duration can never reduce the reservation or final charge.
	billing := audioBillingFor(d, u)
	sourceID := store.GenID("asr")
	seconds, err := measuredAudioSeconds(r.Context(), audio)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	settled := false
	if billing != nil {
		if err := billing.reserve(r.Context(), d, sourceID, billableSeconds(seconds)); err != nil {
			if errors.Is(err, errAudioInsufficientCredits) {
				writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": err.Error(), "code": "insufficient_credits"})
				return
			}
			writeError(w, 500, err)
			return
		}
		defer func() {
			if !settled {
				billing.release(r.Context(), d, sourceID)
			}
		}()
	}

	// Re-package as multipart for the upstream call.
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	filename := header.Filename
	if filename == "" {
		filename = "audio.webm"
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if _, err := part.Write(audio); err != nil {
		writeError(w, 500, err)
		return
	}
	_ = mw.WriteField("model", model)
	_ = mw.WriteField("response_format", "json")
	if err := mw.Close(); err != nil {
		writeError(w, 500, err)
		return
	}
	if !watcher.AllowedNow() {
		writeError(w, http.StatusForbidden, errVoiceGroupPermission)
		return
	}

	endpoint := strings.TrimRight(base, "/") + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, body)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := audioHTTPClient.Do(req)
	if err != nil {
		if watcher.Revoked() {
			writeError(w, http.StatusForbidden, errVoiceGroupPermission)
			return
		}
		writeError(w, 502, fmt.Errorf("transcription upstream: %w", err))
		return
	}
	defer resp.Body.Close()
	respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, transcriptionUpstreamResponseReadCap))
	if watcher.Revoked() {
		writeError(w, http.StatusForbidden, errVoiceGroupPermission)
		return
	}
	if readErr != nil {
		writeError(w, 502, fmt.Errorf("transcription upstream response: %w", readErr))
		return
	}
	if resp.StatusCode >= 400 {
		writeError(w, 502, fmt.Errorf("transcription upstream %d: %s", resp.StatusCode, truncateAudioErr(respBytes)))
		return
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		writeError(w, 502, errors.New("transcription upstream returned an unexpected response"))
		return
	}
	if !watcher.AllowedNow() {
		writeError(w, http.StatusForbidden, errVoiceGroupPermission)
		return
	}
	seconds = math.Max(seconds, upstreamAudioSeconds(respBytes))
	billed := billableSeconds(seconds)
	credits := 0.0
	if billing != nil {
		debit, err := billing.settle(r.Context(), d, sourceID, billed)
		if err != nil {
			if d.Logger != nil {
				d.Logger.Printf("voice charge failed (user=%s source=%s seconds=%d): %v", u.ID, sourceID, billed, err)
			}
			if errors.Is(err, store.ErrInsufficientCredits) || errors.Is(err, errAudioInsufficientCredits) {
				writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": errAudioInsufficientCredits.Error(), "code": "insufficient_credits"})
			} else {
				writeError(w, http.StatusInternalServerError, errors.New("voice transcription billing failed"))
			}
			return
		}
		settled = true
		credits = debit.Total
	}
	recordAudioUsage(r.Context(), d, u.ID, sourceID, billed, billing, credits)
	writeJSON(w, 200, map[string]string{"text": strings.TrimSpace(parsed.Text)})
}

// reportedAudioSeconds is a legacy hint parser. Billing uses independently
// measured samples instead; the hint must never authorize or price a request.
func reportedAudioSeconds(raw string) float64 {
	ms, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || ms <= 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return 0
	}
	return math.Min(ms/1000, audioMaxReportedSeconds)
}

// settingString reads a JSON-string setting, falling back to def when unset.
func settingString(d Deps, key, def string) string {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil {
		return def
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return def
	}
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func truncateAudioErr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > transcriptionUpstreamErrorTruncationLength {
		return s[:transcriptionUpstreamErrorTruncationLength]
	}
	return s
}
