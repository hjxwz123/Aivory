package rag

import (
	"strings"
	"testing"
)

func containsTok(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// CJK runs are segmented into overlapping bigrams plus embedded digits, so a
// mixed-script identifier buried in a spaceless phrase remains matchable.
func TestTokenizeCJKBigrams(t *testing.T) {
	got := tokenize("甲乙98及相关内容")
	for _, want := range []string{"甲乙", "98", "相关", "内容"} {
		if !containsTok(got, want) {
			t.Fatalf("tokenize CJK missing %q in %v", want, got)
		}
	}
}

// Latin/alphanumeric tokenization must be byte-for-byte unchanged so existing
// (non-CJK) indexes and local embeddings keep matching.
func TestTokenizeLatinUnchanged(t *testing.T) {
	got := tokenize("Hello VAT_2024 world")
	want := []string{"Hello", "VAT_2024", "world"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Latin tokenize changed: got %v want %v", got, want)
	}
}

// A follow-up containing a mixed-script identifier must keyword-match the chunk
// that contains it even when the surrounding phrasing differs.
func TestKeywordScoreCJKReference(t *testing.T) {
	terms := tokenize(strings.ToLower("按照前述标准，重点说明甲乙98及相关内容"))
	hit := "后续部分。甲乙98：这里包含与该标识相关的完整内容。"
	miss := "开头部分：这里记录与目标无关的信息。"
	hitScore := keywordScore(terms, hit)
	missScore := keywordScore(terms, miss)
	if hitScore <= 0 {
		t.Fatalf("expected the referenced chunk to match across phrasings, got %v", hitScore)
	}
	if hitScore <= missScore {
		t.Fatalf("referenced chunk (%v) should outscore an unrelated chunk (%v)", hitScore, missScore)
	}
}
