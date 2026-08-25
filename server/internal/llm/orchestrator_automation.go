// Tool routing, sandbox support, titles, and execution wrappers.
package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/fileguard"
	"aivory/server/internal/rag"
	"aivory/server/internal/store"
	"aivory/server/internal/toolnames"
)

const (
	toolRouteMaxOutputTokens = 2
	toolRouteInputRuneCap    = 240
	toolRouteInputHeadRunes  = 150
	toolRouteCustomNameCap   = 24
)

// configuredOfficialToolRequests returns every administrator-configured hosted
// tool in configuration order. The historical name mirrors the persisted
// `official_tools` field; there is deliberately no user-supplied selection.
func configuredOfficialToolRequests(raw json.RawMessage, fast bool) ([]string, []json.RawMessage) {
	definitions, err := store.ParseOfficialTools(raw)
	if err != nil {
		return nil, nil
	}
	names := make([]string, 0, len(definitions))
	requests := make([]json.RawMessage, 0, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		isHostedCodeTool := responsesRequestHasToolType(
			MergeOfficialToolRequests(nil, []json.RawMessage{definition.Request}),
			"code_interpreter",
		)
		if name == "" || (fast && (name == "code_interpreter" || isHostedCodeTool)) {
			continue
		}
		names = append(names, name)
		requests = append(requests, append(json.RawMessage(nil), definition.Request...))
	}
	return names, requests
}

// autoTurnNeedsTools resolves cheap, deterministic positive signals first. If
// the real provider declarations are small, sending them directly to the main
// model is cheaper and faster than adding another network round trip. Only the
// remaining ambiguous turns reach the dedicated route model, with no history,
// file names, schemas, request fragments, skill descriptions, or instructions.
// Failures are fail-open because the main model can still decline every tool.
func (o *Orchestrator) autoTurnNeedsTools(
	ctx context.Context,
	req RunRequest,
	history []store.Message,
	localTools []ToolDef,
	hostedNames []string,
	hostedRequests []json.RawMessage,
	files []ProjectFileSummary,
	skills []SkillIndex,
	selectedUserSkill bool,
	workspaceID, messageID string,
) bool {
	capabilities, capabilitySet := toolRouteCapabilities(localTools, hostedNames, hostedRequests)
	attachmentKinds := toolRouteAttachmentKinds(req.Attachments)
	if selectedUserSkill ||
		(toolRouteCurrentAttachmentNeedsFileTool(req.Attachments) && (capabilitySet["code"] || capabilitySet["file"])) ||
		(capabilitySet["web"] && toolRouteInputHasURL(req.UserText)) ||
		((capabilitySet["code"] || capabilitySet["file"]) && toolRouteMentionsFile(req.UserText, files)) ||
		(capabilitySet["skill"] && toolRouteMentionsSkill(req.UserText, skills)) ||
		(toolRouteIsContinuation(req.UserText) && toolRoutePreviousAssistantUsedTool(history)) {
		return true
	}

	if toolRouteSchemaTokenThreshold > 0 &&
		estimateToolDeclarationTokens(localTools, hostedRequests) <= toolRouteSchemaTokenThreshold {
		return true
	}

	if o.task == nil {
		if o.logger != nil {
			o.logger.Printf("tool route: dedicated model unavailable, enabling tools (conv=%s)", req.ConversationID)
		}
		return true
	}

	prompt := formatToolRoutePrompt(capabilities, attachmentKinds, len(files) > 0, req.UserText)
	routeCtx, cancel := context.WithTimeout(ctx, toolRouteTimeout)
	defer cancel()
	decision, err := o.task.Run(routeCtx, TaskToolRoute, prompt, RunOpts{
		UserID:          req.UserID,
		ConversationID:  req.ConversationID,
		MessageID:       messageID,
		WorkspaceID:     workspaceID,
		MaxOutputTokens: toolRouteMaxOutputTokens,
	})
	if err != nil {
		if o.logger != nil {
			o.logger.Printf("tool route: decision failed, enabling tools (conv=%s): %v", req.ConversationID, err)
		}
		return true
	}
	decision = strings.TrimSpace(decision)
	if strings.HasPrefix(decision, "0") {
		return false
	}
	if !strings.HasPrefix(decision, "1") && o.logger != nil {
		o.logger.Printf("tool route: invalid decision %q, enabling tools (conv=%s)", truncate(decision, 80), req.ConversationID)
	}
	return true
}

func estimateToolDeclarationTokens(localTools []ToolDef, hostedRequests []json.RawMessage) int {
	tokens := 0
	if len(localTools) > 0 {
		if raw, err := json.Marshal(localTools); err == nil {
			tokens += estimateToolJSONTokens(raw)
		}
	}
	if len(hostedRequests) > 0 {
		if raw, err := json.Marshal(MergeOfficialToolRequests(nil, hostedRequests)); err == nil {
			tokens += estimateToolJSONTokens(raw)
		}
	}
	return tokens
}

