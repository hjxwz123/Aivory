// Conversation-history projection, attachments, skills, and prompt context.
package llm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/store"
	"aivory/server/internal/toolnames"
)

func storeToUnified(msgs []store.Message, currentProvider, currentModelID string, nativeToolReplay bool) []UnifiedMessage {
	currentModelID = strings.TrimSpace(currentModelID)
	// §4.13-B / §2.3-C: raw replay re-sends the provider-native exchange
	// verbatim — including encrypted reasoning, tool_use / tool_result, and
	// functionCall blocks. Filter Raw up front on a defensive copy so the empty-
	// turn dropper below sees the exact block-derived view the provider will see.
	// This handles both no-native-tools turns and provider/model switches; prior
	// tool rounds degrade to their readable text trace via renderBlocksAsText.
	cp := make([]store.Message, len(msgs))
	copy(cp, msgs)
	for i := range cp {
		sameNativeModel := cp[i].Role == "assistant" &&
			cp[i].Provider == currentProvider &&
			currentModelID != "" &&
			strings.TrimSpace(cp[i].ModelID) == currentModelID
		if !isPromptToolRawEnvelope(cp[i].Raw) && (!nativeToolReplay || !sameNativeModel) {
			cp[i].Raw = nil
		}
	}
	// A terminal assistant row may legitimately have no provider output when the
	// request failed before its first token. Keep the failed turn in history with a
	// provider-safe, non-visible error block so its preceding user request remains
	// available to follow-ups such as "continue". This also repairs legacy rows
	// that were persisted with blocks=[] before failure blocks were introduced.
	for i := range cp {
		if cp[i].Role == "assistant" && cp[i].Status != "streaming" && assistantRendersEmpty(cp[i]) {
			cp[i].Blocks = ensureAssistantFailureBlocks(cp[i].Blocks)
		}
	}
	msgs = cp
	// §workspaces concurrent turns: a shared conversation is one linear thread, so
	// when B asks while A's answer is still generating, B's question chains directly
	// under A's assistant PLACEHOLDER (status="streaming", empty blocks — streamed
	// text isn't persisted until FinishMessage). Left in the history that placeholder
	// becomes an empty assistant turn, which providers reject (Anthropic disallows
	// empty text content blocks), failing B's whole turn. Drop any in-flight / empty
	// assistant turn TOGETHER with its now-orphaned question — dropping only the
	// answer would leave two consecutive user turns, which providers also reject.
	// Terminal empty/error rows are normalized to a non-empty failure block above;
	// they must remain in history so later turns retain the original user request.
	// Purely a per-call transient: the stored messages are untouched, so once A
	// finishes its real answer is used normally on the next turn.
	drop := make([]bool, len(msgs))
	for i, m := range msgs {
		if m.Role == "assistant" && m.Status == "streaming" {
			drop[i] = true
			if i > 0 && msgs[i-1].Role == "user" {
				drop[i-1] = true
			}
		}
	}
	out := []UnifiedMessage{}
	for i, m := range msgs {
		if drop[i] {
			continue
		}
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		um := UnifiedMessage{Role: m.Role, Blocks: blocks}
		var atts []Attachment
		if len(m.Attachments) > 2 {
			_ = json.Unmarshal(m.Attachments, &atts)
			um.Attachments = activeAttachments(atts)
		}
		if m.Role == "assistant" && len(m.Raw) > 2 {
			um.Raw = m.Raw
		}
		out = append(out, um)
	}
	return out
}

const assistantFailureHistoryText = "[The previous assistant response failed before producing output.]"

// ensureAssistantFailureBlocks preserves partial/provider output, but gives a
// failed-before-output assistant row a canonical non-empty block. The frontend
// intentionally does not render this internal block as answer text; status/error
// still drives its localized failure banner. Provider history flattening does
// render it, which preserves role alternation and the preceding user request.
func ensureAssistantFailureBlocks(raw json.RawMessage) json.RawMessage {
	var blocks []UnifiedBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		blocks = nil
	}
	for _, block := range blocks {
		switch block.Kind {
		case "image", "document", "artifact":
			return raw
		}
	}
	if strings.TrimSpace(renderBlocksAsText(blocks)) != "" {
		return raw
	}
	blocks = append(blocks, UnifiedBlock{Kind: "error", Text: assistantFailureHistoryText})
	normalized, err := json.Marshal(blocks)
	if err != nil {
		return json.RawMessage(`[{"kind":"error","text":"[The previous assistant response failed before producing output.]"}]`)
	}
	return normalized
}

// activeAttachments excludes objects that were removed after their message was
// stored. The transcript keeps their metadata for the UI, but a new provider
// request must never attempt to resolve or replay deleted bytes.
func activeAttachments(attachments []Attachment) []Attachment {
	if len(attachments) == 0 {
		return nil
	}
	active := make([]Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if !attachment.Deleted {
			active = append(active, attachment)
		}
	}
	return active
}

func shouldReplayNativeToolHistory(fast bool, toolMode string, localToolCount int, hostedTools bool) bool {
	return !fast && toolMode == "native" && (localToolCount > 0 || hostedTools)
}

const unsupportedToolHistoryPlaceholder = "[A previous tool step was omitted because this model does not support that tool.]"

