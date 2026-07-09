package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Content before the first heading (preamble: frontmatter / abstract / intro)
// must be kept as a section, not dropped.
func TestSplitByHeadingsKeepsPreamble(t *testing.T) {
	content := "导言：本文概述增值税特殊业务核算。\n\n# 模块一\n正文一。\n## 小节\n正文二。"
	secs := splitByHeadings(content)
	if len(secs) == 0 {
		t.Fatal("no sections produced")
	}
	if !strings.Contains(secs[0].body, "导言") {
		t.Fatalf("preamble should be the first section, got body=%q", secs[0].body)
	}
	if strings.TrimSpace(secs[0].breadcrumb) != "" {
		t.Fatalf("preamble breadcrumb should be empty, got %q", secs[0].breadcrumb)
	}
	all := ""
	for _, s := range secs {
		all += s.body
	}
	if !strings.Contains(all, "导言：本文概述增值税特殊业务核算") {
		t.Fatalf("preamble text dropped; sections=%+v", secs)
	}
}

// CJK token estimate counts Han runes ~1 token each, exceeding the old byte/4
// heuristic (which under-counted CJK at ~0.75 tokens/char).
func TestEstimateTokensCJK(t *testing.T) {
	cjk := "增值税会计核算方法论" // 10 Han runes = 30 bytes
	got := estimateTokens(cjk)
	if got < 9 || got > 12 {
		t.Fatalf("CJK estimate = %d, want ~10", got)
	}
	if naive := len(cjk) / 4; got <= naive {
		t.Fatalf("CJK estimate %d must exceed naive byte/4 = %d", got, naive)
	}
	// Pure ASCII is unchanged (byte/4).
	en := "the quick brown fox jumps" // 25 ASCII bytes
	if got := estimateTokens(en); got < 5 || got > 8 {
		t.Fatalf("ASCII estimate = %d, want ~6", got)
	}
	// Mixed: 2 CJK runes (案例) + "98 case" (7 ASCII bytes / 4 = 1) = 3.
	if got := estimateTokens("案例98 case"); got != 3 {
		t.Fatalf("mixed estimate = %d, want 3", got)
	}
}

func TestChunkerSplitsLongUnpunctuatedParagraph(t *testing.T) {
	long := strings.Repeat("测", childTargetChars*3)
	parents := chunkHierarchical(long)
	if len(parents) != 1 {
		t.Fatalf("parents = %d, want 1", len(parents))
	}
	if len(parents[0].Children) < 3 {
		t.Fatalf("children = %d, want split long paragraph", len(parents[0].Children))
	}
	for i, child := range parents[0].Children {
		if !utf8.ValidString(child) {
			t.Fatalf("child %d is not valid UTF-8", i)
		}
		if len(child) > childTargetChars+chunkOverlapChars+8 {
			t.Fatalf("child %d too large: %d", i, len(child))
		}
	}
}
