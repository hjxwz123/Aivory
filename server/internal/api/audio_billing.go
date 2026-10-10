package api

// Speech-to-text billing (§ voice). Administrators price server-side speech
// recognition per second of audio in USD (audio_transcribe_price_per_second).
// The charge is converted with the platform-wide credits_per_usd rate and moves
// through the usual reservation → settlement ledger, so two concurrent sessions
// can never spend the same balance. Billing is off — the feature is free —
// whenever either the price or the credit rate is zero, and administrators are
// never charged (matching model usage, § admin). Browser-side recognition never
// reaches the server and is always free.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"time"

	"aivory/server/internal/store"
)

const (
	audioBillingSourceType = "audio"
	audioUsagePurpose      = "audio.transcription"
	audioReservationTTL    = 30 * time.Minute
	audioBillingTimeout    = 15 * time.Second
	// The live-streaming wire format: 16 kHz · mono · 16-bit PCM.
	audioPCMBytesPerSecond = 32000
	// Assumed bitrate when a non-WAV upload's length cannot be established any
	// other way (speech-grade Opus/AAC). Browsers always upload WAV or send the
	// recorded duration, so only non-standard API callers reach this estimate.
	audioFallbackBytesPerSecond = 4000
	// Upper bound for a client-reported duration: a 25 MiB upload cannot hold
	// more than a few hours of compressed speech.
	audioMaxReportedSeconds = 4 * 60 * 60
)

var errAudioInsufficientCredits = errors.New("insufficient credits for voice transcription")

// audioBilling is the resolved price for one caller. A nil *audioBilling means
// the call is free.
type audioBilling struct {
	UserID           string
	PricePerSecond   float64 // USD
	CreditsPerSecond float64 // PricePerSecond × credits_per_usd
}

// audioPricePerSecond reads the administrator's USD price for one second of
// server-side transcription. Unset, malformed or negative values mean free.
func audioPricePerSecond(d Deps) float64 {
	raw, err := store.GetSetting(d.DB, "audio_transcribe_price_per_second")
	if err != nil || len(raw) == 0 {
		return 0
	}
	var price float64
	if json.Unmarshal(raw, &price) != nil || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return 0
	}
	return price
}

func audioBillingFor(d Deps, user *store.User) *audioBilling {
	if user == nil || user.Role == "admin" {
		return nil
	}
	price := audioPricePerSecond(d)
	ratio := globalCreditsPerUSD(d)
	if price <= 0 || ratio <= 0 {
		return nil
	}
	perSecond := price * ratio
	// A price too small to move one credit micro per second would make every
	// reservation invalid; treat it as free rather than failing every call.
	if roundCreditMicros(perSecond) <= 0 {
		return nil
	}
	return &audioBilling{UserID: user.ID, PricePerSecond: price, CreditsPerSecond: perSecond}
}

// roundCreditMicros rounds a credit amount to the ledger's micro precision so
// float noise never reaches the reservation arithmetic.
func roundCreditMicros(credits float64) float64 {
	return math.Round(credits*float64(store.CreditMicrosPerUnit)) / float64(store.CreditMicrosPerUnit)
}

// billableSeconds rounds audio up to whole seconds (minimum one second for any
// audio at all), the usual per-second billing granularity.
func billableSeconds(seconds float64) int64 {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	// Absorb float noise such as 3.0000000001 from byte-count division.
	return int64(math.Ceil(seconds - 1e-6))
}

func (b *audioBilling) credits(seconds int64) float64 {
	if b == nil || seconds <= 0 {
		return 0
	}
	return roundCreditMicros(float64(seconds) * b.CreditsPerSecond)
}

// affordableSeconds is how many whole seconds the caller's available balance
// covers.
func (b *audioBilling) affordableSeconds(ctx context.Context, d Deps) (int64, error) {
	balance, err := store.GetCreditBalance(ctx, d.DB, b.UserID)
	if err != nil {
		return 0, err
	}
	return int64(math.Floor(balance.Available/b.CreditsPerSecond + 1e-9)), nil
}

// reserve holds the price of `seconds` under sourceID. It reports
// errAudioInsufficientCredits when the balance cannot cover it.
func (b *audioBilling) reserve(ctx context.Context, d Deps, sourceID string, seconds int64) error {
	amount := b.credits(seconds)
	if amount <= 0 {
		return nil
	}
	if _, err := store.ReserveCredits(ctx, d.DB, b.UserID, amount, audioBillingSourceType, sourceID, audioReservationTTL); err != nil {
		if errors.Is(err, store.ErrInsufficientCredits) {
			return errAudioInsufficientCredits
		}
		return err
	}
	return nil
}

// release drops an unused hold.
func (b *audioBilling) release(ctx context.Context, d Deps, sourceID string) {
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), audioBillingTimeout)
	defer cancel()
	_ = store.ReleaseCreditReservation(ctx, d.DB, audioBillingSourceType, sourceID)
}

