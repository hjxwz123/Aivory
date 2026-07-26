package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestCreditPackageHandlersCRUDFilteringAndCurrency(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "credit-package-handlers.db"))
	defer db.Close()
	if err := store.SetSetting(db, "settlement_currency", "JPY"); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db}

	for _, body := range []string{
		`{"name":"","credits":100,"price_amount_minor":100}`,
		`{"name":"No credits","credits":0,"price_amount_minor":100}`,
		`{"name":"Negative price","credits":100,"price_amount_minor":-1}`,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/credit-packages", strings.NewReader(body))
		createCreditPackageAdmin(d, rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("create %s status = %d, want 400; body=%s", body, rec.Code, rec.Body.String())
		}
	}

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/credit-packages", strings.NewReader(
		`{"name":"Starter","description":"Top up","credits":1000,"price_amount_minor":200,"sort_order":4}`))
	createCreditPackageAdmin(d, createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", createRec.Code, createRec.Body.String())
	}
	var created store.CreditPackage
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Enabled || created.SettlementCurrency != "JPY" || created.Credits != 1000 || created.PriceAmountMinor != 200 {
		t.Fatalf("created package = %+v", created)
	}

	mustExec(t, db, `INSERT INTO credit_packages(id,name,credits,price_amount_minor,enabled,sort_order) VALUES('cp_disabled','Disabled',1000,200,0,1)`)
	mustExec(t, db, `INSERT INTO credit_packages(id,name,credits,price_amount_minor,enabled,sort_order) VALUES('cp_free','Free draft',1000,0,1,2)`)
	mustExec(t, db, `INSERT INTO credit_packages(id,name,credits,price_amount_minor,enabled,sort_order) VALUES('cp_empty','Empty draft',0,200,1,3)`)

	publicRec := httptest.NewRecorder()
	listCreditPackagesPublic(d, publicRec, httptest.NewRequest(http.MethodGet, "/api/credit-packages", nil))
	if publicRec.Code != http.StatusOK {
		t.Fatalf("public list status = %d, body=%s", publicRec.Code, publicRec.Body.String())
	}
	var visible []store.CreditPackage
	if err := json.Unmarshal(publicRec.Body.Bytes(), &visible); err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].ID != created.ID || visible[0].SettlementCurrency != "JPY" {
		t.Fatalf("public packages = %+v, want only created package", visible)
	}

	adminRec := httptest.NewRecorder()
	listCreditPackagesAdmin(d, adminRec, httptest.NewRequest(http.MethodGet, "/api/admin/credit-packages", nil))
	var all []store.CreditPackage
	if err := json.Unmarshal(adminRec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("admin packages count = %d, want 4", len(all))
	}

	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/credit-packages/:id", wrap(d, updateCreditPackageAdmin))
	for _, body := range []string{`{"name":" "}`, `{"credits":0}`, `{"price_amount_minor":-1}`} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/credit-packages/"+created.ID, strings.NewReader(body))
		mx.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("update %s status = %d, want 400; body=%s", body, rec.Code, rec.Body.String())
		}
	}
	updateRec := httptest.NewRecorder()
	updateReq := httptest.NewRequest(http.MethodPatch, "/api/admin/credit-packages/"+created.ID, strings.NewReader(
		`{"name":"Starter Plus","credits":2500,"price_amount_minor":350,"enabled":false}`))
	mx.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updated store.CreditPackage
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Starter Plus" || updated.Credits != 2500 || updated.PriceAmountMinor != 350 || updated.Enabled {
		t.Fatalf("updated package = %+v", updated)
	}

	reorderRec := httptest.NewRecorder()
	reorderReq := httptest.NewRequest(http.MethodPatch, "/api/admin/credit-packages/reorder", strings.NewReader(
		`{"ids":["cp_empty","cp_free","cp_disabled","`+created.ID+`"]}`))
	reorderCreditPackagesAdmin(d, reorderRec, reorderReq)
	if reorderRec.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, body=%s", reorderRec.Code, reorderRec.Body.String())
	}

	deleteMux := newMux()
	deleteMux.handle(http.MethodDelete, "/api/admin/credit-packages/:id", wrap(d, deleteCreditPackageAdmin))
	deleteRec := httptest.NewRecorder()
	deleteMux.ServeHTTP(deleteRec, httptest.NewRequest(http.MethodDelete, "/api/admin/credit-packages/"+created.ID, nil))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", deleteRec.Code, deleteRec.Body.String())
	}
}
