// User-facing redeem-code handler (§ redeem codes).
//
// Mounted at POST /api/me/redeem so the subscription page can submit a code
// from the signed-in user's session.
package api

import (
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

// redeemCodeHandler validates and applies a code on behalf of the
// authenticated user. Returns the new group + expiry so the UI can refresh.
//
// Error mapping (so the frontend can show specific messages):
//
//	400  empty                — `errInvalidInput`
//	404  invalid              — `code_invalid`
//	410  expired              — `code_expired`
//	409  used                 — `code_used`
//	409  already redeemed     — `code_already_owned`
//	403  disabled             — `code_disabled`
func redeemCodeHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var body struct {
		Code string `json:"code"`
		// Confirm acknowledges that the code's group differs from the user's
		// current one and will override it immediately (set by the UI's confirm
		// dialog). Without it, a group-switch is previewed, not applied.
		Confirm bool `json:"confirm"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Code) == "" {
		writeError(w, 400, errInvalidInput)
		return
	}

	red, updated, err := store.RedeemCodeForUser(r.Context(), d.DB, u.ID, body.Code, body.Confirm)
	if err != nil {
		// The code is valid but grants a DIFFERENT group — return a preview so the
		// UI can warn that redeeming overrides the current group immediately (not a
		// renewal) and re-submit with confirm=true. 200 (not an error) so the
		// client reads it without throwing.
		if errors.Is(err, store.ErrRedeemNeedsConfirm) {
			grantGroup, _ := store.GetUserGroup(r.Context(), d.DB, red.GroupID)
			curGroup, _ := store.GetUserGroup(r.Context(), d.DB, updated.GroupID)
			groupName, curName := "", ""
			if grantGroup != nil {
				groupName = grantGroup.Name
			}
			if curGroup != nil {
				curName = curGroup.Name
			}
			writeJSON(w, 200, map[string]any{
				"requires_confirmation": true,
				"group_id":              red.GroupID,
				"group_name":            groupName,
				"current_group_id":      updated.GroupID,
				"current_group_name":    curName,
				"expires_at":            red.ExpiresAt,
			})
			return
		}
		switch {
		case errors.Is(err, store.ErrRedeemCodeInvalid):
			writeJSON(w, 404, map[string]string{"error": "code_invalid"})
		case errors.Is(err, store.ErrRedeemCodeExpired):
			writeJSON(w, 410, map[string]string{"error": "code_expired"})
		case errors.Is(err, store.ErrRedeemCodeUsed):
			writeJSON(w, 409, map[string]string{"error": "code_used"})
		case errors.Is(err, store.ErrRedeemAlreadyOwned):
			writeJSON(w, 409, map[string]string{"error": "code_already_owned"})
		case errors.Is(err, store.ErrRedeemCodeDisabled):
			writeJSON(w, 403, map[string]string{"error": "code_disabled"})
		default:
			writeError(w, 500, err)
		}
		return
	}
	invalidateAuthUser(d, u.ID)

	// Resolve the group's display name so the success toast can say
	// "You're now on VIP" without a second request.
	group, _ := store.GetUserGroup(r.Context(), d.DB, red.GroupID)
	groupName := ""
	if group != nil {
		groupName = group.Name
	}
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"user":       updated,
		"group_id":   red.GroupID,
		"group_name": groupName,
		"expires_at": red.ExpiresAt,
	})
}
