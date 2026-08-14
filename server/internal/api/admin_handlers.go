package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// Env-overridable defaults (§ config-reference). Each falls back to the
// original hardcoded value when its AIVORY_* variable is unset.
var (
	adminUserListPageSizeCap          = envcfg.Int("AIVORY_API_ADMIN_USER_LIST_PAGE_SIZE_CAP", 50)
	adminCreatedUserMinPasswordLength = securityPasswordMinimum("AIVORY_API_ADMIN_CREATED_USER_MIN_PASSWORD_LENGTH", 8)
	adminPasswordResetMinLength       = securityPasswordMinimum("AIVORY_API_ADMIN_PASSWORD_RESET_MIN_LENGTH", 8)
	adminUserConversationsListingCap  = envcfg.Int("AIVORY_API_ADMIN_USER_CONVERSATIONS_LISTING_CAP", 500)
	usageReportPageSizeCap            = envcfg.Int("AIVORY_API_USAGE_REPORT_PAGE_SIZE_CAP", 50)
	analyticsWindow                   = envcfg.Int("AIVORY_API_ANALYTICS_WINDOW", 30)
	analyticsWindow2                  = envcfg.Int("AIVORY_API_ANALYTICS_WINDOW_2", 365)
)

const contextCompactionPromptMaxBytes = 16 * 1024

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
	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.Name == "" || req.Type == "" {
		writeError(w, 400, errors.New("name and type required"))
		return
	}
	if existing, err := store.GetChannelByName(r.Context(), d.DB, req.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrChannelNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
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
	if req.Type == "openai" {
		baseURL, err := normalizeOpenAIChannelBaseURL(req.BaseURL)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		req.BaseURL = baseURL
	}
	c, err := store.CreateChannel(r.Context(), d.DB, req.Name, req.Type, req.APIFormat, req.BaseURL, req.APIKey)
	if err != nil {
		if errors.Is(err, store.ErrChannelNameExists) {
			writeError(w, 409, err)
			return
		}
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
	effBaseURL := existing.BaseURL
	if p.BaseURL != nil {
		baseURL := strings.TrimSpace(*p.BaseURL)
		p.BaseURL = &baseURL
		effBaseURL = baseURL
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
	// Legacy OpenAI rows may still contain a host-only URL. Keep unrelated
	// partial updates working, but require the version root whenever the channel
	// type or base URL is being configured.
	if effType == "openai" && (p.Type != nil || p.BaseURL != nil) {
		baseURL, err := normalizeOpenAIChannelBaseURL(effBaseURL)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		if p.BaseURL != nil || baseURL != existing.BaseURL {
			p.BaseURL = &baseURL
		}
	}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		p.Name = &name
		if name == "" {
			writeError(w, 400, errors.New("name required"))
			return
		}
		if existing, err := store.GetChannelByName(r.Context(), d.DB, name); err == nil && existing != nil && existing.ID != id {
			writeError(w, 409, store.ErrChannelNameExists)
			return
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, 500, err)
			return
		}
	}
	c, err := store.UpdateChannel(r.Context(), d.DB, id, p)
	if err != nil {
		if errors.Is(err, store.ErrChannelNameExists) {
			writeError(w, 409, err)
			return
		}
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

var errOpenAIBaseURLV1Required = errors.New("openai base_url must be an absolute HTTP(S) URL ending in /v1")

// normalizeOpenAIChannelBaseURL keeps the documented empty-value default, but
// requires configured URLs to stop at the version root. Provider code appends
// resource paths such as /chat/completions and /images/generations.
func normalizeOpenAIChannelBaseURL(raw string) (string, error) {
	baseURL := strings.TrimSpace(raw)
	if baseURL == "" {
		return "", nil
	}
	if strings.ContainsAny(baseURL, " \t\r\n") {
		return "", errOpenAIBaseURLV1Required
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errOpenAIBaseURLV1Required
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	if !strings.HasSuffix(parsed.Path, "/v1") {
		return "", errOpenAIBaseURLV1Required
	}
	return parsed.String(), nil
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

type createModelReq struct {
	store.Model
	ResearchEnabled *bool `json:"research_enabled"`
}

var errModelExtraParamsChatOnly = errors.New("extra_params are only supported for chat models")

func normalizeModelExtraParams(m *store.Model) error {
	extraParams, err := store.NormalizeModelExtraParams(m.ExtraParams)
	if err != nil {
		return err
	}
	kind := m.Kind
	if kind == "" {
		kind = "chat"
	}
	if kind != "chat" && string(extraParams) != "{}" {
		return errModelExtraParamsChatOnly
	}
	m.ExtraParams = extraParams
	return nil
}

// decodeModelPatch preserves updateModelAdmin's decode-over-existing-row
// behavior while also reporting whether the client explicitly sent
// extra_params. That distinction matters when a chat model becomes image or
// embedding: an omitted field should not leave inherited chat-only values
// behind, whereas an explicit non-empty value remains a validation error.
func decodeCreateModelReq(r *http.Request, req *createModelReq) (officialToolsProvided bool, err error) {
	var raw json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, req); err != nil {
		return false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false, err
	}
	_, officialToolsProvided = fields["official_tools"]
	return officialToolsProvided, nil
}

func decodeModelPatch(r *http.Request, m *store.Model) (extraParamsProvided, officialToolsProvided bool, err error) {
	var raw json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		return false, false, err
	}
	if len(raw) == 0 {
		return false, false, nil
	}
	if err := json.Unmarshal(raw, m); err != nil {
		return false, false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false, false, err
	}
	_, extraParamsProvided = fields["extra_params"]
	_, officialToolsProvided = fields["official_tools"]
	return extraParamsProvided, officialToolsProvided, nil
}

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
	var req createModelReq
	officialToolsProvided, err := decodeCreateModelReq(r, &req)
	if err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	m := req.Model
	if req.ResearchEnabled != nil {
		m.ResearchEnabled = *req.ResearchEnabled
		m.ResearchEnabledSet = true
	}
	m.RequestID = strings.TrimSpace(m.RequestID)
	m.Label = strings.TrimSpace(m.Label)
	if m.ChannelID == "" || m.RequestID == "" || m.Label == "" {
		writeError(w, 400, errors.New("channel_id, request_id, label required"))
		return
	}
	if !officialToolsProvided && (m.Kind == "" || m.Kind == "chat") {
		if channel, err := store.GetChannel(r.Context(), d.DB, m.ChannelID); err == nil && channel.Type == "openai" && channel.APIFormat == "responses" {
			m.OfficialTools = store.DefaultOpenAIResponsesOfficialToolsJSON()
		}
	}
	if officialTools, err := store.NormalizeOfficialTools(m.OfficialTools); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else {
		m.OfficialTools = officialTools
	}
	if builtinTools, err := store.NormalizeBuiltinTools(m.BuiltinTools); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else {
		m.BuiltinTools = builtinTools
	}
	if err := normalizeModelExtraParams(&m); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if existing, err := store.GetModelByChannelRequestID(r.Context(), d.DB, m.ChannelID, m.RequestID); err == nil && existing != nil {
		writeError(w, 409, store.ErrModelRequestExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	m.Enabled = true
	created, err := store.CreateModel(r.Context(), d.DB, m)
	if err != nil {
		if errors.Is(err, store.ErrModelRequestExists) {
			writeError(w, 409, err)
			return
		}
		if errors.Is(err, store.ErrInvalidModelBilling) {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
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
	extraParamsProvided, officialToolsProvided, err := decodeModelPatch(r, &m)
	if err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if m.Kind != "chat" && !extraParamsProvided {
		m.ExtraParams = json.RawMessage("{}")
	}
	m.RequestID = strings.TrimSpace(m.RequestID)
	m.Label = strings.TrimSpace(m.Label)
	if m.ChannelID == "" || m.RequestID == "" || m.Label == "" {
		writeError(w, 400, errors.New("channel_id, request_id, label required"))
		return
	}
	// Omitted official_tools preserves the existing model value. An explicit
	// value, including [], is validated and normalized to the canonical object
	// array; legacy string arrays are accepted and upgraded.
	if officialToolsProvided {
		if officialTools, err := store.NormalizeOfficialTools(m.OfficialTools); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		} else {
			m.OfficialTools = officialTools
		}
	}
	// The row was decoded over the existing model, so omission preserves the
	// policy while an explicit null resets it to the default-all behavior.
	if builtinTools, err := store.NormalizeBuiltinTools(m.BuiltinTools); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else {
		m.BuiltinTools = builtinTools
	}
	if err := normalizeModelExtraParams(&m); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if existing, err := store.GetModelByChannelRequestID(r.Context(), d.DB, m.ChannelID, m.RequestID); err == nil && existing != nil && existing.ID != id {
		writeError(w, 409, store.ErrModelRequestExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	if err := ensureLockedEmbeddingModelCanUpdate(d, *existing, m); err != nil {
		if errors.Is(err, errEmbeddingModelLocked) {
			writeError(w, http.StatusConflict, errEmbeddingModelLocked)
			return
		}
		writeError(w, 500, err)
		return
	}
	// §fast-mode: the fast model can never have Deep Research enabled — force it
	// off no matter what the edit payload says. The admin UI also disables the
	// toggle for the fast model; this is the server-side guard. (UpdateModel does
	// not write the `fast` column, so a plain edit can't set/clear fast — that
	// only happens through PUT .../fast → SetFastModel.)
	if existing.Fast {
		m.ResearchEnabled = false
	}
	upd, err := store.UpdateModel(r.Context(), d.DB, id, m)
	if err != nil {
		if errors.Is(err, store.ErrModelRequestExists) {
			writeError(w, 409, err)
			return
		}
		if errors.Is(err, store.ErrInvalidModelBilling) {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, upd)
}

func deleteModelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := ensureLockedEmbeddingModelCanDelete(d, id); err != nil {
		if errors.Is(err, errEmbeddingModelLocked) {
			writeError(w, http.StatusConflict, errEmbeddingModelLocked)
			return
		}
		writeError(w, 500, err)
		return
	}
	if err := store.DeleteModel(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// setFastModelAdmin marks (or clears) THE fast model (§fast-mode). Only one model
// is fast at a time — SetFastModel clears the flag on all others. Marking a model
// fast also forces Deep Research off on it, and is refused if it would leave the
// advanced ("进阶") picker with no model.
func setFastModelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var body struct {
		Fast bool `json:"fast"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if !body.Fast {
		// Clear the fast designation entirely (only one model can be fast, so there
		// is nothing to scope to this id).
		if err := store.SetFastModel(r.Context(), d.DB, ""); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "fast": false})
		return
	}
	m, err := store.GetModel(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	if m.Kind != "chat" {
		writeError(w, 400, errors.New("only a chat model can be the fast model"))
		return
	}
	if !m.Enabled {
		writeError(w, 400, errors.New("enable the model before making it the fast model"))
		return
	}
	// Keep at least one advanced (non-fast) chat model, or 进阶 empties out.
	n, err := store.CountAdvancedChatModels(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if n < 1 {
		writeError(w, 400, errors.New("keep at least one advanced model: you can't make the only chat model the fast model"))
		return
	}
	if err := store.SetFastModel(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "fast": true})
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
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.DisplayDescription = strings.TrimSpace(s.DisplayDescription)
	s.Icon = strings.TrimSpace(s.Icon)
	s.Instructions = strings.TrimSpace(s.Instructions)
	if s.Name == "" || s.Description == "" || s.Instructions == "" {
		writeError(w, 400, errors.New("name, description, instructions required"))
		return
	}
	if len(s.DisplayDescription) > libraryDescriptionMaxBytes {
		writeError(w, 400, errors.New("display_description is too long"))
		return
	}
	if existing, err := store.GetSkillByName(r.Context(), d.DB, s.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrSkillNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
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
		if errors.Is(err, store.ErrSkillNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, created)
}

func reorderSkillsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if err := store.ReorderSkills(r.Context(), d.DB, body.IDs); err != nil {
		if errors.Is(err, store.ErrInvalidReorder) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.DisplayDescription = strings.TrimSpace(s.DisplayDescription)
	s.Icon = strings.TrimSpace(s.Icon)
	s.Instructions = strings.TrimSpace(s.Instructions)
	if s.Name == "" || s.Description == "" || s.Instructions == "" {
		writeError(w, 400, errors.New("name, description, instructions required"))
		return
	}
	if len(s.DisplayDescription) > libraryDescriptionMaxBytes {
		writeError(w, 400, errors.New("display_description is too long"))
		return
	}
	if existing, err := store.GetSkillByName(r.Context(), d.DB, s.Name); err == nil && existing != nil && existing.ID != id {
		writeError(w, 409, store.ErrSkillNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
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
		if errors.Is(err, store.ErrSkillNameExists) {
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
		limit = adminUserListPageSizeCap
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

func getUserAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	user, err := store.FindUserByID(r.Context(), d.DB, pathParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	balance, err := store.GetCreditBalance(r.Context(), d.DB, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		*store.User
		CreditsTimed     timedCreditsSnapshot `json:"credits_timed"`
		CreditsAvailable float64              `json:"credits_available"`
	}{
		User:             user,
		CreditsTimed:     timedCreditsFromBalance(balance),
		CreditsAvailable: balance.Available,
	})
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
	// Guarded: refuses accounts mid-purge atomically, so a ban can never
	// overwrite status='deleting' and break crash-resume (§async user delete).
	ok, err := store.SetUserStatusGuarded(r.Context(), d.DB, id, "banned")
	if errors.Is(err, store.ErrLastAdmin) {
		writeError(w, 400, err)
		return
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !ok {
		writeError(w, 409, errors.New("account is being deleted"))
		return
	}
	invalidateAuthUser(d, id)
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
	// Heavy cleanup (bulk SQL, vectors, disk) runs in a background job; this
	// request only locks the account out and returns. The last-admin guard is
	// folded atomically into MarkUserDeleting. §async user delete.
	email := ""
	if target, terr := store.FindUserByID(r.Context(), d.DB, id); terr == nil {
		email = target.Email
	}
	if _, err := startUserDeletion(d, id, email); err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			writeError(w, 400, err)
			return
		}
		if errors.Is(err, store.ErrPaymentOrdersPendingForUser) {
			writeError(w, http.StatusConflict, err)
			return
		}
		if errors.Is(err, store.ErrWorkspaceOwnership) || errors.Is(err, store.ErrWorkspaceOwnerDeleting) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "status": "deleting"})
}

func unbanUserAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	// An account mid-purge must not be revived: the background job would keep
	// deleting its data underneath a "restored" login (§async user delete).
	// Atomic guard — no check-then-act window against a concurrent delete.
	ok, err := store.SetUserStatusGuarded(r.Context(), d.DB, id, "active")
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !ok {
		writeError(w, 409, errors.New("account is being deleted"))
		return
	}
	invalidateAuthUser(d, id)
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
	email, err := store.NormalizeUserEmail(req.Email)
	if err != nil {
		writeError(w, 400, errors.New("valid email required"))
		return
	}
	req.Email = email
	if err := validateNewPassword(req.Password, max(minimumPasswordLength, adminCreatedUserMinPasswordLength)); err != nil {
		writeError(w, 400, err)
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

// setUserEmailAdmin changes an account's sign-in email without touching its
// display name or any membership, credit, and security fields.
func setUserEmailAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if err := store.SetUserEmail(r.Context(), d.DB, id, req.Email); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, errNotFound)
		case errors.Is(err, store.ErrUserEmailInvalid):
			writeError(w, http.StatusBadRequest, errInvalidEmail)
		case errors.Is(err, store.ErrUserEmailExists):
			writeError(w, http.StatusConflict, errEmailAlreadyRegistered)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	invalidateAuthUser(d, id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	if err := validateNewPassword(req.NewPassword, max(minimumPasswordLength, adminPasswordResetMinLength)); err != nil {
		writeError(w, 400, err)
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
	invalidateAuthUser(d, id)
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
	if err := store.SetUserRole(r.Context(), d.DB, id, req.Role); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, 404, errNotFound)
		case errors.Is(err, store.ErrLastAdmin):
			writeError(w, 400, err)
		default:
			writeError(w, 500, err)
		}
		return
	}
	invalidateAuthUser(d, id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// listUserConversationsAdmin returns one user's conversations for support /
// abuse triage (§8.1). Ownership check is intentionally skipped because the
// admin scope already gates this surface in router.go.
func listUserConversationsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	userID := pathParam(r, "id")
	rows, err := store.ListConversations(r.Context(), d.DB, userID, "", "", adminUserConversationsListingCap, 0)
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
	// Capture the owner before the rows disappear — their open tabs get the
	// §23 delete event so the conversation vanishes live, not on next reload.
	ownerID := ""
	if conv, err := store.GetConversationByID(r.Context(), d.DB, id); err == nil {
		ownerID = conv.UserID
	}
	ids, _ := store.ConversationTreeIDs(r.Context(), d.DB, id)
	storagePaths, _ := store.StoragePathsForConversations(r.Context(), d.DB, ids)
	children, err := store.DeleteConversationByID(r.Context(), d.DB, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	if len(ids) == 0 {
		ids = append([]string{id}, children...)
	}
	// Drop RAG vectors for the conversation and every inline sub-conversation
	// removed alongside it.
	for _, cid := range ids {
		cleanupRAGConversation(r.Context(), d, cid, "admin delete conversation "+id)
	}
	cleanupStoragePaths(r.Context(), d, storagePaths, "admin delete conversation "+id)
	publishUserEvent(d, r, ownerID, "conversation.deleted", id) // §23 (owner's devices)
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
		writeJSON(w, 200, enrichWithAuthors(d, r, enrichWithSiblings(d, r, msgs)))
		return
	}
	msgs, err := store.ListMessages(r.Context(), d.DB, id, conv.ActiveLeafID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, enrichWithAuthors(d, r, enrichWithSiblings(d, r, msgs)))
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
	if strings.EqualFold(strings.TrimSpace(q.Get("status")), "error") {
		f.Status = "error"
	}
	// "all" / "" = no purpose constraint; "task" is the umbrella for task.*.
	if p := strings.TrimSpace(q.Get("purpose")); p != "" && !strings.EqualFold(p, "all") {
		f.Purpose = p
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize <= 0 || pageSize > 200 {
		pageSize = usageReportPageSizeCap
	}
	return f, page, pageSize
}

// usageReportAdmin lists retained diagnostic records, filtered and paginated,
// with their matching inventory count and displayed cost. Durable analytics are
// served separately from usage_stats by analyticsAdmin.
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

// analyticsAdmin powers the admin usage and billing workspace. Every aggregate
// comes from append-only usage_stats; the previous period uses the adjacent,
// equal-length half-open interval so comparisons never overlap.
func parseUsageAnalyticsFilter(r *http.Request) store.UsageAnalyticsFilter {
	query := r.URL.Query()
	filter := store.UsageAnalyticsFilter{
		UserQuery: strings.TrimSpace(query.Get("user")),
		ModelID:   strings.TrimSpace(query.Get("model")),
		Purpose:   strings.TrimSpace(query.Get("purpose")),
	}
	if workspaceID := strings.TrimSpace(query.Get("workspace")); workspaceID != "" {
		if workspaceID == "__personal__" {
			workspaceID = ""
		}
		filter.WorkspaceID = &workspaceID
	}
	if channelID := strings.TrimSpace(query.Get("channel")); channelID != "" {
		if channelID == "__unattributed__" {
			channelID = ""
		}
		filter.ChannelID = &channelID
	}
	return filter
}

func usageAnalyticsFilterActive(filter store.UsageAnalyticsFilter) bool {
	return filter.UserQuery != "" || filter.ModelID != "" || filter.WorkspaceID != nil ||
		filter.Purpose != "" || filter.ChannelID != nil
}

func analyticsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	days := analyticsWindow
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= analyticsWindow2 {
			days = n
		}
	}
	ctx := r.Context()
	periodEnd := time.Now().Unix() + 1
	window := int64(days) * 86400
	periodStart := periodEnd - window
	previousStart := periodStart - window
	bucket := store.UsageBucketWidth(days)
	filter := parseUsageAnalyticsFilter(r)

	totals, err := store.AdminUsageTotalsBetween(ctx, d.DB, periodStart, periodEnd, filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	previousTotals, err := store.AdminUsageTotalsBetween(ctx, d.DB, previousStart, periodStart, filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	trend, err := store.AdminUsageTrendBetween(ctx, d.DB, periodStart, periodEnd, bucket, filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	previousTrend, err := store.AdminUsageTrendBetween(ctx, d.DB, previousStart, periodStart, bucket, filter)
	if err != nil {
		writeError(w, 500, err)
		return
	}

	breakdowns := make(map[string][]store.UsageBreakdownRow, 5)
	for key, column := range map[string]string{
		"model": "model_id", "user": "user_id", "workspace": "workspace_id",
		"purpose": "purpose", "channel": "channel_id",
	} {
		rows, breakdownErr := store.AdminUsageBreakdownBetween(ctx, d.DB, periodStart, periodEnd, column, -1, filter)
		if breakdownErr != nil {
			writeError(w, 500, breakdownErr)
			return
		}
		breakdowns[key] = rows
	}
	filterOptions := breakdowns
	if usageAnalyticsFilterActive(filter) {
		filterOptions = make(map[string][]store.UsageBreakdownRow, 5)
		for key, column := range map[string]string{
			"model": "model_id", "user": "user_id", "workspace": "workspace_id",
			"purpose": "purpose", "channel": "channel_id",
		} {
			rows, breakdownErr := store.AdminUsageBreakdownBetween(ctx, d.DB, periodStart, periodEnd, column, -1)
			if breakdownErr != nil {
				writeError(w, 500, breakdownErr)
				return
			}
			filterOptions[key] = rows
		}
	}
	writeJSON(w, 200, map[string]any{
		"days":                  days,
		"bucket":                bucket,
		"generated_at":          periodEnd - 1,
		"period_start":          periodStart,
		"period_end":            periodEnd,
		"previous_period_start": previousStart,
		"previous_period_end":   periodStart,
		"totals":                totals,
		"previous_totals":       previousTotals,
		"trend":                 trend,
		"previous_trend":        previousTrend,
		"breakdowns":            breakdowns,
		"filter_options":        filterOptions,
	})
}

// ===== Settings =====

var settingsKeys = []string{
	"default_model_id", "task_model_id", "context_compaction_model_id", "tool_route_model_id", "embedding_model_id",
	"keep_recent_rounds", "summary_max_tokens", "summary_target_percent", "summary_merge_max_tokens", "compaction_request_max_tokens", "context_compaction_prompt", "compaction_enabled",
	"compaction_token_trigger", "compaction_token_cap", "compaction_retention_percentage",
	"memory_enabled", "daily_message_limit", "daily_image_limit", "signup_open",
	"email_verification_required", "daily_token_limit", "max_concurrent_generations",
	// Anti-abuse registration controls. register_ip_daily_limit: max accounts one
	// IP may create per day (0 = off). register_captcha_required: gate signup
	// behind the slider-puzzle captcha. login_captcha_required: same gate on
	// password sign-in (anti credential-stuffing).
	"register_ip_daily_limit", "register_captcha_required", "login_captcha_required",
	// Public legal/support information. Blank policy text means the frontend uses
	// its complete localized default; a blank contact email falls back to the
	// project default in legalConfigPublicHandler.
	"contact_email", "terms_text", "privacy_text",
	// §credits / settlement pricing: credits_per_usd remains the internal model
	// cost conversion. User-facing group and permanent-credit prices share one
	// deployment-wide settlement currency; card_purchase_url is the only external
	// checkout link outside the configured payment methods.
	"credits_per_usd", "settlement_currency", "card_purchase_url",
	// §B6 partial: JSON array of tool names disabled platform-wide (kill-switch),
	// e.g. ["python_execute","image_generate"].
	"disabled_tools",
	"sandbox_base_url", "sandbox_api_key",
	// §4.5 per-exec wall-clock cap in SECONDS (admin-tunable). Blank/0 = default
	// 120s. Clamped to [10,600] server-side and to the sidecar's hard ceiling.
	"sandbox_exec_timeout_sec",
	// §4.5 idle-recycle window in SECONDS (admin-tunable). How long a sandbox may
	// sit unused before it's archived + torn down. Blank/0 = sidecar default
	// (1800s). Clamped to [60,86400] server-side and to the sidecar's ceiling.
	"sandbox_idle_ttl_sec",
	// §4.5 storage backend: pick one of s3 / aliyun_oss / local. "local" archives
	// to a sidecar-mounted volume (zero external deps). When blank, archive/restore
	// is disabled and the sandbox still works (workspaces reaped = gone). All
	// credentials live in admin settings, plaintext, per the channel api_key policy.
	"storage_provider", // "" | "s3" | "aliyun_oss" | "local"
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
	// § announcement: global notice config (enabled/title/body/image_url/
	// remember_dismiss/updated_at) shown to users on load. Edited via the admin
	// announcement page.
	"announcement",
	// Voice transcription (whisper) — admin-configurable, live-reloaded per call.
	// base_url defaults to https://api.openai.com; model defaults to whisper-1.
	"audio_transcribe_base_url", "audio_transcribe_api_key", "audio_transcribe_model",
	// STT provider selector: "gpt" (OpenAI-compatible, record-then-transcribe,
	// the keys above) or "volcano" (火山引擎 豆包 live streaming ASR, the keys
	// below). volcano_asr_access_token is masked as a secret (see
	// sensitiveKeywords). Live-reloaded per call, same as whisper.
	"audio_transcribe_provider",
	"volcano_asr_app_id", "volcano_asr_access_token", "volcano_asr_resource_id",
	"volcano_asr_ws_url", "volcano_asr_model_name",
	"volcano_asr_enable_itn", "volcano_asr_enable_punc", "volcano_asr_enable_ddc",
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
	// §4.6 upload size caps — per-kind byte ceilings expressed in MB, enforced on
	// /api/files BEFORE bytes land. 0 / blank → default (images: 5 MB; other files:
	// the MAX_UPLOAD_BYTES env ceiling). Both are additionally clamped to that env
	// ceiling server-side, so admins can only tighten, never exceed it.
	"max_image_upload_mb", "max_file_upload_mb",
	// SMTP mail — live-reloaded on each send (see internal/mail).
	"smtp_host", "smtp_port", "smtp_user", "smtp_password",
	"smtp_from", "smtp_tls",
	"email_domain_whitelist",
	// §4.20 Image Generation: the TEXT model used to optimize/expand a user's
	// image prompt (and fold in the style's hidden prompt) before generation.
	// Blank = no optimization (deterministic join). Image MODELS are picked per
	// conversation from the model picker, so there's no default-image-model key.
	"image_prompt_model_id",
	// §verify: the secondary auditor model that fact-checks answers in Verify
	// mode. Blank = Verify mode off platform-wide.
	"verify_model_id",
	// §4.11-B RAG injection knobs (admin → Documents). §4.11-B3 adds the line cap
	// for code/config/txt/unknown-format docs (≤ N lines → full inject; above →
	// embed + retrieve).
	"rag_full_text_threshold", "rag_top_k", "rag_dynamic_topk", "rag_similarity_threshold",
	"rag_code_full_text_max_lines",
	// §credits pre-flight token/affordability check.
	"credit_preflight_enabled",
	// §B5 request logging: log_full_requests turns on persisting the full
	// (sanitized) provider request body on usage rows; log_errors_only (default
	// true) restricts that to failed requests. Off = errors-only (the floor).
	"log_full_requests", "log_errors_only",
}

// sensitiveKeywords lists substrings that identify secret settings fields.
// Any settings key whose name contains one of these (case-insensitive) will
// have its non-empty string value replaced with the mask on GET responses.
var sensitiveKeywords = []string{"password", "secret", "api_key", "token", "key_secret", "key_id", "access_key"}

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
	_, toolsPresent := body["disabled_tools"]
	_, memoryPresent := body["memory_enabled"]
	capabilitiesPresent := toolsPresent || memoryPresent
	capabilitiesBefore := globalCapabilitySnapshot{}
	if capabilitiesPresent {
		capabilitiesBefore = currentGlobalCapabilitySnapshot(d)
	}
	if _, err := applyAdminSettingsPatch(r.Context(), d, body, true); err != nil {
		if errors.Is(err, errInvalidInput) {
			writeError(w, 400, errInvalidInput)
			return
		}
		if errors.Is(err, errEmbeddingModelLocked) {
			writeError(w, http.StatusConflict, errEmbeddingModelLocked)
			return
		}
		writeError(w, 500, err)
		return
	}
	if capabilitiesPresent && !globalCapabilitySnapshotsEqual(capabilitiesBefore, currentGlobalCapabilitySnapshot(d)) {
		revokeGlobalCapabilitySnapshots(d)
		publishGlobalEvent(d, "account.permissions_updated")
	}
	broadcastConfigInvalidate(d) // §2.4: clear the settings cache on every instance
	adminSettingsGet(d, w, r)
}

func applyAdminSettingsPatch(ctx context.Context, d Deps, body map[string]json.RawMessage, skipNull bool) (int64, error) {
	type settingWrite struct {
		key   string
		value json.RawMessage
	}
	writes := make([]settingWrite, 0, len(body))
	for _, k := range settingsKeys {
		if v, ok := body[k]; ok {
			if skipNull && strings.TrimSpace(string(v)) == "null" {
				continue
			}
			// Skip writing back the display mask — treat it as "unchanged" (H-1).
			var s string
			if json.Unmarshal(v, &s) == nil && s == "••••••" {
				continue
			}
			// §4.7 compaction knobs must be non-negative integers — a negative
			// token_trigger inverts the early-exit guard and a zero/negative
			// summary length makes the tiered merge churn the cache every turn.
			switch k {
			case "keep_recent_rounds", "summary_max_tokens", "summary_target_percent", "summary_merge_max_tokens", "compaction_request_max_tokens", "compaction_token_trigger", "compaction_token_cap", "compaction_retention_percentage",
				"daily_message_limit", "daily_image_limit", "daily_token_limit",
				"max_concurrent_generations", "register_ip_daily_limit", "fallback_ttft_sec":
				var n int
				if json.Unmarshal(v, &n) != nil || n < 0 {
					return 0, errInvalidInput
				}
				if k == "summary_target_percent" && (n < 5 || n > 80) {
					return 0, errInvalidInput
				}
				if k == "compaction_retention_percentage" && (n < 10 || n > 50) {
					return 0, errInvalidInput
				}
				if k == "keep_recent_rounds" && n < 1 {
					return 0, errInvalidInput
				}
				if (k == "summary_max_tokens" || k == "summary_merge_max_tokens") && n < 256 {
					return 0, errInvalidInput
				}
				if k == "compaction_request_max_tokens" && n < 8192 {
					return 0, errInvalidInput
				}
			case "credits_per_usd":
				var amount float64
				if json.Unmarshal(v, &amount) != nil || amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
					return 0, errInvalidInput
				}
				if micros, err := store.CreditsToMicros(amount); err != nil || amount > 0 && micros == 0 {
					return 0, errInvalidInput
				}
			case "max_image_upload_mb", "max_file_upload_mb":
				// Per-kind upload caps in MB. Non-negative integer; 0 = "use default".
				// The byte ceiling (env MaxUploadBytes) is applied at read time.
				var n int
				if json.Unmarshal(v, &n) != nil || n < 0 {
					return 0, errInvalidInput
				}
			case "storage_archive_ttl_days":
				// Older admin pages stored this value as a JSON string. Accept that
				// representation, validate it, and normalize future writes to a
				// non-negative integer.
				var days int
				if json.Unmarshal(v, &days) != nil {
					var raw string
					if json.Unmarshal(v, &raw) != nil {
						return 0, errInvalidInput
					}
					raw = strings.TrimSpace(raw)
					if raw != "" {
						var err error
						days, err = strconv.Atoi(raw)
						if err != nil {
							return 0, errInvalidInput
						}
					}
				}
				if days < 0 {
					return 0, errInvalidInput
				}
				v, _ = json.Marshal(days)
			case "settlement_currency":
				var code string
				if json.Unmarshal(v, &code) != nil {
					return 0, errInvalidInput
				}
				code = strings.ToUpper(strings.TrimSpace(code))
				if !validSettlementCurrency(code) {
					return 0, errInvalidInput
				}
				v, _ = json.Marshal(code)
			case "card_purchase_url":
				var purchaseURL string
				if json.Unmarshal(v, &purchaseURL) != nil {
					return 0, errInvalidInput
				}
				purchaseURL = strings.TrimSpace(purchaseURL)
				if purchaseURL != "" && !validPaymentHTTPURL(purchaseURL) {
					return 0, errInvalidInput
				}
				v, _ = json.Marshal(purchaseURL)
			case "contact_email":
				var email string
				if json.Unmarshal(v, &email) != nil {
					return 0, errInvalidInput
				}
				email = strings.TrimSpace(email)
				if email != "" && !validContactEmail(email) {
					return 0, errInvalidInput
				}
				v, _ = json.Marshal(email)
			case "terms_text", "privacy_text":
				var text string
				if json.Unmarshal(v, &text) != nil || len(text) > legalPolicyTextMaxBytes {
					return 0, errInvalidInput
				}
				v, _ = json.Marshal(strings.TrimSpace(text))
			case "context_compaction_prompt":
				var prompt string
				if json.Unmarshal(v, &prompt) != nil {
					return 0, errInvalidInput
				}
				prompt = strings.TrimSpace(prompt)
				if len(prompt) > contextCompactionPromptMaxBytes {
					return 0, errInvalidInput
				}
				v, _ = json.Marshal(prompt)
			case "context_compaction_model_id":
				normalized, err := normalizeContextCompactionModelSetting(ctx, d, v)
				if err != nil {
					return 0, err
				}
				v = normalized
			case "compaction_enabled":
				var enabled bool
				if json.Unmarshal(v, &enabled) != nil {
					return 0, errInvalidInput
				}
				v, _ = json.Marshal(enabled)
			case "embedding_model_id":
				if err := ensureEmbeddingModelSettingCanChange(d, v); err != nil {
					return 0, err
				}
			case "disabled_tools":
				normalized, err := store.NormalizeBuiltinTools(v)
				if err != nil {
					return 0, errInvalidInput
				}
				if normalized == nil {
					normalized = json.RawMessage("[]")
				}
				v = normalized
			}
			writes = append(writes, settingWrite{key: k, value: append(json.RawMessage(nil), v...)})
		}
	}
	if len(writes) == 0 {
		return 0, nil
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for _, write := range writes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			write.key, string(write.value), now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// Direct transactional writes bypass store.SetSetting's per-key invalidation.
	// Clear the local cache before callers compare post-write capability state or
	// render the saved settings response; adminSettingsSet broadcasts the same
	// invalidation to other instances immediately afterwards.
	store.InvalidateConfig()
	return int64(len(writes)), nil
}

func normalizeContextCompactionModelSetting(ctx context.Context, d Deps, raw json.RawMessage) (json.RawMessage, error) {
	var modelID string
	if json.Unmarshal(raw, &modelID) != nil {
		return nil, errInvalidInput
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return json.RawMessage(`""`), nil
	}
	model, err := store.GetModel(ctx, d.DB, modelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errInvalidInput
		}
		return nil, err
	}
	if !model.Enabled || model.Kind != "chat" {
		return nil, errInvalidInput
	}
	channel, err := store.GetChannel(ctx, d.DB, model.ChannelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errInvalidInput
		}
		return nil, err
	}
	if !channel.Enabled {
		return nil, errInvalidInput
	}
	if !isSupportedContextCompactionChannelType(channel.Type) {
		return nil, errInvalidInput
	}
	normalized, _ := json.Marshal(modelID)
	return normalized, nil
}

func isSupportedContextCompactionChannelType(channelType string) bool {
	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case "openai", "anthropic", "claude", "google", "gemini":
		return true
	default:
		return false
	}
}

func ensureEmbeddingModelSettingCanChange(d Deps, next json.RawMessage) error {
	var nextID string
	if err := json.Unmarshal(next, &nextID); err != nil {
		return errInvalidInput
	}
	curID, err := lockedEmbeddingModelID(d)
	if err != nil {
		return err
	}
	if curID == "" {
		return nil
	}
	if curID != strings.TrimSpace(nextID) {
		return errEmbeddingModelLocked
	}
	return nil
}

func lockedEmbeddingModelID(d Deps) (string, error) {
	var curValue string
	err := d.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, "embedding_model_id").Scan(&curValue)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	var curID string
	if json.Unmarshal([]byte(curValue), &curID) != nil {
		return "", nil
	}
	return strings.TrimSpace(curID), nil
}

func ensureLockedEmbeddingModelCanUpdate(d Deps, before, after store.Model) error {
	lockedID, err := lockedEmbeddingModelID(d)
	if err != nil || lockedID == "" || before.ID != lockedID {
		return err
	}
	if before.Kind != after.Kind ||
		before.ChannelID != after.ChannelID ||
		before.RequestID != after.RequestID ||
		before.Dim != after.Dim ||
		!after.Enabled {
		return errEmbeddingModelLocked
	}
	return nil
}

func ensureLockedEmbeddingModelCanDelete(d Deps, id string) error {
	lockedID, err := lockedEmbeddingModelID(d)
	if err != nil || lockedID == "" {
		return err
	}
	if lockedID == id {
		return errEmbeddingModelLocked
	}
	return nil
}

func lockedEmbeddingModelFieldChanged(existing store.Model, row map[string]json.RawMessage) (bool, error) {
	if v, ok, err := backupStringField(row, "kind"); err != nil {
		return false, err
	} else if ok && v != existing.Kind {
		return true, nil
	}
	if v, ok, err := backupStringField(row, "channel_id"); err != nil {
		return false, err
	} else if ok && v != existing.ChannelID {
		return true, nil
	}
	if v, ok, err := backupStringField(row, "request_id"); err != nil {
		return false, err
	} else if ok && v != existing.RequestID {
		return true, nil
	}
	if v, ok, err := backupIntField(row, "dim"); err != nil {
		return false, err
	} else if ok && v != existing.Dim {
		return true, nil
	}
	if v, ok, err := backupBoolField(row, "enabled"); err != nil {
		return false, err
	} else if ok && !v {
		return true, nil
	}
	return false, nil
}

func backupStringField(row map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := row[key]
	if !ok {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true, err
	}
	return strings.TrimSpace(s), true, nil
}

func backupIntField(row map[string]json.RawMessage, key string) (int, bool, error) {
	raw, ok := row[key]
	if !ok {
		return 0, false, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, true, err
	}
	return n, true, nil
}

func backupBoolField(row map[string]json.RawMessage, key string) (bool, bool, error) {
	raw, ok := row[key]
	if !ok {
		return false, false, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return false, true, err
	}
	return n != 0, true, nil
}

func ensureLockedEmbeddingModelArchiveRowCanChange(d Deps, row map[string]json.RawMessage) error {
	lockedID, err := lockedEmbeddingModelID(d)
	if err != nil || lockedID == "" {
		return err
	}
	rowID, ok, err := backupStringField(row, "id")
	if err != nil || !ok || rowID != lockedID {
		return err
	}
	existing, err := store.GetModel(context.Background(), d.DB, lockedID)
	if err != nil {
		return nil
	}
	changed, err := lockedEmbeddingModelFieldChanged(*existing, row)
	if err != nil {
		return err
	}
	if changed {
		return errEmbeddingModelLocked
	}
	return nil
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
