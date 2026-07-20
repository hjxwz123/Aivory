package llm

import (
	"encoding/json"
	"strings"
)

const nonVisionImagePlaceholder = "[Image omitted because the selected model does not support image input.]"

// stripImageBlocks returns an independent copy of history that is safe to send
// to a text-only model. Stored/UI history remains unchanged so a later switch
// back to a vision-capable model can still use the original images.
func stripImageBlocks(history []UnifiedMessage) []UnifiedMessage {
	out := make([]UnifiedMessage, len(history))
	for i, message := range history {
		filtered := UnifiedMessage{Role: message.Role}
		affected := false

		if message.Blocks != nil {
			filtered.Blocks = make([]UnifiedBlock, 0, len(message.Blocks))
		}
		for _, block := range message.Blocks {
			if unifiedBlockIsImage(block) {
				affected = true
				continue
			}
			filtered.Blocks = append(filtered.Blocks, cloneUnifiedBlock(block))
		}

		if message.Attachments != nil {
			filtered.Attachments = make([]Attachment, 0, len(message.Attachments))
		}
		for _, attachment := range message.Attachments {
			if attachmentIsImage(attachment) {
				affected = true
				continue
			}
			filtered.Attachments = append(filtered.Attachments, attachment)
		}

		// Provider-native history can itself contain image_url/input_image,
		// Anthropic image blocks, or Gemini inlineData. Never replay native raw
		// for an image-affected turn, because doing so would bypass block filtering.
		if nativeRawContainsImage(message.Raw) {
			affected = true
		}
		if !affected {
			filtered.Raw = append(json.RawMessage(nil), message.Raw...)
		}

		// A contentless user/model turn is invalid for Anthropic and Gemini and
		// can also collapse adjacent roles. Preserve an image-only turn as plain
		// text while leaving mixed text/image turns otherwise untouched.
		if affected && strings.TrimSpace(renderBlocksAsText(filtered.Blocks)) == "" {
			filtered.Blocks = append(filtered.Blocks, UnifiedBlock{
				Kind: "text",
				Text: nonVisionImagePlaceholder,
			})
		}

		out[i] = filtered
	}
	return out
}

func cloneUnifiedBlock(block UnifiedBlock) UnifiedBlock {
	block.Input = append(json.RawMessage(nil), block.Input...)
	block.Artifacts = append([]ArtifactRef(nil), block.Artifacts...)
	return block
}

func unifiedBlockIsImage(block UnifiedBlock) bool {
	return strings.EqualFold(strings.TrimSpace(block.Kind), "image") || isImageMIME(block.MimeType)
}

func attachmentIsImage(attachment Attachment) bool {
	return strings.EqualFold(strings.TrimSpace(attachment.Kind), "image") || isImageMIME(attachment.MimeType)
}

func isImageMIME(mimeType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/")
}

// nativeRawContainsImage recognizes the four native history shapes supported
// by this package. It deliberately inspects structured JSON rather than doing a
// substring search, so ordinary text that mentions "image_url" is preserved.
func nativeRawContainsImage(raw json.RawMessage) bool {
	if len(raw) <= 2 {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return jsonValueContainsImage(value)
}

func jsonValueContainsImage(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if jsonValueContainsImage(item) {
				return true
			}
		}
	case map[string]any:
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "_", ""))
			switch normalizedKey {
			case "imageurl", "inputimage", "outputimage":
				return true
			case "inlinedata":
				// The application only persists Gemini inlineData for image input.
				// Treat the protocol field itself as media even if a malformed legacy
				// record omitted mimeType.
				return true
			case "type":
				if kind, ok := child.(string); ok && nativeImageType(kind) {
					return true
				}
			case "mimetype", "mediatype":
				if mimeType, ok := child.(string); ok && isImageMIME(mimeType) {
					return true
				}
			}
			if data, ok := child.(string); ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(data)), "data:image/") {
				return true
			}
			if jsonValueContainsImage(child) {
				return true
			}
		}
	}
	return false
}

func nativeImageType(kind string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(kind), "-", "_"))
	switch normalized {
	case "image", "image_url", "input_image", "output_image", "image_generation_call":
		return true
	default:
		return false
	}
}
