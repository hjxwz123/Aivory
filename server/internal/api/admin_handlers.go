package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aurelia/server/internal/store"
)

// ===== Channels =====

func listChannelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListChannels(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

type createChannelReq struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	APIFormat string `json:"api_format"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
}

func createChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var req createChannelReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if req.Name == "" || req.Type == "" {
		writeError(w, 400, errors.New("name and type required"))
		return
	}
	// api_format only applies to OpenAI channels — drop it for other types
	// instead of rejecting, so adding a Claude/Gemini channel never errors on a
	// default value carried over from the form (§2.3-B).
	if req.Type != "openai" {
		req.APIFormat = ""
	}
	if err := validateChannelType(req.Type, req.APIFormat); err != nil {
		writeError(w, 400, err)
		return
	}
	c, err := store.CreateChannel(r.Context(), d.DB, req.Name, req.Type, req.APIFormat, req.BaseURL, req.APIKey)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, c)
}

func reorderChannelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if err := store.ReorderChannels(r.Context(), d.DB, body.IDs); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func updateChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var p store.ChannelPatch
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	// Validate type/api_format coherence against the effective (post-patch)
	// values so a stale api_format can't be orphaned (§2.3-B).
	existing, err := store.GetChannel(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	effType := existing.Type
	if p.Type != nil {
		effType = *p.Type
	}
	effFmt := existing.APIFormat
	if p.APIFormat != nil {
		effFmt = *p.APIFormat
	}
	// Non-OpenAI channels don't use api_format — force it empty rather than
	// rejecting a stale value carried over from the form (§2.3-B).
	if effType != "openai" {
		effFmt = ""
		empty := ""
		p.APIFormat = &empty
	}
	if err := validateChannelType(effType, effFmt); err != nil {
		writeError(w, 400, err)
		return
	}
	c, err := store.UpdateChannel(r.Context(), d.DB, id, p)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, c)
}

// validateChannelType enforces the §2.3-B rule: api_format only applies to
// OpenAI channels (chat | responses); other channel types must leave it empty.
func validateChannelType(typ, apiFormat string) error {
	switch typ {
	case "openai", "claude", "anthropic", "google", "gemini":
	default:
		return errors.New("invalid channel type")
	}
	if typ == "openai" {
		switch apiFormat {
		case "", "chat", "responses":
		default:
			return errors.New("openai api_format must be 'chat' or 'responses'")
		}
	} else if apiFormat != "" {
		return errors.New("api_format only applies to openai channels")
	}
	return nil
}

func deleteChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DeleteChannel(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ===== Models =====

func listModelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	rows, err := store.ListModels(r.Context(), d.DB, kind, false)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Populate model_skills bindings (§4.17) so the model editor can show which
	// skills are currently checked. Admin model lists are small, so the per-row
	// query is cheap; a SkillsForModel failure just leaves that row's skills empty.
	for i := range rows {
		if rows[i].Kind != "chat" {
			continue
		}
		if ids, err := store.SkillsForModel(r.Context(), d.DB, rows[i].ID); err == nil {
			rows[i].Skills = ids
		}
	}
	writeJSON(w, 200, rows)
}

func createModelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var m store.Model
	if err := decodeJSON(r, &m); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if m.ChannelID == "" || m.RequestID == "" || m.Label == "" {
		writeError(w, 400, errors.New("channel_id, request_id, label required"))
		return
	}
	m.Enabled = true
	created, err := store.CreateModel(r.Context(), d.DB, m)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, created)
}

// reorderModelsAdmin persists a new model order in one shot: the body is the
// full list of model ids in the desired order, and each row's sort_order is set
// to its position. One request keeps drag-reordering smooth (no per-swap churn).
func reorderModelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if err := store.ReorderModels(r.Context(), d.DB, body.IDs); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func updateModelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	// Load the existing row and decode the request body OVER it, so a PARTIAL
	// payload (e.g. the inline {"enabled":true} visibility toggle) only changes
	// the fields it sends and leaves channel_id/label/prices/etc. intact. A full
	// edit-form payload still overrides everything (channel changes included).
	existing, err := store.GetModel(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	m := *existing
	if err := decodeJSON(r, &m); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	upd, err := store.UpdateModel(r.Context(), d.DB, id, m)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, upd)
}

func deleteModelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DeleteModel(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type modelSkillsReq struct {
	SkillIDs []string `json:"skill_ids"`
}

func setModelSkillsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req modelSkillsReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if err := store.SetSkillsForModel(r.Context(), d.DB, id, req.SkillIDs); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ===== Skills =====

func listSkillsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListSkills(r.Context(), d.DB, false)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

func createSkillAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var s store.Skill
	if err := decodeJSON(r, &s); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if s.Name == "" || s.Description == "" || s.Instructions == "" {
		writeError(w, 400, errors.New("name, description, instructions required"))
		return
	}
	s.Enabled = true
	normAssets, err := validateSkillAssets(d, s.Assets)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	s.Assets = normAssets
	created, err := store.CreateSkill(r.Context(), d.DB, s)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, created)
}

func updateSkillAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	// Load the existing row and decode the request body OVER it, so a PARTIAL
	// payload (e.g. just {"enabled":false}) only changes the fields it sends and
	// leaves name / instructions / assets intact (mirrors updateModelAdmin).
	existing, err := store.GetSkill(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	s := *existing
	if err := decodeJSON(r, &s); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	normAssets, err := validateSkillAssets(d, s.Assets)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	s.Assets = normAssets
	upd, err := store.UpdateSkill(r.Context(), d.DB, id, s)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, upd)
}

func deleteSkillAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DeleteSkill(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ===== Users =====

func listUsersAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	total, err := store.CountUsersBySearch(r.Context(), d.DB, search)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	rows, err := store.ListUsersBySearch(r.Context(), d.DB, search, limit, offset)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"users": rows, "total": total, "limit": limit, "offset": offset})
}

func reorderUsersAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if err := store.ReorderUsers(r.Context(), d.DB, body.IDs); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func banUserAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	// §D2: never ban yourself or the last active admin — both lock the platform out.
	if u := authUser(r); u != nil && u.ID == id {
		writeError(w, 400, errors.New("you cannot ban your own account"))
		return
	}
	if target, terr := store.FindUserByID(r.Context(), d.DB, id); terr == nil && target.Role == "admin" {
		if n, _ := store.ActiveAdminCount(r.Context(), d.DB); n <= 1 {
			writeError(w, 400, errors.New("cannot ban the last remaining admin"))
			return
		}
	}
	if err := store.SetUserStatus(r.Context(), d.DB, id, "banned"); err != nil {
		writeError(w, 500, err)
		return
	}
	d.Cache.Publish("user:"+id+":kill", "1")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// deleteUserAdmin permanently removes a user and all their data (conversations,
// messages, memories, tokens, …). Same lockout guards as ban: never delete your
// own account or the last active admin.
func deleteUserAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if u := authUser(r); u != nil && u.ID == id {
		writeError(w, 400, errors.New("you cannot delete your own account"))
		return
	}
	if target, terr := store.FindUserByID(r.Context(), d.DB, id); terr == nil && target.Role == "admin" {
		if n, _ := store.ActiveAdminCount(r.Context(), d.DB); n <= 1 {
			writeError(w, 400, errors.New("cannot delete the last remaining admin"))
			return
		}
	}
	// Collect KB IDs before the SQL delete so we can clean up Qdrant vectors
	// after the transaction commits. The rows will be gone by then.
	kbs, _ := store.ListKBs(r.Context(), d.DB, id)

	if err := store.DeleteUser(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}

	// Best-effort Qdrant cleanup: delete vector data for every KB the user owned.
	// Runs after the SQL commit so a Qdrant failure never blocks account deletion.
	for _, kb := range kbs {
		if err := d.RAG.OnKBDeleted(r.Context(), kb.ID); err != nil {
			d.Logger.Printf("admin: delete user %s: drop vectors for kb %s: %v", id, kb.ID, err)
		}
	}

	d.Cache.Publish("user:"+id+":kill", "1") // drop any live sessions immediately
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func unbanUserAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.SetUserStatus(r.Context(), d.DB, id, "active"); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type createUserReq struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// createUserAdmin provisions an account directly (no signup flow, no email
// verification) with the chosen role. Mirrors the registration hashing path.
func createUserAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeError(w, 400, errors.New("valid email required"))
		return
	}
	if len(req.Password) < 8 {
		writeError(w, 400, errors.New("password must be at least 8 characters"))
		return
	}
	if req.Role != "admin" {
		req.Role = "user"
	}
	if u, _ := store.FindUserByEmail(r.Context(), d.DB, req.Email); u != nil {
		writeError(w, 409, errors.New("email already registered"))
		return
	}
	hash, err := store.HashPassword(req.Password)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	user, err := store.CreateUserWithRole(r.Context(), d.DB, req.Email, req.Name, hash, req.Role)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, user)
}

// setUserPasswordAdmin resets another user's password without the
// current-password check (admin authority). Bumps token version + drops live
// sessions so the user must re-authenticate with the new credential.
func setUserPasswordAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil || req.NewPassword == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, 400, errors.New("password must be at least 8 characters"))
		return
	}
	if _, err := store.FindUserByID(r.Context(), d.DB, id); err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	hash, err := store.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := store.UpdateUserPassword(r.Context(), d.DB, id, hash); err != nil {
		writeError(w, 500, err)
		return
	}
	d.Cache.Publish("user:"+id+":kill", "1")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// setUserRoleAdmin changes a user's role. An admin can't change their OWN role
// here (guards against self-lockout — use another admin account).
func setUserRoleAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if u := authUser(r); u != nil && u.ID == id {
		writeError(w, 400, errors.New("cannot change your own role"))
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if req.Role != "admin" && req.Role != "user" {
		writeError(w, 400, errors.New("role must be 'user' or 'admin'"))
		return
	}
	target, err := store.FindUserByID(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	// §D2: don't demote the last active admin to a regular user.
	if req.Role == "user" && target.Role == "admin" {
		if n, _ := store.ActiveAdminCount(r.Context(), d.DB); n <= 1 {
			writeError(w, 400, errors.New("cannot demote the last remaining admin"))
			return
		}
	}
	if err := store.SetUserRole(r.Context(), d.DB, id, req.Role); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// listUserConversationsAdmin returns one user's conversations for support /
// abuse triage (§8.1). Ownership check is intentionally skipped because the
// admin scope already gates this surface in router.go.
func listUserConversationsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	userID := pathParam(r, "id")
	rows, err := store.ListConversations(r.Context(), d.DB, userID, "", "", 500, 0)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// listUserProjectsAdmin / listUserKBsAdmin — read-only drill-down into a target
// user's projects and knowledge bases for support / triage (§8.1), no ownership
// filter (admin scope).
func listUserProjectsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListProjects(r.Context(), d.DB, pathParam(r, "id"))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

func listUserKBsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListKBs(r.Context(), d.DB, pathParam(r, "id"))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// listKBDocumentsAdmin lists the documents in a knowledge base (read-only, admin
// scope — no ownership filter), for the user-library drill-down.
func listKBDocumentsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListDocuments(r.Context(), d.DB, "kb", pathParam(r, "id"))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// getConversationAdmin returns one conversation by id, without the per-user
// ownership filter. The frontend pairs this with /messages to render the
// admin thread view.
func getConversationAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	conv, err := store.GetConversationByID(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, conv)
}

// deleteConversationAdmin removes any user's conversation (support / cleanup).
// No ownership filter — the requireAdmin gate is the authority.
func deleteConversationAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	children, err := store.DeleteConversationByID(r.Context(), d.DB, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	// Drop RAG vectors for the conversation and every inline sub-conversation
	// removed alongside it.
	for _, cid := range append([]string{id}, children...) {
		if err := d.RAG.OnConversationDeleted(r.Context(), cid); err != nil {
			d.Logger.Printf("rag: drop vectors for conversation %s: %v", cid, err)
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// listConversationMessagesAdmin returns either the active path (default) or
// the full tree (?mode=tree) of one conversation, no ownership filter. Used
// by the admin Users drill-down to inspect a reported thread.
func listConversationMessagesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	conv, err := store.GetConversationByID(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "tree" {
		msgs, err := store.ListAllMessages(r.Context(), d.DB, id)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, enrichWithSiblings(d, r, msgs))
		return
	}
	msgs, err := store.ListMessages(r.Context(), d.DB, id, conv.ActiveLeafID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, enrichWithSiblings(d, r, msgs))
}

// ===== Usage report =====

// parseUsageQuery reads the shared usage filter + pagination from the query
// string: days (shortcut for a since-window) | start/end (unix) | user | model |
// page | page_size.
func parseUsageQuery(r *http.Request) (store.UsageFilter, int, int) {
	q := r.URL.Query()
	var f store.UsageFilter
	if s := q.Get("days"); s != "" {
		if days, err := strconv.Atoi(s); err == nil && days > 0 {
			f.Since = time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
		}
	}
	if s := q.Get("start"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			f.Since = v
		}
	}
	if s := q.Get("end"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			f.Until = v
		}
	}
	f.UserQ = strings.TrimSpace(q.Get("user"))
	f.ModelID = strings.TrimSpace(q.Get("model"))
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	return f, page, pageSize
}

// usageReportAdmin lists individual usage records (one per API call), filtered +
// paginated, with the matching total count and summed cost.
func usageReportAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	f, page, pageSize := parseUsageQuery(r)
	records, err := store.AdminUsageRecords(r.Context(), d.DB, f, pageSize, (page-1)*pageSize)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	total, totalCost, _ := store.AdminUsageCount(r.Context(), d.DB, f)
	writeJSON(w, 200, map[string]any{
		"records":    records,
		"total":      total,
		"total_cost": totalCost,
		"page":       page,
		"page_size":  pageSize,
	})
}

// usageDeleteOneAdmin deletes a single usage record by id.
func usageDeleteOneAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(pathParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if derr := store.DeleteUsageRecord(r.Context(), d.DB, id); derr != nil {
		if errors.Is(derr, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, derr)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// usageDeleteFilteredAdmin deletes every usage record matching the filter
// (the same filter the admin is viewing) and returns how many were removed.
func usageDeleteFilteredAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	f, _, _ := parseUsageQuery(r)
	n, err := store.DeleteUsageByFilter(r.Context(), d.DB, f)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// analyticsAdmin powers the admin Analytics dashboard: the overall trend plus
// per-model and per-user breakdowns and their time series (top keys only, so the
// payload stays bounded).
func analyticsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	ctx := r.Context()
	totals, err := store.AdminUsageTotals(ctx, d.DB, days)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	trend, _ := store.AdminUsageTrend(ctx, d.DB, days)
	byModel, _ := store.AdminUsageBreakdown(ctx, d.DB, days, "model_id", 8)
	byUser, _ := store.AdminUsageBreakdown(ctx, d.DB, days, "user_id", 8)
	modelSeries, _ := store.AdminUsageSeries(ctx, d.DB, days, "model_id", breakdownKeys(byModel))
	userSeries, _ := store.AdminUsageSeries(ctx, d.DB, days, "user_id", breakdownKeys(byUser))
	writeJSON(w, 200, map[string]any{
		"days":         days,
		"bucket":       store.UsageBucketWidth(days),
		"totals":       totals,
		"trend":        trend,
		"by_model":     byModel,
		"by_user":      byUser,
		"model_series": modelSeries,
		"user_series":  userSeries,
	})
}

func breakdownKeys(rows []store.UsageBreakdownRow) []string {
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	return keys
}

// ===== Settings =====

var settingsKeys = []string{
	"default_model_id", "task_model_id", "embedding_model_id",
	"keep_recent_rounds", "summary_max_tokens", "compaction_enabled",
	"compaction_token_trigger",
	"memory_enabled", "daily_message_limit", "daily_image_limit", "signup_open",
	"email_verification_required", "daily_token_limit", "max_concurrent_generations",
	// Anti-abuse registration controls. register_ip_daily_limit: max accounts one
	// IP may create per day (0 = off). register_captcha_required: gate signup
	// behind the slider-puzzle captcha.
	"register_ip_daily_limit", "register_captcha_required",
	// §credits: global USD→credit conversion rate (1 USD = N credits) and the two
	// shared purchase links (tier upgrade + permanent-credit top-up). Per-group
	// credit fields keep only allowance + refresh period.
	"credits_per_usd", "group_buy_url", "credit_buy_url",
	// §B6 partial: JSON array of tool names disabled platform-wide (kill-switch),
	// e.g. ["python_execute","image_generate"].
	"disabled_tools",
	"sandbox_base_url", "sandbox_api_key",
	// §4.5 per-exec wall-clock cap in SECONDS (admin-tunable). Blank/0 = default
	// 120s. Clamped to [10,600] server-side and to the sidecar's hard ceiling.
	"sandbox_exec_timeout_sec",
	// §4.5 storage backend: pick exactly one of s3 / aliyun_oss. When blank,
	// archive/restore is disabled and the sandbox still works (workspaces
	// reaped = gone). All credentials live in admin settings, plaintext,
	// consistent with the channel api_key policy.
	"storage_provider", // "" | "s3" | "aliyun_oss"
	"storage_prefix",   // shared key-prefix for archived workspaces
	"storage_s3_bucket", "storage_s3_region", "storage_s3_endpoint",
	"storage_s3_access_key", "storage_s3_secret_key",
	"storage_aliyun_bucket", "storage_aliyun_endpoint",
	"storage_aliyun_access_key_id", "storage_aliyun_access_key_secret",
	// §4.5 archived-workspace GC: age in DAYS after which a workspace tarball is
	// deleted from the bucket. "" / "0" = never auto-delete (archives accumulate).
	"storage_archive_ttl_days",
	// §4.11-C MinerU document parsing. Cloud API at https://mineru.net by
	// default; token comes from the user's MinerU console. When blank, the
	// fallback env vars (MINERU_API_URL/MINERU_API_KEY) are honoured, and if
	// both are unset binary uploads land as placeholder text.
	"mineru_api_url", "mineru_api_token",
	// §user-groups: the prompt shown when a model is locked for a user's group or
	// their windowed quota is exhausted.
	"quota_exceeded_message",
	// § upstream fallback: if the chosen model emits nothing within
	// fallback_ttft_sec (time-to-first-token), the stream is cut and the same
	// message is re-generated with fallback_model_id — transparently. Both blank
	// / 0 = disabled.
	"fallback_model_id", "fallback_ttft_sec",
	// § moderation: keyword blocklist (JSON array of strings), the dedicated
	// moderation model id (for model-mode), the violation categories the model
	// screens for (model-mode), and the message shown when a prompt is blocked.
	// Per-model toggle + mode live on the model row.
	"moderation_keywords", "moderation_model_id", "moderation_categories", "moderation_message",
	// § announcement: global notice config (enabled/body/image_url/remember_dismiss
	// /updated_at) shown to users on load. Edited via the admin announcement page.
	"announcement",
	// Voice transcription (whisper) — admin-configurable, live-reloaded per call.
	// base_url defaults to https://api.openai.com; model defaults to whisper-1.
	"audio_transcribe_base_url", "audio_transcribe_api_key", "audio_transcribe_model",
	// §4.4 web search backend — admin-configurable, live-reloaded each call.
	// Provider ∈ {"", "serper", "brave", "searxng", "auto"}. SearXNG is the
	// self-hosted option and only needs base_url (no api_key). Empty provider
	// falls back to the env values and finally to the no-op placeholder.
	"search_provider", "search_base_url", "search_api_key",
	// §4.6 upload safety — extension allowlist. Stored as a single
	// comma-separated string (e.g. "pdf,docx,txt,png,jpg"). Empty string means
	// "use the safe default allowlist" (see api.defaultUploadExtensions).
	// Enforced on /api/files and /api/kbs/:id/documents BEFORE bytes touch disk.
	"upload_allowed_extensions",
	// SMTP mail — live-reloaded on each send (see internal/mail).
	"smtp_host", "smtp_port", "smtp_user", "smtp_password",
	"smtp_from", "smtp_tls",
	"email_verification_required",
	"email_domain_whitelist",
	// §4.20 Image Generation: the TEXT model used to optimize/expand a user's
	// image prompt (and fold in the style's hidden prompt) before generation.
	// Blank = no optimization (deterministic join). Image MODELS are picked per
	// conversation from the model picker, so there's no default-image-model key.
	"image_prompt_model_id",
	// §verify: the secondary auditor model that fact-checks answers in Verify
	// mode. Blank = Verify mode off platform-wide.
	"verify_model_id",
	// §4.11-B RAG injection knobs (admin → Documents).
	"rag_full_text_threshold", "rag_top_k", "rag_dynamic_topk", "rag_similarity_threshold",
	// §credits pre-flight token/affordability check.
	"credit_preflight_enabled",
}

// sensitiveKeywords lists substrings that identify secret settings fields.
// Any settings key whose name contains one of these (case-insensitive) will
// have its non-empty string value replaced with the mask on GET responses.
var sensitiveKeywords = []string{"password", "secret", "api_key", "token", "key_secret", "key_id"}

// maskSensitiveSettings replaces non-empty string values for sensitive keys
// with the display mask so credentials are never returned in plaintext (H-1).
func maskSensitiveSettings(out map[string]json.RawMessage) map[string]json.RawMessage {
	const mask = `"••••••"`
	for k, v := range out {
		kl := strings.ToLower(k)
		for _, kw := range sensitiveKeywords {
			if strings.Contains(kl, kw) {
				// Only mask non-null, non-empty-string values.
				var s string
				if json.Unmarshal(v, &s) == nil && s != "" {
					out[k] = json.RawMessage(mask)
				}
				break
			}
		}
	}
	return out
}

func adminSettingsGet(d Deps, w http.ResponseWriter, _ *http.Request) {
	out := map[string]json.RawMessage{}
	for _, k := range settingsKeys {
		if raw, err := store.GetSetting(d.DB, k); err == nil {
			out[k] = raw
		} else {
			out[k] = json.RawMessage("null")
		}
	}
	writeJSON(w, 200, maskSensitiveSettings(out))
}

func adminSettingsSet(d Deps, w http.ResponseWriter, r *http.Request) {
	body := map[string]json.RawMessage{}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	for _, k := range settingsKeys {
		if v, ok := body[k]; ok {
			// Skip writing back the display mask — treat it as "unchanged" (H-1).
			var s string
			if json.Unmarshal(v, &s) == nil && s == "••••••" {
				continue
			}
			// §4.7 compaction knobs must be non-negative integers — a negative
			// token_trigger inverts the early-exit guard and a zero/negative
			// summary length makes the tiered merge churn the cache every turn.
			switch k {
			case "keep_recent_rounds", "summary_max_tokens", "compaction_token_trigger":
				var n int
				if json.Unmarshal(v, &n) != nil || n < 0 {
					writeError(w, 400, errInvalidInput)
					return
				}
			}
			if err := store.SetSetting(d.DB, k, json.RawMessage(v)); err != nil {
				writeError(w, 500, err)
				return
			}
		}
	}
	broadcastConfigInvalidate(d) // §2.4: clear the settings cache on every instance
	adminSettingsGet(d, w, r)
}

// broadcastConfigInvalidate tells every instance (including this one, via the
// subscriber wired in main) to drop its cached config after an admin write
// (§2.4 Pub/Sub invalidation). SetSetting already clears the local key; this
// covers the multi-instance case + the channel/model object caches.
func broadcastConfigInvalidate(d Deps) {
	if d.Cache != nil {
		d.Cache.Publish("cfg:invalidate", "1")
	}
	store.InvalidateConfig()
}