func estimateToolJSONTokens(raw []byte) int {
	byBytes := (len(raw) + 3) / 4
	if byContent := estimateTokens(string(raw)); byContent > byBytes {
		return byContent
	}
	return byBytes
}

func toolRouteCapabilities(localTools []ToolDef, hostedNames []string, hostedRequests []json.RawMessage) ([]string, map[string]bool) {
	set := map[string]bool{}
	custom := []string{}
	addCustom := func(name string) {
		capability := toolRouteCustomCapability(name)
		if !set[capability] {
			set[capability] = true
			custom = append(custom, capability)
		}
	}
	addKnown := func(name string) bool {
		capabilities := toolRouteKnownCapabilities(name)
		for _, capability := range capabilities {
			set[capability] = true
		}
		return len(capabilities) > 0
	}

	for _, tool := range localTools {
		if name := strings.TrimSpace(tool.Name); name != "" && !addKnown(name) {
			addCustom(name)
		}
	}
	for index, name := range hostedNames {
		known := addKnown(name)
		if index < len(hostedRequests) {
			for requestName := range unifiedToolNameSet(nil, nil, []json.RawMessage{hostedRequests[index]}) {
				known = addKnown(requestName) || known
			}
		}
		if !known {
			addCustom(name)
		}
	}

	ordered := make([]string, 0, len(set))
	for _, capability := range []string{"web", "code", "file", "image", "memory", "skill"} {
		if set[capability] {
			ordered = append(ordered, capability)
		}
	}
	ordered = append(ordered, custom...)
	return ordered, set
}

func toolRouteKnownCapabilities(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case name == toolnames.AivoryWebSearch,
		name == "web_fetch",
		name == "fetch_image",
		name == "web_search",
		strings.HasPrefix(name, "web_search_"),
		name == "google_search",
		name == "googlesearch",
		name == "url_context",
		name == "computer_use":
		return []string{"web"}
	case name == "python_execute", name == "code_interpreter", name == "shell", name == "bash":
		return []string{"code", "file"}
	case name == "file_search", strings.HasPrefix(name, "file_search_"):
		return []string{"file"}
	case name == "image_generate", name == "image_generation":
		return []string{"image"}
	case name == "save_memory":
		return []string{"memory"}
	case name == "use_skill", strings.HasPrefix(name, "skill:"):
		return []string{"skill"}
	default:
		return nil
	}
}

func toolRouteCustomCapability(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
			if b.Len() >= toolRouteCustomNameCap {
				break
			}
		}
	}
	if b.Len() == 0 {
		b.WriteString("tool")
	}
	return "custom:" + b.String()
}

func toolRouteAttachmentKinds(attachments []Attachment) []string {
	set := map[string]bool{}
	for _, attachment := range attachments {
		kind := strings.ToLower(strings.TrimSpace(attachment.Kind))
		switch {
		case attachmentIsImage(attachment):
			set["image"] = true
		case kind == "sheet" || isSandboxSpreadsheetFilename(attachment.Filename):
			set["sheet"] = true
		case kind == "code":
			set["code"] = true
		case kind == "pdf":
			set["pdf"] = true
		case kind == "doc":
			set["doc"] = true
		case kind == "text":
			set["text"] = true
		default:
			set["file"] = true
		}
	}
	ordered := []string{}
	for _, kind := range []string{"sheet", "code", "pdf", "doc", "text", "image", "file"} {
		if set[kind] {
			ordered = append(ordered, kind)
		}
	}
	return ordered
}

func toolRouteCurrentAttachmentNeedsFileTool(attachments []Attachment) bool {
	for _, attachment := range attachments {
		kind := strings.ToLower(strings.TrimSpace(attachment.Kind))
		if kind == "sheet" || kind == "code" || isSandboxSpreadsheetFilename(attachment.Filename) {
			return true
		}
		switch strings.ToLower(filepath.Ext(strings.TrimSpace(attachment.Filename))) {
		case ".parquet", ".arrow", ".feather", ".jsonl", ".ndjson", ".sqlite", ".sqlite3", ".db", ".sql":
			return true
		}
	}
	return false
}

func toolRouteInputHasURL(input string) bool {
	input = strings.ToLower(input)
	return strings.Contains(input, "https://") || strings.Contains(input, "http://")
}

func toolRouteMentionsFile(input string, files []ProjectFileSummary) bool {
	input = strings.ToLower(input)
	for _, file := range files {
		name := strings.ToLower(strings.TrimSpace(file.Name))
		if name != "" && strings.Contains(input, name) {
			return true
		}
	}
	return false
}

func toolRouteMentionsSkill(input string, skills []SkillIndex) bool {
	input = strings.ToLower(input)
	for _, skill := range skills {
		name := strings.ToLower(strings.TrimSpace(skill.Name))
		if name != "" && strings.Contains(input, name) {
			return true
		}
	}
	return false
}

