package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Regression for the "long section" bug: a hit deep inside a long section must
// be present in the returned snippet, not replaced by the section head.
func TestExpandHitWindowsOnDeepChild(t *testing.T) {
	head := strings.Repeat("前言概述说明。", 400) // long lead-in (~8KB)
	hit := "甲乙98：目标位置的完整内容。"
	parent := head + hit + strings.Repeat("后续补充说明。", 200)
	child := "[模块三 > 特殊业务]\n" + hit

	out := expandHit(parent, child, 600)
	if !strings.Contains(out, "甲乙98") {
		t.Fatalf("expandHit dropped the deep hit; got: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("expandHit produced invalid UTF-8: %q", out)
	}
	if len(out) > 600+64 {
		t.Fatalf("expandHit window far over budget: %d bytes", len(out))
	}
}

// When the child lies past the parent's truncation it isn't found in the parent;
// expandHit must fall back to the child itself (which IS the hit).
func TestExpandHitFallsBackToChild(t *testing.T) {
	parent := strings.Repeat("章节开头内容。", 50) // does NOT contain the hit
	child := "[标题]\n甲乙98 落在被截断 parent 之外的内容。"
	out := expandHit(parent, child, 400)
	if !strings.Contains(out, "甲乙98") {
		t.Fatalf("expandHit should fall back to the child; got: %q", out)
	}
}

func TestSnippetOfRuneSafe(t *testing.T) {
	s := strings.Repeat("增值税", 100) // 3-byte runes
	if out := snippetOf(s, 50); !utf8.ValidString(out) {
		t.Fatalf("snippetOf produced invalid UTF-8: %q", out)
	}
}

func TestTruncateAtRuneSafe(t *testing.T) {
	s := strings.Repeat("会计处理", 100)
	if out := truncateAt(s, 50); !utf8.ValidString(out) { // 50 is mid-rune
		t.Fatalf("truncateAt produced invalid UTF-8: %q", out)
	}
}

// Merging two model-group searches must not double-count a chunk that somehow
// appears in both.
func TestAppendUniqueCandidates(t *testing.T) {
	a := []retrievalCandidate{{chunkID: "x"}, {chunkID: "y"}}
	b := []retrievalCandidate{{chunkID: "y"}, {chunkID: "z"}}
	got := appendUniqueCandidates(a, b)
	if len(got) != 3 {
		t.Fatalf("expected 3 unique, got %d (%+v)", len(got), got)
	}
	ids := map[string]int{}
	for _, c := range got {
		ids[c.chunkID]++
	}
	for _, id := range []string{"x", "y", "z"} {
		if ids[id] != 1 {
			t.Fatalf("id %q appears %d times, want 1", id, ids[id])
		}
	}
	// Empty src is a no-op.
	if got := appendUniqueCandidates(a, nil); len(got) != 2 {
		t.Fatalf("appendUnique with nil src changed dst: %d", len(got))
	}
}

func TestFuseReciprocalRankUsesDeterministicKeywordTieBreak(t *testing.T) {
	got := fuseReciprocalRank([]retrievalCandidate{
		{chunkID: "z", sim: 0.9, bm: 0},
		{chunkID: "a", sim: 0.9, bm: 2},
	})
	if len(got) != 2 || got[0].chunkID != "a" {
		t.Fatalf("keyword tie-break should prefer the exact lexical hit: %+v", got)
	}
}

func TestMergeRetrievedSnippetsContinuesPastOverlappingRank(t *testing.T) {
	got := mergeRetrievedSnippets([][]Snippet{
		{{ID: "a"}, {ID: "b"}},
		{{ID: "b"}, {ID: "a"}, {ID: "c"}},
	}, 3, false)
	if len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("overlapping rewritten results lost the later unique hit: %+v", got)
	}
}

func TestBreadcrumbHelpers(t *testing.T) {
	child := "[模块三 > 特殊业务]\n甲乙98 内容"
	if got := stripBreadcrumb(child); got != "甲乙98 内容" {
		t.Fatalf("stripBreadcrumb = %q", got)
	}
	if got := breadcrumbOf(child); got != "[模块三 > 特殊业务]" {
		t.Fatalf("breadcrumbOf = %q", got)
	}
	if got := stripBreadcrumb("no breadcrumb"); got != "no breadcrumb" {
		t.Fatalf("stripBreadcrumb(plain) = %q", got)
	}
	if got := breadcrumbOf("no breadcrumb"); got != "" {
		t.Fatalf("breadcrumbOf(plain) = %q", got)
	}
}
