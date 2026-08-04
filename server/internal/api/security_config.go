package api

import (
	"time"
	"unicode/utf8"

	"aivory/server/internal/envcfg"
)

// bcrypt rejects passwords longer than 72 bytes. Validate that limit before
// hashing so every password-setting endpoint returns a client error instead of
// surfacing bcrypt's expected input error as an internal server failure.
const bcryptMaxPasswordBytes = 72

func securityDuration(key string, fallback time.Duration) time.Duration {
	value := envcfg.Dur(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}

// A minimum above bcrypt's byte ceiling makes every possible password fail.
// Treat that as an invalid override instead of letting an operator lock all
// password creation and recovery paths.
func securityPasswordMinimum(key string, fallback int) int {
	value := envcfg.Int(key, fallback)
	if value <= 0 || value > bcryptMaxPasswordBytes {
		return fallback
	}
	return value
}

func validateNewPassword(password string, minimumRunes int) error {
	if !utf8.ValidString(password) {
		return errInvalidInput
	}
	if utf8.RuneCountInString(password) < minimumRunes {
		return errPasswordTooShort
	}
	if len(password) > bcryptMaxPasswordBytes {
		return errPasswordTooLong
	}
	return nil
}