func toolRoutePreviousAssistantUsedTool(history []store.Message) bool {
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Role != "assistant" {
			continue
		}
		var blocks []UnifiedBlock
		if json.Unmarshal(message.Blocks, &blocks) != nil {
			return false
		}
		for _, block := range blocks {
			if block.Kind == "tool_call" || block.Kind == "tool_output" {
				return true
			}
		}
		return false
	}
	return false
}

func toolRouteIsContinuation(input string) bool {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" || len([]rune(input)) > 160 {
		return false
	}
	for _, prefix := range []string{
		"continue", "go on", "keep going", "proceed", "do it", "run it", "try again", "retry", "again", "next", "more", "use that",
		"继续", "接着", "往下", "下一步", "再试", "重试", "再来", "就按这个", "按这个", "用这个", "然后呢", "再查", "再运行",
		"続け", "再試行", "poursuis", "continuer", "encore",
	} {
		if strings.HasPrefix(input, prefix) {
			return true
		}
	}
	return false
}

func formatToolRoutePrompt(capabilities, attachmentKinds []string, hasFiles bool, input string) string {
	if len(capabilities) == 0 {
		capabilities = []string{"none"}
	}
	if len(attachmentKinds) == 0 {
		attachmentKinds = []string{"none"}
	}
	files := "0"
	if hasFiles {
		files = "1"
	}
	return "CAP=" + strings.Join(capabilities, ",") +
		"\nATT=" + strings.Join(attachmentKinds, ",") +
		"\nFILES=" + files +
		"\nINPUT=" + truncateToolRouteInput(input)
}

func truncateToolRouteInput(input string) string {
	input = strings.Join(strings.Fields(strings.TrimSpace(input)), " ")
	runes := []rune(input)
	if len(runes) <= toolRouteInputRuneCap {
		return input
	}
	tailRunes := toolRouteInputRuneCap - toolRouteInputHeadRunes
	return string(runes[:toolRouteInputHeadRunes]) + " ... " + string(runes[len(runes)-tailRunes:])
}

// forcedSearchHistoryTurns caps how many recent messages feed the search-query
// task model (keep the prompt small; the latest question dominates intent).
const forcedSearchHistoryTurns = 6

// deriveSearchQueries asks the task model for a few web-search queries that
// would answer the user's latest message given recent context. Falls back to
// the raw user text on any failure so a search still runs.
func (o *Orchestrator) deriveSearchQueries(ctx context.Context, req RunRequest, history []store.Message) []string {
	var b strings.Builder
	start := 0
	if len(history) > forcedSearchHistoryTurns {
		start = len(history) - forcedSearchHistoryTurns
	}
	for _, m := range history[start:] {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		if t := strings.TrimSpace(renderBlocksAsText(blocks)); t != "" {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, truncate(t, 600))
		}
	}
	fmt.Fprintf(&b, "user (latest): %s\n", strings.TrimSpace(req.UserText))

	var out struct {
		Queries []string `json:"queries"`
	}
	if o.task != nil {
		err := o.task.RunJSON(ctx, TaskSearchQueries, b.String(), &out, RunOpts{
			UserID: req.UserID, ConversationID: req.ConversationID,
		})
		if err == nil {
			cleaned := make([]string, 0, len(out.Queries))
			for _, q := range out.Queries {
				if q = strings.TrimSpace(q); q != "" {
					cleaned = append(cleaned, q)
				}
				if len(cleaned) >= forcedSearchQueryCap {
					break
				}
			}
			if len(cleaned) > 0 {
				return cleaned
			}
		}
	}
	if u := strings.TrimSpace(req.UserText); u != "" {
		return []string{u}
	}
	return nil
}

