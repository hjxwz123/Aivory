package store

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeUserEmailRequiresPlainMailbox(t *testing.T) {
	got, err := NormalizeUserEmail("  USER@Example.Test  ")
	if err != nil || got != "user@example.test" {
		t.Fatalf("normalized email = %q, %v", got, err)
	}

	for _, value := range []string{
		"not-an-email",
		"victim@example.test@evil.test",
		"User <user@example.test>",
		"user@example.test\r\nBcc: attacker@example.test",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := NormalizeUserEmail(value); !errors.Is(err, ErrUserEmailInvalid) {
				t.Fatalf("NormalizeUserEmail(%q) error = %v, want ErrUserEmailInvalid", value, err)
			}
		})
	}
}

func TestCreateUserRejectsMalformedMailboxAtStoreBoundary(t *testing.T) {
	db := openAuthSecurityDB(t, "invalid-email.db")
	if _, err := CreateUser(t.Context(), db, "victim@example.test@evil.test", "Victim", "hash"); !errors.Is(err, ErrUserEmailInvalid) {
		t.Fatalf("CreateUser malformed-email error = %v, want ErrUserEmailInvalid", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("malformed email persisted %d users", count)
	}
}

func TestCheckPasswordTreatsBcryptOverflowAsAuthenticationFailure(t *testing.T) {
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if CheckPassword(hash, strings.Repeat("a", 73)) {
		t.Fatal("overlong bcrypt candidate authenticated")
	}
}