// stripRetiredKnowledgeSearchToolBlocks removes persisted calls from the former
// model-driven document-search path. Automatic server-side RAG now owns all
// knowledge retrieval, including turns using provider-hosted Official tools.
func stripRetiredKnowledgeSearchToolBlocks(history []UnifiedMessage) []UnifiedMessage {
	const retiredName = "search_knowledge_base"
	allowed := map[string]bool{}
	found := false
	for _, message := range history {
		for _, block := range message.Blocks {
			name := strings.TrimSpace(block.ToolName)
			if name == "" {
				continue
			}
			if name == retiredName {
				found = true
				continue
			}
			allowed[name] = true
		}
	}
	if !found {
		return history
	}
	return stripDisallowedBuiltinToolBlocks(history, allowed)
}

// stripDisallowedBuiltinToolBlocks removes historical calls that are outside a
// configured model allowlist. Provider-native Raw is dropped on an affected
// message because its vendor-specific call/output items cannot be safely
// filtered here; unaffected messages retain Raw for reasoning/tool continuity.
// Canonical blocks retain only allowed calls and paired outputs. Stored messages
// remain untouched.
func stripDisallowedBuiltinToolBlocks(history []UnifiedMessage, allowed map[string]bool) []UnifiedMessage {
	if allowed == nil {
		return history
	}
	deniedIDs := map[string]bool{}
	for _, message := range history {
		for _, block := range message.Blocks {
			if block.Kind == "tool_call" && !allowed[strings.TrimSpace(block.ToolName)] && block.ToolID != "" {
				deniedIDs[block.ToolID] = true
			}
		}
	}
	out := make([]UnifiedMessage, len(history))
	for index, message := range history {
		filtered := message
		if message.Blocks != nil {
			filtered.Blocks = make([]UnifiedBlock, 0, len(message.Blocks))
		}
		affected := false
		for _, block := range message.Blocks {
			nameDenied := strings.TrimSpace(block.ToolName) != "" && !allowed[strings.TrimSpace(block.ToolName)]
			linkedOutput := block.Kind == "tool_output" && block.ToolID != "" && deniedIDs[block.ToolID]
			if (block.Kind == "tool_call" || block.Kind == "tool_output") && (nameDenied || linkedOutput) {
				affected = true
				continue
			}
			filtered.Blocks = append(filtered.Blocks, cloneUnifiedBlock(block))
		}
		if affected && strings.TrimSpace(renderBlocksAsText(filtered.Blocks)) == "" {
			filtered.Blocks = append(filtered.Blocks, UnifiedBlock{Kind: "text", Text: unsupportedToolHistoryPlaceholder})
		}
		if affected {
			if isPromptToolRawEnvelope(message.Raw) {
				filtered.Raw = filterPromptToolRawEnvelope(message.Raw, func(name string) bool {
					return allowed[strings.TrimSpace(name)]
				})
			} else {
				filtered.Raw = nil
			}
		} else {
			filtered.Raw = append(json.RawMessage(nil), message.Raw...)
		}
		filtered.Attachments = append([]Attachment(nil), message.Attachments...)
		out[index] = filtered
	}
	return out
}

// assistantRendersEmpty reports whether a stored assistant turn would collapse to
// empty provider content (no text, no tool trace, no media, no same-vendor raw
// replay). The provider APIs reject empty content, so such a turn must be dropped
// from the prompt rather than sent. This is exactly the state of a still-streaming
// placeholder (its text isn't persisted until FinishMessage, so mid-generation its
// blocks are []) and of a stopped-before-any-output turn.
func assistantRendersEmpty(m store.Message) bool {
	if len(m.Raw) > 2 {
		return false // raw carries the full native exchange verbatim
	}
	var blocks []UnifiedBlock
	if json.Unmarshal(m.Blocks, &blocks) != nil {
		return false // unparseable — keep it rather than risk dropping real content
	}
	for _, b := range blocks {
		switch b.Kind {
		case "image", "document", "artifact":
			return false // becomes a non-empty media block downstream
		}
	}
	return strings.TrimSpace(renderBlocksAsText(blocks)) == ""
}

