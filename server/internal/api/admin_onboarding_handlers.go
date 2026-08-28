package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

// adminOnboardingSettingKey is deliberately per-user: each administrator can
// dismiss or revisit the guide independently, while its checklist always
// reflects the deployment's current shared configuration.
const adminOnboardingSettingKey = "admin_onboarding_v1"

const (
	adminOnboardingUnseen    = "unseen"
	adminOnboardingDismissed = "dismissed"
	adminOnboardingCompleted = "completed"
)

var errAdminOnboardingRequirementsIncomplete = errors.New("admin_onboarding_requirements_incomplete")

type adminOnboardingStep struct {
	ID       string `json:"id"`
	Complete bool   `json:"complete"`
}

// adminOnboardingResponse intentionally contains only stable machine ids.
// The SPA owns all labels, descriptions, and navigation destinations so the
// guide remains localized and can evolve without changing this contract.
type adminOnboardingResponse struct {
	DeploymentProfile string                `json:"deployment_profile"`
	Status            string                `json:"status"`
	Required          []adminOnboardingStep `json:"required"`
	Optional          []adminOnboardingStep `json:"optional"`
	FullOptional      []adminOnboardingStep `json:"full_optional"`
}

func adminOnboardingGet(d Deps, w http.ResponseWriter, r *http.Request) {
	user := authUser(r)
	settings := user.Settings
	// The auth middleware can serve the profile from its short-lived cache. The
	// guide's dismissal state is per-user and must reflect a just-completed
	// PATCH, even if the next GET is handled by that still-live cache entry.
	if fresh, err := store.FindUserByID(r.Context(), d.DB, user.ID); err == nil {
		settings = fresh.Settings
	} else if d.Logger != nil {
		d.Logger.Printf("admin onboarding: refresh user settings: %v", err)
	}
	response, err := buildAdminOnboardingResponse(r, d, settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type adminOnboardingPatchRequest struct {
	Action string `json:"action"`
}

func adminOnboardingSet(d Deps, w http.ResponseWriter, r *http.Request) {
	var body adminOnboardingPatchRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}

	user := authUser(r)
	var status string
	switch strings.TrimSpace(body.Action) {
	case "skip":
		status = adminOnboardingDismissed
	case "complete":
		status = adminOnboardingCompleted
	case "reset":
		status = adminOnboardingUnseen
	default:
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}

	// The primary button is disabled in the SPA until every required step is
	// ready, but the server remains authoritative: a stale tab or direct API
	// request must not be able to permanently mark an incomplete guide done.
	if status == adminOnboardingCompleted {
		current, err := buildAdminOnboardingResponse(r, d, user.Settings)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !adminOnboardingRequiredReady(current) {
			writeError(w, http.StatusConflict, errAdminOnboardingRequirementsIncomplete)
			return
		}
	}

	updated, err := store.UpdateUserSettings(r.Context(), d.DB, user.ID, map[string]any{
		adminOnboardingSettingKey: status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// The authenticated profile is cached separately from the settings row.
	// Invalidate it so a subsequent GET on this or another admin page observes
	// the new per-admin dismissal state immediately.
	invalidateAuthUser(d, user.ID)

	response, err := buildAdminOnboardingResponse(r, d, updated.Settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func buildAdminOnboardingResponse(r *http.Request, d Deps, userSettings json.RawMessage) (adminOnboardingResponse, error) {
	channels, err := store.ListChannels(r.Context(), d.DB)
	if err != nil {
		return adminOnboardingResponse{}, err
	}
	models, err := store.ListModels(r.Context(), d.DB, "", false)
	if err != nil {
		return adminOnboardingResponse{}, err
	}

	usableChannels := make(map[string]bool, len(channels))
	channelReady := false
	for _, channel := range channels {
		usable := onboardingChannelUsable(channel)
		usableChannels[channel.ID] = usable
		channelReady = channelReady || usable
	}

	usableChatModels := make(map[string]bool)
	usableEmbeddingModels := make(map[string]bool)
	chatModelReady := false
	for _, model := range models {
		if !model.Enabled || !usableChannels[model.ChannelID] || strings.TrimSpace(model.RequestID) == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(model.Kind)) {
		case "chat":
			usableChatModels[model.ID] = true
			chatModelReady = true
		case "embedding":
			usableEmbeddingModels[model.ID] = true
		}
	}

	defaultModelID, err := onboardingSettingString(d.DB, "default_model_id")
	if err != nil {
		return adminOnboardingResponse{}, err
	}
	embeddingModelID, err := onboardingSettingString(d.DB, "embedding_model_id")
	if err != nil {
		return adminOnboardingResponse{}, err
	}
	searchReady, err := onboardingSearchReady(d)
	if err != nil {
		return adminOnboardingResponse{}, err
	}
	smtpReady, err := onboardingSMTPReady(d.DB)
	if err != nil {
		return adminOnboardingResponse{}, err
	}
	disabledBuiltinTools := onboardingDisabledBuiltinTools(d.DB)

	// A configured model is the preferred high-quality embedding path. The
	// optional environment fallback is also real configuration; the bundled hash
	// embedder remains available for basic retrieval but is intentionally not
	// reported as this "make it better" step being complete. Neither credential
	// can provide document retrieval when the vector backend itself is disabled.
	// The environment fallback is usable only with a key. Its base URL may be
	// blank because the runtime deliberately defaults that case to OpenAI's /v1
	// endpoint (see rag.httpEmbedder.endpoint).
	vectorReady := d.RAG != nil && d.RAG.VectorStoreEnabled()
	embeddingReady := vectorReady && (usableEmbeddingModels[embeddingModelID] || strings.TrimSpace(d.Config.EmbeddingAPIKey) != "")

	response := adminOnboardingResponse{
		DeploymentProfile: adminDeploymentProfile(d.Config),
		Status:            parseAdminOnboardingStatus(userSettings),
		Required: []adminOnboardingStep{
			{ID: "channel", Complete: channelReady},
			{ID: "chat_model", Complete: chatModelReady},
			{ID: "default_model", Complete: usableChatModels[defaultModelID]},
		},
		Optional: []adminOnboardingStep{
			{ID: "embedding", Complete: embeddingReady},
			{ID: "search", Complete: searchReady && !disabledBuiltinTools["aivory_web_search"]},
			{ID: "sandbox", Complete: sandboxConfigured(d) && !disabledBuiltinTools["python_execute"]},
		},
		FullOptional: []adminOnboardingStep{},
	}
	if response.DeploymentProfile == "full" {
		response.FullOptional = append(response.FullOptional, adminOnboardingStep{ID: "smtp", Complete: smtpReady})
	}
	return response, nil
}

func adminOnboardingRequiredReady(response adminOnboardingResponse) bool {
	for _, step := range response.Required {
		if !step.Complete {
			return false
		}
	}
	return true
}

// onboardingChannelUsable keeps the guide's first step aligned with the LLM
// registry. Imports and old backups can contain rows that bypass the channel
// editor's validation; an enabled row with an unknown provider would otherwise
// make the guide green even though every chat request fails before dispatch.
func onboardingChannelUsable(channel store.Channel) bool {
	if !channel.Enabled || !channel.HasAPIKey {
		return false
	}
	// The runtime switches behavior from these persisted values without
	// normalizing their case. Do not report an old malformed row as usable when
	// it would dispatch to a different provider path at request time.
	channelType := strings.TrimSpace(channel.Type)
	apiFormat := strings.TrimSpace(channel.APIFormat)
	if validateChannelType(channelType, apiFormat) != nil {
		return false
	}
	return channelType != "openai" || onboardingOpenAIBaseURLUsable(channel.BaseURL)
}

// onboardingOpenAIBaseURLUsable accepts the historical host-only shape that
// llm.OpenAIBaseURL still upgrades to /v1, while rejecting URLs that cannot
// become a valid HTTP request. New and edited channels remain subject to the
// stricter /v1 normalization in the admin handlers.
func onboardingOpenAIBaseURLUsable(raw string) bool {
	if raw == "" {
		return true
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Hostname() != "" &&
		parsed.User == nil &&
		parsed.RawQuery == "" &&
		parsed.Fragment == ""
}

// onboardingDisabledBuiltinTools mirrors the runtime's fail-open behavior for
// missing or malformed global policy data. A valid disabled tool must never be
// presented as ready just because its provider settings happen to be present.
func onboardingDisabledBuiltinTools(db *sql.DB) map[string]bool {
	raw, err := store.GetSetting(db, "disabled_tools")
	if err != nil || len(raw) == 0 {
		return nil
	}
	names, _, err := store.ParseBuiltinTools(raw)
	if err != nil {
		return nil
	}
	disabled := make(map[string]bool, len(names))
	for _, name := range names {
		disabled[name] = true
	}
	return disabled
}

// adminDeploymentProfile recognizes the topology shipped in the personal
// compose file. A self-managed full deployment can deliberately choose the
// same dependencies, in which case it receives the compatible personal-guide
// subset rather than requiring an extra deployment-profile setting.
func adminDeploymentProfile(cfg config.Config) string {
	backend, err := config.ResolveVectorBackend(cfg)
	if err == nil &&
		backend == config.VectorBackendSQLite &&
		strings.TrimSpace(cfg.RedisURL) == "" &&
		strings.TrimSpace(cfg.QdrantURL) == "" {
		return "personal"
	}
	return "full"
}

func parseAdminOnboardingStatus(settings json.RawMessage) string {
	var values map[string]json.RawMessage
	if json.Unmarshal(settings, &values) != nil {
		return adminOnboardingUnseen
	}
	var status string
	if json.Unmarshal(values[adminOnboardingSettingKey], &status) != nil {
		return adminOnboardingUnseen
	}
	switch status {
	case adminOnboardingDismissed, adminOnboardingCompleted:
		return status
	default:
		return adminOnboardingUnseen
	}
}

// onboardingSettingString distinguishes an absent setting from a transient
// database failure and mirrors the settings API's JSON-string representation.
func onboardingSettingString(db *sql.DB, key string) (string, error) {
	raw, err := store.GetSetting(db, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", nil
	}
	return strings.TrimSpace(value), nil
}

// onboardingSearchReady mirrors the provider requirements in settingsSearcher:
// Serper and Brave need a key; SearXNG needs an endpoint; auto can use either.
// A present admin setting deliberately overrides its environment fallback,
// including an empty value, just as the live tool resolver does.
func onboardingSearchReady(d Deps) (bool, error) {
	provider, err := onboardingSettingStringWithFallback(d.DB, "search_provider", d.Config.SearchProvider)
	if err != nil {
		return false, err
	}
	baseURL, err := onboardingSettingStringWithFallback(d.DB, "search_base_url", d.Config.SearchBaseURL)
	if err != nil {
		return false, err
	}
	apiKey, err := onboardingSettingStringWithFallback(d.DB, "search_api_key", d.Config.SearchAPIKey)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(provider) {
	case "serper", "brave":
		return apiKey != "", nil
	case "searxng":
		return baseURL != "", nil
	case "", "auto":
		return apiKey != "" || baseURL != "", nil
	default:
		return false, nil
	}
}

func onboardingSettingStringWithFallback(db *sql.DB, key, fallback string) (string, error) {
	raw, err := store.GetSetting(db, key)
	if errors.Is(err, sql.ErrNoRows) {
		return strings.TrimSpace(fallback), nil
	}
	if err != nil {
		return "", err
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return strings.TrimSpace(fallback), nil
	}
	return strings.TrimSpace(value), nil
}

func onboardingSMTPReady(db *sql.DB) (bool, error) {
	// Keep this shape in lockstep with mail.SMTPSender.loadConfig. Older
	// deployments may not have persisted the optional defaults, which the mailer
	// now resolves as zero values. A malformed persisted value remains invalid:
	// the guide must not promise mail delivery that will fail on first use.
	host, ok, err := onboardingSMTPString(db, "smtp_host", true)
	if err != nil || !ok {
		return false, err
	}
	if _, ok, err = onboardingSMTPString(db, "smtp_port", false); err != nil || !ok {
		return false, err
	}
	user, ok, err := onboardingSMTPString(db, "smtp_user", false)
	if err != nil || !ok {
		return false, err
	}
	if _, ok, err = onboardingSMTPString(db, "smtp_password", false); err != nil || !ok {
		return false, err
	}
	from, ok, err := onboardingSMTPString(db, "smtp_from", false)
	if err != nil || !ok {
		return false, err
	}
	if _, ok, err = onboardingSMTPBool(db, "smtp_tls", false); err != nil || !ok {
		return false, err
	}
	// mail.SMTPSender permits an unauthenticated relay and uses smtp_user as its
	// From fallback when smtp_from is blank. Match that live behavior here.
	return host != "" && (from != "" || user != ""), nil
}

func onboardingSMTPString(db *sql.DB, key string, required bool) (string, bool, error) {
	raw, err := store.GetSetting(db, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", !required, nil
	}
	if err != nil {
		return "", false, err
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false, nil
	}
	return strings.TrimSpace(value), true, nil
}

func onboardingSMTPBool(db *sql.DB, key string, required bool) (bool, bool, error) {
	raw, err := store.GetSetting(db, key)
	if errors.Is(err, sql.ErrNoRows) {
		return false, !required, nil
	}
	if err != nil {
		return false, false, err
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false, nil
	}
	return value, true, nil
}
