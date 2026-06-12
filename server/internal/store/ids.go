package store

import (
	"crypto/rand"
	"encoding/hex"
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
