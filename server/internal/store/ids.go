package store

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// genID returns a stable, URL-safe identifier with the given prefix. The
// non-prefix part is 12 hex characters (48 bits of randomness) — plenty for
// the size of state this server holds.
func genID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// GenID is the exported helper used by handlers when minting new rows.
func GenID(prefix string) string { return genID(prefix) }

// genToken returns a high-entropy (192-bit) URL-safe token, used where the id
// doubles as an unguessable capability secret — e.g. public share links (§D1).
// 48 hex chars vs genID's 12 makes brute-force enumeration infeasible.
func genToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// redeemCodeAlphabet — Crockford-style base32 minus ambiguous chars (no
// 0/O/I/1) so codes are easy to type from a printed flyer or chat message.
const redeemCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GenRedeemCode returns a human-typeable redeem code of the form XXXX-XXXX-XXXX
// using Crockford-style base32 minus ambiguous chars. 12 chars × 5 bits = 60 bits
// of entropy — plenty to defeat brute force even at the alphabet limit. Panics
// on entropy failure (mirrors genCode6's posture: a redeem code grants real
// value, never degrade to math/rand).
func GenRedeemCode() string {
	out := make([]byte, 0, 14)
	for i := 0; i < 12; i++ {
		if i > 0 && i%4 == 0 {
			out = append(out, '-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(redeemCodeAlphabet))))
		if err != nil {
			panic("GenRedeemCode: crypto/rand unavailable — refusing to use a predictable code: " + err.Error())
		}
		out = append(out, redeemCodeAlphabet[n.Int64()])
	}
	return string(out)
}

// NormalizeRedeemCode strips whitespace + dashes and uppercases, so users can
// paste "abcd efgh - ijkl" and we'll find it.
func NormalizeRedeemCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '-' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		out = append(out, c)
	}
	// Re-insert dashes every 4 chars so storage form is canonical.
	if len(out) > 4 {
		buf := make([]byte, 0, len(out)+(len(out)-1)/4)
		for i, c := range out {
			if i > 0 && i%4 == 0 {
				buf = append(buf, '-')
			}
			buf = append(buf, c)
		}
		return string(buf)
	}
	return string(out)
}

func hashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(h), err
}

// CheckPassword reports whether the bcrypt hash matches the given plain text.
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// HashPassword is the exported wrapper for handlers.
func HashPassword(plain string) (string, error) { return hashPassword(plain) }

// trim is a small helper used by validation code that does not want to pull
// in strings/strings.TrimSpace at every call site.
func trim(s string) string { return strings.TrimSpace(s) }

var _ = trim
