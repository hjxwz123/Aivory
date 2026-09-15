package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func registerBackupPasskeyForTest(t *testing.T, d Deps, userID string) *softwareAuthenticator {
	t.Helper()
	a := newSoftwareAuthenticator(t)
	a.backupEligible = true
	a.backupState = true
	u := &PasskeyUser{ID: userID, Email: "admin@example.test", DisplayName: "Admin"}
	options, session, err := d.Passkeys.BeginRegistration(e2eOrigin, u)
	if err != nil {
		t.Fatal(err)
	}
	var creation struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &creation); err != nil {
		t.Fatal(err)
	}
	credential, err := d.Passkeys.FinishRegistration(e2eOrigin, u, session, a.registrationResponse(t, creation.PublicKey.Challenge))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePasskey(t.Context(), d.DB, &store.Passkey{
		UserID: userID, CredentialID: credential.CredentialID, PublicKey: credential.PublicKey,
		SignCount: credential.SignCount, AuthenticatorFlags: credential.AuthenticatorFlags, FlagsKnown: credential.FlagsKnown,
		Name: "Current device", CreatedAt: 123, LastUsedAt: 456,
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

func verifyBackupPasskeyLoginForTest(t *testing.T, d Deps, a *softwareAuthenticator, handle, userID string, status int) {
	t.Helper()
	beginReq := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/begin", nil)
	beginReq.Host = e2eRPID
	beginReq.Header.Set("Origin", e2eOrigin)
	beginRec := httptest.NewRecorder()
	passkeyLoginBeginHandler(d, beginRec, beginReq)
	if beginRec.Code != http.StatusOK {
		t.Fatalf("login begin: %d %s", beginRec.Code, beginRec.Body.String())
	}
	var begin passkeyLoginBeginResp
	if err := json.Unmarshal(beginRec.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	var options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(begin.Options, &options); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(passkeyLoginVerifyReq{Ticket: begin.Ticket, Response: a.assertionResponse(t, options.PublicKey.Challenge, handle)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/verify", bytes.NewReader(body))
	req.Host = e2eRPID
	req.Header.Set("Origin", e2eOrigin)
	rec := httptest.NewRecorder()
	passkeyLoginVerifyHandler(d, rec, req)
	if rec.Code != status {
		t.Fatalf("login verify: %d want %d body=%s", rec.Code, status, rec.Body.String())
	}
	if status == http.StatusOK {
		var response authResp
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.User == nil || response.User.ID != userID || response.User.Role != "admin" || response.AccessToken == "" {
			t.Fatalf("wrong login result: %s", rec.Body.String())
		}
	}
}

func passkeyBackupCell(value []byte) map[string]string {
	return map[string]string{"__b64__": base64.StdEncoding.EncodeToString(value)}
}

func TestFullBackupRestorePreservesAdminPasskeyLogin(t *testing.T) {
	for _, scenario := range []string{"same account id", "different account id", "id collision", "passkey only"} {
		t.Run(scenario, func(t *testing.T) {
			d := newAuthSecurityDeps(t, "backup-passkeys.db")
			d.Passkeys = NewPasskeyService("Aivory")
			d.Config.UploadDir = filepath.Join(t.TempDir(), "uploads")
			d.Config.ArtifactDir = filepath.Join(t.TempDir(), "artifacts")
			mustExec(t, d.DB, `INSERT INTO users(id,email,password_hash,role,status,password_set) VALUES('adm','admin@example.test','current-admin-hash','admin','active',1)`)
			if scenario == "passkey only" {
				mustExec(t, d.DB, `UPDATE users SET password_hash='',password_set=0 WHERE id='adm'`)
			}
			a := registerBackupPasskeyForTest(t, d, "adm")
			// Credentials registered by previous builds have no explicit user_handle.
			mustExec(t, d.DB, `UPDATE passkeys SET user_handle=NULL WHERE user_id='adm'`)
			verifyBackupPasskeyLoginForTest(t, d, a, "adm", "adm", http.StatusOK)
			before, err := store.GetPasskeyByCredentialID(t.Context(), d.DB, a.credential)
			if err != nil {
				t.Fatal(err)
			}
			chosenID := "imported"
			if scenario == "same account id" {
				chosenID = "adm"
			}
			archiveDevice := newSoftwareAuthenticator(t)
			rows := map[string][]map[string]any{
				"users":    {{"id": chosenID, "email": "admin@example.test", "password_hash": "archive-hash", "role": "user", "status": "active", "password_set": 1}, {"id": "ordinary", "email": "ordinary@example.test", "password_hash": "hash", "role": "user"}},
				"passkeys": {{"id": "archive-admin-key", "user_id": chosenID, "credential_id": passkeyBackupCell(archiveDevice.credential), "public_key": passkeyBackupCell(archiveDevice.coseKey(t))}, {"id": "ordinary-key", "user_id": "ordinary", "credential_id": passkeyBackupCell([]byte("ordinary-credential")), "public_key": passkeyBackupCell([]byte("ordinary-public-key"))}},
			}
			if scenario == "id collision" {
				rows["users"][0]["id"] = "adm"
				rows["users"][0]["email"] = "different@example.test"
				rows["passkeys"][0]["user_id"] = "adm"
			}
			zr, man := backupRowsArchiveForTest(t, rows)
			counts, err := restoreDatabase(t.Context(), d, zr, man, "adm")
			if err != nil {
				t.Fatal(err)
			}
			if err := d.DB.QueryRow(`SELECT id FROM users WHERE email='admin@example.test'`).Scan(&chosenID); err != nil {
				t.Fatal(err)
			}
			after, err := store.GetPasskeyByCredentialID(t.Context(), d.DB, a.credential)
			if err != nil {
				t.Fatal(err)
			}
			if after.ID != before.ID || after.UserID != chosenID || string(after.UserHandle) != "adm" || !bytes.Equal(after.PublicKey, before.PublicKey) || after.SignCount != before.SignCount || after.AuthenticatorFlags != before.AuthenticatorFlags || after.FlagsKnown != before.FlagsKnown || after.Name != before.Name || after.CreatedAt != before.CreatedAt || after.LastUsedAt != before.LastUsedAt {
				t.Fatal("current administrator credential metadata changed")
			}
			wantCount := int64(2)
			if scenario == "id collision" {
				wantCount = 3
			}
			if counts["passkeys"] != wantCount {
				t.Fatalf("passkey count=%d want %d", counts["passkeys"], wantCount)
			}
			if scenario != "id collision" {
				if _, err := store.GetPasskeyByCredentialID(t.Context(), d.DB, archiveDevice.credential); !errors.Is(err, store.ErrPasskeyNotFound) {
					t.Fatalf("archive admin credential retained: %v", err)
				}
				verifyBackupPasskeyLoginForTest(t, d, archiveDevice, chosenID, chosenID, http.StatusUnauthorized)
			}
			ordinary, err := store.GetPasskeyByCredentialID(t.Context(), d.DB, []byte("ordinary-credential"))
			if err != nil || ordinary.UserID != "ordinary" {
				t.Fatalf("ordinary credential changed: %+v %v", ordinary, err)
			}
			verifyBackupPasskeyLoginForTest(t, d, a, "adm", chosenID, http.StatusOK)
			if chosenID != "adm" {
				verifyBackupPasskeyLoginForTest(t, d, a, chosenID, chosenID, http.StatusUnauthorized)
			}
			if scenario == "different account id" {
				// New credentials use the new account id; another restore must keep
				// both devices' original handles rather than overwriting them.
				newDevice := registerBackupPasskeyForTest(t, d, chosenID)
				rows["users"][0]["id"] = "imported-again"
				delete(rows, "passkeys")
				zr, man = backupRowsArchiveForTest(t, rows)
				if _, err := restoreDatabase(t.Context(), d, zr, man, chosenID); err != nil {
					t.Fatal(err)
				}
				verifyBackupPasskeyLoginForTest(t, d, a, "adm", "imported-again", http.StatusOK)
				verifyBackupPasskeyLoginForTest(t, d, newDevice, chosenID, "imported-again", http.StatusOK)
				// The new handle column must survive a logical export/import too.
				rec := httptest.NewRecorder()
				exportBackupAdmin(d, rec, httptest.NewRequest(http.MethodGet, "/api/admin/backup/export?files=0&qdrant=false", nil))
				if rec.Code != http.StatusOK {
					t.Fatal(rec.Body.String())
				}
				zr = backupZipReaderForCompatibilityTest(t, rec.Body.Bytes())
				man, err = readBackupManifest(zr)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateBackupArchive(zr, man); err != nil {
					t.Fatal(err)
				}
				if _, err := restoreDatabase(t.Context(), d, zr, man, ""); err != nil {
					t.Fatal(err)
				}
				verifyBackupPasskeyLoginForTest(t, d, a, "adm", "imported-again", http.StatusOK)
				verifyBackupPasskeyLoginForTest(t, d, newDevice, chosenID, "imported-again", http.StatusOK)
			}
		})
	}
}

func TestFullBackupRestoreRollsBackAdminPasskeyConflicts(t *testing.T) {
	for _, scenario := range []string{"row id collision", "credential collision", "passkey disabled"} {
		t.Run(scenario, func(t *testing.T) {
			d := newBackupAdminFixture(t, false)
			p := store.Passkey{ID: "current-key", UserID: "adm", CredentialID: []byte("current-credential"), PublicKey: []byte("current-public-key")}
			if err := store.CreatePasskey(t.Context(), d.DB, &p); err != nil {
				t.Fatal(err)
			}
			mustExec(t, d.DB, `INSERT INTO settings(key,value) VALUES('restore-sentinel','keep')`)
			rows := map[string][]map[string]any{
				"users":    {{"id": "imported", "email": "admin@example.test", "password_hash": "hash", "role": "user"}, {"id": "other", "email": "other@example.test", "password_hash": "hash", "role": "user"}},
				"passkeys": {{"id": "other-key", "user_id": "other", "credential_id": passkeyBackupCell([]byte("other-credential")), "public_key": passkeyBackupCell([]byte("other-public-key"))}},
			}
			switch scenario {
			case "row id collision":
				rows["passkeys"][0]["id"] = p.ID
			case "credential collision":
				rows["passkeys"][0]["credential_id"] = passkeyBackupCell(p.CredentialID)
			case "passkey disabled":
				mustExec(t, d.DB, `UPDATE users SET password_set=0,password_hash='' WHERE id='adm'`)
				rows["settings"] = []map[string]any{{"key": "passkey_login_enabled", "value": "false"}}
			}
			zr, man := backupRowsArchiveForTest(t, rows)
			if _, err := restoreDatabase(t.Context(), d, zr, man, "adm"); !errors.Is(err, errBackupImportAdminUnauthorized) {
				t.Fatalf("restore error: %v", err)
			}
			var marker string
			if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key='restore-sentinel'`).Scan(&marker); err != nil || marker != "keep" {
				t.Fatalf("rollback failed: %q %v", marker, err)
			}
			current, err := store.GetPasskeyByCredentialID(t.Context(), d.DB, p.CredentialID)
			if err != nil || current.UserID != "adm" {
				t.Fatalf("local credential lost: %+v %v", current, err)
			}
		})
	}
}
