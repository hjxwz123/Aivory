package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

const defaultCompactionMediaInlineBytes int64 = 20 * 1024 * 1024

// Compaction media is persisted as metadata-only references without an item
// count cap. Hydration has a separate byte budget so a long conversation can
// retain every image reference without making a provider request unbounded.
var compactionMediaInlineBytes = envcfg.Int64(
	"AIVORY_LLM_COMPACTION_MEDIA_INLINE_BYTES",
	defaultCompactionMediaInlineBytes,
)

// CompactionMediaRef is a metadata-only pointer to an image whose source turn
// has moved behind the summary frontier. Binary data is re-authorized and read
// from server storage for each request; it is never embedded in summary_blocks.
type CompactionMediaRef struct {
	Kind      string `json:"kind"` // attachment | artifact
	ID        string `json:"id"`
	MessageID string `json:"message_id,omitempty"`
	Filename  string `json:"filename,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
}

func collectCompactionMediaRefs(messages []store.Message) []CompactionMediaRef {
	refs := make([]CompactionMediaRef, 0)
	for _, message := range messages {
		if message.Role == "user" {
			var attachments []Attachment
			if json.Unmarshal(message.Attachments, &attachments) == nil {
				for _, attachment := range attachments {
					if !attachmentIsImage(attachment) {
						continue
					}
					refs = appendRecentCompactionMediaRef(refs, CompactionMediaRef{
						Kind: "attachment", ID: strings.TrimSpace(attachment.ID), MessageID: message.ID,
						Filename: attachment.Filename, MimeType: attachment.MimeType,
					})
				}
			}
		}

		var blocks []UnifiedBlock
		if json.Unmarshal(message.Blocks, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Kind != "artifact" {
				continue
			}
			artifactID := strings.TrimSpace(block.FileRef)
			filename := strings.TrimSpace(block.Title)
			mimeType := strings.TrimSpace(block.MimeType)
			if len(block.Artifacts) > 0 {
				if artifactID == "" {
					artifactID = strings.TrimSpace(block.Artifacts[0].ID)
				}
				if filename == "" {
					filename = strings.TrimSpace(block.Artifacts[0].Filename)
				}
				if mimeType == "" {
					mimeType = strings.TrimSpace(block.Artifacts[0].MimeType)
				}
			}
			refs = appendRecentCompactionMediaRef(refs, CompactionMediaRef{
				Kind: "artifact", ID: artifactID, MessageID: message.ID,
				Filename: filename, MimeType: mimeType,
			})
		}
	}
	return refs
}

func compactionMediaMetadataText(refs []CompactionMediaRef, omitted bool) string {
	if len(refs) == 0 {
		return ""
	}
	// The per-field metadata setting is also the source of the aggregate
	// request budget. Do not silently promote a deliberately small administrator
	// value to 1024 tokens: compacted media metadata is optional context and must
	// remain within the configured bound even when the setting is used for a
	// smoke-test or a tightly constrained task model.
	maxTokens := compactionMetadataLimit() * 4
	if maxTokens <= 0 {
		maxTokens = 1
	}
	label := "compacted_media"
	if omitted {
		label += " omitted"
	}
	// Keep the persisted list complete, but only place the newest metadata in a
	// bounded request. The omitted count makes the truncation explicit and lets a
	// later turn still explain that older media exists.
	omittedCount := 0
	selected := make([]string, 0, len(refs))
	selectedTokens := 0
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		line := fmt.Sprintf("[%s kind=%s id=%s filename=%s mime=%s message_id=%s]\n",
			label, quoteCompactionMediaMeta(ref.Kind), quoteCompactionMediaMeta(ref.ID),
			quoteCompactionMediaMeta(ref.Filename), quoteCompactionMediaMeta(ref.MimeType),
			quoteCompactionMediaMeta(ref.MessageID))
		lineTokens := estimateTokens(line)
		if lineTokens > maxTokens || selectedTokens+lineTokens > maxTokens {
			omittedCount++
			continue
		}
		selected = append(selected, line)
		selectedTokens += lineTokens
	}
	if omittedCount > 0 {
		marker := fmt.Sprintf("[compacted_media_metadata omitted=%d]\n", omittedCount)
		markerTokens := estimateTokens(marker)
		for selectedTokens+markerTokens > maxTokens && len(selected) > 0 {
			// selected is newest-first. Make room by evicting the oldest retained
			// reference so the byte/token budget preserves the most recent media.
			last := len(selected) - 1
			selectedTokens -= estimateTokens(selected[last])
			selected = selected[:last]
		}
		if markerTokens <= maxTokens {
			selected = append(selected, marker)
		}
	}
	var metadata strings.Builder
	for i := len(selected) - 1; i >= 0; i-- {
		metadata.WriteString(selected[i])
	}
	return metadata.String()
}

func mergeCompactionMediaRefs(blocks []SummaryBlock) []CompactionMediaRef {
	refs := make([]CompactionMediaRef, 0)
	for _, block := range blocks {
		for _, ref := range block.Media {
			refs = appendRecentCompactionMediaRef(refs, ref)
		}
	}
	return refs
}

func appendRecentCompactionMediaRef(refs []CompactionMediaRef, ref CompactionMediaRef) []CompactionMediaRef {
	ref.Kind = strings.ToLower(strings.TrimSpace(ref.Kind))
	ref.ID = strings.TrimSpace(ref.ID)
	ref.MessageID = strings.TrimSpace(ref.MessageID)
	ref.Filename = strings.TrimSpace(ref.Filename)
	ref.MimeType = strings.TrimSpace(ref.MimeType)
	if ref.ID == "" || (ref.Kind != "attachment" && ref.Kind != "artifact") {
		return refs
	}
	for i := range refs {
		if refs[i].Kind == ref.Kind && refs[i].ID == ref.ID {
			// A later occurrence may carry richer metadata (for example an
			// artifact block created after an attachment was first referenced).
			if ref.Filename == "" {
				ref.Filename = refs[i].Filename
			}
			if ref.MimeType == "" {
				ref.MimeType = refs[i].MimeType
			}
			refs = append(refs[:i], refs[i+1:]...)
			break
		}
	}
	refs = append(refs, ref)
	return refs
}

// injectCompactionMedia hydrates metadata-only summary references into the
// first retained user message. Every reference is checked against current
// access, conversation ownership, bounded local storage and byte-sniffed MIME.
func (o *Orchestrator) injectCompactionMedia(
	ctx context.Context,
	userID, conversationID string,
	history []UnifiedMessage,
	blocks []SummaryBlock,
	vision bool,
) {
	if o == nil || o.db == nil || len(history) == 0 || len(blocks) == 0 {
		return
	}
	target := -1
	for i := range history {
		if history[i].Role == "user" {
			target = i
			break
		}
	}
	if target < 0 {
		return
	}

	refs := mergeCompactionMediaRefs(blocks)
	if !vision {
		// A text-only model cannot consume image bytes, but it still needs to
		// know that an earlier turn supplied media and which source it came
		// from. Keep this metadata in the message layer after the summary so
		// the reference survives even when the binary cannot be rehydrated.
		if metadata := compactionMediaMetadataText(refs, false); metadata != "" {
			history[target].Blocks = append([]UnifiedBlock{{Kind: "text", Text: metadata}}, history[target].Blocks...)
		}
		return
	}
	refs = selectCompactionMediaRefsForHydration(refs, compactionMediaInlineLimit())
	hydrated := make(map[string]bool, len(refs))
	hydratedBlocks := make([]UnifiedBlock, 0, len(refs))
	var hydratedBytes int64
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		var data []byte
		var mimeType, title string
		switch ref.Kind {
		case "attachment":
			file, err := store.GetFile(ctx, o.db, ref.ID, userID)
			if err != nil || file == nil || file.ConversationID != conversationID {
				continue
			}
			if file.SizeBytes <= 0 || file.SizeBytes > attachmentImageInlineBytes || hydratedBytes+file.SizeBytes > compactionMediaInlineLimit() {
				continue
			}
			var state verifiedAttachmentImageState
			data, mimeType, state = readVerifiedProviderImage(file, o.uploadDir)
			if state != verifiedAttachmentImage {
				continue
			}
			title = file.Filename
		case "artifact":
			artifact, err := store.GetArtifact(ctx, o.db, ref.ID, userID)
			if err != nil || artifact == nil || (ref.MessageID != "" && artifact.MessageID != ref.MessageID) {
				continue
			}
			if artifact.SizeBytes <= 0 || artifact.SizeBytes > attachmentImageInlineBytes || hydratedBytes+artifact.SizeBytes > compactionMediaInlineLimit() {
				continue
			}
			sourceMessage, err := store.GetMessage(ctx, o.db, artifact.MessageID)
			if err != nil || sourceMessage == nil || sourceMessage.ConversationID != conversationID {
				continue
			}
			data, mimeType = readVerifiedProviderArtifactImage(artifact, o.artifactDir)
			if len(data) == 0 {
				continue
			}
			title = artifact.Filename
		}
		if hydratedBytes+int64(len(data)) > compactionMediaInlineLimit() {
			continue
		}
		hydratedBytes += int64(len(data))
		hydratedBlocks = append(hydratedBlocks, UnifiedBlock{
			Kind: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: mimeType, Title: title,
		})
		hydrated[compactionMediaRefKey(ref)] = true
	}
	for i, j := 0, len(hydratedBlocks)-1; i < j; i, j = i+1, j-1 {
		hydratedBlocks[i], hydratedBlocks[j] = hydratedBlocks[j], hydratedBlocks[i]
	}
	history[target].Blocks = append(history[target].Blocks, hydratedBlocks...)
	// Keep an explicit record for references excluded by the byte budget or
	// rejected because the source was deleted/invalid. This lets the model and
	// UI explain why an older image is unavailable instead of treating it as if
	// it never existed.
	omitted := make([]CompactionMediaRef, 0)
	for _, ref := range mergeCompactionMediaRefs(blocks) {
		if hydrated[compactionMediaRefKey(ref)] {
			continue
		}
		omitted = append(omitted, ref)
	}
	if metadata := compactionMediaMetadataText(omitted, true); metadata != "" {
		history[target].Blocks = append([]UnifiedBlock{{Kind: "text", Text: metadata}}, history[target].Blocks...)
	}
}

func compactionMediaInlineLimit() int64 {
	if compactionMediaInlineBytes <= 0 {
		return defaultCompactionMediaInlineBytes
	}
	return compactionMediaInlineBytes
}

// selectCompactionMediaRefsForHydration chooses the newest references that fit
// the request byte budget, then restores chronological order. References that
// do not fit remain persisted and are represented by metadata on text-only
// turns; they are not silently deleted.
func selectCompactionMediaRefsForHydration(refs []CompactionMediaRef, budget int64) []CompactionMediaRef {
	if budget <= 0 || len(refs) == 0 {
		return nil
	}
	// Size is checked after ownership and MIME validation, so retain the full
	// ordered set here and let hydration select the newest refs that actually fit.
	return append([]CompactionMediaRef(nil), refs...)
}

func compactionMediaRefKey(ref CompactionMediaRef) string {
	return strings.ToLower(strings.TrimSpace(ref.Kind)) + ":" + strings.TrimSpace(ref.ID)
}

func quoteCompactionMediaMeta(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return strconv.Quote("")
	}
	// Metadata is placed in a provider prompt, so avoid control characters and
	// keep each field bounded independently of the image byte budget.
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, value)
	return quotedCompactionMetadata(value, compactionMetadataLimit())
}

func readVerifiedProviderArtifactImage(artifact *store.Artifact, roots ...string) ([]byte, string) {
	if artifact == nil || attachmentImageInlineBytes <= 0 || artifact.SizeBytes <= 0 || artifact.SizeBytes > attachmentImageInlineBytes {
		return nil, ""
	}
	safePath, err := resolveLLMStoragePath(artifact.StoragePath, roots...)
	if err != nil {
		return nil, ""
	}
	file, err := os.Open(safePath)
	if err != nil {
		return nil, ""
	}
	data, readErr := io.ReadAll(io.LimitReader(file, attachmentImageInlineBytes+1))
	_ = file.Close()
	if readErr != nil || len(data) == 0 || int64(len(data)) > attachmentImageInlineBytes {
		return nil, ""
	}
	mimeType := providerImageMIMEFromBytes(data)
	if mimeType == "" {
		return nil, ""
	}
	return data, mimeType
}
