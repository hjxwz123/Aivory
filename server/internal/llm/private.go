package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"aivory/server/internal/store"
	"github.com/google/uuid"
)

var ErrPrivateTools = errors.New("private_tools_disabled")

func enforcePrivateRequest(body map[string]any, req UnifiedChatRequest, provider string) {
	if !req.Private {
		return
	}
	for _, key := range []string{"tools", "tool_choice", "toolConfig", "tool_config", "functions", "function_call", "parallel_tool_calls", "mcp_servers", "container", "context_management", "previous_response_id", "conversation", "background", "metadata", "user", "safety_identifier", "prompt_cache_key", "prompt_cache_retention", "cachedContent", "cached_content", "cache_control"} {
		delete(body, key)
	}
	if provider == "openai" {
		body["store"] = false
	}
	stripPrivateCacheControl(body)
}

func stripPrivateCacheControl(value any) {
	switch node := value.(type) {
	case map[string]any:
		delete(node, "cache_control")
		for _, child := range node {
			stripPrivateCacheControl(child)
		}
	case []any:
		for _, child := range node {
			stripPrivateCacheControl(child)
		}
	case []map[string]any:
		for _, child := range node {
			stripPrivateCacheControl(child)
		}
	}
}

func privateProvider(registry *Registry, channelType string) (Provider, error) {
	provider, err := registry.Get(channelType)
	if err != nil {
		return nil, err
	}
	switch provider.(type) {
	case *OpenAIProvider:
		return &OpenAIProvider{}, nil
	case *AnthropicProvider:
		return &AnthropicProvider{}, nil
	case *GoogleProvider:
		return &GoogleProvider{}, nil
	default:
		return provider, nil
	}
}

func (o *Orchestrator) RunPrivate(ctx context.Context, userID string, model *store.Model, history []UnifiedMessage, emit func(SseEvent)) error {
	if len(history) == 0 || model == nil || !model.Enabled || model.Kind != "chat" || model.Fast {
		return errors.New("private_model_unavailable")
	}
	if model.ModerationEnabled {
		var prompt strings.Builder
		for _, message := range history {
			if message.Role == "user" {
				for _, block := range message.Blocks {
					if block.Kind == "text" {
						prompt.WriteString(block.Text)
						prompt.WriteByte('\n')
					}
				}
			}
		}
		blocked, err := o.moderatePrivate(ctx, userID, model, prompt.String())
		if err != nil {
			return err
		}
		if blocked {
			logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := store.LogUsageAnalytics(logCtx, o.db, store.UsageLog{UserID: userID, MessageID: "private_" + uuid.NewString(), ModelID: model.ID, ChannelID: model.ChannelID, Purpose: "chat", Status: "error", Error: "moderation_blocked"}); err != nil {
				return errors.New("private_billing_error")
			}
			return errors.New("private_moderation_blocked")
		}
	}
	result, credits, err := o.privateCall(ctx, userID, model, history, model.SystemPrompt, "chat", 0, emit)
	if err != nil {
		return err
	}
	for _, image := range result.GeneratedImages {
		if image.MimeType == "image/png" || image.MimeType == "image/jpeg" || image.MimeType == "image/webp" || image.MimeType == "image/gif" {
			emit(SseEvent{Type: "image", URL: "data:" + image.MimeType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)})
		}
	}
	emit(SseEvent{Type: "done", Usage: &result.Usage, Credits: credits, StopReason: result.StopReason})
	return nil
}

func (o *Orchestrator) moderatePrivate(ctx context.Context, userID string, model *store.Model, prompt string) (bool, error) {
	if strings.TrimSpace(prompt) == "" {
		return false, nil
	}
	if model.ModerationMode == "model" {
		var modelID string
		if raw, err := store.GetSetting(o.db, "moderation_model_id"); err == nil {
			_ = json.Unmarshal(raw, &modelID)
		}
		moderator, err := store.GetModel(ctx, o.db, modelID)
		if err == nil && moderator.Enabled && moderator.Kind == "chat" {
			system := moderationModelSystemPrompt
			if categories := o.moderationCategories(); len(categories) > 0 {
				system += " The administrator's prohibited categories are: " + strings.Join(categories, "; ")
			}
			result, _, callErr := o.privateCall(ctx, userID, moderator, []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: prompt}}}}, system, "task.moderation", moderationVerdictMaxOutputTokens, func(SseEvent) {})
			if callErr == nil {
				var verdict strings.Builder
				for _, block := range result.Blocks {
					if block.Kind == "text" {
						verdict.WriteString(block.Text)
					}
				}
				text := strings.ToUpper(strings.TrimSpace(verdict.String()))
				if strings.Contains(text, "BLOCK") {
					return true, nil
				}
				if text == "ALLOW" {
					return false, nil
				}
			} else if callErr.Error() != "private_provider_error" && callErr.Error() != "private_model_unavailable" {
				return false, callErr
			}
		}
	}
	_, blocked := matchKeyword(o.moderationKeywords(), prompt)
	return blocked, nil
}

