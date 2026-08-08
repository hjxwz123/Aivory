package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

// ===== User groups (membership tiers) =====

// listUserGroupsPublic lists groups for any signed-in user (subscription page).
// Returns the same rows admins see — prices/features are public marketing info.
func listUserGroupsPublic(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListUserGroups(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Public/subscription page lists only tiers the admin marked visible
	// (is_public). Admins keep the full list via listUserGroupsAdmin.
	currency := globalSettlementCurrency(d)
	visible := make([]store.UserGroup, 0, len(rows))
	for _, g := range rows {
		if g.IsPublic {
			g.SettlementCurrency = currency
			visible = append(visible, g)
		}
	}
	writeJSON(w, 200, visible)
}

func listUserGroupsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListUserGroups(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	attachSettlementCurrency(rows, globalSettlementCurrency(d))
	writeJSON(w, 200, rows)
}

func createUserGroupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	var g store.UserGroup
	if err := json.Unmarshal(raw, &g); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	var purchase struct {
		IsPurchasable *bool `json:"is_purchasable"`
	}
	if err := json.Unmarshal(raw, &purchase); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	if g.MonthlyPriceAmountMinor < 0 || g.YearlyPriceAmountMinor < 0 {
		writeError(w, 400, errInvalidInput)
		return
	}
	if existing, err := store.GetUserGroupByName(r.Context(), d.DB, g.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrUserGroupNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	isPurchasable := true
	if purchase.IsPurchasable != nil {
		isPurchasable = *purchase.IsPurchasable
	}
	created, err := store.CreateUserGroupWithPurchaseAvailability(r.Context(), d.DB, g, isPurchasable)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCreditConfig) {
			writeError(w, 400, errInvalidInput)
			return
		}
		if errors.Is(err, store.ErrUserGroupNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	created.SettlementCurrency = globalSettlementCurrency(d)
	writeJSON(w, 201, created)
}

func reorderUserGroupsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if err := store.ReorderUserGroups(r.Context(), d.DB, body.IDs); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func updateUserGroupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var p store.UserGroupPatch
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		p.Name = &name
		if name == "" {
			writeError(w, 400, errors.New("name required"))
			return
		}
		if existing, err := store.GetUserGroupByName(r.Context(), d.DB, name); err == nil && existing != nil && existing.ID != id {
			writeError(w, 409, store.ErrUserGroupNameExists)
			return
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, 500, err)
			return
		}
	}
	if p.MonthlyPriceAmountMinor != nil && *p.MonthlyPriceAmountMinor < 0 {
		writeError(w, 400, errInvalidInput)
		return
	}
	if p.YearlyPriceAmountMinor != nil && *p.YearlyPriceAmountMinor < 0 {
		writeError(w, 400, errInvalidInput)
		return
	}
	upd, err := store.UpdateUserGroup(r.Context(), d.DB, id, p)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCreditConfig) {
			writeError(w, 400, errInvalidInput)
			return
		}
		if errors.Is(err, store.ErrUserGroupNameExists) {
			writeError(w, 409, err)
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	upd.SettlementCurrency = globalSettlementCurrency(d)
	writeJSON(w, 200, upd)
}

func attachSettlementCurrency(groups []store.UserGroup, currency string) {
	for i := range groups {
		groups[i].SettlementCurrency = currency
	}
}

func deleteUserGroupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DeleteUserGroup(r.Context(), d.DB, id); err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// setUserGroupAdmin assigns a user to a group (admin-assigned membership).
func setUserGroupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req struct {
		GroupID string `json:"group_id"`
		// Unix-seconds expiry; 0/absent = permanent (downgrades to default on expiry).
		GroupExpiresAt int64 `json:"group_expires_at"`
	}
	if err := decodeJSON(r, &req); err != nil || req.GroupID == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	if err := store.SetUserGroup(r.Context(), d.DB, id, req.GroupID, req.GroupExpiresAt); err != nil {
		writeError(w, 400, err)
		return
	}
	invalidateAuthUser(d, id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// adjustUserCreditsAdmin applies a positive or negative delta to only the
// user's permanent (non-expiring) credit balance.
func adjustUserCreditsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req struct {
		Operation  string  `json:"operation"`
		Amount     float64 `json:"amount"`
		NotifyUser bool    `json:"notify_user"`
		Reason     string  `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Operation = strings.ToLower(strings.TrimSpace(req.Operation))
	if (req.Operation != "add" && req.Operation != "remove") ||
		math.IsNaN(req.Amount) || math.IsInf(req.Amount, 0) || req.Amount <= 0 {
		writeError(w, 400, errInvalidInput)
		return
	}
	delta := req.Amount
	if req.Operation == "remove" {
		delta = -req.Amount
	}
	adjustment, err := store.AdjustPermanentCredits(
		r.Context(), d.DB, id, delta, req.NotifyUser, req.Reason,
	)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, 404, errNotFound)
		case errors.Is(err, store.ErrInsufficientPermanentCredits):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, store.ErrInvalidCreditAmount), errors.Is(err, store.ErrInvalidCreditNotification):
			writeError(w, 400, err)
		default:
			writeError(w, 500, err)
		}
		return
	}
	invalidateAuthUser(d, id)
	response := map[string]any{
		"ok":                true,
		"operation":         req.Operation,
		"amount":            math.Abs(adjustment.Delta),
		"delta":             adjustment.Delta,
		"credits_permanent": adjustment.After,
	}
	if adjustment.NotificationID != "" {
		response["notification_id"] = adjustment.NotificationID
	}
	writeJSON(w, 200, response)
}

// ===== Per-model group quotas =====

func listModelQuotasAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	rows, err := store.ListModelQuotas(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// setModelQuotasAdmin replaces all per-group quota rows for a model.
func setModelQuotasAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req struct {
		Quotas []store.ModelGroupQuota `json:"quotas"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	for i := range req.Quotas {
		req.Quotas[i].ModelID = id
	}
	if err := store.SetModelQuotas(r.Context(), d.DB, id, req.Quotas); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