// resolveAttachments loads image attachments from disk and appends them as
// base64 image blocks to their messages so vision-capable providers can see
// them (§4.6). Errors are silent — a missing file never blocks the turn.
//
// §4.6 vision gating: non-vision requests are normally filtered before this
// function runs. Retain the defensive branch below for legacy/misclassified
// history so no image bytes can reach a text-only provider.
//
// Documents are deliberately NOT attached as native provider file/document
// blocks. Every LLM API request uses the RAG text path for PDFs/DOCX/PPTX/etc.:
// upload -> parse/OCR -> chunks -> retrieval/full-text injection. This keeps
// provider wire formats simple and avoids gateway-specific file-block failures.
func (o *Orchestrator) resolveAttachments(ctx context.Context, userID, convID string, hist []UnifiedMessage, model *store.Model, onEvent func(SseEvent)) {
	visionCapable := model != nil && model.Vision
	notedNonVision := false
	notedPDFRAGOnly := false
	notedOversizeImage := false
	for i := range hist {
		// §4.6-C role gate: attachments belong to the user turn that uploaded them.
		// A non-user row can only carry an image attachment via a copy path (share
		// import / fork / branch), and inlining it would emit an image content part
		// on an assistant/model role — which every provider rejects (OpenAI: "unknown
		// variant `image_url`, expected `text`"; Anthropic: assistant content must be
		// text/tool_use; Gemini: media not allowed on a model turn). Resolve
		// attachments on user turns only; the provider serializers gate again in depth.
		if hist[i].Role != "user" {
			continue
		}
		for _, a := range hist[i].Attachments {
			f, err := store.GetFile(ctx, o.db, a.ID, userID)
			if err != nil || f.ConversationID != convID {
				continue
			}

			data, imageMIME, imageState := readVerifiedProviderImage(f, o.uploadDir)
			switch imageState {
			case verifiedAttachmentImage:
				if !visionCapable {
					// This path covers legacy history whose stored metadata did not say
					// image. Current normalized attachments are removed from the
					// provider request earlier, while their durable file rows remain
					// available to sandbox tools.
					if !notedNonVision && onEvent != nil {
						onEvent(SseEvent{Type: "rag", Status: "warning", Summary: "model does not support images; attached images were skipped"})
						notedNonVision = true
					}
					hist[i].Blocks = append(hist[i].Blocks, UnifiedBlock{
						Kind: "text",
						Text: "[image attachment skipped — current model lacks vision capability]",
					})
					continue
				}
				hist[i].Blocks = append(hist[i].Blocks, UnifiedBlock{
					Kind: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: imageMIME, Title: f.Filename,
				})
				continue
			case rejectedOversizeAttachmentImage:
				if !notedOversizeImage && onEvent != nil {
					onEvent(SseEvent{Type: "rag", Status: "warning", Summary: "image attachment exceeded the provider inline limit and was skipped"})
					notedOversizeImage = true
				}
				continue
			}

			// Documents use the RAG text path. Use only server-owned metadata here;
			// a forged client kind can neither turn a PDF into an image nor suppress
			// its document handling.
			if storedFileIsPDF(f) {
				if !store.ConversationDocReady(ctx, o.db, convID, f.Filename) && !notedPDFRAGOnly && onEvent != nil {
					onEvent(SseEvent{Type: "rag", Status: "warning", Summary: "PDF attachment is still indexing; documents are read through RAG text, not native file blocks"})
					notedPDFRAGOnly = true
				}
				hist[i].Blocks = append(hist[i].Blocks, UnifiedBlock{
					Kind: "text",
					Text: fmt.Sprintf("[PDF attachment %q is read through the indexed RAG text path; do not expect a native PDF/file block in the provider request.]", f.Filename),
				})
				continue
			}
		}
	}
}

// resolveImageArtifactBlocks hydrates generated-image artifact blocks only for a
// provider path that explicitly needs them as model input (currently OpenAI's
// hosted Responses image tool). The database remains metadata-only; base64 lives
// only in this request copy and is ownership-checked and byte-sniffed.
func (o *Orchestrator) resolveImageArtifactBlocks(ctx context.Context, userID string, hist []UnifiedMessage) {
	for i := range hist {
		if hist[i].Role != "assistant" {
			continue
		}
		for j := range hist[i].Blocks {
			block := &hist[i].Blocks[j]
			if block.Kind != "artifact" || block.Data != "" {
				continue
			}
			artifactID := strings.TrimSpace(block.FileRef)
			if artifactID == "" && len(block.Artifacts) > 0 {
				artifactID = strings.TrimSpace(block.Artifacts[0].ID)
			}
			if artifactID == "" {
				continue
			}
			artifact, err := store.GetArtifact(ctx, o.db, artifactID, userID)
			if err != nil || artifact == nil || !reusableProviderImageArtifact(artifact.Source) ||
				artifact.SizeBytes <= 0 || artifact.SizeBytes > attachmentImageInlineBytes {
				continue
			}
			safePath, err := resolveLLMStoragePath(artifact.StoragePath, o.artifactDir)
			if err != nil {
				continue
			}
			file, err := os.Open(safePath)
			if err != nil {
				continue
			}
			data, readErr := io.ReadAll(io.LimitReader(file, attachmentImageInlineBytes+1))
			_ = file.Close()
			if readErr != nil || int64(len(data)) > attachmentImageInlineBytes {
				continue
			}
			mimeType := providerImageMIMEFromBytes(data)
			if mimeType == "" {
				continue
			}
			block.Data = base64.StdEncoding.EncodeToString(data)
			block.MimeType = mimeType
		}
	}
}

func reusableProviderImageArtifact(source string) bool {
	switch strings.TrimSpace(source) {
	case "", store.ArtifactSourceImageGenerate, store.ArtifactSourceHostedImageGeneration:
		// Empty source keeps image artifacts produced before source attribution was
		// introduced. Python and unrelated downloadable artifacts are never selected
		// implicitly as an image-generation edit source.
		return true
	default:
		return false
	}
}

func (o *Orchestrator) persistProviderGeneratedImages(ctx context.Context, tc *ToolContext, images []GeneratedImage) (int, error) {
	registry, ok := o.tools.(providerArtifactRegistry)
	if !ok {
		return 0, errors.New("provider image artifact storage is unavailable")
	}
	persisted := 0
	for i, image := range images {
		mimeType := providerImageMIMEFromBytes(image.Data)
		if mimeType == "" {
			return persisted, fmt.Errorf("provider generated image %d has an unsupported format", i+1)
		}
		name := fmt.Sprintf("image_%d%s", i+1, providerImageExtension(mimeType))
		if err := registry.SaveArtifact(ctx, tc, name, mimeType, image.Data); err != nil {
			return persisted, fmt.Errorf("persist provider generated image %d: %w", i+1, err)
		}
		persisted++
	}
	return persisted, nil
}

func providerImageExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

type verifiedAttachmentImageState uint8

const (
	notAttachmentImage verifiedAttachmentImageState = iota
	verifiedAttachmentImage
	rejectedOversizeAttachmentImage
)

// readVerifiedProviderImage reads at most one bounded file and classifies it
// from bytes, not attachment or database claims. The size check happens both
// before and during the read because legacy metadata may be stale.
func readVerifiedProviderImage(file *store.File, roots ...string) ([]byte, string, verifiedAttachmentImageState) {
	if file == nil || attachmentImageInlineBytes <= 0 {
		return nil, "", notAttachmentImage
	}
	metadataImage := strings.EqualFold(strings.TrimSpace(file.Kind), "image") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.MimeType)), "image/") ||
		providerImageFilename(file.Filename)
	if file.SizeBytes > attachmentImageInlineBytes {
		if metadataImage {
			return nil, "", rejectedOversizeAttachmentImage
		}
		return nil, "", notAttachmentImage
	}
	safePath, err := resolveLLMStoragePath(file.StoragePath, roots...)
	if err != nil {
		return nil, "", notAttachmentImage
	}
	f, err := os.Open(safePath)
	if err != nil {
		return nil, "", notAttachmentImage
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, attachmentImageInlineBytes+1))
	if err != nil {
		return nil, "", notAttachmentImage
	}
	if int64(len(data)) > attachmentImageInlineBytes {
		return nil, "", rejectedOversizeAttachmentImage
	}
	mimeType := providerImageMIMEFromBytes(data)
	if mimeType == "" {
		return nil, "", notAttachmentImage
	}
	return data, mimeType, verifiedAttachmentImage
}

func storedFileIsPDF(file *store.File) bool {
	if file == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(file.Kind), "pdf") ||
		strings.EqualFold(strings.TrimSpace(strings.SplitN(file.MimeType, ";", 2)[0]), "application/pdf") ||
		strings.EqualFold(filepath.Ext(file.Filename), ".pdf")
}

func providerImageFilename(filename string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(filename))) {
	case ".png", ".apng", ".jpg", ".jpeg", ".jpe", ".jfif", ".gif", ".webp", ".bmp",
		".tif", ".tiff", ".heic", ".heif", ".avif", ".ico", ".cur", ".jxl", ".psd", ".svg":
		return true
	default:
		return false
	}
}

func providerImageMIMEFromBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	if detected := strings.ToLower(http.DetectContentType(head)); strings.HasPrefix(detected, "image/") {
		return detected
	}
	if len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) {
		brands := head[8:]
		if len(brands) > 64 {
			brands = brands[:64]
		}
		for _, brand := range []string{"avif", "avis"} {
			if bytes.Contains(brands, []byte(brand)) {
				return "image/avif"
			}
		}
		for _, brand := range []string{"heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1"} {
			if bytes.Contains(brands, []byte(brand)) {
				return "image/heif"
			}
		}
	}
	if bytes.HasPrefix(head, []byte("8BPS")) {
		return "image/vnd.adobe.photoshop"
	}
	if bytes.HasPrefix(head, []byte{0xff, 0x0a}) || bytes.HasPrefix(head, []byte("\x00\x00\x00\x0cJXL \r\n\x87\n")) {
		return "image/jxl"
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(head, []byte{0xef, 0xbb, 0xbf}))
	if bytes.Contains(bytes.ToLower(trimmed), []byte("<svg")) {
		return "image/svg+xml"
	}
	return ""
}

