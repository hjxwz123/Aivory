package api

// Live speech-to-text relay for the Volcano (火山引擎 豆包) ASR provider.
//
// The browser can't talk to Volcano directly (custom auth headers on the WS
// handshake, a binary wire format), so the composer opens a WebSocket to us and
// streams raw 16 kHz mono PCM. We relay it to Volcano's bigmodel 双向流式 endpoint
// and stream the incremental transcripts back as JSON events. The OpenAI/Whisper
// provider keeps its simple record-then-POST path in audio_handlers.go; this
// file is only reached when the admin selects the Volcano provider.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"

	"github.com/gorilla/websocket"
)

// Tunables (env-overridable, § config-reference).
var (
	audioStreamUserRateLimit = envcfg.Int("AIVORY_API_AUDIO_STREAM_USER_RATE_LIMIT", 30)
	// Segmented clients renew the upstream connection at this byte threshold.
	// Audio is forwarded and discarded; this is not an in-memory recording size.
	audioStreamMaxBytes = envcfg.Int64("AIVORY_API_AUDIO_STREAM_MAX_BYTES", 64*1024*1024)
	// Zero disables the total recording deadline for segmented clients.
	audioStreamMaxDur        = envcfg.Dur("AIVORY_API_AUDIO_STREAM_MAX_SESSION", 0)
	audioStreamBufferSeconds = envcfg.Int("AIVORY_API_AUDIO_STREAM_BUFFER_SECONDS", 30)
	audioStreamSegmentDur    = envcfg.Dur("AIVORY_API_AUDIO_STREAM_SEGMENT_SESSION", 5*time.Minute)

	// Set AIVORY_ASR_DEBUG=1 to log every decoded Volcano frame (message code,
	// last-package marker, transcript length, raw JSON). Off by default — the
	// raw payloads contain the user's speech, so this is a deliberate opt-in.
	asrDebug = envcfg.Bool("AIVORY_ASR_DEBUG", false)
)

var audioStreamUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Same-origin only (defence against cross-site WebSocket hijacking): the
	// auth_token cookie rides along automatically, so an unchecked origin would
	// let any page drive a user's mic relay.
	CheckOrigin: sameOriginWS,
}

func sameOriginWS(r *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	return origin != "" && sameRequestOrigin(origin, r)
}

// streamEvent is the browser-facing JSON frame (backend → browser).
type streamEvent struct {
	Type    string `json:"type"`              // ready | partial | segment | final | error
	Text    string `json:"text,omitempty"`    // cumulative transcript within this segment
	Message string `json:"message,omitempty"` // error detail
	// Code is machine-readable: "insufficient_credits" on an error that refused
	// the session, "credits_exhausted" on a final cut short by the balance.
	Code string `json:"code,omitempty"`
}

// audioCapabilitiesHandler tells the composer which STT provider is active so it
// can choose record-then-transcribe (gpt) vs. live streaming (volcano), whether
// its required credentials are present, and what it costs this caller. No
// secrets are exposed.
func audioCapabilitiesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	provider := settingString(d, "audio_transcribe_provider", "gpt")
	enabled := settingString(d, "audio_transcribe_api_key", "") != ""
	if provider == "volcano" {
		enabled = settingString(d, "volcano_asr_app_id", "") != "" &&
			settingString(d, "volcano_asr_access_token", "") != ""
	}
	creditsPerMinute := 0.0
	if billing := audioBillingFor(d, authUser(r)); billing != nil {
		creditsPerMinute = billing.credits(60)
	}
	writeJSON(w, 200, map[string]any{
		"provider":           provider,
		"streaming":          provider == "volcano",
		"enabled":            enabled,
		"credits_per_minute": creditsPerMinute,
	})
}

