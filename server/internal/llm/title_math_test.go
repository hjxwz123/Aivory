package llm

import (
	"strings"
	"testing"
)

func TestClipTitleUsesReadableMathContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "inline formula",
			input: `Solve \(x^2 = 4\) now`,
			want:  `Solve x^2 = 4 now`,
		},
		{
			name:  "block formula",
			input: "Compute:\n\\[ \\sum_{i=1}^n i \\]",
			want:  `Compute: \sum_{i=1}^n i`,
		},
		{
			name:  "ordinary title",
			input: "  Database\n\tconnection   pooling  ",
			want:  "Database connection pooling",
		},
		{
			name:  "truncate after formula cleanup",
			input: `\(` + strings.Repeat("界", 30) + `\) trailing`,
			want:  strings.Repeat("界", 28),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clipTitle(tt.input); got != tt.want {
				t.Fatalf("clipTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestImageConversationTitleUsesFilenameAndLocalizedFallback(t *testing.T) {
	if got := imageConversationTitle([]Attachment{{Filename: "Quarterly dashboard.png", Kind: "image"}}, "en"); got != "Quarterly dashboard" {
		t.Fatalf("filename title = %q", got)
	}
	if got := imageConversationTitle([]Attachment{{Filename: ".png", MimeType: "image/png"}}, "zh-CN"); got != "图片对话" {
		t.Fatalf("localized fallback = %q", got)
	}
	if got := imageConversationTitle(nil, "fr-FR"); got != "Conversation sur une image" {
		t.Fatalf("missing attachment fallback = %q", got)
	}
}

func TestImageOnlyTitleSourceIsBounded(t *testing.T) {
	if got := imageOnlyTitleSource("   "); got != "" {
		t.Fatalf("empty answer source = %q", got)
	}
	source := imageOnlyTitleSource(strings.Repeat("界", 1300))
	if !strings.HasSuffix(source, strings.Repeat("界", 1200)) || strings.Contains(source, strings.Repeat("界", 1201)) {
		t.Fatalf("answer source was not bounded to 1200 runes")
	}
}

func TestTitleMathContentToPlainTextMatchesComposerBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "inline dollar math without treating currency as math",
			input: `The price is $100 and tax is $20, while $x^2$ is math.`,
			want:  `The price is $100 and tax is $20, while x^2 is math.`,
		},
		{
			name:  "block dollar math",
			input: `Compute $$\sum_{i=1}^n i$$ now`,
			want:  `Compute \sum_{i=1}^n i now`,
		},
		{
			name:  "shell variables and embedded dollar pairs",
			input: `Use $PATH:$HOME, run $HOME/$USER, and keep prefix$x$ plus $y$suffix literal.`,
			want:  `Use $PATH:$HOME, run $HOME/$USER, and keep prefix$x$ plus $y$suffix literal.`,
		},
		{
			name:  "inline fenced and indented code stay literal",
			input: "`\\(x\\)`\n```txt\n\\[y\\]\n```\n~~~txt\n\\(w\\)\n~~~\n    \\(q\\)\nOutside \\(z\\)",
			want:  "`\\(x\\)`\n```txt\n\\[y\\]\n```\n~~~txt\n\\(w\\)\n~~~\n    \\(q\\)\nOutside z",
		},
		{
			name:  "escaped delimiters stay literal",
			input: `Keep \\(x\\) and \\[y\\] literal`,
			want:  `Keep \\(x\\) and \\[y\\] literal`,
		},
		{
			name:  "unmatched nested and empty delimiters stay literal",
			input: `literal \( and \(  \), plus \[ \]`,
			want:  `literal \( and \(  \), plus \[ \]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := titleMathContentToPlainText(tt.input); got != tt.want {
				t.Fatalf("titleMathContentToPlainText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