// forcedWebSearch runs a NON-tool web search for a no-tools + web-search turn
// (§4.4-B): a task model turns the conversation into queries, the configured
// searcher runs them, progress streams as aivory_web_search rounds,
// and the results become a <web-search-result> block for prompt injection.
// Returns (contextText, citations); ("", nil) when search is unconfigured or
// yields nothing. Best-effort — a failure never blocks the turn.
func (o *Orchestrator) forcedWebSearch(ctx context.Context, req RunRequest, conv *store.Conversation, history []store.Message, baseIndex int, allowedTools map[string]bool, onEvent func(SseEvent)) (string, []Citation) {
	// Respect the admin platform kill-switch: if aivory_web_search is globally
	// disabled, the forced-search path must not run it either (it would
	// otherwise be a back door around `disabled_tools`).
	if o.disabledToolSet()[toolnames.AivoryWebSearch] {
		return "", nil
	}
	if allowedTools != nil && !allowedTools[toolnames.AivoryWebSearch] {
		return "", nil
	}
	queries := o.deriveSearchQueries(ctx, req, history)
	if len(queries) == 0 {
		return "", nil
	}
	searchTimeout := toolCallTimeout(toolnames.AivoryWebSearch)
	tc := &ToolContext{UserID: req.UserID, ConvID: req.ConversationID, WorkspaceID: conv.WorkspaceID, ModelID: req.ModelID, BuiltinTools: allowedTools}
	var cites []Citation
	var b strings.Builder
	for i, q := range queries {
		id := fmt.Sprintf("fws_%d", i+1)
		input, _ := json.Marshal(map[string]any{"query": q})
		onEvent(SseEvent{Type: "tool_start", Name: toolnames.AivoryWebSearch, ID: id, Input: input})
		// Bound each search with the same per-call timeout orchToolRunner applies
		// (§4.3) so a stalled search backend can't hang the turn pre-first-token.
		sctx, cancel := context.WithTimeout(ctx, searchTimeout)
		out, qcites, err := o.tools.Run(sctx, toolnames.AivoryWebSearch, input, tc)
		cancel()
		if err != nil {
			onEvent(SseEvent{Type: "tool_result", Name: toolnames.AivoryWebSearch, ID: id, Summary: "search failed", Status: "error"})
			continue
		}
		// The searcher returns this exact sentence when no backend is configured
		// (settings + env both empty). Injecting that placeholder would only add
		// noise — stop and let the model answer from training knowledge.
		if strings.HasPrefix(out, "Search not yet configured") {
			onEvent(SseEvent{Type: "tool_result", Name: toolnames.AivoryWebSearch, ID: id, Summary: "search not configured", Status: "error"})
			return "", nil
		}
		onEvent(SseEvent{Type: "tool_result", Name: toolnames.AivoryWebSearch, ID: id, Summary: truncate(out, 400), Status: "complete"})
		// The searcher numbers its inline [n] markers 1..k locally (per query),
		// but the citation RECORDS are renumbered globally with an offset so the
		// KB + web source lists never collide. Remap the injected text's markers
		// to the same offset numbering, or the model's [n] references point at
		// the wrong source.
		offset := baseIndex + len(cites)
		for j := range qcites {
			c := qcites[j]
			c.Index = offset + j + 1
			cites = append(cites, c)
			onEvent(SseEvent{Type: "citation", Citation: &c})
		}
		fmt.Fprintf(&b, "Query: %s\n%s\n\n", q, remapCitationMarkers(strings.TrimSpace(out), len(qcites), offset))
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", nil
	}
	return "<web-search-result>\n" + strings.TrimSpace(b.String()) + "\n</web-search-result>", cites
}

// listSandboxFiles returns every conversation upload eligible for sandbox
// staging. The original bytes are made available regardless of format so
// Python can perform targeted edits without reconstructing Office/PDF files.
// Shared by the system-prompt listing and the no-tools forced read.
func listSandboxFiles(ctx context.Context, db *sql.DB, convID, userID string, roots ...string) []ProjectFileSummary {
	out := []ProjectFileSummary{}
	convFiles, err := store.ListFilesByConversation(ctx, db, convID, userID)
	if err == nil {
		for _, f := range convFiles {
			if f.SizeBytes < 0 || f.SizeBytes > sandboxUploadStagingFileSize {
				continue
			}
			kind := strings.TrimSpace(f.Kind)
			if kind == "" {
				kind = strings.TrimSpace(f.MimeType)
			}
			if kind == "" {
				kind = "file"
			}
			out = append(out, ProjectFileSummary{Name: f.Filename, Kind: kind})
		}
	}
	if artifacts, err := store.ListImageArtifactsByConversation(ctx, db, convID, userID); err == nil {
		for _, artifact := range artifacts {
			if !reusableGeneratedImageSource(artifact.Source) || !storedSandboxArtifactLooksLikeImage(artifact, roots...) {
				continue
			}
			name := filepath.Base(strings.TrimSpace(artifact.Filename))
			if name == "" || name == "." || name == "/" {
				name = "image"
			}
			out = append(out, ProjectFileSummary{Name: "generated-" + name, Kind: "image"})
		}
	}
	return out
}

func reusableGeneratedImageSource(source string) bool {
	switch strings.TrimSpace(source) {
	case "", store.ArtifactSourceImageGenerate, store.ArtifactSourceHostedImageGeneration:
		return true
	default:
		return false
	}
}

func storedSandboxArtifactLooksLikeImage(artifact store.Artifact, roots ...string) bool {
	if artifact.SizeBytes < 0 || artifact.SizeBytes > sandboxUploadStagingFileSize {
		return false
	}
	safePath, err := resolveLLMStoragePath(artifact.StoragePath, roots...)
	if err != nil {
		return false
	}
	f, err := os.Open(safePath)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, sandboxUploadStagingFileSize+1))
	return err == nil && int64(len(data)) <= sandboxUploadStagingFileSize && providerImageMIMEFromBytes(data) != ""
}

func isSandboxSpreadsheetFilename(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".csv", ".tsv", ".xlsx", ".xls", ".xlsm":
		return true
	}
	return false
}

// sandboxFilesHaveSheet reports whether any staged file is a spreadsheet — the
// only sandbox kind with no message-layer fallback (text/code are RAG-injected,
// images go to vision), so it's the only one the forced read must cover.
func sandboxFilesHaveSheet(files []ProjectFileSummary) bool {
	for _, f := range files {
		if f.Kind == "sheet" || isSandboxSpreadsheetFilename(f.Name) {
			return true
		}
	}
	return false
}