// audioStreamHandler upgrades to a WebSocket and relays the mic PCM stream to
// Volcano, forwarding incremental transcripts back. Auth is enforced by
// requireAuth before we get here (the auth_token cookie travels with the
// handshake).
func audioStreamHandler(d Deps, w http.ResponseWriter, r *http.Request) {
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

	if settingString(d, "audio_transcribe_provider", "gpt") != "volcano" {
		writeError(w, 400, errors.New("live transcription is not enabled"))
		return
	}
	if !rateLimitUser(d, u.ID, "audio_stream", audioStreamUserRateLimit, time.Minute) {
		writeError(w, 429, errors.New("voice rate limit exceeded — try again shortly"))
		return
	}

	cfg := volcanoConfig{
		AppID:       settingString(d, "volcano_asr_app_id", ""),
		AccessToken: settingString(d, "volcano_asr_access_token", ""),
		ResourceID:  settingString(d, "volcano_asr_resource_id", "volc.bigasr.sauc.duration"),
		WSURL:       settingString(d, "volcano_asr_ws_url", "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel"),
		ModelName:   settingString(d, "volcano_asr_model_name", "bigmodel"),
		EnableITN:   settingBoolDefault(d, "volcano_asr_enable_itn", true),
		EnablePunc:  settingBoolDefault(d, "volcano_asr_enable_punc", true),
		EnableDDC:   settingBoolDefault(d, "volcano_asr_enable_ddc", false),
	}
	if cfg.AppID == "" || cfg.AccessToken == "" {
		writeError(w, 400, errors.New("voice transcription is not configured — set Volcano credentials in Admin → Voice"))
		return
	}

	// Upgrade the browser connection. After this point we must not use
	// writeError (the HTTP response is hijacked); failures are reported as
	// in-band error events instead.
	bconn, err := audioStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response
	}
	defer bconn.Close()
	// ReadMessage allocates the entire message, including text frames. Enforce
	// the wire limit before the first read, not after allocating the payload.
	bconn.SetReadLimit(256 * 1024)
	if r.URL.Query().Get("segmented") == "1" {
		relaySegmentedAudio(d, r, bconn, cfg)
		return
	}

	// Older clients only understand cumulative results and one final event.
	// Retain their bounded single-upstream behavior until the app is updated.
	maxDuration := audioStreamMaxDur
	if maxDuration <= 0 {
		maxDuration = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(watcher.Context(), maxDuration)
	defer cancel()

	// § voice billing: the session length is unknown up front, so hold what the
	// balance can pay for (up to the session ceiling) and cap the relayed audio
	// at that length; settlement charges only the seconds actually relayed.
	billing := audioBillingFor(d, u)
	sourceID := store.GenID("asr")
	maxBytes := audioStreamByteLimit()
	creditCapped := false
	if billing != nil {
		affordable, err := billing.affordableSeconds(ctx, d)
		if err != nil {
			writeStreamEvent(bconn, streamEvent{Type: "error", Message: "couldn't check your credit balance"})
			return
		}
		if affordable < 1 {
			writeStreamEvent(bconn, streamEvent{Type: "error", Code: "insufficient_credits", Message: errAudioInsufficientCredits.Error()})
			return
		}
		maxSeconds := min(affordable, int64(maxDuration/time.Second))
		if err := billing.reserve(ctx, d, sourceID, maxSeconds); err != nil {
			event := streamEvent{Type: "error", Message: "couldn't reserve credits"}
			if errors.Is(err, errAudioInsufficientCredits) {
				event = streamEvent{Type: "error", Code: "insufficient_credits", Message: err.Error()}
			}
			writeStreamEvent(bconn, event)
			return
		}
		if capBytes := maxSeconds * audioPCMBytesPerSecond; capBytes < maxBytes {
			maxBytes = capBytes
			creditCapped = true
		}
	}
	// sent counts the PCM bytes actually relayed upstream. G1 is its only writer;
	// wg.Wait (or no goroutine at all on an early return) orders it before this
	// deferred settlement.
	var sent int64
	defer func() {
		seconds := billableSeconds(float64(sent) / audioPCMBytesPerSecond)
		credits := 0.0
		if billing != nil {
			debit, err := billing.settle(r.Context(), d, sourceID, seconds) // 0 seconds releases the hold
			if err != nil {
				if d.Logger != nil {
					d.Logger.Printf("voice charge failed (user=%s source=%s seconds=%d): %v", u.ID, sourceID, seconds, err)
				}
			} else {
				credits = debit.Total
			}
		}
		if seconds > 0 {
			recordAudioUsage(r.Context(), d, u.ID, sourceID, seconds, billing, credits)
		}
	}()
	var creditsExhausted atomic.Bool

	vsess, err := dialVolcano(ctx, cfg)
	if err != nil {
		if watcher.Revoked() {
			return
		}
		writeStreamEvent(bconn, streamEvent{Type: "error", Message: "couldn't reach the transcription service"})
		if d.Logger != nil {
			d.Logger.Printf("volcano dial failed: %v", err)
		}
		return
	}
	defer vsess.close()
	if d.Logger != nil && vsess.logID != "" {
		// Volcano's docs recommend keeping X-Tt-Logid as the troubleshooting key.
		d.Logger.Printf("volcano ASR connected (logid=%s)", vsess.logID)
	}

	// Tear both sockets down as soon as the session ends, unblocking any read.
	go func() {
		<-ctx.Done()
		vsess.close()
		_ = bconn.Close()
	}()

	// The browser is idle until we say we're connected.
	writeStreamEvent(bconn, streamEvent{Type: "ready"})

	var wg sync.WaitGroup
	wg.Add(2)

	// G1: browser PCM → Volcano audio packets. Sole writer of the Volcano conn.
	go func() {
		defer wg.Done()
		for {
			mt, data, rerr := bconn.ReadMessage()
			if rerr != nil {
				break
			}
			if mt == websocket.TextMessage {
				if isEndControl(data) {
					break
				}
				continue
			}
			if mt != websocket.BinaryMessage || len(data) == 0 {
				continue
			}
			if sent+int64(len(data)) > maxBytes {
				if creditCapped {
					creditsExhausted.Store(true)
				}
				break
			}
			if serr := vsess.sendAudio(data); serr != nil {
				break
			}
			sent += int64(len(data))
		}
		// Flush the final (negative-seq) packet so Volcano emits its last result,
		// then keep the upstream open until G2 has drained it (or the session
		// times out). Only G2 cancels the context.
		_ = vsess.sendLast(nil)
		<-ctx.Done()
	}()

	// G2: Volcano transcripts → browser events. Sole writer of the browser conn.
	go func() {
		defer wg.Done()
		defer cancel() // finishing (final / error / timeout) ends the session
		for {
			resp, rerr := vsess.readResponse()
			if asrDebug && d.Logger != nil && rerr == nil {
				raw := resp.Raw
				if len(raw) > 400 {
					raw = raw[:400]
				}
				d.Logger.Printf("volcano frame: code=%d last=%v textlen=%d raw=%s",
					resp.Code, resp.IsLastPackage, len(resp.Text), raw)
			}
			if rerr != nil {
				// Clean close after a final packet looks like an error here; only
				// surface it if the context is still live (unexpected drop).
				if ctx.Err() == nil {
					writeStreamEvent(bconn, streamEvent{Type: "error", Message: "the transcription stream ended unexpectedly"})
				}
				return
			}
			if resp.Code != 0 {
				if d.Logger != nil {
					d.Logger.Printf("volcano ASR error (code=%d logid=%s): %s", resp.Code, vsess.logID, resp.ErrMessage)
				}
				writeStreamEvent(bconn, streamEvent{Type: "error", Message: "the transcription service reported an error"})
				return
			}
			if resp.Text != "" || resp.IsLastPackage {
				ev := streamEvent{Type: "partial", Text: resp.Text}
				if resp.IsLastPackage {
					ev.Type = "final"
					if creditsExhausted.Load() {
						ev.Code = "credits_exhausted"
					}
				}
				writeStreamEvent(bconn, ev)
			}
			if resp.IsLastPackage {
				return
			}
		}
	}()

	wg.Wait()
}

// writeStreamEvent sends one JSON event to the browser with a short write
// deadline so a dead peer can't wedge the writer.
func writeStreamEvent(conn *websocket.Conn, ev streamEvent) error {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(ev)
}

func audioStreamByteLimit() int64 {
	if audioStreamMaxBytes < 2 {
		return 64 * 1024 * 1024
	}
	return audioStreamMaxBytes - audioStreamMaxBytes%2
}

// isEndControl reports whether a browser text frame is the {"type":"end"} signal
// that the user stopped recording.
func isEndControl(data []byte) bool {
	var ctrl struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &ctrl) != nil {
		return false
	}
	return ctrl.Type == "end"
}

// settingBoolDefault reads a JSON-bool setting, falling back to def when unset
// or unparseable.
func settingBoolDefault(d Deps, key string, def bool) bool {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil {
		return def
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return def
	}
	return v
}
