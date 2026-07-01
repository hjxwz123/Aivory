package api

import (
	"errors"
	"net/http"
	"strings"

	"aurelia/server/internal/store"
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
	writeJSON(w, 200, rows)
}

func listUserGroupsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	listUserGroupsPublic(d, w, r)
}

func createUserGroupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var g store.UserGroup
	if err := decodeJSON(r, &g); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	if existing, err := store.GetUserGroupByName(r.Context(), d.DB, g.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrUserGroupNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	created, err := store.CreateUserGroup(r.Context(), d.DB, g)
	if err != nil {
		if errors.Is(err, store.ErrUserGroupNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
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
	upd, err := store.UpdateUserGroup(r.Context(), d.DB, id, p)
	if err != nil {
		if errors.Is(err, store.ErrUserGroupNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, upd)
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
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// setUserCreditsAdmin overwrites a user's permanent (non-expiring) credit balance
// (§ credits) — the admin edits it on the users page.
func setUserCreditsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req struct {
		CreditsPermanent float64 `json:"credits_permanent"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if req.CreditsPermanent < 0 {
		req.CreditsPermanent = 0
	}
	if _, err := store.FindUserByID(r.Context(), d.DB, id); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	if err := store.SetPermanentCredits(r.Context(), d.DB, id, req.CreditsPermanent); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "credits_permanent": req.CreditsPermanent})
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
