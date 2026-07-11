package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/store"
)

// maxAudioBytes caps an upload at the Whisper API's 25 MiB limit.
const maxAudioBytes = 25 * 1024 * 1024

var audioHTTPClient = &http.Client{Timeout: envcfg.Dur("AURELIA_API_AUDIO_TRANSCRIPTION_UPSTREAM_HTTP_TIMEOUT", 120*time.Second)}

// Env-overridable defaults (§ config-reference); each falls back to the
// original hardcoded value when its AURELIA_* variable is unset.
var (
	audioTranscriptionUserRateLimit            = envcfg.Int("AURELIA_API_AUDIO_TRANSCRIPTION_USER_RATE_LIMIT", 20)
	transcriptionUpstreamResponseReadCap       = envcfg.Int64("AURELIA_API_TRANSCRIPTION_UPSTREAM_RESPONSE_READ_CAP", 1<<20)
	transcriptionUpstreamErrorTruncationLength = envcfg.Int("AURELIA_API_TRANSCRIPTION_UPSTREAM_ERROR_TRUNCATION_LENGTH", 240)
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
	base := settingString(d, "audio_transcribe_base_url", "https://api.openai.com")
	key := settingString(d, "audio_transcribe_api_key", "")
	model := settingString(d, "audio_transcribe_model", "whisper-1")
	if key == "" {
		writeError(w, 400, errors.New("voice transcription is not configured — set it in Admin → Voice"))
		return
	}
	// §D6: per-user rate limit — each call buffers up to 25 MiB and burns the
	// admin's transcription spend.
	if u := authUser(r); u != nil && !rateLimitUser(d, u.ID, "audio", audioTranscriptionUserRateLimit, time.Minute) {
		writeError(w, 429, errors.New("transcription rate limit exceeded — try again shortly"))
		return
	}

	if err := r.ParseMultipartForm(maxAudioBytes + 1024); err != nil {
		writeError(w, 400, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, errors.New("audio file required (field 'file')"))
		return
	}
	defer file.Close()

	// Re-package as multipart for the upstream call. Stream through a capped
	// reader so an oversized upload can't balloon memory.
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
	if _, err := io.Copy(part, io.LimitReader(file, maxAudioBytes)); err != nil {
		writeError(w, 500, err)
		return
	}
	_ = mw.WriteField("model", model)
	_ = mw.WriteField("response_format", "json")
	if err := mw.Close(); err != nil {
		writeError(w, 500, err)
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
		writeError(w, 502, fmt.Errorf("transcription upstream: %w", err))
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, transcriptionUpstreamResponseReadCap))
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
	writeJSON(w, 200, map[string]string{"text": strings.TrimSpace(parsed.Text)})
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
