package api

// Volcano Engine (火山引擎) 豆包 streaming ASR client.
//
// Speaks the ByteDance SAUC "bigmodel" binary WebSocket protocol so the browser
// never has to: browsers can't set the required X-Api-* auth headers on a
// WebSocket handshake, and the wire format is a custom big-endian binary framing
// (gzip'd JSON / PCM) — both of which belong on the server. This file is a
// faithful port of the vendored reference demo in /sauc_go (see request/*.go,
// response/response.go, common/common.go), adapted from "read a whole WAV file"
// to "relay a live PCM stream": audio arrives chunk-by-chunk from the composer
// mic and we forward incremental transcripts back.
//
// Protocol recap (all multi-byte integers big-endian):
//
//	4-byte header:
//	  byte0 = protocolVersion(4b)<<4 | headerSize(4b)   // 0x11 → v1, header = 1*4 bytes
//	  byte1 = messageType(4b)<<4    | flags(4b)
//	  byte2 = serialization(4b)<<4  | compression(4b)
//	  byte3 = reserved (0x00)
//	full client request : header | int32(seq=1) | uint32(gzipLen) | gzip(JSON config)
//	audio-only request  : header | int32(seq)   | uint32(gzipLen) | gzip(pcm chunk)
//	                      (last chunk uses a negative seq + NEG_WITH_SEQUENCE flag)
//	server response     : header | [int32 seq] | [int32 event] | uint32(payloadLen) | payload
//	                      (payload gzip'd JSON; error frames carry a uint32 code first)

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// --- protocol constants (mirror sauc_go/common/common.go) -------------------

const (
	volcProtocolVersion = 0b0001

	volcClientFullRequest      = 0b0001
	volcClientAudioOnlyRequest = 0b0010
	volcServerFullResponse     = 0b1001
	volcServerErrorResponse    = 0b1111

	volcFlagPosSequence     = 0b0001 // header trailer holds a positive sequence number
	volcFlagNegWithSequence = 0b0011 // …a negative sequence number, and this is the last packet

	// The reference demo reuses its JSON-serialization header for the audio
	// frames too (it never switches the nibble to raw), so we match it — the
	// server keys off the message-type nibble, not this one.
	volcSerializationJSON = 0b0001
	volcCompressionGzip   = 0b0001
)

// volcanoConfig is the admin-configured Volcano credential + behaviour set.
type volcanoConfig struct {
	AppID       string // X-Api-App-Key (火山控制台 App ID)
	AccessToken string // X-Api-Access-Key (Access Token)
	ResourceID  string // X-Api-Resource-Id, e.g. volc.bigasr.sauc.duration
	WSURL       string // wss://openspeech.bytedance.com/api/v3/sauc/bigmodel
	ModelName   string // "bigmodel"
	EnableITN   bool
	EnablePunc  bool
	EnableDDC   bool
}

// --- request framing --------------------------------------------------------

func volcGzip(in []byte) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write(in)
	_ = w.Close()
	return b.Bytes()
}

