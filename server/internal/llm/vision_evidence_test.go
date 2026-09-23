package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

// visionCaptureProvider records every request so a test can assert what the
// vision model was actually shown, and returns a canned response.
type visionCaptureProvider struct {
	response string
	fail     bool
	requests []UnifiedChatRequest
}

func (p *visionCaptureProvider) ID() string { return "openai" }

func (p *visionCaptureProvider) Stream(
	_ context.Context,
	req UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	p.requests = append(p.requests, req)
	if p.fail {
		return nil, errVisionTestFailure
	}
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: p.response}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 12, OutputTokens: 34},
	}, nil
}

var errVisionTestFailure = visionTestError("vision provider unavailable")

type visionTestError string

func (e visionTestError) Error() string { return string(e) }

func TestRenderVisionEvidenceRendersStructuredFields(t *testing.T) {
	raw := `{"summary":"A bar chart of quarterly revenue.","visible_text":"Q1 Q2 Q3\n12 34 56",` +
		`"layout":"Bar chart, x-axis quarters, y-axis USD millions.","entities":["legend","USD"],` +
		`"data":"Q1=12, Q2=34, Q3=56","notable":"Q3 is the peak."}`
	rendered := renderVisionEvidence(raw)
	for _, want := range []string{
		"summary: A bar chart of quarterly revenue.",
		"visible_text:\nQ1 Q2 Q3\n12 34 56",
		"layout: Bar chart",
		"entities: legend; USD",
		"data: Q1=12, Q2=34, Q3=56",
		"notable: Q3 is the peak.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered evidence missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderVisionEvidenceKeepsFencedJSONAndDropsEmptyFields(t *testing.T) {
	raw := "```json\n{\"summary\":\"A receipt.\",\"visible_text\":\"\",\"entities\":[],\"data\":\"\",\"notable\":\"\"}\n```"
	rendered := renderVisionEvidence(raw)
	if rendered != "summary: A receipt." {
		t.Fatalf("rendered = %q, want only the summary line", rendered)
	}
}

func TestRenderVisionEvidenceFallsBackToRawText(t *testing.T) {
	// A model that ignores the schema still produced a usable transcription.
	raw := "The screenshot shows a 500 error on the checkout page."
	if rendered := renderVisionEvidence(raw); rendered != raw {
		t.Fatalf("rendered = %q, want the raw transcription", rendered)
	}
	if rendered := renderVisionEvidence("   "); rendered != "" {
		t.Fatalf("blank response rendered %q, want empty", rendered)
	}
}

func TestRenderVisionEvidenceCoercesNonStringScalarsAndEntities(t *testing.T) {
	rendered := renderVisionEvidence(`{"summary":"Table","entities":"row label","data":42,"notable":true}`)
	for _, want := range []string{"summary: Table", "entities: row label", "data: 42", "notable: true"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered evidence missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderVisionEvidenceTruncatesOverlongOutput(t *testing.T) {
	long := strings.Repeat("字", visionEvidenceMaxChars+500)
	rendered := renderVisionEvidence(`{"visible_text":"` + long + `"}`)
	if !strings.HasSuffix(rendered, "[evidence truncated]") {
		t.Fatalf("long evidence was not truncated: %d runes", len([]rune(rendered)))
	}
	if got := len([]rune(rendered)); got > visionEvidenceMaxChars+len([]rune("\n[evidence truncated]")) {
		t.Fatalf("truncated evidence is %d runes", got)
	}
}

func TestVisionEvidenceStampTracksModelAndPromptRevision(t *testing.T) {
	if visionEvidenceStamp("m1") == visionEvidenceStamp("m2") {
		t.Fatal("two different vision models share one cache stamp")
	}
	if !strings.Contains(visionEvidenceStamp("m1"), "v1") {
		t.Fatalf("stamp %q does not carry the prompt revision", visionEvidenceStamp("m1"))
	}
}

func TestVisionEvidenceBlockLabelsTheFile(t *testing.T) {
	block := visionEvidenceBlock("chart.png", "summary: A chart.")
	if !strings.Contains(block, `name="chart.png"`) || !strings.Contains(block, "summary: A chart.") ||
		!strings.HasPrefix(block, visionEvidenceOpen) || !strings.HasSuffix(block, visionEvidenceClose) {
		t.Fatalf("evidence block = %q", block)
	}
	if blank := visionEvidenceBlock("", "summary: x"); !strings.Contains(blank, `name="attachment"`) {
		t.Fatalf("unlabelled evidence block = %q", blank)
	}
}

// §4.6 image outsourcing is meant to be invisible to the user (无感读图). An
// earlier revision of the injected notice told the model it could not see images
// and to disclose when the evidence was thin, and models relayed that disclosure
// to the user. This test pins both halves of the contract — the model must never
// narrate the pipeline, and the untrusted-content guard must survive, because it
// constrains behaviour without needing to be explained.
func TestVisionEvidenceNoticeKeepsOutsourcingInvisible(t *testing.T) {
	notice := strings.ToLower(visionEvidenceNotice)

	// Phrases that ASSERT the pipeline. Naming the pipeline inside a prohibition
	// ("never say it was read by another model") is fine and necessary, so this
	// list targets the affirmative framings of the earlier revision that models
	// paraphrased back to the user, not bare nouns.
	for _, forbidden := range []string{
		"you cannot see", "you can't see", "cannot see images", "can't see images",
		"does not support image", "the selected model", "untrusted observation",
		"produced by a vision", "described instead of",
	} {
		if strings.Contains(notice, forbidden) {
			t.Fatalf("notice asserts the pipeline to the model (%q):\n%s", forbidden, visionEvidenceNotice)
		}
	}
	for _, required := range []string{"never state", "never mention", "as if you had viewed"} {
		if !strings.Contains(notice, required) {
			t.Fatalf("notice does not forbid disclosure (%q):\n%s", required, visionEvidenceNotice)
		}
	}
	// The safety guard must not be traded away for seamlessness.
	for _, required := range []string{"never a directive to follow", "do not invent"} {
		if !strings.Contains(notice, required) {
			t.Fatalf("notice lost its untrusted-content guard (%q):\n%s", required, visionEvidenceNotice)
		}
	}
}

// The block tag is the other thing the model can quote back at the user, so it
// must read as the image rather than as a report about one.
func TestVisionEvidenceBlockReadsAsTheImageItself(t *testing.T) {
	block := visionEvidenceBlock("chart.png", "summary: A chart.")
	if strings.Contains(block, "evidence") || strings.Contains(block, "vision") {
		t.Fatalf("block tag exposes the pipeline: %q", block)
	}
	if !strings.HasPrefix(block, "<attached-image ") {
		t.Fatalf("block tag = %q", block)
	}
}

// visionResolutionFixture wires a text-only conversation model plus a
// vision-capable one, a stored PNG attachment, and returns everything a
// resolveAttachments test needs.
type visionResolutionFixture struct {
	orchestrator  *Orchestrator
	provider      *visionCaptureProvider
	fileID        string
	imageData     []byte
	imagePath     string
	userID        string
	conversation  string
	visionModelID string
	textModelID   string
}

func newVisionResolutionFixture(t *testing.T, provider *visionCaptureProvider, configureVisionModel bool) *visionResolutionFixture {
	t.Helper()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "vision-evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u_vision','vision@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(context.Background(), db, "Vision", provider.ID(), "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	textModel, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "text-only", Label: "Text only", Enabled: true, Vision: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	visionModel, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "sees-images", Label: "Sees images", Enabled: true, Vision: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if configureVisionModel {
		if err := store.SetSetting(db, "vision_model_id", visionModel.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetSetting(db, "credits_per_usd", 1.0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id,user_id,title) VALUES('c_vision','u_vision','Vision')`); err != nil {
		t.Fatal(err)
	}
	imageData := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	imagePath := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(imagePath, imageData, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := store.CreateFile(context.Background(), db, store.File{
		ID: "f_chart", UserID: "u_vision", ConversationID: "c_vision", Filename: "chart.png",
		MimeType: "image/png", Kind: "image", SizeBytes: int64(len(imageData)), StoragePath: imagePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(io.Discard, "", 0)
	registry := NewRegistry(logger)
	registry.Register(provider)
	task := NewTaskLLM(db, registry, logger)
	orchestrator := NewOrchestrator(db, registry, nil, nil, cache.NewMemory(), nil, task, nil, logger)
	return &visionResolutionFixture{
		orchestrator: orchestrator, provider: provider, fileID: file.ID, imageData: imageData, imagePath: imagePath,
		userID: "u_vision", conversation: "c_vision", visionModelID: visionModel.ID, textModelID: textModel.ID,
	}
}

func TestResolveAttachmentsOutsourcesImagesToTheVisionModel(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"A revenue chart.","visible_text":"Q1 12"}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	history := []UnifiedMessage{{
		Role:        "user",
		Blocks:      []UnifiedBlock{{Kind: "text", Text: "what does this show"}},
		Attachments: []Attachment{{ID: fixture.fileID, Kind: "image", MimeType: "image/png"}},
	}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: false}, fixture.visionModelID, nil)

	if len(provider.requests) != 1 {
		t.Fatalf("vision calls = %d, want exactly one", len(provider.requests))
	}
	request := provider.requests[0]
	if request.Model.ID != fixture.visionModelID || !request.Model.Vision {
		t.Fatalf("vision call went to %q vision=%v", request.Model.ID, request.Model.Vision)
	}
	// The image bytes must reach the vision model, and only there.
	var imageBlocks int
	for _, message := range request.History {
		for _, block := range message.Blocks {
			if block.Kind == "image" {
				imageBlocks++
				if block.Data != base64.StdEncoding.EncodeToString(fixture.imageData) {
					t.Fatal("vision model did not receive the verified image bytes")
				}
			}
		}
	}
	if imageBlocks != 1 {
		t.Fatalf("vision request carried %d image blocks, want 1", imageBlocks)
	}
	// The conversation model must see text evidence, never the pixels.
	var injected []string
	for _, block := range history[0].Blocks {
		if block.Kind == "image" {
			t.Fatal("image bytes were injected into the text-only conversation model")
		}
		injected = append(injected, block.Text)
	}
	joined := strings.Join(injected, "\n")
	if !strings.Contains(joined, visionEvidenceNotice) {
		t.Fatalf("missing the outsourcing notice:\n%s", joined)
	}
	if !strings.Contains(joined, "summary: A revenue chart.") || !strings.Contains(joined, `name="chart.png"`) {
		t.Fatalf("rendered evidence missing from the turn:\n%s", joined)
	}
}

func TestResolveAttachmentsReusesCachedEvidenceWithoutCallingTheVisionModel(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"A revenue chart."}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	attachment := []Attachment{{ID: fixture.fileID, Kind: "image", MimeType: "image/png"}}

	first := []UnifiedMessage{{Role: "user", Attachments: attachment}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, first, &store.Model{Vision: false}, fixture.visionModelID, nil)
	if len(provider.requests) != 1 {
		t.Fatalf("first turn vision calls = %d, want 1", len(provider.requests))
	}
	// Persist the evidence the way the production path does, then prove a later
	// turn reuses it instead of paying for another read.
	stored, err := store.GetFile(context.Background(), fixture.orchestrator.db, fixture.fileID, fixture.userID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.VisionEvidence == "" || stored.VisionEvidenceKey != visionEvidenceStamp(fixture.visionModelID) {
		t.Fatalf("evidence was not cached on the file row: %+v", stored)
	}

	second := []UnifiedMessage{{Role: "user", Attachments: attachment}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, second, &store.Model{Vision: false}, fixture.visionModelID, nil)
	if len(provider.requests) != 1 {
		t.Fatalf("cached turn made %d extra vision calls, want 0", len(provider.requests)-1)
	}
	joined := ""
	for _, block := range second[0].Blocks {
		joined += block.Text
	}
	if !strings.Contains(joined, "summary: A revenue chart.") {
		t.Fatalf("cached evidence was not injected:\n%s", joined)
	}
}

func TestResolveAttachmentsRegeneratesEvidenceWhenTheVisionModelChanges(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"A revenue chart."}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	attachment := []Attachment{{ID: fixture.fileID, Kind: "image", MimeType: "image/png"}}
	history := []UnifiedMessage{{Role: "user", Attachments: attachment}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: false}, fixture.visionModelID, nil)
	if len(provider.requests) != 1 {
		t.Fatalf("vision calls = %d, want 1", len(provider.requests))
	}
	// A stamp written for a different model must not satisfy this turn.
	if err := store.SetFileVisionEvidence(context.Background(), fixture.orchestrator.db, fixture.fileID, "some-other-model:v1", "summary: stale"); err != nil {
		t.Fatal(err)
	}
	next := []UnifiedMessage{{Role: "user", Attachments: attachment}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, next, &store.Model{Vision: false}, fixture.visionModelID, nil)
	if len(provider.requests) != 2 {
		t.Fatalf("stale evidence was served instead of re-read: calls=%d", len(provider.requests))
	}
}

func TestResolveAttachmentsKeepsPlaceholderWhenVisionModelFails(t *testing.T) {
	provider := &visionCaptureProvider{fail: true}
	fixture := newVisionResolutionFixture(t, provider, true)
	history := []UnifiedMessage{{
		Role:        "user",
		Attachments: []Attachment{{ID: fixture.fileID, Kind: "image", MimeType: "image/png"}},
	}}
	var events []SseEvent
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: false}, fixture.visionModelID, func(event SseEvent) {
		events = append(events, event)
	})
	joined := ""
	for _, block := range history[0].Blocks {
		joined += block.Text
	}
	if !strings.Contains(joined, "could not be loaded") {
		t.Fatalf("failed read did not degrade to a placeholder:\n%s", joined)
	}
	if strings.Contains(joined, "vision provider unavailable") {
		t.Fatal("raw provider error leaked into the prompt")
	}
	found := false
	for _, event := range events {
		found = found || strings.Contains(event.Summary, "could not be read")
	}
	if !found {
		t.Fatalf("missing failure warning: %+v", events)
	}
}

func TestResolveAttachmentsCapsFreshVisionReadsPerTurn(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"An image."}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	attachments := make([]Attachment, 0, visionEvidenceMaxImagesPerTurn+2)
	for i := 0; i < visionEvidenceMaxImagesPerTurn+2; i++ {
		id := fixture.fileID
		if i > 0 {
			// Distinct rows keep every attachment an independent cache miss.
			copied, err := store.CreateFile(context.Background(), fixture.orchestrator.db, store.File{
				UserID: fixture.userID, ConversationID: fixture.conversation, Filename: "chart.png",
				MimeType: "image/png", Kind: "image", SizeBytes: int64(len(fixture.imageData)), StoragePath: fixture.imagePath,
			})
			if err != nil {
				t.Fatal(err)
			}
			id = copied.ID
		}
		attachments = append(attachments, Attachment{ID: id, Kind: "image", MimeType: "image/png"})
	}
	history := []UnifiedMessage{{Role: "user", Attachments: attachments}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: false}, fixture.visionModelID, nil)
	if len(provider.requests) != visionEvidenceMaxImagesPerTurn {
		t.Fatalf("vision calls = %d, want the per-turn cap %d", len(provider.requests), visionEvidenceMaxImagesPerTurn)
	}
	deferred := 0
	for _, block := range history[0].Blocks {
		if strings.Contains(block.Text, "could not be loaded") {
			deferred++
		}
	}
	if deferred != 2 {
		t.Fatalf("deferred images = %d, want 2", deferred)
	}
}

func TestResolveAttachmentsWithoutAVisionReaderKeepsExistingBehaviour(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"A revenue chart."}`}
	fixture := newVisionResolutionFixture(t, provider, false)
	history := []UnifiedMessage{{
		Role:        "user",
		Attachments: []Attachment{{ID: fixture.fileID, Kind: "image", MimeType: "image/png"}},
	}}
	// An empty reader is how the caller reports "no usable vision model".
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: false}, "", nil)
	if len(provider.requests) != 0 {
		t.Fatalf("unconfigured outsourcing made %d vision calls", len(provider.requests))
	}
	if len(history[0].Blocks) != 1 || !strings.Contains(history[0].Blocks[0].Text, "lacks vision") {
		t.Fatalf("placeholder changed: %+v", history[0].Blocks)
	}
}

// A vision-capable turn must ignore the outsourcing reader entirely: the model
// sees the pixels, so generating evidence would be wasted spend and the reader's
// text would duplicate the image.
func TestResolveAttachmentsIgnoresTheReaderForAVisionCapableModel(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"A revenue chart."}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	history := []UnifiedMessage{{
		Role:        "user",
		Attachments: []Attachment{{ID: fixture.fileID, Kind: "image", MimeType: "image/png"}},
	}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: true}, fixture.visionModelID, nil)
	if len(provider.requests) != 0 {
		t.Fatalf("a vision-capable turn made %d outsourcing calls", len(provider.requests))
	}
	if len(history[0].Blocks) != 1 || history[0].Blocks[0].Kind != "image" {
		t.Fatalf("image was not inlined natively: %+v", history[0].Blocks)
	}
}

