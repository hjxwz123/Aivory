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
	"net/url"
	"strings"
	"sync"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"

	"github.com/gorilla/websocket"
)

// Tunables (env-overridable, § config-reference).
var (
	audioStreamUserRateLimit = envcfg.Int("AIVORY_API_AUDIO_STREAM_USER_RATE_LIMIT", 30)
	// Hard ceiling on relayed audio per session: 16 kHz·mono·16-bit = 32 KB/s,
	// so 24 MB ≈ 12.5 min — a runaway-tab backstop, not a normal limit.
	audioStreamMaxBytes = envcfg.Int64("AIVORY_API_AUDIO_STREAM_MAX_BYTES", 24*1024*1024)
	audioStreamMaxDur   = envcfg.Dur("AIVORY_API_AUDIO_STREAM_MAX_SESSION", 15*time.Minute)

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
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// streamEvent is the browser-facing JSON frame (backend → browser).
type streamEvent struct {
	Type    string `json:"type"`              // ready | partial | final | error
	Text    string `json:"text,omitempty"`    // cumulative transcript
	Message string `json:"message,omitempty"` // error detail
}

// audioCapabilitiesHandler tells the composer which STT provider is active so it
// can choose record-then-transcribe (gpt) vs. live streaming (volcano), and
// whether its required credentials are present. No secrets are exposed.
func audioCapabilitiesHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	provider := settingString(d, "audio_transcribe_provider", "gpt")
	enabled := settingString(d, "audio_transcribe_api_key", "") != ""
	if provider == "volcano" {
		enabled = settingString(d, "volcano_asr_app_id", "") != "" &&
			settingString(d, "volcano_asr_access_token", "") != ""
	}
	writeJSON(w, 200, map[string]any{
		"provider":  provider,
		"streaming": provider == "volcano",
		"enabled":   enabled,
	})
}

// audioStreamHandler upgrades to a WebSocket and relays the mic PCM stream to
// Volcano, forwarding incremental transcripts back. Auth is enforced by
// requireAuth before we get here (the auth_token cookie travels with the
// handshake).
func audioStreamHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if settingString(d, "audio_transcribe_provider", "gpt") != "volcano" {
		writeError(w, 400, errors.New("live transcription is not enabled"))
		return
	}
	if u := authUser(r); u != nil && !rateLimitUser(d, u.ID, "audio_stream", audioStreamUserRateLimit, time.Minute) {
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

	ctx, cancel := context.WithTimeout(r.Context(), audioStreamMaxDur)
	defer cancel()

	vsess, err := dialVolcano(ctx, cfg)
	if err != nil {
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
		var total int64
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
			total += int64(len(data))
			if total > audioStreamMaxBytes {
				break
			}
			if serr := vsess.sendAudio(data); serr != nil {
				break
			}
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
				msg := resp.ErrMessage
				if msg == "" {
					msg = "the transcription service reported an error"
				}
				writeStreamEvent(bconn, streamEvent{Type: "error", Message: msg})
				return
			}
			if resp.Text != "" || resp.IsLastPackage {
				ev := streamEvent{Type: "partial", Text: resp.Text}
				if resp.IsLastPackage {
					ev.Type = "final"
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
func writeStreamEvent(conn *websocket.Conn, ev streamEvent) {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = conn.WriteJSON(ev)
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
