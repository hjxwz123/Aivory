package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	// capTol is the accepted error between the submitted fraction and the true
	// gap fraction (~9px on a 228px track). Forgiving on purpose — the per-IP
	// daily cap is the real abuse backstop; this just deters trivial scripts.
	capTol = 0.04
)

// captchaHandler issues a fresh slider-puzzle challenge.
func captchaHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
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

	id := randToken(12)
	track := float64(capW - capPiece)
	gapFraction := float64(gapX) / track
	d.Cache.Set("captcha:"+id, strconv.FormatFloat(gapFraction, 'f', 6, 64), 5*time.Minute)

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
// single-use PASS token that the register call presents instead of re-solving.
// The underlying challenge is consumed on every attempt (verifyPuzzleCaptcha is
// single-use), so a wrong drag forces a fresh puzzle.
func captchaVerifyHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string  `json:"id"`
		Fraction float64 `json:"fraction"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false})
		return
	}
	if !verifyPuzzleCaptcha(d, req.ID, strconv.FormatFloat(req.Fraction, 'f', 6, 64)) {
		writeJSON(w, 200, map[string]any{"ok": false})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "token": mintCaptchaPass(d.Config.JWTSecret)})
}

// captchaPassTTL bounds how long a solved-captcha pass stays valid.
const captchaPassTTL = 10 * time.Minute

// mintCaptchaPass returns a STATELESS pass proving a captcha was just solved:
// "<expiryUnix>.<HMAC>". It is signed with the server's JWT secret and carries no
// server-side state — so, unlike the previous cache-backed token, it survives an
// API restart and works across multiple API replicas WITHOUT a shared (Redis)
// cache. With the in-memory cache, the old token lived in ONE process's memory,
// so a restart between /captcha/verify and /api/auth/register — or a second
// replica handling the register — lost it and the register 400'd "captcha_failed".
func mintCaptchaPass(secret string) string {
	payload := strconv.FormatInt(time.Now().Add(captchaPassTTL).Unix(), 10)
	return payload + "." + captchaSig(payload, secret)
}

// consumeCaptchaPass verifies a stateless captcha pass (signature + not expired).
// The name is kept for callers; statelessness trades strict single-use for
// restart/multi-instance robustness — acceptable for a registration deterrent,
// where the per-IP daily cap + rate limits are the real abuse backstop, and the
// 10-minute TTL bounds replay.
func consumeCaptchaPass(d Deps, token string) bool {
	payload, sig, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || payload == "" || sig == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(captchaSig(payload, d.Config.JWTSecret))) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	return err == nil && time.Now().Unix() <= exp
}

// captchaSig is the HMAC-SHA256 of the pass payload, keyed by the server secret.
func captchaSig(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("captcha-pass:" + payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyPuzzleCaptcha consumes the cached challenge and checks the submitted drop
// fraction against the true gap fraction within capTol. Single-use: the entry is
// deleted on any attempt so a guessed id can't be hammered.
func verifyPuzzleCaptcha(d Deps, id, answer string) bool {
	if id == "" {
		return false
	}
	key := "captcha:" + id
	saved, ok := d.Cache.Get(key)
	d.Cache.Delete(key)
	if !ok {
		return false
	}
	want, err1 := strconv.ParseFloat(saved, 64)
	got, err2 := strconv.ParseFloat(strings.TrimSpace(answer), 64)
	if err1 != nil || err2 != nil {
		return false
	}
	return math.Abs(got-want) <= capTol
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