// renderBlocksAsText flattens a block list to plain text for history rebuild:
// text/error blocks verbatim; tool rounds compressed to a one-line summary
// (§2.3-D cross-vendor downgrade, e.g. "[已执行 python_execute，输出：均值=5.5]");
// thinking blocks are never replayed as visible text.
func renderBlocksAsText(blocks []UnifiedBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		switch blk.Kind {
		case "text":
			if blk.Text != "" {
				b.WriteString(blk.Text)
				b.WriteString("\n")
			}
		case "error":
			if blk.Text != "" {
				b.WriteString(blk.Text)
				b.WriteString("\n")
			}
		case "tool_call":
			fmt.Fprintf(&b, "[已执行 %s，输出：%s]\n", blk.ToolName, blk.Summary)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// SkillIndex is a slim view used for system prompt composition.
type SkillIndex struct {
	Name string
	When string
}

// SkillFull carries a skill's full instructions, injected inline for
// prompt/none tool-mode models that can't call use_skill (§4.17).
type SkillFull struct {
	Name         string
	Instructions string
}

func loadEnabledModelSkills(ctx context.Context, db *sql.DB, modelID string, policy *ToolAccessPolicy) ([]SkillIndex, []SkillFull) {
	indexes := []SkillIndex{}
	full := []SkillFull{}
	skillIDs, _ := store.SkillsForModel(ctx, db, modelID)
	for _, skillID := range skillIDs {
		if !skillAccessPolicyAllows(policy, skillID) {
			continue
		}
		skill, err := store.GetSkill(ctx, db, skillID)
		if err != nil || !skill.Enabled {
			continue
		}
		indexes = append(indexes, SkillIndex{Name: skill.Name, When: skill.Description})
		full = append(full, SkillFull{Name: skill.Name, Instructions: skill.Instructions})
	}
	return indexes, full
}

type systemPromptOpts struct {
	ModelSystem string
	// ModelLabel is the admin-configured display name of the model. It drives the
	// built-in identity line so the assistant identifies as this name (§ identity).
	ModelLabel string
	// Locale is the user's UI language code; anchors the reply-language line so
	// replies follow the user's message language (defaulting to this on ambiguity).
	Locale              string
	ToolMode            string   // native | prompt | none
	ToolNames           []string // names of the tools actually enabled for this model
	ProjectName         string
	ProjectInstructions string
	Skills              []SkillIndex
	SkillsFull          []SkillFull
	Memories            []store.Memory
	ProjectFiles        []ProjectFileSummary
	// SandboxFiles are the original conversation uploads staged at
	// /workspace/uploads. Listed only when python_execute is enabled.
	SandboxFiles []ProjectFileSummary
	// Persona is the user's personalization (tone traits + custom instructions
	// + nickname). Empty fields render nothing.
	Persona UserPersona
	// InlineQuote is the excerpt a text-selection sub-conversation is anchored to.
	// When non-empty the assistant is told to focus on explaining/discussing it.
	InlineQuote string
	// InlineSource is the FULL text of the message the excerpt was lifted from,
	// injected so a short ambiguous quote has the context it needs.
	InlineSource string
	// SkillToolAvailable is true only when the use_skill tool is actually exposed
	// to the model this turn. When false (official/hosted tools, none mode, or
	// use_skill disabled), skills are inlined in full so they still take effect
	// instead of pointing the model at a tool it can't call.
	SkillToolAvailable bool
	// SkillsAllowed captures the primary request ceiling separately from whether
	// use_skill is natively declared. It remains true for tool_mode=none models
	// that inline skills, but false for a per-turn disable or model/global policy.
	// TTFT fallback uses it to avoid broadening the original request policy.
	SkillsAllowed bool
	// SkillMode carries the effective administrator/per-turn skill policy into
	// prompt composition. Selected policies may expose database skills while
	// still excluding code-defined skills without catalog IDs.
	SkillMode string
}

func skillModeAllowsBuiltinDocGen(mode string) bool {
	return mode == "" || mode == store.ResourceAccessAll
}

// UserPersona is the per-user personalization read from settings.
type UserPersona struct {
	Traits   []string `json:"traits"`   // stable trait keys (concise, friendly, …)
	Custom   string   `json:"custom"`   // free-form custom instructions
	Nickname string   `json:"nickname"` // what to call the user
}

func (p UserPersona) empty() bool {
	return len(p.Traits) == 0 && strings.TrimSpace(p.Custom) == "" && strings.TrimSpace(p.Nickname) == ""
}

// personaTraitPhrases maps the UI's trait keys to a short instruction phrase.
// Unknown keys fall through to the raw key so a future preset still reads okay.
var personaTraitPhrases = map[string]string{
	"concise":      "concise and to the point",
	"detailed":     "thorough and detailed",
	"friendly":     "warm and friendly",
	"professional": "professional",
	"encouraging":  "encouraging and supportive",
	"direct":       "direct and straight-shooting",
	"witty":        "witty, with light humor",
	"socratic":     "Socratic — guide with questions",
	"genz":         "casual, Gen-Z tone",
	"formal":       "formal",
}

// readUserPersona loads the persona from per-user settings keys persona_traits
// / persona_custom / persona_nickname. Missing keys yield empty fields.
func readUserPersona(ctx context.Context, db *sql.DB, userID string) UserPersona {
	var p UserPersona
	if raw, err := store.GetUserSettingKey(ctx, db, userID, "persona_traits"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &p.Traits)
	}
	if raw, err := store.GetUserSettingKey(ctx, db, userID, "persona_custom"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &p.Custom)
	}
	if raw, err := store.GetUserSettingKey(ctx, db, userID, "persona_nickname"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &p.Nickname)
	}
	return p
}

// recentHistoryStrings returns up to n trailing "role: text" strings from the
// message path, used to give the RAG query router conversational context.
func recentHistoryStrings(msgs []store.Message, n int) []string {
	out := []string{}
	start := 0
	if len(msgs) > n {
		start = len(msgs) - n
	}
	for _, m := range msgs[start:] {
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		text := strings.Builder{}
		for _, b := range blocks {
			if b.Kind == "text" {
				text.WriteString(b.Text)
				text.WriteString(" ")
			}
		}
		t := strings.TrimSpace(text.String())
		if t == "" {
			continue
		}
		if len([]rune(t)) > ragRouterRecentHistoryTruncate {
			t = string([]rune(t)[:ragRouterRecentHistoryTruncate])
		}
		out = append(out, m.Role+": "+t)
	}
	return out
}

// (§4.8-L10N) The former replyLanguageDirective was removed: the whole system
// prompt now renders in the user's language (see composeSystemPrompt +
// prompt_l10n.go), so a separate "reply in X" line is redundant. Title
// generation keeps its own directive below because a task-model call has no
// localized system prompt.

