package llm

import (
	"strings"
	"testing"
)

// TestReplyLanguageDirective locks the locale → directive mapping (the directive
// is written IN the target language) and tolerates region/case variants.
func TestReplyLanguageDirective(t *testing.T) {
	cases := map[string]string{
		"en":      "Always reply in English",
		"en-US":   "Always reply in English",
		"zh":      "请始终使用简体中文回复",
		"zh-CN":   "请始终使用简体中文回复",
		"zh-Hant": "請一律使用繁體中文回覆",
		"zh-TW":   "請一律使用繁體中文回覆",
		"ja":      "常に日本語で返信してください",
		"fr":      "Réponds toujours en français",
	}
	for locale, want := range cases {
		got := replyLanguageDirective(locale)
		if !strings.Contains(got, want) {
			t.Errorf("replyLanguageDirective(%q) = %q, want it to contain %q", locale, got, want)
		}
	}
	if d := replyLanguageDirective(""); d != "" {
		t.Errorf("empty locale should yield no directive, got %q", d)
	}
	if d := replyLanguageDirective("xx-YY"); d != "" {
		t.Errorf("unknown locale should yield no directive, got %q", d)
	}
}

// TestComposeSystemPromptCarriesReplyLanguage proves the directive actually lands
// in the composed system prompt (so an English UI forces an English reply even
// when the model-level prompt is Chinese), and that an unknown locale omits it.
func TestComposeSystemPromptCarriesReplyLanguage(t *testing.T) {
	// English UI over a Chinese model-level prompt → the English directive is present.
	sys := composeSystemPrompt(systemPromptOpts{ModelSystem: "你是一个助手。", Locale: "en"})
	if !strings.Contains(sys, "Always reply in English") {
		t.Errorf("composed prompt missing English reply directive:\n%s", sys)
	}
	// Chinese UI → Chinese directive.
	sysZh := composeSystemPrompt(systemPromptOpts{Locale: "zh"})
	if !strings.Contains(sysZh, "请始终使用简体中文回复") {
		t.Errorf("composed prompt missing Chinese reply directive:\n%s", sysZh)
	}
	// No locale → no forced language line (the default prompt no longer hardcodes one).
	sysNone := composeSystemPrompt(systemPromptOpts{})
	if strings.Contains(sysNone, "Always reply in") || strings.Contains(sysNone, "请始终使用") {
		t.Errorf("expected no forced reply-language line without a locale:\n%s", sysNone)
	}
}