func (o *Orchestrator) privateCall(ctx context.Context, userID string, model *store.Model, history []UnifiedMessage, system, purpose string, maxTokens int, emit func(SseEvent)) (*UnifiedResult, float64, error) {
	channel, err := store.GetChannel(ctx, o.db, model.ChannelID)
	if err != nil || !channel.Enabled {
		return nil, 0, errors.New("private_model_unavailable")
	}
	provider, err := privateProvider(o.reg, channel.Type)
	if err != nil {
		return nil, 0, errors.New("private_model_unavailable")
	}
	req := UnifiedChatRequest{
		Private: true, History: history, SystemPrompt: system, Stream: model.Stream,
		Model:       ModelInfo{ID: model.ID, RequestID: model.RequestID, Provider: channel.Type, Vision: model.Vision, BaseURL: channel.BaseURL, APIKey: channel.APIKey, APIFormat: channel.APIFormat},
		ExtraParams: model.ExtraParams, MaxOutputTokens: maxTokens, StrictMaxOutputTokens: maxTokens > 0,
	}
	operationID := "private_" + uuid.NewString()
	admission, message, err := o.reserveUsageBilling(ctx, userID, model, store.QuotaScopeModelChat, 1, estimateTurnUSD(*model, req), estimateTurnTokens(req), "private_chat", operationID)
	if err != nil || message != "" {
		return nil, 0, errors.New("private_quota_exceeded")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = o.releaseUsageBilling(releaseCtx, admission)
	}()
	recorder := newProviderRequestRecorder(channel.Type)
	recorder.captureBody = false
	providerCtx := contextWithProviderRequestRecorder(ctx, recorder)
	var emittedText bool
	result, providerErr := provider.Stream(providerCtx, req, &noopToolRunner{}, func(event SseEvent) {
		if event.Type == "text_delta" {
			emittedText = true
			emit(SseEvent{Type: "text_delta", Text: event.Text})
		} else if event.Type == "thinking_delta" {
			emit(SseEvent{Type: "thinking_delta", Text: event.Text})
		}
	})
	if result == nil {
		result = &UnifiedResult{}
		if providerErr == nil {
			providerErr = errors.New("empty response")
		}
	}
	if providerErr == nil && len(result.GeneratedImages) == 0 {
		hasText := false
		for _, block := range result.Blocks {
			if block.Kind == "text" && strings.TrimSpace(block.Text) != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			providerErr = errors.New("empty response")
		}
	}
	result.Usage = mergeProviderRequestUsage(result.Usage, recorder.snapshots())
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	usage := store.UsageLog{
		UserID: userID, MessageID: operationID, ModelID: model.ID, ChannelID: model.ChannelID, Purpose: purpose,
		InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens,
		CacheReadTokens: result.Usage.CacheReadTokens, CacheWriteTokens: result.Usage.CacheWriteTokens,
		Cost: computeCost(*model, result.Usage), Currency: model.Currency,
	}
	if providerErr != nil {
		usage.Status = "error"
		usage.Error = "provider_request_failed"
		if ctx.Err() != nil {
			usage.Error = "request_canceled"
		}
	}
	if providerErr == nil || usageHasValue(result.Usage) {
		admission.KeepReserved = true
		if err := store.RecordBillingUsage(logCtx, o.db, usage); err != nil {
			return nil, 0, errors.New("private_billing_error")
		}
		debit, err := o.settleUsageBilling(logCtx, admission, 1, usage.Cost, result.Usage.InputTokens+result.Usage.OutputTokens)
		if err != nil {
			usage.Status = "error"
			usage.Error = "billing_settlement_failed"
			_ = store.LogUsageAnalytics(logCtx, o.db, usage)
			return nil, 0, errors.New("private_billing_error")
		}
		usage.Credits = debit.Total
	}
	if err := store.LogUsageAnalytics(logCtx, o.db, usage); err != nil {
		return nil, 0, errors.New("private_billing_error")
	}
	if providerErr != nil {
		return nil, 0, errors.New("private_provider_error")
	}
	if !emittedText {
		for _, block := range result.Blocks {
			if block.Kind == "text" {
				emit(SseEvent{Type: "text_delta", Text: block.Text})
			}
		}
	}
	return result, usage.Credits, nil
}