// settle charges the seconds actually transcribed against the hold taken by
// reserve (a missing hold is taken first, so settlement never depends on one).
// It runs detached from the request so a closed connection cannot skip it.
func (b *audioBilling) settle(ctx context.Context, d Deps, sourceID string, seconds int64) (store.CreditDebit, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), audioBillingTimeout)
	defer cancel()
	amount := b.credits(seconds)
	if amount <= 0 {
		_ = store.ReleaseCreditReservation(ctx, d.DB, audioBillingSourceType, sourceID)
		return store.CreditDebit{}, nil
	}
	if _, err := store.LookupCreditReservation(ctx, d.DB, audioBillingSourceType, sourceID); errors.Is(err, store.ErrNotFound) {
		if _, err := store.ReserveCredits(ctx, d.DB, b.UserID, amount, audioBillingSourceType, sourceID, audioReservationTTL); err != nil {
			return store.CreditDebit{}, err
		}
	} else if err != nil {
		return store.CreditDebit{}, err
	}
	debit, err := store.SettleCreditReservation(ctx, d.DB, audioBillingSourceType, sourceID, amount)
	if err != nil {
		// A failed settlement rolls back; drop the hold instead of leaving the
		// balance blocked until the reservation expires.
		_ = store.ReleaseCreditReservation(ctx, d.DB, audioBillingSourceType, sourceID)
		return store.CreditDebit{}, err
	}
	return debit, nil
}

// recordAudioUsage publishes one transcription to Admin → Usage & billing. Free
// calls are recorded too (cost and credits 0) so administrators can see how
// much the feature is used.
func recordAudioUsage(ctx context.Context, d Deps, userID, sourceID string, seconds int64, billing *audioBilling, credits float64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), audioBillingTimeout)
	defer cancel()
	usage := store.UsageLog{
		UserID:    userID,
		MessageID: sourceID,
		Purpose:   audioUsagePurpose,
		Credits:   credits,
		Status:    "ok",
		CreatedAt: time.Now().Unix(),
	}
	if billing != nil && seconds > 0 {
		usage.Cost = float64(seconds) * billing.PricePerSecond
		usage.Currency = "USD"
	}
	if err := store.LogUsage(ctx, d.DB, usage); err != nil && d.Logger != nil {
		d.Logger.Printf("voice usage record failed (user=%s source=%s): %v", userID, sourceID, err)
	}
}

// wavDurationSeconds returns the duration of a RIFF/WAVE PCM upload from the
// audio bytes actually present — never from the header's claimed length, so a
// truncated or forged header cannot shrink the bill. ok is false for anything
// that is not a well-formed WAV.
func wavDurationSeconds(data []byte) (seconds float64, ok bool) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, false
	}
	var byteRate uint32
	var sampleBlockAlign uint16
	var dataBytes int
	offset := 12
	for offset+8 <= len(data) {
		id := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		body := offset + 8
		if size < 0 || size > len(data)-body {
			return 0, false
		}
		switch id {
		case "fmt ":
			if byteRate != 0 || size < 16 || body+16 > len(data) {
				return 0, false
			}
			channels := binary.LittleEndian.Uint16(data[body+2 : body+4])
			format := binary.LittleEndian.Uint16(data[body : body+2])
			bits := binary.LittleEndian.Uint16(data[body+14 : body+16])
			sampleRate := binary.LittleEndian.Uint32(data[body+4 : body+8])
			byteRate = binary.LittleEndian.Uint32(data[body+8 : body+12])
			blockAlign := binary.LittleEndian.Uint16(data[body+12 : body+14])
			sampleBlockAlign = blockAlign
			// Reject inconsistent formats: a header claiming a huge byte rate over
			// ordinary samples would otherwise make long audio look short.
			if (format != 1 && format != 3) || channels == 0 || channels > 8 ||
				(bits != 8 && bits != 16 && bits != 24 && bits != 32 && bits != 64) ||
				(format == 3 && bits != 32 && bits != 64) ||
				blockAlign != channels*(bits/8) || sampleRate < 8000 || sampleRate > 192000 ||
				byteRate != sampleRate*uint32(blockAlign) {
				return 0, false
			}
		case "data":
			if byteRate == 0 || size%int(sampleBlockAlign) != 0 {
				return 0, false
			}
			dataBytes += size
		}
		// Chunks are word-aligned.
		offset = body + size + size%2
	}
	if byteRate == 0 || dataBytes == 0 {
		return 0, false
	}
	return float64(dataBytes) / float64(byteRate), true
}

// upstreamAudioSeconds reads the audio length an OpenAI-compatible
// transcription response reports, when it reports one: verbose_json's
// top-level "duration", or the newer usage block {"type":"duration","seconds":N}.
func upstreamAudioSeconds(body []byte) float64 {
	var parsed struct {
		Duration float64 `json:"duration"`
		Usage    struct {
			Type    string  `json:"type"`
			Seconds float64 `json:"seconds"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return 0
	}
	if parsed.Usage.Type == "duration" && parsed.Usage.Seconds > 0 {
		return parsed.Usage.Seconds
	}
	if parsed.Duration > 0 {
		return parsed.Duration
	}
	return 0
}
