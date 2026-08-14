package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"aivory/server/internal/store"
)

// ===== User groups (membership tiers) =====

// publicUserGroupResponse excludes administrator-only capability policies and
// catalog allowlists from subscription and landing-page responses.
type publicUserGroupResponse struct {
	ID                      string          `json:"id"`
	Name                    string          `json:"name"`
	Description             string          `json:"description"`
	Features                json.RawMessage `json:"features"`
	MonthlyPriceAmountMinor int64           `json:"monthly_price_amount_minor"`
	YearlyPriceAmountMinor  int64           `json:"yearly_price_amount_minor"`
	SettlementCurrency      string          `json:"settlement_currency"`
	IsDefault               bool            `json:"is_default"`
	SortOrder               int             `json:"sort_order"`
	MaxProjects             int             `json:"max_projects"`
	MaxKBs                  int             `json:"max_kbs"`
	CreditAllowance         float64         `json:"credit_allowance"`
	CreditPeriodSeconds     int             `json:"credit_period_seconds"`
	MaxWorkspaces           int             `json:"max_workspaces"`
	MaxStorageMB            int             `json:"max_storage_mb"`
	IsPublic                bool            `json:"is_public"`
	IsPurchasable           bool            `json:"is_purchasable"`
	CreatedAt               int64           `json:"created_at"`
	UpdatedAt               int64           `json:"updated_at"`
}

func publicUserGroup(g store.UserGroup, currency string) publicUserGroupResponse {
	return publicUserGroupResponse{
		ID: g.ID, Name: g.Name, Description: g.Description, Features: g.Features,
		MonthlyPriceAmountMinor: g.MonthlyPriceAmountMinor,
		YearlyPriceAmountMinor:  g.YearlyPriceAmountMinor,
		SettlementCurrency:      currency,
		IsDefault:               g.IsDefault,
		SortOrder:               g.SortOrder,
		MaxProjects:             g.MaxProjects,
		MaxKBs:                  g.MaxKBs,
		CreditAllowance:         g.CreditAllowance,
		CreditPeriodSeconds:     g.CreditPeriodSeconds,
		MaxWorkspaces:           g.MaxWorkspaces,
		MaxStorageMB:            g.MaxStorageMB,
		IsPublic:                g.IsPublic,
		IsPurchasable:           g.IsPurchasable,
		CreatedAt:               g.CreatedAt,
		UpdatedAt:               g.UpdatedAt,
	}
}

// listUserGroupsPublic lists subscription metadata without exposing internal
// authorization policy.
func listUserGroupsPublic(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListUserGroups(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Public/subscription page lists only tiers the admin marked visible
	// (is_public). Admins keep the full list via listUserGroupsAdmin.
	currency := globalSettlementCurrency(d)
	visible := make([]publicUserGroupResponse, 0, len(rows))
	for _, g := range rows {
		if g.IsPublic {
			visible = append(visible, publicUserGroup(g, currency))
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
		IsPurchasable *bool            `json:"is_purchasable"`
		Permissions   *json.RawMessage `json:"permissions"`
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
	created, err := store.CreateUserGroupWithPermissions(r.Context(), d.DB, g, isPurchasable, purchase.Permissions)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCreditConfig) || errors.Is(err, store.ErrInvalidUserGroupPermissions) {
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
	upd, permissionsChanged, err := store.UpdateUserGroupWithPermissionChange(r.Context(), d.DB, id, p)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCreditConfig) || errors.Is(err, store.ErrInvalidUserGroupPermissions) {
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
	if permissionsChanged {
		revokeGroupPermissionSnapshots(d, id)
		publishGroupEvent(d, id, "account.permissions_updated")
	}
	upd.SettlementCurrency = globalSettlementCurrency(d)
	writeJSON(w, 200, upd)
}

func listUserGroupUsersAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	groupID := pathParam(r, "id")
	if _, err := store.GetUserGroup(r.Context(), d.DB, groupID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	query := r.URL.Query()
	search := strings.TrimSpace(query.Get("search"))
	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	if err := store.ExpireDueUserGroups(r.Context(), d.DB); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	total, err := store.CountUsersByGroup(r.Context(), d.DB, groupID, search)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	users, err := store.ListUsersByGroup(r.Context(), d.DB, groupID, search, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type groupUserSummary struct {
		ID     string `json:"id"`
		Email  string `json:"email"`
		Name   string `json:"name"`
		Role   string `json:"role"`
		Status string `json:"status"`
	}
	summaries := make([]groupUserSummary, 0, len(users))
	for _, user := range users {
		summaries = append(summaries, groupUserSummary{
			ID: user.ID, Email: user.Email, Name: user.Name, Role: user.Role, Status: user.Status,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users": summaries, "total": total, "limit": limit, "offset": offset,
	})
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
	// Existing streams are still registered under the deleted id. Fan out before
	// they reconnect so every affected account refreshes to the default group.
	revokeGroupPermissionSnapshots(d, id)
	publishGroupEvent(d, id, "account.permissions_updated")
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
	revokeUserPermissionSnapshots(d, id)
	publishUserEvent(d, nil, id, "account.permissions_updated", "")
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
