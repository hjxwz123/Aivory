package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aivory/server/internal/store"
)

const (
	channelModelDiscoveryTimeout      = 30 * time.Second
	channelModelDiscoveryPageLimit    = 20
	channelModelDiscoveryModelLimit   = 2000
	channelModelDiscoveryResponseSize = 4 << 20
)

var (
	errChannelModelDiscoveryFailed   = errors.New("channel_model_discovery_failed")
	errChannelModelDiscoveryTooLarge = errors.New("channel model discovery result exceeds the supported limit")
	channelModelDiscoveryHTTPClient  = newChannelModelDiscoveryHTTPClient()
)

type discoveredChannelModel struct {
	RequestID   string `json:"request_id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
}

type channelModelDiscovery struct {
	Models             []discoveredChannelModel `json:"models"`
	Discovered         int                      `json:"discovered"`
	SkippedUnsupported int                      `json:"skipped_unsupported"`
}

type channelModelImportResponse struct {
	Discovered         int `json:"discovered"`
	Created            int `json:"created"`
	SkippedExisting    int `json:"skipped_existing"`
	SkippedUnsupported int `json:"skipped_unsupported"`
}

type channelModelBatchResponse struct {
	Requested        int `json:"requested"`
	Created          int `json:"created"`
	SkippedExisting  int `json:"skipped_existing"`
	SkippedDuplicate int `json:"skipped_duplicate"`
}

func newChannelModelDiscoveryHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 20 * time.Second
	transport.ExpectContinueTimeout = time.Second

	return &http.Client{
		Transport: transport,
		Timeout:   channelModelDiscoveryTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			previous := via[len(via)-1].URL
			if !strings.EqualFold(previous.Scheme, req.URL.Scheme) || !strings.EqualFold(previous.Host, req.URL.Host) {
				return errors.New("cross-origin model discovery redirect rejected")
			}
			if len(via) >= 5 {
				return errors.New("too many model discovery redirects")
			}
			return nil
		},
	}
}

// discoverDraftChannelModelsAdmin previews the models exposed by credentials
// that have not been saved yet. The temporary channel is never persisted and
// its API key is never included in the response.
func discoverDraftChannelModelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var req createChannelReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	req.Type = strings.TrimSpace(req.Type)
	req.APIFormat = strings.TrimSpace(req.APIFormat)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.Type != "openai" {
		req.APIFormat = ""
	}
	if err := validateChannelType(req.Type, req.APIFormat); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Type == "openai" {
		baseURL, err := normalizeOpenAIChannelBaseURL(req.BaseURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		req.BaseURL = baseURL
	}

	channel := &store.Channel{
		Type:      req.Type,
		APIFormat: req.APIFormat,
		BaseURL:   req.BaseURL,
		APIKey:    req.APIKey,
	}
	ctx, cancel := context.WithTimeout(r.Context(), channelModelDiscoveryTimeout)
	defer cancel()
	discovery, err := discoverChannelModels(ctx, channel)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Printf("admin: draft channel model discovery failed (type=%s): %v", channel.Type, err)
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": errChannelModelDiscoveryFailed.Error()})
		return
	}
	writeJSON(w, http.StatusOK, discovery)
}

func importChannelModelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	channel, err := store.GetChannel(r.Context(), d.DB, pathParam(r, "id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), channelModelDiscoveryTimeout)
	defer cancel()
	discovery, err := discoverChannelModels(ctx, channel)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Printf("admin: channel model discovery failed (channel=%s type=%s): %v", channel.ID, channel.Type, err)
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": errChannelModelDiscoveryFailed.Error()})
		return
	}

	result := channelModelImportResponse{
		Discovered:         discovery.Discovered,
		SkippedUnsupported: discovery.SkippedUnsupported,
	}
	for _, found := range discovery.Models {
		model := newDiscoveredChannelModel(channel.ID, found)
		if _, err := store.CreateModel(r.Context(), d.DB, model); err != nil {
			if errors.Is(err, store.ErrModelRequestExists) {
				result.SkippedExisting++
				continue
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		result.Created++
	}

	writeJSON(w, http.StatusOK, result)
}

func createChannelModelsBatchAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	channelID := pathParam(r, "id")
	if _, err := store.GetChannel(r.Context(), d.DB, channelID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var body struct {
		Models []discoveredChannelModel `json:"models"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.Models) == 0 || len(body.Models) > channelModelDiscoveryModelLimit {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}

	result := channelModelBatchResponse{Requested: len(body.Models)}
	seen := make(map[string]struct{}, len(body.Models))
	normalized := make([]discoveredChannelModel, 0, len(body.Models))
	for _, candidate := range body.Models {
		candidate.RequestID = strings.TrimSpace(candidate.RequestID)
		candidate.Label = strings.TrimSpace(candidate.Label)
		candidate.Description = strings.TrimSpace(candidate.Description)
		candidate.Kind = strings.ToLower(strings.TrimSpace(candidate.Kind))
		if candidate.RequestID == "" {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		if candidate.Label == "" {
			candidate.Label = candidate.RequestID
		}
		if candidate.Kind == "" {
			candidate.Kind = "chat"
		}
		if candidate.Kind != "chat" && candidate.Kind != "image" && candidate.Kind != "embedding" {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		key := strings.ToLower(candidate.RequestID)
		if _, exists := seen[key]; exists {
			result.SkippedDuplicate++
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, candidate)
	}

	for _, candidate := range normalized {
		if _, err := store.CreateModel(r.Context(), d.DB, newDiscoveredChannelModel(channelID, candidate)); err != nil {
			if errors.Is(err, store.ErrModelRequestExists) {
				result.SkippedExisting++
				continue
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		result.Created++
	}
	writeJSON(w, http.StatusCreated, result)
}

func newDiscoveredChannelModel(channelID string, found discoveredChannelModel) store.Model {
	return store.Model{
		ChannelID:   channelID,
		Kind:        found.Kind,
		RequestID:   found.RequestID,
		Label:       found.Label,
		Description: found.Description,
		Enabled:     true,
		ToolMode:    "native",
		Vision:      true,
		Stream:      true,
		Currency:    "USD",
	}
}

func discoverChannelModels(ctx context.Context, channel *store.Channel) (channelModelDiscovery, error) {
	if channel == nil {
		return channelModelDiscovery{}, errors.New("channel required")
	}
	switch strings.ToLower(strings.TrimSpace(channel.Type)) {
	case "openai":
		return discoverOpenAIChannelModels(ctx, channel)
	case "claude", "anthropic":
		return discoverAnthropicChannelModels(ctx, channel)
	case "gemini", "google":
		return discoverGeminiChannelModels(ctx, channel)
	default:
		return channelModelDiscovery{}, errors.New("unsupported channel type")
	}
}

type channelModelAccumulator struct {
	result channelModelDiscovery
	seen   map[string]struct{}
}

func newChannelModelAccumulator() *channelModelAccumulator {
	return &channelModelAccumulator{
		result: channelModelDiscovery{Models: []discoveredChannelModel{}},
		seen:   make(map[string]struct{}),
	}
}

func (a *channelModelAccumulator) add(requestID, label, description, kind string, supported bool) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	key := strings.ToLower(requestID)
	if _, exists := a.seen[key]; exists {
		return nil
	}
	if a.result.Discovered >= channelModelDiscoveryModelLimit {
		return errChannelModelDiscoveryTooLarge
	}
	a.seen[key] = struct{}{}
	a.result.Discovered++
	if !supported {
		a.result.SkippedUnsupported++
		return nil
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = requestID
	}
	a.result.Models = append(a.result.Models, discoveredChannelModel{
		RequestID:   requestID,
		Label:       label,
		Description: strings.TrimSpace(description),
		Kind:        kind,
	})
	return nil
}

func discoverOpenAIChannelModels(ctx context.Context, channel *store.Channel) (channelModelDiscovery, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(channel.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := fetchChannelModelJSON(ctx, baseURL+"/models", channel, &response); err != nil {
		return channelModelDiscovery{}, err
	}

	accumulator := newChannelModelAccumulator()
	for _, item := range response.Data {
		kind, supported := classifyOpenAIModel(item.ID)
		if err := accumulator.add(item.ID, item.ID, "", kind, supported); err != nil {
			return channelModelDiscovery{}, err
		}
	}
	return accumulator.result, nil
}

func discoverAnthropicChannelModels(ctx context.Context, channel *store.Channel) (channelModelDiscovery, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(channel.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	nextAfterID := ""
	accumulator := newChannelModelAccumulator()
	for page := 0; page < channelModelDiscoveryPageLimit; page++ {
		endpoint, err := channelModelEndpoint(baseURL+"/v1/models", map[string]string{
			"limit":    "1000",
			"after_id": nextAfterID,
		})
		if err != nil {
			return channelModelDiscovery{}, err
		}
		var response struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := fetchChannelModelJSON(ctx, endpoint, channel, &response); err != nil {
			return channelModelDiscovery{}, err
		}
		for _, item := range response.Data {
			if err := accumulator.add(item.ID, item.DisplayName, "", "chat", true); err != nil {
				return channelModelDiscovery{}, err
			}
		}
		if !response.HasMore {
			return accumulator.result, nil
		}
		nextAfterID = strings.TrimSpace(response.LastID)
		if nextAfterID == "" {
			return channelModelDiscovery{}, errors.New("anthropic model pagination omitted last_id")
		}
	}
	return channelModelDiscovery{}, errChannelModelDiscoveryTooLarge
}

func discoverGeminiChannelModels(ctx context.Context, channel *store.Channel) (channelModelDiscovery, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(channel.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	nextPageToken := ""
	accumulator := newChannelModelAccumulator()
	for page := 0; page < channelModelDiscoveryPageLimit; page++ {
		endpoint, err := channelModelEndpoint(baseURL+"/v1beta/models", map[string]string{
			"pageSize":  "1000",
			"pageToken": nextPageToken,
		})
		if err != nil {
			return channelModelDiscovery{}, err
		}
		var response struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				Description                string   `json:"description"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := fetchChannelModelJSON(ctx, endpoint, channel, &response); err != nil {
			return channelModelDiscovery{}, err
		}
		for _, item := range response.Models {
			requestID := strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
			kind, supported := classifyGeminiModel(requestID, item.SupportedGenerationMethods)
			if err := accumulator.add(requestID, item.DisplayName, item.Description, kind, supported); err != nil {
				return channelModelDiscovery{}, err
			}
		}
		nextPageToken = strings.TrimSpace(response.NextPageToken)
		if nextPageToken == "" {
			return accumulator.result, nil
		}
	}
	return channelModelDiscovery{}, errChannelModelDiscoveryTooLarge
}

func channelModelEndpoint(rawURL string, values map[string]string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func fetchChannelModelJSON(ctx context.Context, endpoint string, channel *store.Channel, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build model discovery request: %w", err)
	}
	request.Header.Set("accept", "application/json")
	switch strings.ToLower(strings.TrimSpace(channel.Type)) {
	case "openai":
		if channel.APIKey != "" {
			request.Header.Set("authorization", "Bearer "+channel.APIKey)
		}
	case "claude", "anthropic":
		if channel.APIKey != "" {
			request.Header.Set("x-api-key", channel.APIKey)
		}
		request.Header.Set("anthropic-version", "2023-06-01")
	case "gemini", "google":
		if channel.APIKey != "" {
			request.Header.Set("x-goog-api-key", channel.APIKey)
		}
	}

	response, err := channelModelDiscoveryHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("model discovery request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("model discovery returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, channelModelDiscoveryResponseSize+1))
	if err != nil {
		return fmt.Errorf("read model discovery response: %w", err)
	}
	if len(raw) > channelModelDiscoveryResponseSize {
		return errors.New("model discovery response too large")
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode model discovery response: %w", err)
	}
	return nil
}

func classifyOpenAIModel(requestID string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(requestID))
	if id == "" {
		return "", false
	}
	for _, marker := range []string{
		"moderation", "whisper", "transcri", "realtime", "speech", "tts", "audio", "sora", "video",
	} {
		if strings.Contains(id, marker) {
			return "", false
		}
	}
	if strings.Contains(id, "embedding") || strings.HasPrefix(id, "embed-") {
		return "embedding", true
	}
	if strings.HasPrefix(id, "dall-e") || strings.HasPrefix(id, "gpt-image-") || strings.Contains(id, "image-generation") {
		return "image", true
	}
	return "chat", true
}

func classifyGeminiModel(requestID string, methods []string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(requestID))
	if id == "" {
		return "", false
	}
	for _, marker := range []string{"tts", "audio", "live", "aqa"} {
		if strings.Contains(id, marker) {
			return "", false
		}
	}
	methodSet := make(map[string]bool, len(methods))
	for _, method := range methods {
		methodSet[strings.ToLower(strings.TrimSpace(method))] = true
	}
	isImage := strings.HasPrefix(id, "imagen-") || strings.Contains(id, "-image-") ||
		strings.HasSuffix(id, "-image") || strings.Contains(id, "image-generation")
	if isImage && (methodSet["generatecontent"] || methodSet["predict"]) {
		return "image", true
	}
	if methodSet["generatecontent"] {
		return "chat", true
	}
	// Aivory's configured embedding path currently speaks the OpenAI-compatible
	// /embeddings protocol, not Gemini's embedContent wire format.
	return "", false
}
