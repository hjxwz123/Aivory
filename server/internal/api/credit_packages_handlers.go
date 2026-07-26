package api

import (
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

func attachCreditPackageCurrency(packages []store.CreditPackage, currency string) {
	for i := range packages {
		packages[i].SettlementCurrency = currency
	}
}

func listCreditPackagesPublic(d Deps, w http.ResponseWriter, r *http.Request) {
	packages, err := store.ListPublicCreditPackages(r.Context(), d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	attachCreditPackageCurrency(packages, globalSettlementCurrency(d))
	writeJSON(w, http.StatusOK, packages)
}

func listCreditPackagesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	packages, err := store.ListCreditPackages(r.Context(), d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	attachCreditPackageCurrency(packages, globalSettlementCurrency(d))
	writeJSON(w, http.StatusOK, packages)
}

type createCreditPackageRequest struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	Credits          float64 `json:"credits"`
	PriceAmountMinor int64   `json:"price_amount_minor"`
	Enabled          *bool   `json:"enabled"`
	SortOrder        int     `json:"sort_order"`
}

func createCreditPackageAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var req createCreditPackageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || req.Credits <= 0 || req.PriceAmountMinor < 0 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p, err := store.CreateCreditPackage(r.Context(), d.DB, store.CreditPackage{
		Name:             req.Name,
		Description:      req.Description,
		Credits:          req.Credits,
		PriceAmountMinor: req.PriceAmountMinor,
		Enabled:          enabled,
		SortOrder:        req.SortOrder,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	p.SettlementCurrency = globalSettlementCurrency(d)
	writeJSON(w, http.StatusCreated, p)
}

func updateCreditPackageAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var patch store.CreditPackagePatch
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		patch.Name = &name
		if name == "" {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
	}
	if patch.Credits != nil && *patch.Credits <= 0 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if patch.PriceAmountMinor != nil && *patch.PriceAmountMinor < 0 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	p, err := store.UpdateCreditPackage(r.Context(), d.DB, id, patch)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	p.SettlementCurrency = globalSettlementCurrency(d)
	writeJSON(w, http.StatusOK, p)
}

func deleteCreditPackageAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	err := store.DeleteCreditPackage(r.Context(), d.DB, pathParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func reorderCreditPackagesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if err := store.ReorderCreditPackages(r.Context(), d.DB, body.IDs); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
