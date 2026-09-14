package llm

import (
	"encoding/json"
	"testing"

	"aivory/server/internal/store"
)

func TestImageEditMetadataRoundTripAndProviderIsolation(t *testing.T) {
	edit := &ImageEditRequest{BaseArtifactID: "chosen-image", MaskFileID: "selected-mask"}
	input, _ := json.Marshal(edit)
	blocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "replace the sky"}, {Kind: "image_edit", Input: input}})
	got, err := ImageEditFromBlocks(blocks)
	if err != nil || *got != *edit {
		t.Fatalf("lost retry metadata: %+v %v", got, err)
	}
	history := storeToUnified([]store.Message{{Role: "user", Blocks: blocks}}, "openai", "image", false)
	if len(history) != 1 || len(history[0].Blocks) != 1 || history[0].Blocks[0].Kind != "text" {
		t.Fatalf("internal mask metadata leaked into history: %+v", history)
	}
	if _, err := ImageEditFromBlocks(json.RawMessage(`[{"kind":"image_edit","input":{}}]`)); err == nil {
		t.Fatal("malformed persisted edit accepted")
	}
	if edit, err := ImageEditFromBlocks(json.RawMessage(`[{"kind":"text","text":"ordinary turn"}]`)); err != nil || edit != nil {
		t.Fatalf("ordinary turn changed: %v %v", edit, err)
	}
}

func TestSupportsImageMaskEdit(t *testing.T) {
	model := &store.Model{Kind: "image", Enabled: true, RequestID: "gpt-image-2"}
	channel := &store.Channel{Type: "openai", Enabled: true}
	if !SupportsImageMaskEdit(model, channel) {
		t.Fatal("OpenAI image edit unavailable")
	}
	channel.Type = "google"
	if SupportsImageMaskEdit(model, channel) {
		t.Fatal("Gemini must not claim OpenAI mask support")
	}
	channel.Type = "openai"
	model.RequestID = "dall-e-3"
	if SupportsImageMaskEdit(model, channel) {
		t.Fatal("DALL-E 3 does not support edits")
	}
}
