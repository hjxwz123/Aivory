package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
)

// Slider-puzzle captcha (§ registration anti-abuse). The server renders a
// textured background with a puzzle-piece notch at a random X, plus the matching
// cut-out piece. The client slides the piece horizontally; on submit it sends the
// drop position as a FRACTION of the slidable track (0–1), which is resolution-
// independent (no pixel/scale coupling between client CSS size and image size).
// The correct fraction is cached server-side under a random id, single-use.
//
// This is an image-based but NOT an OCR ("type the letters") challenge.

const (
	capW     = 280 // background width  (natural px)
	capH     = 160 // background height (natural px)
	capPiece = 52  // puzzle piece bounding box (square)

	captchaTraceMinDuration = 180 * time.Millisecond
	captchaTraceMaxDuration = 20 * time.Second
	captchaTraceMaxPoints   = 80
)

// capTol is the accepted error between the submitted fraction and the true
// gap fraction (~6px on a 228px track). The server also validates the drag
// trace, challenge binding and one-time pass; position alone is not trusted.
var capTol = envcfg.F64("AIVORY_API_CAP_TOL", 0.025)

// captchaChallengeCacheTTL bounds how long an unsolved challenge stays valid.
var captchaChallengeCacheTTL = securityDuration("AIVORY_API_CAPTCHA_CHALLENGE_CACHE_TTL", 2*time.Minute)

type captchaPurpose string

const (
	captchaPurposeLogin    captchaPurpose = "login"
	captchaPurposeRegister captchaPurpose = "register"
)

type captchaChallengeState struct {
	Answer   float64        `json:"answer"`
	Purpose  captchaPurpose `json:"purpose"`
	Binding  string         `json:"binding"`
	IssuedAt int64          `json:"issued_at"`
}

type captchaPassState struct {
	Purpose captchaPurpose `json:"purpose"`
	Binding string         `json:"binding"`
}

type captchaTracePoint struct {
	X float64 `json:"x"`
	T int64   `json:"t"`
}

type captchaInteraction struct {
	Mode   string              `json:"mode"`
	Points []captchaTracePoint `json:"points"`
}

// captchaHandler issues a fresh slider-puzzle challenge.
func captchaHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	purpose, ok := parseCaptchaPurpose(r.URL.Query().Get("purpose"))
	if !ok {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if d.Cache == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("captcha cache unavailable"))
		return
	}

	// Gap sits in the right ~⅔ so the piece (starting at x=0) always slides right.
	gapX := capPiece + 24 + cRandInt(capW-2*capPiece-32)
	gapY := 10 + cRandInt(capH-capPiece-20)

	bg := genCaptchaBackground()
	piece := image.NewRGBA(image.Rect(0, 0, capPiece, capPiece))
	for dy := 0; dy < capPiece; dy++ {
		for dx := 0; dx < capPiece; dx++ {
			if !isPiecePixel(dx, dy, capPiece) {
				continue
			}
			if isPieceEdge(dx, dy, capPiece) {
				// Bright rim on the piece + a faint rim on the notch so both read.
				piece.SetRGBA(dx, dy, color.RGBA{255, 255, 255, 240})
				blendPixel(bg, gapX+dx, gapY+dy, color.RGBA{255, 255, 255, 150})
				continue
			}
			src := bg.RGBAAt(gapX+dx, gapY+dy)
			piece.SetRGBA(dx, dy, color.RGBA{src.R, src.G, src.B, 255})
			// Carve the notch into the background.
			blendPixel(bg, gapX+dx, gapY+dy, color.RGBA{0, 0, 0, 140})
		}
	}

	id := randToken(16)
	if id == "" {
		writeError(w, http.StatusInternalServerError, errors.New("secure random source unavailable"))
		return
	}
	track := float64(capW - capPiece)
	gapFraction := float64(gapX) / track
	state, err := json.Marshal(captchaChallengeState{
		Answer:   gapFraction,
		Purpose:  purpose,
		Binding:  captchaClientBinding(d, r),
		IssuedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("captcha state unavailable"))
		return
	}
	d.Cache.Set("captcha:"+id, string(state), captchaChallengeCacheTTL)

	writeJSON(w, 200, map[string]any{
		"id":         id,
		"background": pngDataURL(bg),
		"piece":      pngDataURL(piece),
		"w":          capW,
		"h":          capH,
		"piece_size": capPiece,
		"piece_y":    gapY,
	})
}