// titleLanguageDirective returns a "write the title in this language" instruction
// WRITTEN IN the user's selected UI language. Empty for unknown/blank locales.
func titleLanguageDirective(locale string) string {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "en", "en-us", "en-gb":
		return "Write the title in English."
	case "zh", "zh-cn", "zh-hans", "zh-sg":
		return "请用简体中文写这个标题。"
	case "zh-hant", "zh-tw", "zh-hk", "zh-mo":
		return "請用繁體中文寫這個標題。"
	case "ja", "ja-jp":
		return "タイトルは日本語で書いてください。"
	case "fr", "fr-fr", "fr-ca":
		return "Rédige le titre en français."
	default:
		return ""
	}
}

// composeSystemPrompt implements the §4.8 six-segment composition in stable
// order. Stable = cache-friendly (§4.9).
func composeSystemPrompt(o systemPromptOpts) string {
	var b strings.Builder
	// §4.8-L10N: the WHOLE authored prompt renders in the user's UI language via
	// `l` (English is the default/fallback). Because the prompt itself is in the
	// target language, a separate "always reply in X" directive is no longer
	// needed — and was removed. Only tool NAMES, boundary tags, markdown/paths,
	// and admin/user DATA stay language-neutral (see prompt_l10n.go).
	l := promptL10nFor(o.Locale)
	// ① built-in identity (§ identity): the assistant identifies as the model's
	// admin-configured display NAME — never a hardcoded product name. So a model
	// labelled "GPT 5.5" answers "who are you?" with "I am GPT 5.5", regardless of
	// the actual upstream provider.
	label := strings.TrimSpace(o.ModelLabel)
	if label == "" {
		label = "an AI assistant"
	}
	fmt.Fprintf(&b, l.identity, label, label)

	// ② model-level system prompt (admin-customised behaviour/persona), or the
	// localized default style line when the admin hasn't set one.
	if s := strings.TrimSpace(o.ModelSystem); s != "" {
		b.WriteString("\n\n")
		b.WriteString(s)
	} else {
		b.WriteString(l.defaultStyle)
	}

	// ①.1 ground the model in real time. Without this it falls back to its
	// training-era date, so "today" / "latest" — and the queries it hands to
	// a web-search tool — silently target the wrong year. Server-local time; operators
	// set TZ to their zone. English keeps the weekday; other locales use the ISO
	// date to avoid an English weekday inside a localized sentence.
	now := time.Now()
	dateStr := now.Format("2006-01-02")
	if promptLocaleKey(o.Locale) == "en" {
		dateStr = now.Format("Monday, 2006-01-02")
	}
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf(l.dateGrounding, dateStr))

	// ①.5 user personalization — tone traits + custom instructions + nickname.
	// Placed high so the assistant adopts the user's preferred style.
	if !o.Persona.empty() {
		b.WriteString("\n\n")
		b.WriteString(l.personaHeader)
		var phrases []string
		for _, key := range o.Persona.Traits {
			if ph, ok := personaTraitPhrases[key]; ok {
				phrases = append(phrases, ph)
			} else if k := strings.TrimSpace(key); k != "" {
				phrases = append(phrases, k)
			}
		}
		if len(phrases) > 0 {
			fmt.Fprintf(&b, l.personaTone, strings.Join(phrases, "; "))
		}
		if n := strings.TrimSpace(o.Persona.Nickname); n != "" {
			fmt.Fprintf(&b, l.personaAddress, n)
		}
		if c := strings.TrimSpace(o.Persona.Custom); c != "" {
			b.WriteString(c)
			b.WriteString("\n")
		}
	}

	// §4.11.7 prompt-injection defense — added inline so the rule travels with
	// the stable system prefix (cacheable). Without this, a poisoned document
	// in retrieval can hijack the model with "Ignore previous instructions…".
	b.WriteString("\n\n")
	b.WriteString(l.trustHeader)
	b.WriteString(l.trustBody)

	// ② tool guidance — only mention tools actually enabled for this model.
	has := map[string]bool{}
	for _, n := range o.ToolNames {
		has[n] = true
	}
	if o.ToolMode != "none" && len(o.ToolNames) > 0 {
		if o.ToolMode == "native" {
			// Native function-calling: each enabled tool already ships its NAME +
			// DESCRIPTION + input schema in the request's `tools` array (the
			// descriptions are in fact more detailed than a one-line hint), so
			// per-tool "use X for Y" guidance here would just duplicate them.
			// Emit ONLY the cross-cutting steering the schema doesn't carry:
			// cite web/KB results inline, and retry weak tool results. use_skill
			// usage is already mandated by the "## Skills available" section.
			b.WriteString("\n\n")
			b.WriteString(l.toolHeader)
			if has[toolnames.AivoryWebSearch] {
				b.WriteString(l.toolCite)
			}
			b.WriteString(l.toolMultiRound)
		} else {
			// Prompt mode (§4.13): the model has NO tool schema — it learns each
			// tool ONLY from this section, so list every enabled one. use_skill is
			// excluded (prompt/none mode inlines skills in segment ③).
			guidance := []struct{ name, line string }{
				{toolnames.AivoryWebSearch, l.toolWebSearch},
				{"fetch_image", l.toolFetchImage},
				{"python_execute", l.toolPython},
				{"image_generate", l.toolImage},
				{"save_memory", l.toolSaveMemory},
			}
			wrote := false
			for _, g := range guidance {
				if has[g.name] {
					if !wrote {
						b.WriteString("\n\n")
						b.WriteString(l.toolHeader)
						wrote = true
					}
					b.WriteString(g.line)
				}
			}
			if wrote {
				b.WriteString(l.toolMultiRound)
			}
		}

		// §4.5.1 "quality watershed": when the user asks for a downloadable
		// document (PDF / PPT / DOCX / XLSX), the model MUST follow the DocGen
		// recipes rather than improvise. Without them, the output looks like
		// LaTeX from 1995. With them, it looks like an editorial deck.
		// Progressive disclosure (§4.17): a model that can call use_skill loads
		// them on demand via the built-in document-generation entry in the
		// skills index below — inlining ~800 tokens on every turn that never
		// produces a document is dead weight. Models that can't call use_skill
		// still get them inline.
		if has["python_execute"] {
			if o.SkillsAllowed && !o.SkillToolAvailable && skillModeAllowsBuiltinDocGen(o.SkillMode) {
				b.WriteString("\n")
				b.WriteString(DocGenRecipes)
			}

			// Conversation-uploaded data files persist in the sandbox across turns
			// — list them so the model can act on a file uploaded earlier.
			if len(o.SandboxFiles) > 0 {
				b.WriteString(l.sandboxHeader)
				for _, f := range o.SandboxFiles {
					fmt.Fprintf(&b, "- /workspace/uploads/%s (%s)\n", f.Name, f.Kind)
				}
				b.WriteString(l.sandboxBody)
			}
		}
	}

	// ③ skills (§4.17). When use_skill is actually exposed → slim index +
	// progressive disclosure (the model loads a skill on demand). When it is not
	// (official/hosted tools, none mode, or use_skill disabled) → inline full
	// instructions so the skill still takes effect instead of pointing the model
	// at a tool it can't call.
	// The built-in document-generation skill (§4.5.1) joins the index when the
	// model can run python_execute; an admin skill with the same name shadows
	// it (mirrored in useSkillTool's lookup order).
	skillIdx := []SkillIndex(nil)
	if o.SkillsAllowed {
		skillIdx = o.Skills
	}
	if o.SkillsAllowed && o.SkillToolAvailable && o.ToolMode != "none" && has["python_execute"] && skillModeAllowsBuiltinDocGen(o.SkillMode) {
		shadowed := false
		for _, s := range o.Skills {
			if strings.EqualFold(s.Name, DocGenSkillName) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			skillIdx = append(append([]SkillIndex{}, o.Skills...), SkillIndex{Name: DocGenSkillName, When: DocGenWhen})
		}
	}
	if o.SkillsAllowed && o.SkillToolAvailable && len(skillIdx) > 0 {
		b.WriteString(l.skillsAvailHeader)
		b.WriteString(l.skillsAvailBody)
		for _, s := range skillIdx {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.When)
		}
	} else if o.SkillsAllowed && len(o.SkillsFull) > 0 {
		b.WriteString(l.skillsInlineHeader)
		b.WriteString(l.skillsInlineBody)
		for _, s := range o.SkillsFull {
			fmt.Fprintf(&b, "\n### %s\n%s\n", s.Name, s.Instructions)
		}
	}

	// ④ project instructions
	if o.ProjectInstructions != "" {
		fmt.Fprintf(&b, l.projectHeader, o.ProjectName)
		b.WriteString(o.ProjectInstructions)
		b.WriteString("\n")
	}

	// ⑤ current memories (only ACTIVE + QUERY_DEPENDENT, §4.16). The
	// [CURRENT]/[CONTEXT-DEPENDENT] markers stay language-neutral (the rules line
	// references them literally).
	if len(o.Memories) > 0 {
		b.WriteString(l.memoryHeader)
		for _, m := range o.Memories {
			label := "[CURRENT]"
			if m.Status == "QUERY_DEPENDENT" {
				label = "[CONTEXT-DEPENDENT]"
			}
			fmt.Fprintf(&b, "%s %s\n", label, m.MemoryText)
		}
		b.WriteString(l.memoryRules)
	}

	// ⑥ available documents
	if len(o.ProjectFiles) > 0 {
		b.WriteString(l.documentsHeader)
		for _, f := range o.ProjectFiles {
			fmt.Fprintf(&b, "- %s\n", f.Name)
		}
	}

	// ⑦ inline-thread excerpt (§ text-selection sub-conversations). The user
	// highlighted a passage from a previous answer and started a side thread to
	// ask about it; keep answers tightly scoped to this excerpt. Wrapped in a
	// trust boundary like other injected content.
	if strings.TrimSpace(o.InlineQuote) != "" {
		b.WriteString(l.excerptHeader)
		b.WriteString(l.excerptBody)
		b.WriteString("<excerpt>\n")
		b.WriteString(o.InlineQuote)
		b.WriteString("\n</excerpt>\n")
		if strings.TrimSpace(o.InlineSource) != "" {
			b.WriteString("<source-message>\n")
			b.WriteString(o.InlineSource)
			b.WriteString("\n</source-message>\n")
		}
	}

	// NOTE: the long-context summary (§4.7) and RAG snippets (§4.11-B) are
	// deliberately NOT part of the system prompt — they belong to the message
	// layer (injected by the orchestrator) so the system prefix stays stable
	// and cacheable (§4.9). See injectSummaryIntoHistory / injectRAGIntoHistory.
	return b.String()
}