// shouldInjectSpreadsheetPreview selects the server-side spreadsheet path when
// the current turn cannot read staged uploads with python_execute. Fast mode and
// explicit no-tools turns therefore share the same parsing/injection behavior.
func shouldInjectSpreadsheetPreview(files []ProjectFileSummary, pythonExecuteAvailable bool) bool {
	return !pythonExecuteAvailable && sandboxFilesHaveSheet(files)
}

// Spreadsheet preview bounds for the in-process read used without Python.
const (
	// spreadsheetPreviewInjectionCap bounds the injected preview so a wide or long
	// sheet can't blow the context budget (runes).
	spreadsheetPreviewInjectionCap = 8000
	spreadsheetPreviewRows         = 30
	spreadsheetPreviewCols         = 40
)

// previewSpreadsheetFiles parses the conversation's uploaded spreadsheets
// IN-PROCESS (stdlib rag.SpreadsheetPreview — no code sandbox, no python_execute)
// and returns a bounded <uploaded-data-preview> block. It replaces the sandbox
// read whenever python_execute is unavailable, including fast and no-tools turns,
// so xlsx/csv are parsed server-side and injected as prompt context. Returns ""
// when there are no sheets or none could be parsed (each failure is logged and
// skipped, so one bad file doesn't sink the rest).
func (o *Orchestrator) previewSpreadsheetFiles(ctx context.Context, userID, convID string) string {
	files, err := store.ListFilesByConversation(ctx, o.db, convID, userID)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, f := range files {
		if f.Kind != "sheet" && !isSandboxSpreadsheetFilename(f.Filename) {
			continue
		}
		safePath, err := resolveLLMStoragePath(f.StoragePath, o.uploadDir)
		if err != nil {
			continue
		}
		text, perr := rag.SpreadsheetPreview(safePath, f.Filename, spreadsheetPreviewRows, spreadsheetPreviewCols)
		if perr != nil || strings.TrimSpace(text) == "" {
			if o.logger != nil {
				o.logger.Printf("spreadsheet preview skipped file=%q: %v", f.Filename, perr)
			}
			continue
		}
		b.WriteString(strings.TrimRight(text, "\n"))
		b.WriteString("\n\n")
	}
	preview := strings.TrimSpace(b.String())
	if preview == "" {
		return ""
	}
	if r := []rune(preview); len(r) > spreadsheetPreviewInjectionCap {
		preview = string(r[:spreadsheetPreviewInjectionCap]) + "\n…(truncated)"
	}
	return "<uploaded-data-preview>\n" + preview + "\n</uploaded-data-preview>"
}

// resolveLLMStoragePath keeps legacy unit fixtures usable when no deployment
// roots are supplied, while the production constructor always passes both
// configured roots. Remote/object-storage URIs are not accepted by these local
// readers and therefore fail closed.
func resolveLLMStoragePath(path string, roots ...string) (string, error) {
	if len(roots) == 0 || strings.TrimSpace(roots[0]) == "" {
		return path, nil
	}
	return fileguard.ResolveExisting(path, roots...)
}

// remapCitationMarkers rewrites a searcher's local inline citation markers
// `[1]..[maxLocal]` to `[offset+1]..[offset+maxLocal]` so injected search text
// references the globally-renumbered citation records. Markers outside
// 1..maxLocal (incidental bracketed numbers in snippets) are left untouched.
// Single pass, no double-remapping (each match consumed once).
func remapCitationMarkers(text string, maxLocal, offset int) string {
	if maxLocal <= 0 || offset <= 0 || !strings.Contains(text, "[") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '[' {
			b.WriteByte(text[i])
			i++
			continue
		}
		j := i + 1
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		// A valid marker is "[<digits>]" with at least one digit.
		if j > i+1 && j < len(text) && text[j] == ']' {
			n, _ := strconv.Atoi(text[i+1 : j])
			if n >= 1 && n <= maxLocal {
				fmt.Fprintf(&b, "[%d]", offset+n)
				i = j + 1
				continue
			}
		}
		b.WriteByte('[')
		i++
	}
	return b.String()
}

// injectRAGIntoHistory appends retrieved context to the LAST user message.
func injectRAGIntoHistory(msgs []UnifiedMessage, text string) []UnifiedMessage {
	if strings.TrimSpace(text) == "" {
		return msgs
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			msgs[i].Blocks = append(msgs[i].Blocks, UnifiedBlock{Kind: "text", Text: text})
			return msgs
		}
	}
	return msgs
}

// formatSelectedUserSkills renders private skill content as an explicit part of
// the user's request. The store has already enforced the five-skill/64 KiB
// instruction-body limits. No part of this text is included in the system
// prompt or administrator skill index.
func formatSelectedUserSkills(skills []store.UserSkill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<user-selected-skills>\n")
	b.WriteString("Apply the following private skills as user-provided instructions for this request.\n")
	for _, skill := range skills {
		b.WriteString("\n<user-selected-skill name=\"")
		b.WriteString(skill.Name)
		b.WriteString("\">\nDescription: ")
		b.WriteString(skill.Description)
		b.WriteString("\n\n")
		b.WriteString(skill.Instructions)
		b.WriteString("\n</user-selected-skill>\n")
	}
	b.WriteString("</user-selected-skills>\n")
	return b.String()
}

