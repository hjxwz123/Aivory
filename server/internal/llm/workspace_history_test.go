package llm

import (
	"encoding/json"
	"testing"

	"aivory/server/internal/store"
)

func textBlocks(text string) json.RawMessage {
	b, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: text}})
	return b
}

// §workspaces concurrent turns: B asks while A's answer is still streaming, so
// B's question chains under A's empty (status="streaming") assistant placeholder.
// storeToUnified must drop that in-flight pair so the provider never sees an empty
// assistant turn (Anthropic rejects it) nor two consecutive user turns.
func TestStoreToUnifiedDropsInflightPair(t *testing.T) {
	msgs := []store.Message{
		{Role: "user", Blocks: textBlocks("Q1"), Status: "complete"},
		{Role: "assistant", Blocks: textBlocks("A1"), Status: "complete"},
		{Role: "user", Blocks: textBlocks("A's question"), Status: "complete"},
		{Role: "assistant", Blocks: json.RawMessage("[]"), Status: "streaming"},
		{Role: "user", Blocks: textBlocks("B's question"), Status: "complete"},
	}
	out := storeToUnified(msgs, "anthropic")

	wantRoles := []string{"user", "assistant", "user"}
	if len(out) != len(wantRoles) {
		t.Fatalf("want %d messages, got %d", len(wantRoles), len(out))
	}
	for i, r := range wantRoles {
		if out[i].Role != r {
			t.Errorf("msg %d role = %q, want %q", i, out[i].Role, r)
		}
	}
	for _, m := range out {
		if m.Role == "assistant" && renderBlocksAsText(m.Blocks) == "" {
			t.Errorf("empty assistant leaked into provider history")
		}
	}
	if got := renderBlocksAsText(out[2].Blocks); got != "B's question" {
		t.Errorf("last user turn = %q, want %q", got, "B's question")
	}
}

// A completed-but-empty assistant (e.g. stopped before any output) is also dropped
// with its question, for the same empty-content reason.
func TestStoreToUnifiedDropsEmptyCompletedPair(t *testing.T) {
	msgs := []store.Message{
		{Role: "user", Blocks: textBlocks("Q1"), Status: "complete"},
		{Role: "assistant", Blocks: textBlocks("A1"), Status: "complete"},
		{Role: "user", Blocks: textBlocks("orphaned"), Status: "complete"},
		{Role: "assistant", Blocks: json.RawMessage("[]"), Status: "complete"},
	}
	out := storeToUnified(msgs, "anthropic")
	if len(out) != 2 {
		t.Fatalf("want 2 messages, got %d", len(out))
	}
	if out[len(out)-1].Role != "assistant" || renderBlocksAsText(out[len(out)-1].Blocks) != "A1" {
		t.Errorf("tail should be the last complete answer A1")
	}
}

// A normal, fully-complete history is passed through unchanged.
func TestStoreToUnifiedKeepsCompleteHistory(t *testing.T) {
	msgs := []store.Message{
		{Role: "user", Blocks: textBlocks("Q1"), Status: "complete"},
		{Role: "assistant", Blocks: textBlocks("A1"), Status: "complete"},
		{Role: "user", Blocks: textBlocks("Q2"), Status: "complete"},
	}
	if out := storeToUnified(msgs, "anthropic"); len(out) != 3 {
		t.Fatalf("want 3 messages, got %d", len(out))
	}
}

// An image-only assistant turn renders to empty TEXT but carries real media, so it
// must be kept.
func TestStoreToUnifiedKeepsImageOnlyAssistant(t *testing.T) {
	img, _ := json.Marshal([]UnifiedBlock{{Kind: "image", URL: "https://example/y.png"}})
	msgs := []store.Message{
		{Role: "user", Blocks: textBlocks("draw a cat"), Status: "complete"},
		{Role: "assistant", Blocks: img, Status: "complete"},
		{Role: "user", Blocks: textBlocks("now a dog"), Status: "complete"},
	}
	if out := storeToUnified(msgs, "anthropic"); len(out) != 3 {
		t.Fatalf("image-only assistant was dropped; want 3, got %d", len(out))
	}
}
