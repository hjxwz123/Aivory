package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestUserGroupHandlersRejectNegativeSettlementPrice(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "negative-group-price.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO user_groups(id,name,monthly_price_amount_minor,yearly_price_amount_minor,is_public) VALUES('ug_existing','Existing',500,5000,1)`)
	d := Deps{DB: db}

	for _, field := range []string{"monthly_price_amount_minor", "yearly_price_amount_minor"} {
		t.Run("create_"+field, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/admin/user-groups", strings.NewReader(`{"name":"Negative","`+field+`":-1}`))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			createUserGroupAdmin(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			var count int
			mustQuery(t, db, `SELECT COUNT(*) FROM user_groups WHERE name='Negative'`).Scan(&count)
			if count != 0 {
				t.Fatalf("negative-priced group was created")
			}
		})

		t.Run("update_"+field, func(t *testing.T) {
			mx := newMux()
			mx.handle(http.MethodPatch, "/api/admin/user-groups/:id", wrap(d, updateUserGroupAdmin))
			req := httptest.NewRequest(http.MethodPatch, "/api/admin/user-groups/ug_existing", strings.NewReader(`{"`+field+`":-1}`))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			mx.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			var monthly, yearly int64
			mustQuery(t, db, `SELECT monthly_price_amount_minor, yearly_price_amount_minor FROM user_groups WHERE id='ug_existing'`).Scan(&monthly, &yearly)
			if monthly != 500 || yearly != 5000 {
				t.Fatalf("stored prices = %d/%d, want unchanged 500/5000", monthly, yearly)
			}
		})
	}
}

