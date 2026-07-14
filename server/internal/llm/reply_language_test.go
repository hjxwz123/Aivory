package llm

import (
	"strings"
	"testing"
)

// TestPromptLocaleKey locks the UI-locale → prompt-language folding, incl.
// region/case variants and the Traditional-before-generic-zh ordering.
func TestPromptLocaleKey(t *testing.T) {
	cases := map[string]string{
		"":        "en",
		"en":      "en",
		"en-US":   "en",
		"zh":      "zh",
		"zh-CN":   "zh",
		"zh-Hans": "zh",
		"zh-Hant": "zh-Hant",
		"zh-TW":   "zh-Hant",
		"zh-HK":   "zh-Hant",
		"ja":      "ja",
		"ja-JP":   "ja",
		"fr":      "fr",
		"fr-CA":   "fr",
		"xx-YY":   "en",
	}
	for locale, want := range cases {
		if got := promptLocaleKey(locale); got != want {
			t.Errorf("promptLocaleKey(%q) = %q, want %q", locale, got, want)
		}
	}
}

// TestComposeSystemPromptIsLocalized proves §4.8-L10N: the WHOLE authored prompt
// renders in the UI language, and the old "reply in X" directive is gone.
func TestComposeSystemPromptIsLocalized(t *testing.T) {
	// English (default) → English identity + trust header, no Chinese.
	en := composeSystemPrompt(systemPromptOpts{ModelLabel: "GPT-X", Locale: "en"})
	if !strings.Contains(en, "You are GPT-X") || !strings.Contains(en, "Trust boundary") {
		t.Errorf("english prompt not in English:\n%s", en)
	}
	// Chinese UI → the whole prompt is Chinese (identity + trust header), not English.
	zh := composeSystemPrompt(systemPromptOpts{ModelLabel: "GPT-X", Locale: "zh"})
	if !strings.Contains(zh, "你是 GPT-X") || !strings.Contains(zh, "信任边界") {
		t.Errorf("zh prompt not localized:\n%s", zh)
	}
	if strings.Contains(zh, "Trust boundary") || strings.Contains(zh, "You are") {
		t.Errorf("zh prompt leaked English segments:\n%s", zh)
	}
	// The removed reply-language directive must appear in NO locale.
	for _, loc := range []string{"", "en", "zh", "zh-Hant", "ja", "fr"} {
		sys := composeSystemPrompt(systemPromptOpts{ModelLabel: "GPT-X", Locale: loc})
		for _, banned := range []string{"Always reply in", "请始终使用简体中文回复", "請一律使用繁體中文回覆", "常に日本語で返信", "Réponds toujours en"} {
			if strings.Contains(sys, banned) {
				t.Errorf("locale %q still carries the removed reply directive %q", loc, banned)
			}
		}
	}
	// Traditional / Japanese / French each land in their own language.
	if !strings.Contains(composeSystemPrompt(systemPromptOpts{Locale: "zh-Hant"}), "信任邊界") {
		t.Error("zh-Hant prompt not in Traditional Chinese")
	}
	if !strings.Contains(composeSystemPrompt(systemPromptOpts{Locale: "ja"}), "信頼境界") {
		t.Error("ja prompt not in Japanese")
	}
	if !strings.Contains(composeSystemPrompt(systemPromptOpts{Locale: "fr"}), "Frontière de confiance") {
		t.Error("fr prompt not in French")
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
