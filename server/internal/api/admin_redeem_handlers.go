// Admin handlers for redeem codes (§ redeem codes).
//
// Routes mounted under /api/admin/redeem-codes/* via requireAdmin. Admins can
// list, create (singles or bulk batches), patch (enabled flag / note /
// batch_name / expires_at / max_uses), and delete codes.
package api

import (
	"errors"
	"net/http"
	"strconv"

	"aurelia/server/internal/store"
)

// listRedeemCodesAdmin returns redeem codes newest-first.
// Query params: batch=<name>, status=unused|redeemed|disabled|expired,
// limit, offset.
func listRedeemCodesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, err := store.ListRedeemCodes(r.Context(), d.DB, store.RedeemCodeFilter{
		BatchName: q.Get("batch"),
		Status:    q.Get("status"),
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// createRedeemCodeAdmin creates either a single code or a batch.
// Body: { group_id, duration_days, max_uses, expires_at, note, batch_name,
//         quantity?: int (when >1 a batch is generated) }
func createRedeemCodeAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var body struct {
		GroupID      string `json:"group_id"`
		DurationDays int    `json:"duration_days"`
		MaxUses      int    `json:"max_uses"`
		ExpiresAt    int64  `json:"expires_at"`
		Note         string `json:"note"`
		BatchName    string `json:"batch_name"`
		Code         string `json:"code"`      // optional — supply your own
		Quantity     int    `json:"quantity"`  // when >1 → bulk
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if body.GroupID == "" {
		writeError(w, 400, errors.New("group_id required"))
		return
	}
	if body.DurationDays < 0 {
		writeError(w, 400, errors.New("duration_days must be 0 or greater"))
		return
	}
	if body.MaxUses < 0 {
		writeError(w, 400, errors.New("max_uses must be 0 or greater"))
		return
	}
	if body.Quantity < 0 || body.Quantity > 1000 {
		writeError(w, 400, errors.New("quantity must be between 1 and 1000"))
		return
	}

	tpl := store.RedeemCode{
		Code:         body.Code,
		GroupID:      body.GroupID,
		DurationDays: body.DurationDays,
		MaxUses:      body.MaxUses,
		ExpiresAt:    body.ExpiresAt,
		Note:         body.Note,
		BatchName:    body.BatchName,
		CreatedBy:    u.ID,
	}

	if body.Quantity > 1 {
		if tpl.Code != "" {
			writeError(w, 400, errors.New("cannot supply a fixed code when quantity > 1"))
			return
		}
		out, err := store.BulkGenerateRedeemCodes(r.Context(), d.DB, tpl, body.Quantity)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, out)
		return
	}

	created, err := store.CreateRedeemCode(r.Context(), d.DB, tpl)
	if err != nil {
		if errors.Is(err, store.ErrRedeemCodeExists) {
			writeError(w, 409, errors.New("code already exists — pick a different one"))
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, created)
}

// updateRedeemCodeAdmin patches a single code.
func updateRedeemCodeAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var p store.RedeemCodePatch
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	upd, err := store.UpdateRedeemCode(r.Context(), d.DB, id, p)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, upd)
}

// deleteRedeemCodeAdmin removes one code.
func deleteRedeemCodeAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DeleteRedeemCode(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// deleteRedeemBatchAdmin removes every code in a batch.
func deleteRedeemBatchAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	name := pathParam(r, "name")
	n, err := store.DeleteRedeemBatch(r.Context(), d.DB, name)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "removed": n})
}

// listRedeemCodeRedemptionsAdmin returns the audit trail for a single code.
func listRedeemCodeRedemptionsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	rows, err := store.ListRedemptionsForCode(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}