// injectSelectedUserSkillsIntoHistory appends selected skill instructions to
// the last user turn. It intentionally mirrors RAG's last-user placement, but
// runs first so later provider-neutral context additions preserve their order.
func injectSelectedUserSkillsIntoHistory(msgs []UnifiedMessage, skills []store.UserSkill) []UnifiedMessage {
	text := formatSelectedUserSkills(skills)
	if text == "" {
		return msgs
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			msgs[i].Blocks = append(msgs[i].Blocks, UnifiedBlock{Kind: "text", Text: text})
			return msgs
		}
	}
	return msgs
}

func computeCost(m store.Model, u Usage) float64 {
	cost := 0.0
	if m.Kind == "image" {
		// For mock image generation, OutputTokens is repurposed as image count.
		return float64(u.OutputTokens) * m.PricePerImage
	}
	cost += float64(u.InputTokens) / 1_000_000 * m.PriceInput
	cost += float64(u.OutputTokens) / 1_000_000 * m.PriceOutput
	cost += float64(u.CacheReadTokens) / 1_000_000 * m.PriceCacheRead
	cost += float64(u.CacheWriteTokens) / 1_000_000 * m.PriceCacheWrite
	return cost
}

// shouldGenerateTitle is true when the conversation still has its default title.
func shouldGenerateTitle(c *store.Conversation) bool {
	t := strings.TrimSpace(c.Title)
	return t == "" || t == "新对话" || t == "New conversation"
}

// scheduleTitle fires a TaskLLM call in the background to generate a real title.
func (o *Orchestrator) scheduleTitle(convID, userID, userText, locale string) {
	// First, set a deterministic clip so the sidebar updates immediately even
	// when no task model/queue is configured.
	first := clipTitle(userText)
	if first != "" {
		o.persistGeneratedTitle(context.Background(), convID, userID, first)
	}
	o.enqueueTitleTask(convID, userID, userText, locale)
}

// scheduleTitleUpgrade keeps the already-persisted image filename fallback
// visible until the title task has produced a better semantic label.
func (o *Orchestrator) scheduleTitleUpgrade(convID, userID, sourceText, locale string) {
	o.enqueueTitleTask(convID, userID, sourceText, locale)
}

func (o *Orchestrator) enqueueTitleTask(convID, userID, sourceText, locale string) {
	if o.queue == nil || o.task == nil || strings.TrimSpace(sourceText) == "" {
		return
	}
	// Force the title language to the user's UI language. The task model is a
	// separate, often language-biased model that ignores a soft "follow the user"
	// hint, so we append an authoritative directive WRITTEN IN the target language
	// (strongest signal); fall back to matching the message when locale is unknown.
	sys := defaultSystem(TaskTitle, false)
	if dir := titleLanguageDirective(locale); dir != "" {
		sys += " " + dir
	} else {
		sys += " Write the title in the same language as the user's message."
	}
	o.queue.Enqueue("title.generate", func(ctx context.Context) error {
		text, err := o.task.Run(ctx, TaskTitle, sourceText, RunOpts{
			UserID:          userID,
			ConversationID:  convID,
			MaxOutputTokens: titleGenerationOutputTokens,
			SystemPrompt:    sys,
		})
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) == "" {
			if o.logger != nil {
				o.logger.Printf("title generation: task returned no visible text (conv=%s user=%s)", convID, userID)
			}
			return nil
		}
		title := cleanTitle(text)
		if title == "" {
			if o.logger != nil {
				o.logger.Printf("title generation: task output was empty after cleanup (conv=%s user=%s)", convID, userID)
			}
			return nil
		}
		o.persistGeneratedTitle(ctx, convID, userID, title)
		return nil
	})
}

func imageConversationTitle(attachments []Attachment, locale string) string {
	for _, attachment := range attachments {
		if attachment.Kind != "image" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.MimeType)), "image/") {
			continue
		}
		base := filepath.Base(strings.TrimSpace(attachment.Filename))
		name := strings.TrimSuffix(base, filepath.Ext(base))
		if title := clipTitle(name); title != "" {
			return title
		}
	}
	switch promptLocaleKey(locale) {
	case "zh":
		return "图片对话"
	case "zh-Hant":
		return "圖片對話"
	case "ja":
		return "画像の会話"
	case "fr":
		return "Conversation sur une image"
	default:
		return "Image conversation"
	}
}

func imageOnlyTitleSource(answer string) string {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return ""
	}
	runes := []rune(answer)
	if len(runes) > 1200 {
		runes = runes[:1200]
	}
	return "The user sent an image without accompanying text. Label the image topic described in this assistant response:\n" + string(runes)
}