// captchaVerifyHandler checks a slider solution NOW (so the client shows
// immediate green/red feedback — the modern UX) and, on success, issues a
// single-use PASS token that the pending auth request presents instead of re-solving.
// The underlying challenge is consumed on every attempt (verifyPuzzleCaptcha is
// single-use), so a wrong drag forces a fresh puzzle.
func captchaVerifyHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string             `json:"id"`
		Fraction    float64            `json:"fraction"`
		Interaction captchaInteraction `json:"interaction"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false})
		return
	}
	challenge, ok := verifyPuzzleCaptcha(d, r, req.ID, req.Fraction, req.Interaction)
	if !ok {
		writeJSON(w, 200, map[string]any{"ok": false})
		return
	}
	token := mintCaptchaPass(d, challenge.Purpose, challenge.Binding)
	if token == "" {
		writeError(w, http.StatusInternalServerError, errors.New("captcha pass unavailable"))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "token": token})
}

// captchaPassTTL bounds how long a solved-captcha pass stays valid.
var captchaPassTTL = securityDuration("AIVORY_API_CAPTCHA_PASS_TTL", 10*time.Minute)

// mintCaptchaPass returns a signed, cache-backed one-time credential. A shared
// Redis cache is required for multi-replica deployments, just like the puzzle
// challenge itself; retaining server-side state is what makes replay impossible.
func mintCaptchaPass(d Deps, purpose captchaPurpose, binding string) string {
	if d.Cache == nil {
		return ""
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	payload := strconv.FormatInt(time.Now().Add(captchaPassTTL).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	token := payload + "." + captchaSig(payload, d.Config.JWTSecret)
	state, err := json.Marshal(captchaPassState{Purpose: purpose, Binding: binding})
	if err != nil {
		return ""
	}
	d.Cache.Set(captchaPassKey(token), string(state), captchaPassTTL)
	return token
}

// consumeCaptchaPass verifies and atomically consumes a captcha pass.
func consumeCaptchaPass(d Deps, r *http.Request, token string, purpose captchaPurpose) bool {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return false
	}
	payload := parts[0] + "." + parts[1]
	sig := parts[2]
	if subtle.ConstantTimeCompare([]byte(sig), []byte(captchaSig(payload, d.Config.JWTSecret))) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp || d.Cache == nil {
		return false
	}
	raw, ok := d.Cache.Take(captchaPassKey(token))
	if !ok {
		return false
	}
	var state captchaPassState
	if json.Unmarshal([]byte(raw), &state) != nil || state.Purpose != purpose {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(state.Binding), []byte(captchaClientBinding(d, r))) == 1
}

func captchaPassKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "captcha:pass:" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// captchaSig is the HMAC-SHA256 of the pass payload, keyed by the server secret.
func captchaSig(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("captcha-pass:" + payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyPuzzleCaptcha consumes the cached challenge before checking any answer.
// This prevents retries against one image and keeps every failure indistinguishable.
func verifyPuzzleCaptcha(d Deps, r *http.Request, id string, answer float64, interaction captchaInteraction) (captchaChallengeState, bool) {
	var challenge captchaChallengeState
	decodedID, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(id))
	if err != nil || len(decodedID) != 16 || d.Cache == nil {
		return challenge, false
	}
	key := "captcha:" + id
	saved, ok := d.Cache.Take(key)
	if !ok {
		return challenge, false
	}
	if json.Unmarshal([]byte(saved), &challenge) != nil || !validCaptchaPurpose(challenge.Purpose) {
		return captchaChallengeState{}, false
	}
	if subtle.ConstantTimeCompare([]byte(challenge.Binding), []byte(captchaClientBinding(d, r))) != 1 {
		return captchaChallengeState{}, false
	}
	challengeAge := time.Since(time.UnixMilli(challenge.IssuedAt))
	if challengeAge < captchaTraceMinDuration || challengeAge > captchaChallengeCacheTTL+time.Second {
		return captchaChallengeState{}, false
	}
	if math.IsNaN(answer) || math.IsInf(answer, 0) || math.Abs(answer-challenge.Answer) > capTol {
		return captchaChallengeState{}, false
	}
	if !validCaptchaInteraction(interaction, answer) {
		return captchaChallengeState{}, false
	}
	return challenge, true
}

func parseCaptchaPurpose(raw string) (captchaPurpose, bool) {
	purpose := captchaPurpose(strings.TrimSpace(raw))
	// Register remains the default for one rolling-deploy window because older
	// registration clients issued challenges without an explicit purpose.
	if purpose == "" {
		purpose = captchaPurposeRegister
	}
	return purpose, validCaptchaPurpose(purpose)
}

func validCaptchaPurpose(purpose captchaPurpose) bool {
	return purpose == captchaPurposeLogin || purpose == captchaPurposeRegister
}

func validCaptchaInteraction(interaction captchaInteraction, answer float64) bool {
	if interaction.Mode != "pointer" && interaction.Mode != "keyboard" {
		return false
	}
	points := interaction.Points
	if len(points) < 3 || len(points) > captchaTraceMaxPoints {
		return false
	}
	first := points[0]
	last := points[len(points)-1]
	if first.T < 0 || first.T > 50 || first.X < 0 || first.X > 0.12 {
		return false
	}
	duration := time.Duration(last.T) * time.Millisecond
	if duration < captchaTraceMinDuration || duration > captchaTraceMaxDuration || math.Abs(last.X-answer) > 0.015 {
		return false
	}
	minX, maxX := first.X, first.X
	previousT := int64(-1)
	distinctX := map[int]struct{}{}
	distinctT := map[int64]struct{}{}
	for _, point := range points {
		if math.IsNaN(point.X) || math.IsInf(point.X, 0) || point.X < 0 || point.X > 1 ||
			point.T < 0 || point.T < previousT || point.T > captchaTraceMaxDuration.Milliseconds() {
			return false
		}
		minX = math.Min(minX, point.X)
		maxX = math.Max(maxX, point.X)
		previousT = point.T
		distinctX[int(math.Round(point.X*1000))] = struct{}{}
		distinctT[point.T] = struct{}{}
	}
	return maxX-minX >= 0.20 && len(distinctX) >= 3 && len(distinctT) >= 3
}

func captchaClientBinding(d Deps, r *http.Request) string {
	ip := net.ParseIP(strings.TrimSpace(clientIP(r)))
	network := ""
	// Prefix binding blocks cross-client token transfer without tying a short
	// interaction to one exact mobile address that may rotate within the prefix.
	if ip4 := ip.To4(); ip4 != nil {
		network = ip4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	} else if ip != nil {
		network = ip.Mask(net.CIDRMask(56, 128)).String() + "/56"
	}
	userAgent := strings.TrimSpace(r.UserAgent())
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	mac := hmac.New(sha256.New, []byte(d.Config.JWTSecret))
	mac.Write([]byte("captcha-client\x00" + network + "\x00" + userAgent))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// genCaptchaBackground paints a soft base tinted toward the brand and scatters a
// few translucent blobs so the carved notch is visible.
func genCaptchaBackground() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, capW, capH))
	for y := 0; y < capH; y++ {
		// Subtle vertical gradient so the surface isn't flat.
		shade := uint8(228 - y*16/capH)
		for x := 0; x < capW; x++ {
			img.SetRGBA(x, y, color.RGBA{shade, uint8(int(shade) - 4), uint8(int(shade) - 12), 255})
		}
	}
	for i := 0; i < 7; i++ {
		cx := cRandInt(capW)
		cy := cRandInt(capH)
		r := 16 + cRandInt(40)
		col := color.RGBA{uint8(70 + cRandInt(170)), uint8(70 + cRandInt(170)), uint8(80 + cRandInt(160)), 95}
		fillCircle(img, cx, cy, r, col)
	}
	return img
}

// isPiecePixel describes the puzzle shape: a square body with a round knob
// protruding from the centre of the top edge.
func isPiecePixel(dx, dy, size int) bool {
	knobR := size * 18 / 100
	bodyTop := knobR + 3
	m := 3
	inBody := dx >= m && dx <= size-m && dy >= bodyTop && dy <= size-m
	cx, cy := size/2, bodyTop
	ddx, ddy := dx-cx, dy-cy
	inKnob := dy < bodyTop && ddx*ddx+ddy*ddy <= knobR*knobR
	return inBody || inKnob
}

// isPieceEdge marks a piece pixel that borders a non-piece pixel (for the rim).
func isPieceEdge(dx, dy, size int) bool {
	if !isPiecePixel(dx, dy, size) {
		return false
	}
	return !isPiecePixel(dx-1, dy, size) || !isPiecePixel(dx+1, dy, size) ||
		!isPiecePixel(dx, dy-1, size) || !isPiecePixel(dx, dy+1, size)
}

func fillCircle(dst *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dxv, dyv := x-cx, y-cy
			if dxv*dxv+dyv*dyv <= r*r {
				blendPixel(dst, x, y, c)
			}
		}
	}
}

// blendPixel alpha-composites c over the existing pixel.
func blendPixel(dst *image.RGBA, x, y int, c color.RGBA) {
	b := dst.Bounds()
	if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
		return
	}
	o := dst.RGBAAt(x, y)
	a := float64(c.A) / 255
	dst.SetRGBA(x, y, color.RGBA{
		R: uint8(float64(c.R)*a + float64(o.R)*(1-a)),
		G: uint8(float64(c.G)*a + float64(o.G)*(1-a)),
		B: uint8(float64(c.B)*a + float64(o.B)*(1-a)),
		A: 255,
	})
}

func pngDataURL(img image.Image) string {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// cRandInt returns a crypto-random int in [0, n). Falls back to 0 if the OS
// entropy source is unavailable.
func cRandInt(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}
