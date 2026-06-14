package llm

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"

	"aurelia/server/internal/store"
)

// moderationModelSystemPrompt instructs the dedicated moderation model to return
// a one-word verdict. Kept strict so parsing is trivial.
const moderationModelSystemPrompt = "You are a strict content-safety classifier for a chat assistant. " +
	"Decide whether the USER message below violates content policy — e.g. child sexual content, " +
	"instructions facilitating weapons/explosives or serious violence, self-harm instructions, " +
	"credible threats, doxxing, or illegal activity. Ordinary, benign requests are ALLOW. " +
	"Reply with EXACTLY one word: ALLOW or BLOCK. Output nothing else."

const defaultModerationMessage = "Your message was blocked by content moderation. Please rephrase and try again."

// moderatePrompt screens a single user prompt (no history) before generation.
// Returns (blocked, message). It honours the model's per-model toggle + mode:
//   - "keyword": match the admin keyword list (defeats basic leetspeak evasion).
//   - "model":   ask the configured moderation model for an ALLOW/BLOCK verdict;
//     if that model is unset or errors, it falls back to the keyword screen so
//     enabling moderation never silently disables protection — but an infra
//     error never hard-blocks a benign chat (fail-open on error).
func (o *Orchestrator) moderatePrompt(ctx context.Context, model *store.Model, userText, userID, convID, msgID string) (bool, string) {
	if model == nil || !model.ModerationEnabled || strings.TrimSpace(userText) == "" {
		return false, ""
	}
	mode := model.ModerationMode
	if mode == "" {
		mode = "keyword"
	}
	if mode == "model" {
		if blocked, decided := o.moderateByModel(ctx, userText, userID, convID, msgID); decided {
			if blocked {
				return true, o.moderationMessage()
			}
			return false, ""
		}
		// Moderation model unavailable/errored — fall through to keywords.
	}
	if kw := o.moderationKeywords(); len(kw) > 0 {
		if _, hit := matchKeyword(kw, userText); hit {
			return true, o.moderationMessage()
		}
	}
	return false, ""
}

// moderateByModel runs the configured moderation model. The second return value
// reports whether a verdict was actually obtained (false ⇒ caller should fall
// back / fail open).
func (o *Orchestrator) moderateByModel(ctx context.Context, userText, userID, convID, msgID string) (blocked bool, decided bool) {
	if o.task == nil {
		return false, false
	}
	var modelID string
	if raw, err := store.GetSetting(o.db, "moderation_model_id"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &modelID)
	}
	if strings.TrimSpace(modelID) == "" {
		return false, false
	}
	// When the admin has configured violation categories, screen specifically
	// against them; otherwise fall back to the generic safety prompt.
	system := moderationModelSystemPrompt
	if cats := o.moderationCategories(); len(cats) > 0 {
		system = "You are a strict content-safety classifier for a chat assistant. Decide whether the USER message " +
			"falls into any of these prohibited categories: " + strings.Join(cats, "; ") + ". " +
			"If it clearly does, reply BLOCK. Otherwise reply ALLOW. " +
			"Reply with EXACTLY one word: ALLOW or BLOCK. Output nothing else."
	}
	verdict, err := o.task.Run(ctx, TaskModeration, userText, RunOpts{
		ModelID:         modelID,
		SystemPrompt:    system,
		UserID:          userID,
		ConversationID:  convID,
		MessageID:       msgID,
		MaxOutputTokens: 8,
	})
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("moderation model %q error (fail-open): %v", modelID, err)
		}
		return false, false
	}
	v := strings.ToUpper(strings.TrimSpace(verdict))
	return strings.Contains(v, "BLOCK"), true
}

// moderationKeywords reads the admin-managed keyword blocklist.
func (o *Orchestrator) moderationKeywords() []string {
	raw, err := store.GetSetting(o.db, "moderation_keywords")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var kw []string
	if json.Unmarshal(raw, &kw) != nil {
		return nil
	}
	return kw
}

// moderationCategories reads the admin-managed violation categories used to
// steer the moderation model (model mode only).
func (o *Orchestrator) moderationCategories() []string {
	raw, err := store.GetSetting(o.db, "moderation_categories")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var cats []string
	if json.Unmarshal(raw, &cats) != nil {
		return nil
	}
	out := cats[:0]
	for _, c := range cats {
		if strings.TrimSpace(c) != "" {
			out = append(out, strings.TrimSpace(c))
		}
	}
	return out
}

// moderationMessage returns the admin-customised block message, or a default.
func (o *Orchestrator) moderationMessage() string {
	if raw, err := store.GetSetting(o.db, "moderation_message"); err == nil && len(raw) > 0 {
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return defaultModerationMessage
}

// matchKeyword reports the first blocklist keyword found in text, matching both
// the raw lowercased text and a normalized form (leetspeak/spacing/punctuation
// folded) so the easiest evasions are caught.
func matchKeyword(keywords []string, text string) (string, bool) {
	low := strings.ToLower(text)
	norm := normalizeForModeration(text)
	for _, w := range keywords {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(w)) || (norm != "" && strings.Contains(norm, normalizeForModeration(w))) {
			return w, true
		}
	}
	return "", false
}

// normalizeForModeration lowercases, folds common leetspeak to letters, and
// strips everything except letters/digits.
func normalizeForModeration(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t", "@", "a", "$", "s").Replace(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