func TestResolveVisionModelIDValidatesTheConfiguredModel(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"x"}`}
	fixture := newVisionResolutionFixture(t, provider, false)
	ctx := context.Background()

	if got := fixture.orchestrator.resolveVisionModelID(ctx); got != "" {
		t.Fatalf("unset setting resolved to %q", got)
	}
	if err := store.SetSetting(fixture.orchestrator.db, "vision_model_id", fixture.visionModelID); err != nil {
		t.Fatal(err)
	}
	if got := fixture.orchestrator.resolveVisionModelID(ctx); got != fixture.visionModelID {
		t.Fatalf("configured vision model resolved to %q", got)
	}
	// A chat model without the Vision flag cannot read images, so pointing the
	// setting at one must not advertise outsourcing.
	if err := store.SetSetting(fixture.orchestrator.db, "vision_model_id", fixture.textModelID); err != nil {
		t.Fatal(err)
	}
	if got := fixture.orchestrator.resolveVisionModelID(ctx); got != "" {
		t.Fatalf("a model without the Vision flag resolved to %q", got)
	}
	// Neither can a disabled one.
	if err := store.SetSetting(fixture.orchestrator.db, "vision_model_id", fixture.visionModelID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.orchestrator.db.Exec(`UPDATE models SET enabled=0 WHERE id=?`, fixture.visionModelID); err != nil {
		t.Fatal(err)
	}
	if got := fixture.orchestrator.resolveVisionModelID(ctx); got != "" {
		t.Fatalf("a disabled vision model resolved to %q", got)
	}
}

// The regression that made outsourcing dead code: history assembly stripped the
// image attachments for a text-only model before resolveAttachments ever ran, so
// there was nothing left to turn into evidence.
func TestHistoryAssemblyKeepsImageAttachmentsForOutsourcing(t *testing.T) {
	imageAttachment := Attachment{ID: "f1", Kind: "image", MimeType: "image/png"}
	history := []UnifiedMessage{
		{Role: "user", Attachments: []Attachment{imageAttachment}},
		{Role: "user", Attachments: []Attachment{{ID: "f2", Kind: "pdf", MimeType: "application/pdf"}}},
	}

	stripped := stripImageBlocks(history)
	if len(stripped[0].Attachments) != 0 {
		t.Fatalf("strict strip kept an image attachment: %+v", stripped[0].Attachments)
	}
	if len(stripped[0].Blocks) == 0 {
		t.Fatal("strict strip must leave the placeholder so the turn stays non-empty")
	}

	kept := stripImageBlocksKeepingAttachments(history)
	if len(kept[0].Attachments) != 1 || kept[0].Attachments[0].ID != "f1" {
		t.Fatalf("outsourcing strip dropped the image attachment: %+v", kept[0].Attachments)
	}
	// The substitute belongs to resolveAttachments, which knows whether evidence
	// is available; writing it here would contradict real evidence.
	if len(kept[0].Blocks) != 0 {
		t.Fatalf("outsourcing strip wrote its own placeholder: %+v", kept[0].Blocks)
	}
	if len(kept[1].Attachments) != 1 {
		t.Fatalf("non-image attachment was dropped: %+v", kept[1].Attachments)
	}
}

// The end-to-end shape of the fix: a stored message with an image attachment
// must still reach resolveAttachments when outsourcing is on, and produce text.
func TestCompactionHistoryKeepsAttachmentsSoEvidenceCanBeBuilt(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"A revenue chart.","visible_text":"Q1 12"}`}
	fixture := newVisionResolutionFixture(t, provider, true)

	attachments, err := json.Marshal([]Attachment{{ID: fixture.fileID, Kind: "image", MimeType: "image/png"}})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "what does this show"}})
	if err != nil {
		t.Fatal(err)
	}
	stored := []store.Message{{
		ID: "msg1", ConversationID: fixture.conversation, Role: "user",
		Blocks: blocks, Attachments: attachments, Citations: json.RawMessage("[]"),
	}}

	policy := imageInputPolicy{Native: false, Outsource: true}
	history := compactionHistoryForRequest(stored, "openai", fixture.textModelID, true, map[string]bool{}, false, policy)
	if len(history) != 1 || len(history[0].Attachments) != 1 {
		t.Fatalf("attachment did not survive history assembly: %+v", history)
	}

	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: false}, fixture.visionModelID, nil)

	joined := ""
	for _, block := range history[0].Blocks {
		joined += block.Text
	}
	if !strings.Contains(joined, "summary: A revenue chart.") {
		t.Fatalf("evidence was not injected after history assembly:\n%s", joined)
	}
	for _, block := range history[0].Blocks {
		if block.Kind == "image" {
			t.Fatal("image bytes were injected into a text-only request")
		}
	}
}