func volcGunzip(in []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// volcHeader assembles the 4-byte frame header.
func volcHeader(messageType, flags, serialization byte) []byte {
	return []byte{
		byte(volcProtocolVersion<<4 | 1),
		messageType<<4 | flags,
		serialization<<4 | volcCompressionGzip,
		0x00,
	}
}

// volcRequestPayload is the JSON body of the full client request.
type volcRequestPayload struct {
	User struct {
		UID string `json:"uid"`
	} `json:"user"`
	Audio struct {
		Format  string `json:"format"`
		Codec   string `json:"codec"`
		Rate    int    `json:"rate"`
		Bits    int    `json:"bits"`
		Channel int    `json:"channel"`
	} `json:"audio"`
	Request struct {
		ModelName       string `json:"model_name"`
		EnableITN       bool   `json:"enable_itn"`
		EnablePunc      bool   `json:"enable_punc"`
		EnableDDC       bool   `json:"enable_ddc"`
		ShowUtterances  bool   `json:"show_utterances"`
		EnableNonstream bool   `json:"enable_nonstream"`
		ResultType      string `json:"result_type"`
	} `json:"request"`
}

// newVolcFullClientRequest builds the initial config frame. Unlike the demo
// (which reads a .wav file and streams it with format "wav"), we stream headerless
// 16 kHz mono s16le PCM straight from the browser capture pipeline
// (lib/audio-stream.ts), so the format is "pcm" — no server-side transcoding.
func newVolcFullClientRequest(cfg volcanoConfig) []byte {
	var p volcRequestPayload
	p.User.UID = "aurelia"
	p.Audio.Format = "pcm"
	p.Audio.Codec = "raw"
	p.Audio.Rate = 16000
	p.Audio.Bits = 16
	p.Audio.Channel = 1
	model := cfg.ModelName
	if model == "" {
		model = "bigmodel"
	}
	p.Request.ModelName = model
	p.Request.EnableITN = cfg.EnableITN
	p.Request.EnablePunc = cfg.EnablePunc
	p.Request.EnableDDC = cfg.EnableDDC
	p.Request.ShowUtterances = true
	p.Request.ResultType = "full"

	body, _ := json.Marshal(p)
	gz := volcGzip(body)

	var buf bytes.Buffer
	buf.Write(volcHeader(volcClientFullRequest, volcFlagPosSequence, volcSerializationJSON))
	_ = binary.Write(&buf, binary.BigEndian, int32(1)) // seq = 1
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(gz)))
	buf.Write(gz)
	return buf.Bytes()
}

// newVolcAudioRequest frames one PCM chunk. A negative seq (last=true) also sets
// the NEG_WITH_SEQUENCE flag, telling the server this is the final packet.
func newVolcAudioRequest(seq int32, chunk []byte, last bool) []byte {
	flags := byte(volcFlagPosSequence)
	if last {
		flags = volcFlagNegWithSequence
	}
	gz := volcGzip(chunk)

	var buf bytes.Buffer
	// Serialization nibble stays JSON to mirror the demo (see const block).
	buf.Write(volcHeader(volcClientAudioOnlyRequest, flags, volcSerializationJSON))
	_ = binary.Write(&buf, binary.BigEndian, seq)
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(gz)))
	buf.Write(gz)
	return buf.Bytes()
}

// --- response parsing (mirror sauc_go/response/response.go) ------------------

type volcResponse struct {
	Code          int    // non-zero on a server error frame
	IsLastPackage bool   // server signalled the final packet
	Text          string // cumulative transcript for result_type=full
	ErrMessage    string // decoded error text, when Code != 0
}