func TestUserGroupPurchaseAvailabilityIsPersistedAndPublic(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "group-purchase-availability.db"))
	defer db.Close()
	d := Deps{DB: db}

	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/user-groups", strings.NewReader(`{
		"name":"Waitlist", "is_public":true, "is_purchasable":false,
		"monthly_price_amount_minor":1200, "yearly_price_amount_minor":12000
	}`))
	createRec := httptest.NewRecorder()
	createUserGroupAdmin(d, createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d; body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	var created store.UserGroup
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created group: %v (%s)", err, createRec.Body.String())
	}
	if created.IsPurchasable {
		t.Fatalf("created group is_purchasable = true, want false")
	}
	var stored int
	if err := db.QueryRow(`SELECT is_purchasable FROM user_groups WHERE id=?`, created.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored purchase availability: %v", err)
	}
	if stored != 0 {
		t.Fatalf("stored is_purchasable = %d, want 0", stored)
	}

	publicRec := httptest.NewRecorder()
	listUserGroupsPublic(d, publicRec, httptest.NewRequest(http.MethodGet, "/api/public/user-groups", nil))
	if publicRec.Code != http.StatusOK {
		t.Fatalf("public list status = %d, want %d; body=%s", publicRec.Code, http.StatusOK, publicRec.Body.String())
	}
	var publicGroups []store.UserGroup
	if err := json.Unmarshal(publicRec.Body.Bytes(), &publicGroups); err != nil {
		t.Fatalf("decode public groups: %v (%s)", err, publicRec.Body.String())
	}
	if len(publicGroups) != 1 || publicGroups[0].ID != created.ID || publicGroups[0].IsPurchasable {
		t.Fatalf("public groups = %+v, want displayed non-purchasable group", publicGroups)
	}
	var publicRaw []map[string]json.RawMessage
	if err := json.Unmarshal(publicRec.Body.Bytes(), &publicRaw); err != nil {
		t.Fatalf("decode public group fields: %v", err)
	}
	if _, exposed := publicRaw[0]["permissions"]; exposed {
		t.Fatalf("public group exposed internal permission policy: %s", publicRec.Body.String())
	}

	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/user-groups/:id", wrap(d, updateUserGroupAdmin))
	updateReq := httptest.NewRequest(http.MethodPatch, "/api/admin/user-groups/"+created.ID, strings.NewReader(`{"is_purchasable":true}`))
	updateRec := httptest.NewRecorder()
	mx.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d; body=%s", updateRec.Code, http.StatusOK, updateRec.Body.String())
	}
	var updated store.UserGroup
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated group: %v (%s)", err, updateRec.Body.String())
	}
	if !updated.IsPurchasable {
		t.Fatalf("updated group is_purchasable = false, want true")
	}

	defaultReq := httptest.NewRequest(http.MethodPost, "/api/admin/user-groups", strings.NewReader(`{
		"name":"Default purchase availability", "is_public":true,
		"monthly_price_amount_minor":800, "yearly_price_amount_minor":8000
	}`))
	defaultRec := httptest.NewRecorder()
	createUserGroupAdmin(d, defaultRec, defaultReq)
	if defaultRec.Code != http.StatusCreated {
		t.Fatalf("default create status = %d, want %d; body=%s", defaultRec.Code, http.StatusCreated, defaultRec.Body.String())
	}
	var defaultGroup store.UserGroup
	if err := json.Unmarshal(defaultRec.Body.Bytes(), &defaultGroup); err != nil {
		t.Fatalf("decode default group: %v (%s)", err, defaultRec.Body.String())
	}
	if !defaultGroup.IsPurchasable {
		t.Fatalf("omitted is_purchasable defaulted to false, want true")
	}
}

func TestAdminSettingsRejectsInvalidSettlementCurrency(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "invalid-settlement-currency.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('settlement_currency','"EUR"')`)
	d := Deps{DB: db}

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"settlement_currency":"US1"}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var stored string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='settlement_currency'`).Scan(&stored)
	if stored != `"EUR"` {
		t.Fatalf("invalid patch changed settlement currency to %s", stored)
	}
}

func TestMeCreditsIncludesCurrencyAndFreshPermanentBalance(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "me-credits-pricing.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO user_groups(id,name,is_default,is_public,credit_allowance,credit_period_seconds) VALUES('ug_free','Free',1,1,100,86400)`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,group_id,credits_permanent) VALUES('u1','u1@example.test','hash','ug_free',42.5)`)
	for key, value := range map[string]any{
		"credits_per_usd":     100.0,
		"settlement_currency": "EUR",
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	// Deliberately stale auth-context balance: the endpoint must read the current
	// database value so an asynchronous payment grant is visible immediately.
	authUserSnapshot := &store.User{ID: "u1", GroupID: "ug_free", CreditsPermanent: 1}
	req := httptest.NewRequest(http.MethodGet, "/api/me/credits", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, authUserSnapshot))
	rec := httptest.NewRecorder()
	meCreditsHandler(Deps{DB: db}, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Enabled            bool    `json:"enabled"`
		Permanent          float64 `json:"permanent"`
		Available          float64 `json:"available"`
		SettlementCurrency string  `json:"settlement_currency"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode credits response: %v (%s)", err, rec.Body.String())
	}
	if !body.Enabled {
		t.Fatalf("credits enabled = false, want true")
	}
	if body.Permanent != 42.5 {
		t.Fatalf("permanent = %v, want fresh database balance 42.5", body.Permanent)
	}
	if body.Available != 142.5 {
		t.Fatalf("available = %v, want timed + permanent balance 142.5", body.Available)
	}
	if body.SettlementCurrency != "EUR" {
		t.Fatalf("settlement currency = %q, want EUR", body.SettlementCurrency)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"buy_url", "group_buy_url"} {
		if _, ok := raw[retired]; ok {
			t.Fatalf("credits response still exposes retired field %q: %s", retired, rec.Body.String())
		}
	}
}

func TestPublicUserGroupsAttachSettlementCurrencyWithUSDFallback(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "public-group-currency.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO user_groups(id,name,monthly_price_amount_minor,yearly_price_amount_minor,is_public) VALUES('ug_public','Public',1200,12000,1)`)
	d := Deps{DB: db}

	for _, tc := range []struct {
		name     string
		stored   *string
		expected string
	}{
		{name: "valid", stored: stringPtr("JPY"), expected: "JPY"},
		{name: "invalid", stored: stringPtr("US1"), expected: "USD"},
		{name: "missing", expected: "USD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustExec(t, db, `DELETE FROM settings WHERE key='settlement_currency'`)
			if tc.stored != nil {
				if err := store.SetSetting(db, "settlement_currency", *tc.stored); err != nil {
					t.Fatalf("set settlement currency: %v", err)
				}
			}
			store.InvalidateConfig()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/public/user-groups", nil)
			listUserGroupsPublic(d, rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var groups []store.UserGroup
			if err := json.Unmarshal(rec.Body.Bytes(), &groups); err != nil {
				t.Fatalf("decode groups: %v (%s)", err, rec.Body.String())
			}
			if len(groups) != 1 || groups[0].SettlementCurrency != tc.expected {
				t.Fatalf("groups = %+v, want one group with settlement_currency=%q", groups, tc.expected)
			}
		})
	}
}

func stringPtr(value string) *string { return &value }