// persistGeneratedTitle couples the asynchronous title write to the realtime
// notification that follows it. Usage logging alone does not prove that a task
// produced a usable, persisted title, so failed writes reach the server log.
func (o *Orchestrator) persistGeneratedTitle(ctx context.Context, convID, userID, title string) bool {
	if _, err := store.UpdateConversation(ctx, o.db, convID, userID, store.ConversationPatch{Title: &title}); err != nil {
		if o.logger != nil {
			o.logger.Printf("title generation: update conversation %s: %v", convID, err)
		}
		return false
	}
	if o.onConversationUpdated != nil {
		o.onConversationUpdated(userID, convID)
	}
	return true
}

func clipTitle(s string) string {
	s = strings.Join(strings.Fields(titleMathContentToPlainText(s)), " ")
	if s == "" {
		return ""
	}
	rs := []rune(s)
	if len(rs) > 28 {
		rs = rs[:28]
	}
	return string(rs)
}

func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'.。．＂")
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	// §6.3: keep titles short. CJK is dense (≤24 runes is plenty); a Western title
	// (≈8 words) needs more room, so clamp higher and back off to a word boundary
	// rather than cutting mid-word.
	limit := 24
	if !hasCJK(s) {
		limit = 56
	}
	rs := []rune(s)
	if len(rs) > limit {
		cut := strings.TrimSpace(string(rs[:limit]))
		if !hasCJK(s) {
			if idx := strings.LastIndexByte(cut, ' '); idx > limit/2 {
				cut = cut[:idx]
			}
		}
		return strings.TrimSpace(cut)
	}
	return strings.TrimSpace(s)
}

// hasCJK reports whether s contains a CJK ideograph, kana, or hangul — used to
// pick a tighter title clamp for dense CJK vs a roomier one for Western text.
func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x3040 && r <= 0x30ff) || // hiragana + katakana
			(r >= 0x3400 && r <= 0x4dbf) || // CJK ext A
			(r >= 0x4e00 && r <= 0x9fff) || // CJK unified
			(r >= 0xf900 && r <= 0xfaff) || // CJK compatibility
			(r >= 0xac00 && r <= 0xd7a3) { // hangul syllables
			return true
		}
	}
	return false
}

// orchToolRunner adapts the tool registry's Run signature to the provider's
// expectation (no ToolContext parameter), threading the orchestrator's
// captured tool context through.
type orchToolRunner struct {
	orch    *Orchestrator
	ctx     *ToolContext
	onEvent func(SseEvent)
}

// toolDefAllowlistRunner binds execution to the exact definitions sent in the
// current provider request. It blocks unsolicited or stale calls even when a
// broader persisted model policy would otherwise allow the tool.
type toolDefAllowlistRunner struct {
	next    ToolRunner
	allowed map[string]bool
}

func (r toolDefAllowlistRunner) Run(ctx context.Context, name string, input []byte) (string, []Citation, error) {
	if !r.allowed[name] {
		return "", nil, fmt.Errorf("tool %q is not enabled for the current model request", name)
	}
	return r.next.Run(ctx, name, input)
}