// formatRAGContext renders retrieved snippets as a text block to append to the
// current user turn (closest to the question → best recall).
//
// §4.11.7 prompt-injection protection: wrap context with explicit boundary
// tags. Combined with the system-prompt declaration that <context>…</context>
// is reference material (NOT instructions), this neutralizes prompt-injected
// "ignore the user" patterns embedded in retrieved documents.
func formatRAGContext(snips []Citation, locale string) string {
	if len(snips) == 0 {
		return ""
	}
	b := strings.Builder{}
	b.WriteString("\n\n<context-from-knowledge-base>\n")
	b.WriteString(promptL10nFor(locale).ragIntro)
	for i, c := range snips {
		index := c.Index
		if index <= 0 {
			index = i + 1
		}
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", index, c.Title, c.Snippet)
	}
	b.WriteString("</context-from-knowledge-base>\n")
	return b.String()
}

// resolvedTurnCitations preserves the historical append-and-renumber behavior
// unless this turn actually attached a knowledge base. In that branch, the
// answer model has already seen stable [n] labels, so unused KB sources are
// removed without closing numbering gaps; web/tool sources are retained.
func resolvedTurnCitations(ragCitations, providerCitations []Citation, blocks []UnifiedBlock, pruneUnusedKB bool) []Citation {
	if !pruneUnusedKB {
		out := append(append([]Citation{}, ragCitations...), providerCitations...)
		for i := range out {
			out[i].Index = i + 1
		}
		return out
	}

	normalizedRAG := append([]Citation(nil), ragCitations...)
	maxIndex := 0
	for i := range normalizedRAG {
		if normalizedRAG[i].Index <= 0 {
			normalizedRAG[i].Index = i + 1
		}
		if normalizedRAG[i].Index > maxIndex {
			maxIndex = normalizedRAG[i].Index
		}
	}
	out := append([]Citation(nil), normalizedRAG...)
	for _, citation := range providerCitations {
		if citation.GlobalIndex && citation.Index > 0 {
			if citation.Index > maxIndex {
				maxIndex = citation.Index
			}
		} else {
			maxIndex++
			citation.Index = maxIndex
		}
		out = append(out, citation)
	}

	used := citationMarkersInBlocks(blocks)
	kept := out[:0]
	for _, citation := range out {
		if isKnowledgeBaseCitation(citation) {
			if _, ok := used[citation.Index]; !ok {
				continue
			}
		}
		kept = append(kept, citation)
	}
	return kept
}

