package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPasskeyStoreCRUDAndCascade(t *testing.T) {
	db := openAuthSecurityDB(t, "passkeys.db")
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('pk-user','pk@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('pk-other','other@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}

	pk := &Passkey{
		UserID: "pk-user", CredentialID: []byte{0x01, 0x02, 0x03}, PublicKey: []byte{0xAA, 0xBB},
		SignCount: 7, AuthenticatorFlags: 0x0D, FlagsKnown: true, Name: "  MacBook Pro  ",
	}
	if err := CreatePasskey(ctx, db, pk); err != nil {
		t.Fatal(err)
	}
	if pk.ID == "" {
		t.Fatal("id not minted")
	}
	if pk.Name != "MacBook Pro" {
		t.Fatalf("name not trimmed: %q", pk.Name)
	}

	listed, err := ListPasskeys(ctx, db, "pk-user")
	if err != nil || len(listed) != 1 || listed[0].Name != "MacBook Pro" || listed[0].SignCount != 7 {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	if listed[0].CredentialID != nil || listed[0].PublicKey != nil {
		t.Fatal("list should not load raw credential bytes")
	}
	count, err := CountPasskeys(ctx, db, "pk-user")
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}

	byCred, err := GetPasskeyByCredentialID(ctx, db, []byte{0x01, 0x02, 0x03})
	if err != nil || byCred.ID != pk.ID || byCred.UserID != "pk-user" || byCred.SignCount != 7 ||
		!byCred.FlagsKnown || byCred.AuthenticatorFlags != 0x0D {
		t.Fatalf("byCred=%+v err=%v", byCred, err)
	}
	if _, err := GetPasskeyByCredentialID(ctx, db, []byte{0xFF}); !errors.Is(err, ErrPasskeyNotFound) {
		t.Fatalf("unknown credential err=%v", err)
	}

	if err := TouchPasskey(ctx, db, pk.ID, 12, 0x1D); err != nil {
		t.Fatal(err)
	}
	byCred, err = GetPasskeyByCredentialID(ctx, db, []byte{0x01, 0x02, 0x03})
	if err != nil || byCred.SignCount != 12 || byCred.AuthenticatorFlags != 0x1D || byCred.LastUsedAt == 0 {
		t.Fatalf("touch not persisted: %+v err=%v", byCred, err)
	}

	// Cross-user deletes are impossible.
	if err := DeletePasskey(ctx, db, "pk-other", pk.ID); !errors.Is(err, ErrPasskeyNotFound) {
		t.Fatalf("foreign delete err=%v", err)
	}

	// Session issuance guard: with a passkey present the WithPasskey mode locks
	// and inserts; without one (or with a TOTP secret threaded in) it refuses.
	jti := "jti-pk"
	meta := SessionMeta{SessionID: "sess-pk", IP: "1.2.3.4"}
	if err := SaveRefreshTokenForLogin(ctx, db, jti, "pk-user", 0, LoginSessionWithPasskey, "", time.Now().Add(time.Hour), meta); err != nil {
		t.Fatalf("passkey session insert: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM passkeys WHERE user_id='pk-user'`); err != nil {
		t.Fatal(err)
	}
	if err := SaveRefreshTokenForLogin(ctx, db, "jti-gone", "pk-user", 0, LoginSessionWithPasskey, "", time.Now().Add(time.Hour), meta); !errors.Is(err, ErrLoginStateChanged) {
		t.Fatalf("deleted-during-login err=%v", err)
	}
	if err := SaveRefreshTokenForLogin(ctx, db, "jti-secret", "pk-user", 0, LoginSessionWithPasskey, "SECRET", time.Now().Add(time.Hour), meta); !errors.Is(err, ErrLoginStateChanged) {
		t.Fatalf("totp-coupled err=%v", err)
	}
}

func TestPasskeyFlagsMigrationPreservesLegacyUnknownState(t *testing.T) {
	db := openAuthSecurityDB(t, "passkeys-flags-migration.db")
	if _, err := db.Exec(`ALTER TABLE passkeys DROP COLUMN authenticator_flags`); err != nil {
		t.Fatalf("make legacy schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('pk-legacy','legacy@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO passkeys(id,user_id,credential_id,public_key,sign_count,name,created_at,last_used_at)
		VALUES('pk-old','pk-legacy',X'0102',X'0304',0,'old device',1,0)`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy passkeys: %v", err)
	}
	row, err := GetPasskeyByCredentialID(context.Background(), db, []byte{0x01, 0x02})
	if err != nil {
		t.Fatal(err)
	}
	if row.FlagsKnown {
		t.Fatalf("legacy credential flags must remain unknown, got %+v", row)
	}
}

func TestPasskeyCascadeOnUserDeleteAndUniqueCredential(t *testing.T) {
	db := openAuthSecurityDB(t, "passkeys-cascade.db")
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('pk-c','c@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}
	cred := []byte{0xDE, 0xAD}
	if err := CreatePasskey(ctx, db, &Passkey{UserID: "pk-c", CredentialID: cred, PublicKey: []byte{0x01}}); err != nil {
		t.Fatal(err)
	}
	if err := CreatePasskey(ctx, db, &Passkey{UserID: "pk-c", CredentialID: cred, PublicKey: []byte{0x02}}); err == nil {
		t.Fatal("duplicate credential_id accepted")
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id='pk-c'`); err != nil {
		t.Fatal(err)
	}
	count, err := CountPasskeys(ctx, db, "pk-c")
	if err != nil || count != 0 {
		t.Fatalf("cascade failed count=%d err=%v", count, err)
	}
}

func TestRecordLoginHistoryAcceptsPasskeyMethod(t *testing.T) {
	db := openAuthSecurityDB(t, "passkeys-history.db")
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('pk-h','h@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}
	row, err := RecordLoginHistory(context.Background(), db, "pk-h", LoginMethodPasskey, SessionMeta{})
	if err != nil || row.Method != LoginMethodPasskey {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}
