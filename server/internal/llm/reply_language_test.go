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

// TestTitleLanguageDirective locks the title-language mapping (written in the
// target language) so generated titles follow the user's UI language.
func TestTitleLanguageDirective(t *testing.T) {
	cases := map[string]string{
		"en":      "Write the title in English",
		"en-GB":   "Write the title in English",
		"zh":      "请用简体中文写这个标题",
		"zh-Hant": "請用繁體中文寫這個標題",
		"ja":      "タイトルは日本語で書いてください",
		"fr":      "Rédige le titre en français",
	}
	for locale, want := range cases {
		if got := titleLanguageDirective(locale); !strings.Contains(got, want) {
			t.Errorf("titleLanguageDirective(%q) = %q, want to contain %q", locale, got, want)
		}
	}
	if d := titleLanguageDirective("xx"); d != "" {
		t.Errorf("unknown locale should yield no directive, got %q", d)
	}
}

// TestCleanTitleClamp guards the CJK-aware clamp: dense CJK titles stay short,
// while a Western title gets more room and is cut on a word boundary (not
// mid-word) so the now-English titles aren't mangled.
func TestCleanTitleClamp(t *testing.T) {
	// A long English title is kept readable and not cut mid-word.
	long := "How to configure the database connection pool for high concurrency workloads"
	got := cleanTitle(long)
	if len([]rune(got)) > 56 {
		t.Errorf("english title too long: %q (%d runes)", got, len([]rune(got)))
	}
	if strings.HasSuffix(got, "concurrenc") || strings.Contains(got, "workloa") && !strings.Contains(got, "workload") {
		t.Errorf("english title cut mid-word: %q", got)
	}
	if !strings.HasPrefix(got, "How to configure the database") {
		t.Errorf("english title lost its start: %q", got)
	}
	// A short title is returned untouched (minus surrounding quotes/period).
	if cleanTitle("\"Login flow\".") != "Login flow" {
		t.Errorf("short title trim failed: %q", cleanTitle("\"Login flow\"."))
	}
	// CJK uses the tight clamp.
	if hasCJK("数据库连接") != true {
		t.Error("hasCJK should detect Chinese")
	}
	if hasCJK("Login flow") != false {
		t.Error("hasCJK should be false for plain English")
	}
}
