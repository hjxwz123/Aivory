package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

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
	case "openai", "claude", "anthropic", "google", "gemini", "mock":
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

func updateModelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var m store.Model
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
	created, err := store.CreateSkill(r.Context(), d.DB, s)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, created)
}

func updateSkillAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var s store.Skill
	if err := decodeJSON(r, &s); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
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
	rows, err := store.ListUsers(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

func banUserAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.SetUserStatus(r.Context(), d.DB, id, "banned"); err != nil {
		writeError(w, 500, err)
		return
	}
	d.Cache.Publish("user:"+id+":kill", "1")
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

// listUserConversationsAdmin returns one user's conversations for support /
// abuse triage (§8.1). Ownership check is intentionally skipped because the
// admin scope already gates this surface in router.go.
func listUserConversationsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	userID := pathParam(r, "id")
	rows, err := store.ListConversations(r.Context(), d.DB, userID, "", "")
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

func usageReportAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			days = n
		}
	}
	rows, err := store.AdminUsageReport(r.Context(), d.DB, days)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	trend, _ := store.AdminUsageTrend(r.Context(), d.DB, days)
	writeJSON(w, 200, map[string]any{"days": days, "rows": rows, "trend": trend})
}

// ===== Settings =====

var settingsKeys = []string{
	"default_model_id", "task_model_id", "embedding_model_id",
	"keep_recent_rounds", "summary_max_tokens", "compaction_enabled",
	"compaction_token_trigger",
	"memory_enabled", "daily_message_limit", "daily_image_limit", "signup_open",
	"email_verification_required",
	"sandbox_base_url", "sandbox_api_key",
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
	// §4.11-C MinerU document parsing. Cloud API at https://mineru.net by
	// default; token comes from the user's MinerU console. When blank, the
	// fallback env vars (MINERU_API_URL/MINERU_API_KEY) are honoured, and if
	// both are unset binary uploads land as placeholder text.
	"mineru_api_url", "mineru_api_token",
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
	writeJSON(w, 200, out)
}

func adminSettingsSet(d Deps, w http.ResponseWriter, r *http.Request) {
	body := map[string]json.RawMessage{}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	for _, k := range settingsKeys {
		if v, ok := body[k]; ok {
			if err := store.SetSetting(d.DB, k, json.RawMessage(v)); err != nil {
				writeError(w, 500, err)
				return
			}
		}
	}
	adminSettingsGet(d, w, r)
}