// parseVolcResponse decodes one server frame. It is defensive about truncated
// payloads because the upstream is untrusted at the byte level.
func parseVolcResponse(msg []byte) (*volcResponse, error) {
	if len(msg) < 4 {
		return nil, fmt.Errorf("volcano: short frame (%d bytes)", len(msg))
	}
	headerSize := int(msg[0]&0x0f) * 4
	if headerSize < 4 || headerSize > len(msg) {
		return nil, fmt.Errorf("volcano: bad header size")
	}
	messageType := msg[1] >> 4
	flags := msg[1] & 0x0f
	serialization := msg[2] >> 4
	compression := msg[2] & 0x0f
	payload := msg[headerSize:]

	res := &volcResponse{}

	// Optional trailer fields, in the same order the reference client reads them.
	if flags&0x01 != 0 { // sequence number present
		if len(payload) < 4 {
			return res, nil
		}
		payload = payload[4:]
	}
	if flags&0x02 != 0 { // last-package marker
		res.IsLastPackage = true
	}
	if flags&0x04 != 0 { // event id present
		if len(payload) < 4 {
			return res, nil
		}
		payload = payload[4:]
	}

	switch messageType {
	case volcServerFullResponse:
		if len(payload) < 4 {
			return res, nil
		}
		size := binary.BigEndian.Uint32(payload[:4])
		payload = payload[4:]
		if int(size) < len(payload) {
			payload = payload[:size]
		}
	case volcServerErrorResponse:
		if len(payload) < 8 {
			return res, fmt.Errorf("volcano: truncated error frame")
		}
		res.Code = int(binary.BigEndian.Uint32(payload[:4]))
		size := binary.BigEndian.Uint32(payload[4:8])
		payload = payload[8:]
		if int(size) < len(payload) {
			payload = payload[:size]
		}
	default:
		return res, nil
	}

	if len(payload) == 0 {
		return res, nil
	}
	if compression == volcCompressionGzip {
		dec, err := volcGunzip(payload)
		if err != nil {
			return res, fmt.Errorf("volcano: gunzip payload: %w", err)
		}
		payload = dec
	}

	if serialization == volcSerializationJSON {
		var body struct {
			Result struct {
				Text string `json:"text"`
			} `json:"result"`
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload, &body); err == nil {
			res.Text = body.Result.Text
			if res.Code != 0 {
				res.ErrMessage = firstNonEmpty(body.Message, body.Error, string(payload))
			}
		} else if res.Code != 0 {
			res.ErrMessage = string(payload)
		}
	} else if res.Code != 0 {
		res.ErrMessage = string(payload)
	}
	return res, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// --- session ----------------------------------------------------------------

// volcanoSession is a live connection to the Volcano ASR service. Writes
// (sendAudio / sendLast) must all come from a single goroutine; reads
// (readResponse) from a single (other) goroutine — matching gorilla's
// one-reader + one-writer concurrency contract.
type volcanoSession struct {
	conn  *websocket.Conn
	seq   int32
	logID string
}

// dialVolcano opens the WebSocket, performs the auth handshake and sends the
// full client (config) request. The returned session is ready for audio.
func dialVolcano(ctx context.Context, cfg volcanoConfig) (*volcanoSession, error) {
	if cfg.AppID == "" || cfg.AccessToken == "" {
		return nil, fmt.Errorf("volcano ASR is not configured (missing App ID / Access Token)")
	}
	resource := cfg.ResourceID
	if resource == "" {
		resource = "volc.bigasr.sauc.duration"
	}
	url := cfg.WSURL
	if url == "" {
		url = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel"
	}

	header := http.Header{}
	header.Set("X-Api-App-Key", cfg.AppID)
	header.Set("X-Api-Access-Key", cfg.AccessToken)
	header.Set("X-Api-Resource-Id", resource)
	header.Set("X-Api-Request-Id", uuid.New().String())

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second
	conn, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("volcano dial %s: %w", resp.Status, err)
		}
		return nil, fmt.Errorf("volcano dial: %w", err)
	}

	s := &volcanoSession{conn: conn, seq: 1}
	if resp != nil {
		s.logID = resp.Header.Get("X-Tt-Logid")
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, newVolcFullClientRequest(cfg)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("volcano send config: %w", err)
	}
	s.seq = 2 // seq 1 was the config frame; audio packets continue from 2
	return s, nil
}

// sendAudio forwards one PCM chunk as a (positive-seq) audio packet.
//
// NB: audio frames go out as WebSocket *text* frames, not binary. That looks
// wrong for binary payloads, but it's exactly what the reference demo does
// (client.go sendMessages), and the config frame stays binary (dialVolcano) —
// the server dispatches on the in-payload message-type nibble, not the WS
// opcode. We mirror the demo to stay on its proven path.
func (s *volcanoSession) sendAudio(chunk []byte) error {
	frame := newVolcAudioRequest(s.seq, chunk, false)
	s.seq++
	return s.conn.WriteMessage(websocket.TextMessage, frame)
}

// sendLast forwards the final packet (negative seq) so the server flushes and
// emits its last transcript. chunk may be nil/empty.
func (s *volcanoSession) sendLast(chunk []byte) error {
	frame := newVolcAudioRequest(-s.seq, chunk, true)
	s.seq++
	return s.conn.WriteMessage(websocket.TextMessage, frame)
}

// readResponse blocks for the next server frame. Each read refreshes the read
// deadline so a silently-dead upstream can't hang the relay forever. Like the
// demo, we parse whatever arrives regardless of the WS opcode (the server may
// answer with text frames).
func (s *volcanoSession) readResponse() (*volcResponse, error) {
	_ = s.conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	_, data, err := s.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	return parseVolcResponse(data)
}

func (s *volcanoSession) close() {
	_ = s.conn.Close()
}