// toolRunnerForModelRequest retargets the concrete orchestrator runner during a
// TTFT model switch. In particular, use_skill must query bindings for the
// fallback model, not the primary model whose context built the first request.
func toolRunnerForModelRequest(runner ToolRunner, modelID string, definitions []ToolDef) ToolRunner {
	base := runner
	if restricted, ok := runner.(toolDefAllowlistRunner); ok {
		base = restricted.next
	}
	if current, ok := base.(*orchToolRunner); ok && current.ctx != nil {
		source := current.ctx
		var params map[string]any
		if source.ImageRequestParams != nil {
			params = make(map[string]any, len(source.ImageRequestParams))
			for key, value := range source.ImageRequestParams {
				params[key] = value
			}
		}
		fallbackContext := &ToolContext{
			UserID:               source.UserID,
			ConvID:               source.ConvID,
			MessageID:            source.MessageID,
			WorkspaceID:          source.WorkspaceID,
			ModelID:              modelID,
			ProjectID:            source.ProjectID,
			ProjectName:          source.ProjectName,
			DB:                   source.DB,
			WorkspaceAccessCheck: source.WorkspaceAccessCheck,
			DeepResearch:         source.DeepResearch,
			Fast:                 source.Fast,
			BuiltinTools:         toolDefNameSet(definitions),
			AdminSkillIDs:        cloneBoolMap(source.AdminSkillIDs),
			ImageModelID:         source.ImageModelID,
			ImageRequestParams:   params,
			ImageInputIDs:        append([]string(nil), source.ImageInputIDs...),
			ImageUserPrompt:      source.ImageUserPrompt,
			SkipImageQuota:       source.SkipImageQuota,
			ImageBilling:         source.ImageBilling,
			OnArtifact:           source.OnArtifact,
			counts:               map[string]int{},
			toolState:            source.requestToolExecutionState(),
			citationIndexes:      source.citationIndexes,
		}
		base = &orchToolRunner{orch: current.orch, ctx: fallbackContext, onEvent: current.onEvent}
	}
	return toolDefAllowlistRunner{next: base, allowed: toolDefNameSet(definitions)}
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// toolTimeouts bounds a single tool invocation per tool type (§4.3: search
// 10s / sandbox 120s / image 60s) so one slow tool can't stall the turn.
var toolTimeouts = map[string]time.Duration{
	toolnames.AivoryWebSearch: envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUTS", 10*time.Second),
	"web_fetch":               envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUTS_2", 30*time.Second),
	"fetch_image":             envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUTS_FETCH_IMAGE", 45*time.Second),
	"python_execute":          120 * time.Second,
	"image_generate":          envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUTS_3", 600*time.Second), // slow third-party image gateways need a wide window
}

var toolTimeoutDefault = envcfg.Dur("AIVORY_LLM_TOOL_TIMEOUT_DEFAULT", 100*time.Second)

// sandboxExecCtxTimeout sizes the per-call ctx for python_execute to the
// admin-configured sandbox exec cap (settings.sandbox_exec_timeout_sec, default
// 120s, clamped [10,600]) PLUS margin, so the ctx outlasts the sandbox HTTP
// client timeout (exec + ~120s overhead) and never cancels a valid long run
// early. Mirrors the clamp in tools.settingsSandbox.execTimeout (kept here
// rather than imported to avoid an llm→tools import cycle via ToolContext).
func sandboxExecCtxTimeout(db *sql.DB) time.Duration {
	secs := 120
	if db != nil {
		if raw, err := store.GetSetting(db, "sandbox_exec_timeout_sec"); err == nil {
			n := 0
			if json.Unmarshal(raw, &n) != nil {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					n, _ = strconv.Atoi(strings.TrimSpace(s))
				}
			}
			if n > sandboxExecTimeoutClampRangeMax {
				secs = sandboxExecTimeoutClampRangeMax
			} else if n >= sandboxExecTimeoutClampRangeMin {
				secs = n
			} else if n > 0 {
				secs = sandboxExecTimeoutClampRangeMin
			}
		}
	}
	return time.Duration(secs)*time.Second + sandboxExecCtxSafetyMargin
}

func (r *orchToolRunner) Run(ctx context.Context, name string, input []byte) (string, []Citation, error) {
	if r.ctx == nil {
		return "", nil, errors.New("tool context unavailable")
	}
	if r.ctx.WorkspaceAccessCheck != nil {
		if err := r.ctx.WorkspaceAccessCheck(ctx); err != nil {
			return "", nil, fmt.Errorf("workspace access revoked before tool execution: %w", err)
		}
	}
	out, cites, err := r.ctx.executeTrackedTool(ctx, name, input, func() (string, []Citation, error) {
		return r.runUntracked(ctx, name, input)
	})
	if err == nil && r.onEvent != nil {
		for _, citation := range cites {
			citationCopy := citation
			r.onEvent(SseEvent{Type: "citation", Citation: &citationCopy})
		}
	}
	return out, cites, err
}

func (r *orchToolRunner) runUntracked(ctx context.Context, name string, input []byte) (string, []Citation, error) {
	if name == "python_execute" {
		release, err := acquirePythonConversationGate(ctx, r.ctx.ConvID)
		if err != nil {
			return "", nil, err
		}
		defer release()
	}
	if err := r.ctx.charge(name); err != nil {
		return "", nil, err
	}
	timeout, ok := toolTimeouts[name]
	if !ok {
		timeout = toolTimeoutDefault
	}
	if name == "python_execute" {
		// The sandbox exec cap is admin-configurable (sandbox_exec_timeout_sec,
		// up to 600s); a static 120s ctx here would silently cancel a longer-but-
		// valid run before the sidecar/client deadline. Size the ctx to the
		// configured cap + margin so raising the setting actually takes effect.
		timeout = sandboxExecCtxTimeout(r.orch.db)
	}
	if remaining, limited := r.ctx.toolTimeRemaining(); limited {
		if remaining <= 0 {
			return "", nil, r.ctx.toolTimeBudgetError()
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()
	out, cites, err := r.orch.tools.Run(ctx, name, input, r.ctx)
	if err != nil && parentCtx.Err() == nil && r.ctx.toolTimeBudgetExceeded() {
		err = r.ctx.toolTimeBudgetError()
	}
	if err != nil && r.orch.logger != nil {
		r.orch.logger.Printf("tool execution failed (conv=%s msg=%s tool=%s): %v", r.ctx.ConvID, r.ctx.MessageID, name, err)
	}
	if err == nil && len(cites) > 0 && r.ctx != nil && r.ctx.citationIndexes != nil {
		offset := r.ctx.citationIndexes.allocate(len(cites))
		out = remapCitationMarkers(out, len(cites), offset)
		for i := range cites {
			cites[i].Index = offset + i + 1
			cites[i].GlobalIndex = true
		}
	}
	return out, cites, err
}
