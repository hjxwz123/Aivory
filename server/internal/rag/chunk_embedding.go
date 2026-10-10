package rag

import (
	"strings"
	"unicode/utf8"
)

// A UTF-8 byte ceiling is more conservative than estimateTokens for dense code,
// OCR and non-CJK scripts. Keep headroom below the usual 8192-token limit.
const embeddingChunkMaxBytes = 6 * 1024

// limitEmbeddingChildren leaves ordinary chunks intact and splits oversized
// protected atoms, including their overlap and heading prefix. It must run
// before embedding and persistence, never inside Embed's one-vector-per-input
// contract. Unknown providers use the conservative default byte ceiling.
func limitEmbeddingChildren(parents []parentChunk, em Embedder) int {
	maxBytes := embeddingChunkMaxBytes
	if limiter, ok := em.(interface{ maxTokensPerInput() int }); ok {
		if limit := limiter.maxTokensPerInput(); limit > 0 {
			maxBytes = min(maxBytes, limit-limit/4)
		}
	}
	extra := 0
	for i := range parents {
		var children []string
		for j, child := range parents[i].Children {
			if len(child) <= maxBytes {
				if children != nil {
					children = append(children, child)
				}
				continue
			}
			if children == nil {
				children = append(make([]string, 0, len(parents[i].Children)+1), parents[i].Children[:j]...)
			}
			parts := splitEmbeddingChild(child, parents[i].Breadcrumb, maxBytes)
			children = append(children, parts...)
			extra += len(parts) - 1
		}
		if children != nil {
			parents[i].Children = children
		}
	}
	return extra
}

func splitEmbeddingChild(child, breadcrumb string, maxBytes int) []string {
	prefix := ""
	if breadcrumb != "" {
		label := "[" + breadcrumb + "]\n"
		// Repeat ordinary headings for context. An oversized heading is itself
		// split with the text instead of being dropped or exhausting every chunk.
		if len(label) <= maxBytes/4 && strings.HasPrefix(child, label) {
			prefix = label
			child = child[len(label):]
		}
	}
	target := min(maxBytes-len(prefix), max(childTargetChars, utf8.UTFMax))
	parts := splitEmbeddingText(child, target)
	for i := range parts {
		parts[i] = prefix + parts[i]
	}
	return parts
}

// splitEmbeddingText prefers row/line boundaries, then sentence/word boundaries.
// A single oversized row or OCR paragraph falls back to a rune-safe hard cut.
// Slices preserve all source text, including whitespace at chunk boundaries.
func splitEmbeddingText(text string, maxBytes int) []string {
	var parts []string
	for len(text) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			_, cut = utf8.DecodeRuneInString(text)
		}
		if line := strings.LastIndexByte(text[:cut], '\n'); line >= cut/2 {
			cut = line + 1
		} else if boundary := strings.LastIndexAny(text[:cut], ".!?;\u3002\uff01\uff1f\uff1b \t"); boundary >= cut/2 {
			_, size := utf8.DecodeRuneInString(text[boundary:])
			cut = boundary + size
		}
		part := text[:cut]
		if strings.TrimSpace(part) != "" {
			parts = append(parts, part)
		}
		text = text[cut:]
	}
	if strings.TrimSpace(text) != "" {
		parts = append(parts, text)
	}
	return parts
}