func maxCitationIndex(citations []Citation) int {
	maxIndex := 0
	for _, citation := range citations {
		if citation.Index > maxIndex {
			maxIndex = citation.Index
		}
	}
	return maxIndex
}

func isKnowledgeBaseCitation(citation Citation) bool {
	return citation.Source == "kb"
}

func citationMarkersInBlocks(blocks []UnifiedBlock) map[int]struct{} {
	used := map[int]struct{}{}
	for _, block := range blocks {
		if block.Kind != "text" || block.Text == "" {
			continue
		}
		for index := range citationMarkersOutsideCode(block.Text) {
			used[index] = struct{}{}
		}
	}
	return used
}

// citationMarkersOutsideCode mirrors the frontend citation renderer's most
// important boundary: markers inside inline/fenced code are examples, not
// evidence references. Escaped markers are likewise left literal.
func citationMarkersOutsideCode(text string) map[int]struct{} {
	used := map[int]struct{}{}
	for i := 0; i < len(text); {
		if text[i] == '`' {
			runEnd := i + 1
			for runEnd < len(text) && text[runEnd] == '`' {
				runEnd++
			}
			delimiter := text[i:runEnd]
			if closeAt := strings.Index(text[runEnd:], delimiter); closeAt >= 0 {
				i = runEnd + closeAt + len(delimiter)
				continue
			}
			// An unmatched code delimiter makes the remainder literal markdown.
			break
		}
		if text[i] != '[' || (i > 0 && (text[i-1] == '\\' || text[i-1] == '!')) {
			i++
			continue
		}
		end := i + 1
		for end < len(text) && end-i <= 3 && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		if end == i+1 || end >= len(text) || text[end] != ']' {
			i++
			continue
		}
		// A normal Markdown link label such as [1](https://example.test) is not
		// transformed into a citation marker by the frontend either.
		if end+1 < len(text) && text[end+1] == '(' {
			i = end + 1
			continue
		}
		index, err := strconv.Atoi(text[i+1 : end])
		if err == nil && index > 0 {
			used[index] = struct{}{}
		}
		i = end + 1
	}
	return used
}

// injectSummaryIntoHistory prepends the rolled-up summary to the FIRST user
// message so it sits in the message layer between system and recent turns
// (§4.8) without breaking role alternation (important for Gemini).
func injectSummaryIntoHistory(msgs []UnifiedMessage, text string) []UnifiedMessage {
	if strings.TrimSpace(text) == "" {
		return msgs
	}
	for i := range msgs {
		if msgs[i].Role == "user" {
			msgs[i].Blocks = append([]UnifiedBlock{{Kind: "text", Text: text}}, msgs[i].Blocks...)
			return msgs
		}
	}
	return msgs
}