func TestResolveAttachmentsLeavesNoContentlessTurnWhenAnImageCannotBeRead(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"x"}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	// A stale attachment id: the row is gone, so nothing can be resolved.
	history := []UnifiedMessage{{
		Role:        "user",
		Attachments: []Attachment{{ID: "f_missing", Kind: "image", MimeType: "image/png"}},
	}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: false}, fixture.visionModelID, nil)
	if len(history[0].Blocks) != 1 || !strings.Contains(history[0].Blocks[0].Text, "does not support image input") {
		t.Fatalf("unresolvable image left a contentless turn: %+v", history[0].Blocks)
	}
}

func TestVisionModelSettingPointingAtNonVisionModelIsIgnored(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"A revenue chart."}`}
	fixture := newVisionResolutionFixture(t, provider, false)
	if err := store.SetSetting(fixture.orchestrator.db, "vision_model_id", fixture.textModelID); err != nil {
		t.Fatal(err)
	}
	// The caller resolves the setting before calling resolveAttachments, so a
	// rejected setting arrives as an empty reader.
	reader := fixture.orchestrator.resolveVisionModelID(context.Background())
	history := []UnifiedMessage{{
		Role:        "user",
		Attachments: []Attachment{{ID: fixture.fileID, Kind: "image", MimeType: "image/png"}},
	}}
	fixture.orchestrator.resolveAttachments(context.Background(), fixture.userID, fixture.conversation, history, &store.Model{Vision: false}, reader, nil)
	if len(provider.requests) != 0 {
		t.Fatalf("a text-only vision model was used for %d reads", len(provider.requests))
	}
	if !strings.Contains(history[0].Blocks[0].Text, "lacks vision") {
		t.Fatalf("expected the placeholder path: %+v", history[0].Blocks)
	}
}

func TestOutsourcedPrivateImagesReadsOnlyTheNewestTurn(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"json":true}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	provider.response = `{"summary":"A revenue chart."}`
	older := base64.StdEncoding.EncodeToString(fixture.imageData)
	history := []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "first"}, {Kind: "image", Data: older, MimeType: "image/png"}}},
		{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "ok"}}},
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "and this"}, {Kind: "image", Data: older, MimeType: "image/png"}}},
	}
	out := fixture.orchestrator.OutsourcedPrivateImages(context.Background(), fixture.userID, history, fixture.visionModelID)
	if len(provider.requests) != 1 {
		t.Fatalf("private vision calls = %d, want only the newest turn", len(provider.requests))
	}
	olderText := ""
	for _, block := range out[0].Blocks {
		olderText += block.Text
	}
	if !strings.Contains(olderText, "no longer available") {
		t.Fatalf("earlier image was not degraded to a placeholder: %+v", out[0].Blocks)
	}
	newestText := ""
	for _, block := range out[2].Blocks {
		newestText += block.Text
	}
	if !strings.Contains(newestText, "summary: A revenue chart.") {
		t.Fatalf("newest image was not replaced by evidence: %+v", out[2].Blocks)
	}
	if strings.Contains(newestText, older) {
		t.Fatal("base64 image data survived into the text-only request")
	}
}

func TestOutsourcedPrivateImagesIsANoOpWithoutAVisionModel(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"x"}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	history := []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "image", Data: "AAAA", MimeType: "image/png"}}}}
	out := fixture.orchestrator.OutsourcedPrivateImages(context.Background(), fixture.userID, history, "")
	if len(provider.requests) != 0 {
		t.Fatalf("unconfigured private outsourcing made %d calls", len(provider.requests))
	}
	if out[0].Blocks[0].Kind != "image" {
		t.Fatalf("history was rewritten without a vision model: %+v", out[0].Blocks)
	}
}

func TestTaskVisionCaptionRefusesImageBlocksForANonVisionModel(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"x"}`}
	fixture := newVisionResolutionFixture(t, provider, false)
	_, err := fixture.orchestrator.task.Run(context.Background(), TaskVisionCaption, "read this", RunOpts{
		ModelID:     fixture.textModelID,
		UserID:      fixture.userID,
		ImageBlocks: []UnifiedBlock{{Kind: "image", Data: "AAAA", MimeType: "image/png"}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("err = %v, want a refusal before any provider call", err)
	}
	if len(provider.requests) != 0 {
		t.Fatal("image bytes reached a text-only provider")
	}
}

func TestTaskVisionCaptionNeverInheritsTheConversationModel(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"x"}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	_, err := fixture.orchestrator.task.Run(context.Background(), TaskVisionCaption, "read this", RunOpts{
		UserID: fixture.userID, ConversationID: fixture.conversation,
		ImageBlocks: []UnifiedBlock{{Kind: "image", Data: "AAAA", MimeType: "image/png"}},
	})
	if err == nil || !strings.Contains(err.Error(), "explicit vision model") {
		t.Fatalf("err = %v, want an explicit-model requirement", err)
	}
	if len(provider.requests) != 0 {
		t.Fatal("an unset vision model fell back to a provider call")
	}
}

func TestTaskVisionCaptionSystemPromptDemandsJSONAndUntrustedData(t *testing.T) {
	system := defaultSystem(TaskVisionCaption, false)
	for _, want := range []string{"untrusted data", "visible_text", "strict JSON"} {
		if !strings.Contains(system, want) {
			t.Fatalf("vision system prompt missing %q:\n%s", want, system)
		}
	}
}

func TestVisionEvidenceForSkipsTheCallWhenBytesAreMissing(t *testing.T) {
	provider := &visionCaptureProvider{response: `{"summary":"x"}`}
	fixture := newVisionResolutionFixture(t, provider, true)
	file, err := store.GetFile(context.Background(), fixture.orchestrator.db, fixture.fileID, fixture.userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.orchestrator.visionEvidenceFor(context.Background(), fixture.userID, fixture.conversation, file, nil, "image/png", fixture.visionModelID); err == nil {
		t.Fatal("empty image bytes produced evidence")
	}
	if len(provider.requests) != 0 {
		t.Fatal("a provider call was made without image bytes")
	}
}
