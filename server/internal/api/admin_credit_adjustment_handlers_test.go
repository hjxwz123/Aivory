package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestAdminCreditAdjustmentAndOneTimeUserNotification(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-credit-adjustment.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,credits_permanent,credits_permanent_micros)
		VALUES('admin','admin@example.test','Admin','hash','admin','active',0,0)`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,credits_permanent,credits_permanent_micros)
		VALUES('user','user@example.test','User','hash','user','active',5,5000000)`)

	c := cache.NewMemory()
	d := Deps{DB: db, Cache: c, Auth: authsvc.New("admin-credit-adjustment-secret-32-bytes", time.Hour, 24*time.Hour, c)}
	admin, err := store.FindUserByID(t.Context(), db, "admin")
	if err != nil {
		t.Fatalf("find admin: %v", err)
	}
	user, err := store.FindUserByID(t.Context(), db, "user")
	if err != nil {
		t.Fatalf("find user: %v", err)
	}
	adminToken := issueBoundTestAccessToken(t, db, d.Auth, admin)
	userToken := issueBoundTestAccessToken(t, db, d.Auth, user)
	c.Set("seen:"+admin.ID, "1", time.Minute)
	c.Set("seen:"+user.ID, "1", time.Minute)

	mx := newMux()
	mx.handle(http.MethodPost, "/api/admin/users/:id/credits", requireAdmin(d, adjustUserCreditsAdmin))
	mx.handle(http.MethodPost, "/api/me/credit-adjustments/claim", requireAuth(d, claimCreditAdjustmentNotificationHandler))
	post := func(path, token string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		return rec
	}

	t.Run("add and notify", func(t *testing.T) {
		rec := post("/api/admin/users/user/credits", adminToken, map[string]any{
			"operation": "add", "amount": 2.25, "notify_user": true, "reason": "Activity reward",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Operation        string  `json:"operation"`
			Amount           float64 `json:"amount"`
			Delta            float64 `json:"delta"`
			CreditsPermanent float64 `json:"credits_permanent"`
			NotificationID   string  `json:"notification_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Operation != "add" || body.Amount != 2.25 || body.Delta != 2.25 || body.CreditsPermanent != 7.25 || body.NotificationID == "" {
			t.Fatalf("response = %+v", body)
		}
	})

	t.Run("user claims only once", func(t *testing.T) {
		rec := post("/api/me/credit-adjustments/claim", userToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("first claim status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var first struct {
			Notification *store.CreditAdjustmentNotification `json:"notification"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
			t.Fatalf("decode first claim: %v", err)
		}
		if first.Notification == nil || first.Notification.Direction != "add" || first.Notification.Amount != 2.25 || first.Notification.Reason != "Activity reward" {
			t.Fatalf("first notification = %+v", first.Notification)
		}

		rec = post("/api/me/credit-adjustments/claim", userToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("second claim status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var second struct {
			Notification *store.CreditAdjustmentNotification `json:"notification"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
			t.Fatalf("decode second claim: %v", err)
		}
		if second.Notification != nil {
			t.Fatalf("second notification = %+v, want nil", second.Notification)
		}
	})

	t.Run("remove", func(t *testing.T) {
		rec := post("/api/admin/users/user/credits", adminToken, map[string]any{
			"operation": "remove", "amount": 3, "notify_user": false, "reason": "",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if got, err := store.PermanentCredits(t.Context(), db, "user"); err != nil || got != 4.25 {
			t.Fatalf("permanent credits = %v, err=%v; want 4.25", got, err)
		}
	})

	t.Run("reject legacy overwrite request", func(t *testing.T) {
		rec := post("/api/admin/users/user/credits", adminToken, map[string]any{"credits_permanent": 99})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})

	t.Run("require reason when notifying", func(t *testing.T) {
		rec := post("/api/admin/users/user/credits", adminToken, map[string]any{
			"operation": "add", "amount": 1, "notify_user": true, "reason": "   ",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})

	t.Run("reject removal above permanent balance", func(t *testing.T) {
		rec := post("/api/admin/users/user/credits", adminToken, map[string]any{
			"operation": "remove", "amount": 5, "notify_user": false, "reason": "",
		})
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
		}
		if got, err := store.PermanentCredits(t.Context(), db, "user"); err != nil || got != 4.25 {
			t.Fatalf("permanent credits after rejection = %v, err=%v; want 4.25", got, err)
		}
	})

	t.Run("regular user cannot adjust credits", func(t *testing.T) {
		rec := post("/api/admin/users/user/credits", userToken, map[string]any{
			"operation": "add", "amount": 1, "notify_user": false, "reason": "",
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})
}
