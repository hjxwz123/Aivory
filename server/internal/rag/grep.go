package rag

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"aivory/server/internal/store"
)

const (
	defaultGrepFullTextThreshold    = 500
	defaultGrepCodeFullTextMaxLines = 200
	defaultGrepMaxMatches           = 20
	defaultGrepContextLines         = 3
	defaultGrepReadMaxLines         = 200
	defaultGrepReadMaxChars         = 16000

	grepMaxPatternRunes    = 500
	grepOutlineMaxHeadings = 80
)

// GrepSettings holds the live admin-tunable Grep mode parameters.
type GrepSettings struct {
	FullTextThreshold    int // small conversation docs ≤ this (tokens) inject whole; 0 = grep everything
	CodeFullTextMaxLines int // small conversation code/text ≤ this (lines) inject whole; 0 = grep everything
	MaxMatches           int // maximum match hunks returned per doc_grep call
	ContextLines         int // context lines shown before/after each match
	ReadMaxLines         int // maximum lines returned per doc_read call
	ReadMaxChars         int // character cap per doc_grep / doc_read response
}

func (s *Service) GrepSettings() GrepSettings {
	cfg := GrepSettings{
		FullTextThreshold:    defaultGrepFullTextThreshold,
		CodeFullTextMaxLines: defaultGrepCodeFullTextMaxLines,
		MaxMatches:           defaultGrepMaxMatches,
		ContextLines:         defaultGrepContextLines,
		ReadMaxLines:         defaultGrepReadMaxLines,
		ReadMaxChars:         defaultGrepReadMaxChars,
	}
	if s == nil || s.db == nil {
		return cfg
	}
	readInt := func(key string, minVal, maxVal int, target *int) {
		raw, err := store.GetSetting(s.db, key)
		if err != nil || len(raw) == 0 {
			return
		}
		var v int
		if json.Unmarshal(raw, &v) != nil {
			var str string
			if json.Unmarshal(raw, &str) != nil {
				return
			}
			parsed, perr := strconv.Atoi(strings.TrimSpace(str))
			if perr != nil {
				return
			}
			v = parsed
		}
		if v < minVal {
			v = minVal
		}
		if maxVal > 0 && v > maxVal {
			v = maxVal
		}
		*target = v
	}
	readInt("grep_full_text_threshold", 0, 200000, &cfg.FullTextThreshold)
	readInt("grep_code_full_text_max_lines", 0, 20000, &cfg.CodeFullTextMaxLines)
	readInt("grep_max_matches", 1, 100, &cfg.MaxMatches)
	readInt("grep_context_lines", 0, 20, &cfg.ContextLines)
	readInt("grep_read_max_lines", 10, 1000, &cfg.ReadMaxLines)
	readInt("grep_read_max_chars", 1000, 64000, &cfg.ReadMaxChars)
	return cfg
}

// ExtractMarkdownOutline scans normalized document text for Markdown headings
// (skipping fenced code blocks) so doc_list can expose structural navigation.
func ExtractMarkdownOutline(content string) []store.DocumentHeading {
	lines := splitNormalizedLines(content)
	headings := []store.DocumentHeading{}
	inFence := false
	for idx, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for level < len(trimmed) && level < 6 && trimmed[level] == '#' {
			level++
		}
		if level == 0 || level >= len(trimmed) || (trimmed[level] != ' ' && trimmed[level] != '\t') {
			continue
		}
		title := strings.TrimSpace(strings.TrimRight(trimmed[level:], "# \t"))
		if title == "" {
			continue
		}
		headings = append(headings, store.DocumentHeading{
			Level: level,
			Line:  idx + 1,
			Title: truncateRunes(title, 120),
		})
		if len(headings) >= grepOutlineMaxHeadings {
			break
		}
	}
	return headings
}

func splitNormalizedLines(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	if normalized == "" {
		return nil
	}
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func (s *Service) saveDocumentText(ctx context.Context, d *store.Document, content string) error {
	if s == nil || s.db == nil || d == nil {
		return nil
	}
	lines := splitNormalizedLines(content)
	headings := ExtractMarkdownOutline(content)
	outlineRaw, _ := json.Marshal(headings)
	return store.UpsertDocumentText(ctx, s.db, store.DocumentText{
		DocumentID:     d.ID,
		KBID:           d.KBID,
		ConversationID: d.ConversationID,
		Content:        content,
		LineCount:      len(lines),
		CharCount:      utf8.RuneCountInString(content),
		TokenEstimate:  estimateTokens(content),
		OutlineJSON:    string(outlineRaw),
	})
}

// reconstructDocumentTextFromChunks rebuilds a usable full-text view from
// stored child chunks for legacy documents ingested before document_texts
// existed. Overlap tails from withOverlap are trimmed best-effort.
func reconstructDocumentTextFromChunks(ctx context.Context, db *sql.DB, doc store.Document) (*store.DocumentText, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT content FROM chunks WHERE document_id=? AND chunk_type<>'parent' ORDER BY seq ASC`,
		doc.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parts := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		c = strings.TrimSpace(c)
		if c != "" {
			parts = append(parts, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, store.ErrNotFound
	}
	var merged strings.Builder
	for i, part := range parts {
		if i == 0 {
			merged.WriteString(part)
			continue
		}
		// withOverlap prepends `tail + "\n\n" + child`. If the tail matches the
		// previous chunk suffix, strip that duplicate prefix.
		if idx := strings.Index(part, "\n\n"); idx > 0 {
			prefix := strings.TrimSpace(part[:idx])
			if prefix != "" && strings.HasSuffix(strings.TrimSpace(parts[i-1]), prefix) {
				part = strings.TrimSpace(part[idx+2:])
			}
		}
		if part != "" {
			if merged.Len() > 0 {
				merged.WriteString("\n\n")
			}
			merged.WriteString(part)
		}
	}
	content := merged.String()
	lines := splitNormalizedLines(content)
	headings := ExtractMarkdownOutline(content)
	outlineRaw, _ := json.Marshal(headings)
	dt := &store.DocumentText{
		DocumentID:     doc.ID,
		KBID:           doc.KBID,
		ConversationID: doc.ConversationID,
		Filename:       doc.Filename,
		MimeType:       doc.MimeType,
		SizeBytes:      doc.SizeBytes,
		Content:        content,
		LineCount:      len(lines),
		CharCount:      utf8.RuneCountInString(content),
		TokenEstimate:  estimateTokens(content),
		OutlineJSON:    string(outlineRaw),
	}
	// Best-effort backfill so subsequent Grep calls read directly from document_texts.
	_ = store.UpsertDocumentText(ctx, db, *dt)
	return dt, nil
}

// GrepDocument is one ready document authorized for the current turn.
type GrepDocument struct {
	DocumentID    string                  `json:"document_id"`
	KBID          string                  `json:"kb_id,omitempty"`
	Filename      string                  `json:"filename"`
	Source        string                  `json:"source"` // "kb" | "document"
	URL           string                  `json:"url"`
	LineCount     int                     `json:"line_count"`
	CharCount     int                     `json:"char_count"`
	TokenEstimate int                     `json:"token_estimate"`
	Pinned        bool                    `json:"pinned"`
	CurrentTurn   bool                    `json:"current_turn"`
	Outline       []store.DocumentHeading `json:"outline,omitempty"`
	Content       string                  `json:"-"`
	Lines         []string                `json:"-"`
}

// GrepScope holds the authorized Grep corpus and pinned small-doc snippets for
// a single chat turn.
type GrepScope struct {
	Settings       GrepSettings
	Documents      []GrepDocument
	ByID           map[string]*GrepDocument
	PinnedSnippets []Snippet
	HasUnpinned    bool
}

// LoadGrepScope resolves all authorized ready documents in the KB + conversation
// scope, partitions small conversation uploads into PinnedSnippets when under
// the admin thresholds, and keeps the remaining corpus ready for Grep/Read.
func (s *Service) LoadGrepScope(
	ctx context.Context,
	convID string,
	kbIDs []string,
	allowedDocumentIDs []string,
	currentDocumentIDs []string,
) (*GrepScope, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("rag: service unavailable")
	}
	kbIDs = fixedKnowledgeBaseScope(kbIDs)
	allowedSet := make(map[string]bool, len(allowedDocumentIDs))
	for _, id := range fixedDocumentScope(allowedDocumentIDs) {
		allowedSet[id] = true
	}
	currentSet := make(map[string]bool, len(currentDocumentIDs))
	for _, id := range fixedDocumentScope(currentDocumentIDs) {
		currentSet[id] = true
	}

	readyDocs, err := store.ListReadyDocumentsInScope(ctx, s.db, kbIDs, convID)
	if err != nil {
		return nil, fmt.Errorf("rag: list ready documents for grep: %w", err)
	}
	texts, err := store.ListDocumentTextsInScope(ctx, s.db, kbIDs, convID)
	if err != nil {
		return nil, fmt.Errorf("rag: load document texts for grep: %w", err)
	}
	textByDoc := make(map[string]store.DocumentText, len(texts))
	for _, dt := range texts {
		textByDoc[dt.DocumentID] = dt
	}

	cfg := s.GrepSettings()
	scope := &GrepScope{
		Settings: cfg,
		ByID:     map[string]*GrepDocument{},
	}

	for _, doc := range readyDocs {
		// KB docs come from explicitly attached/project KBs. Conversation uploads
		// are strictly bounded to the active branch's allowedDocumentIDs.
		if doc.KBID == "" && !allowedSet[doc.ID] {
			continue
		}
		dt, ok := textByDoc[doc.ID]
		if !ok {
			rebuilt, rerr := reconstructDocumentTextFromChunks(ctx, s.db, doc)
			if rerr != nil {
				// Conversation spreadsheets intentionally skip text indexing when
				// python_execute handles them directly; skip silently here.
				continue
			}
			dt = *rebuilt
		}
		lines := splitNormalizedLines(dt.Content)
		if dt.LineCount <= 0 {
			dt.LineCount = len(lines)
		}
		if dt.TokenEstimate <= 0 {
			dt.TokenEstimate = estimateTokens(dt.Content)
		}
		var outline []store.DocumentHeading
		_ = json.Unmarshal([]byte(dt.OutlineJSON), &outline)
		gd := GrepDocument{
			DocumentID:    doc.ID,
			KBID:          doc.KBID,
			Filename:      doc.Filename,
			Source:        snippetSource(doc.KBID),
			URL:           snippetDocumentURL(doc.ID, doc.KBID),
			LineCount:     dt.LineCount,
			CharCount:     dt.CharCount,
			TokenEstimate: dt.TokenEstimate,
			CurrentTurn:   currentSet[doc.ID],
			Outline:       outline,
			Content:       dt.Content,
			Lines:         lines,
		}
		scope.Documents = append(scope.Documents, gd)
	}

	// Pin small conversation attachments in full (default <= 500 tokens or
	// <= 200 lines for code/text) up to the cumulative token threshold.
	// Threshold 0 disables full-text pinning so every document uses Grep.
	pinnedTokens := 0
	if cfg.FullTextThreshold > 0 {
		// Prioritize current-turn conversation attachments first so an older
		// upload never crowds out a small file the user just attached.
		pinOrder := make([]int, 0, len(scope.Documents))
		for i := range scope.Documents {
			if scope.Documents[i].KBID == "" && scope.Documents[i].CurrentTurn {
				pinOrder = append(pinOrder, i)
			}
		}
		for i := range scope.Documents {
			if scope.Documents[i].KBID == "" && !scope.Documents[i].CurrentTurn {
				pinOrder = append(pinOrder, i)
			}
		}
		for _, idx := range pinOrder {
			doc := &scope.Documents[idx]
			individuallyPinnable := false
			if isLineGatedText(doc.Filename) {
				if cfg.CodeFullTextMaxLines > 0 {
					individuallyPinnable = doc.LineCount <= cfg.CodeFullTextMaxLines &&
						doc.TokenEstimate <= cfg.CodeFullTextMaxLines*ragCodeTokensPerLine &&
						doc.TokenEstimate <= cfg.FullTextThreshold
				}
			} else {
				individuallyPinnable = doc.TokenEstimate <= cfg.FullTextThreshold
			}
			if !individuallyPinnable {
				continue
			}
			if pinnedTokens+doc.TokenEstimate > cfg.FullTextThreshold {
				continue
			}
			doc.Pinned = true
			pinnedTokens += doc.TokenEstimate
			scope.PinnedSnippets = append(scope.PinnedSnippets, Snippet{
				ID:      "grep-pin:" + doc.DocumentID,
				Index:   len(scope.PinnedSnippets) + 1,
				Title:   doc.Filename,
				URL:     doc.URL,
				Snippet: doc.Content,
				Source:  doc.Source,
			})
		}
	}

	for i := range scope.Documents {
		doc := &scope.Documents[i]
		scope.ByID[doc.DocumentID] = doc
		if !doc.Pinned {
			scope.HasUnpinned = true
		}
	}
	return scope, nil
}

// GrepSearchRequest describes one doc_grep tool query.
type GrepSearchRequest struct {
	Pattern       string   `json:"pattern"`
	IsRegex       bool     `json:"regex"`
	CaseSensitive bool     `json:"case_sensitive"`
	DocumentIDs   []string `json:"document_ids,omitempty"`
	ContextLines  *int     `json:"context_lines,omitempty"`
	MaxMatches    *int     `json:"max_matches,omitempty"`
}

// GrepHunk is a merged contiguous range of matching + context lines.
type GrepHunk struct {
	DocumentID string   `json:"document_id"`
	Filename   string   `json:"filename"`
	Source     string   `json:"source"`
	StartLine  int      `json:"start_line"`
	EndLine    int      `json:"end_line"`
	MatchLines []int    `json:"match_lines"`
	Text       string   `json:"text"`
	URL        string   `json:"url"`
}

// GrepSearchResult captures formatted search output plus citation records.
type GrepSearchResult struct {
	TotalMatches int        `json:"total_matches"`
	Hunks        []GrepHunk `json:"hunks"`
	Truncated    bool       `json:"truncated"`
	Formatted    string     `json:"formatted"`
}

var ErrGrepInvalidPattern = errors.New("invalid grep pattern")

// SearchGrep executes a safe RE2 regex or literal search across authorized docs.
func SearchGrep(scope *GrepScope, req GrepSearchRequest) (GrepSearchResult, []Snippet, error) {
	var out GrepSearchResult
	if scope == nil || len(scope.Documents) == 0 {
		out.Formatted = "No documents are available in the current scope."
		return out, nil, nil
	}
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" {
		return out, nil, fmt.Errorf("%w: pattern is required", ErrGrepInvalidPattern)
	}
	if utf8.RuneCountInString(pattern) > grepMaxPatternRunes {
		return out, nil, fmt.Errorf("%w: pattern exceeds %d characters", ErrGrepInvalidPattern, grepMaxPatternRunes)
	}

	expr := pattern
	if !req.IsRegex {
		expr = regexp.QuoteMeta(pattern)
	}
	if !req.CaseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return out, nil, fmt.Errorf("%w: %v", ErrGrepInvalidPattern, err)
	}

	targetDocs := make([]*GrepDocument, 0, len(scope.Documents))
	if len(req.DocumentIDs) > 0 {
		seen := make(map[string]bool, len(req.DocumentIDs))
		for _, id := range req.DocumentIDs {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			doc, ok := scope.ByID[id]
			if !ok {
				return out, nil, fmt.Errorf("document %q is not accessible in the current scope", id)
			}
			targetDocs = append(targetDocs, doc)
		}
	} else {
		for i := range scope.Documents {
			targetDocs = append(targetDocs, &scope.Documents[i])
		}
	}

	ctxLines := scope.Settings.ContextLines
	if req.ContextLines != nil {
		ctxLines = *req.ContextLines
		if ctxLines < 0 {
			ctxLines = 0
		}
		if ctxLines > scope.Settings.ContextLines && scope.Settings.ContextLines >= 0 {
			// Allow callers up to max 10 context lines, capped reasonably.
			if ctxLines > 10 {
				ctxLines = 10
			}
		}
	}
	maxMatches := scope.Settings.MaxMatches
	if req.MaxMatches != nil && *req.MaxMatches > 0 && *req.MaxMatches < maxMatches {
		maxMatches = *req.MaxMatches
	}
	if maxMatches <= 0 {
		maxMatches = defaultGrepMaxMatches
	}
	maxChars := scope.Settings.ReadMaxChars
	if maxChars <= 0 {
		maxChars = defaultGrepReadMaxChars
	}

	type lineWindow struct {
		start      int // 0-based inclusive
		end        int // 0-based inclusive
		matchLines []int
	}

	snippets := []Snippet{}
	var formatted strings.Builder
	totalChars := 0

	for _, doc := range targetDocs {
		if len(doc.Lines) == 0 {
			continue
		}
		var windows []lineWindow
		for idx, line := range doc.Lines {
			if !re.MatchString(line) {
				continue
			}
			out.TotalMatches++
			winStart := idx - ctxLines
			if winStart < 0 {
				winStart = 0
			}
			winEnd := idx + ctxLines
			if winEnd >= len(doc.Lines) {
				winEnd = len(doc.Lines) - 1
			}
			if len(windows) > 0 && winStart <= windows[len(windows)-1].end+1 {
				last := &windows[len(windows)-1]
				if winEnd > last.end {
					last.end = winEnd
				}
				last.matchLines = append(last.matchLines, idx+1)
			} else {
				windows = append(windows, lineWindow{
					start:      winStart,
					end:        winEnd,
					matchLines: []int{idx + 1},
				})
			}
			if out.TotalMatches >= maxMatches {
				out.Truncated = true
				break
			}
		}

		for _, win := range windows {
			var hunkBody strings.Builder
			matchSet := make(map[int]bool, len(win.matchLines))
			for _, ml := range win.matchLines {
				matchSet[ml] = true
			}
			for li := win.start; li <= win.end; li++ {
				lineNo := li + 1
				marker := " "
				if matchSet[lineNo] {
					marker = ">"
				}
				fmt.Fprintf(&hunkBody, "%s L%d: %s\n", marker, lineNo, doc.Lines[li])
			}
			hunkText := strings.TrimRight(hunkBody.String(), "\n")
			citeIdx := len(snippets) + 1
			header := fmt.Sprintf("[%d] %s (doc_id=%s, L%d-L%d)\n", citeIdx, doc.Filename, doc.DocumentID, win.start+1, win.end+1)
			entryLen := len(header) + len(hunkText) + 2
			if len(snippets) > 0 && totalChars+entryLen > maxChars {
				out.Truncated = true
				break
			}
			if totalChars+entryLen > maxChars {
				hunkText = truncateRunes(hunkText, max(200, maxChars-len(header)-16)) + "\n... [truncated]"
				out.Truncated = true
			}

			out.Hunks = append(out.Hunks, GrepHunk{
				DocumentID: doc.DocumentID,
				Filename:   doc.Filename,
				Source:     doc.Source,
				StartLine:  win.start + 1,
				EndLine:    win.end + 1,
				MatchLines: win.matchLines,
				Text:       hunkText,
				URL:        doc.URL,
			})
			snippets = append(snippets, Snippet{
				ID:      fmt.Sprintf("grep:%s:%d-%d", doc.DocumentID, win.start+1, win.end+1),
				Index:   citeIdx,
				Title:   fmt.Sprintf("%s (L%d-L%d)", doc.Filename, win.start+1, win.end+1),
				URL:     doc.URL,
				Snippet: hunkText,
				Source:  doc.Source,
			})
			formatted.WriteString(header)
			formatted.WriteString(hunkText)
			formatted.WriteString("\n\n")
			totalChars += entryLen
		}
		if out.Truncated {
			break
		}
	}

	if out.TotalMatches == 0 {
		out.Formatted = fmt.Sprintf("No matches found for %q across %d document(s).", pattern, len(targetDocs))
		return out, nil, nil
	}
	if out.Truncated {
		fmt.Fprintf(&formatted, "Note: output truncated at %d match(es) or %d chars. Narrow document_ids or refine pattern, or use doc_read for full context.", len(out.Hunks), maxChars)
	}
	out.Formatted = strings.TrimSpace(formatted.String())
	return out, snippets, nil
}

// GrepReadResult is the output of reading a slice of lines from one document.
type GrepReadResult struct {
	DocumentID string `json:"document_id"`
	Filename   string `json:"filename"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
	HasMore    bool   `json:"has_more"`
	Formatted  string `json:"formatted"`
}

// ReadGrepLines reads a bounded range of 1-based lines from one authorized doc.
func ReadGrepLines(scope *GrepScope, documentID string, startLine, lineCount int) (GrepReadResult, *Snippet, error) {
	var res GrepReadResult
	if scope == nil {
		return res, nil, errors.New("no documents available in current scope")
	}
	documentID = strings.TrimSpace(documentID)
	doc, ok := scope.ByID[documentID]
	if !ok {
		return res, nil, fmt.Errorf("document %q is not accessible in the current scope", documentID)
	}
	total := len(doc.Lines)
	if startLine <= 0 {
		startLine = 1
	}
	maxLines := scope.Settings.ReadMaxLines
	if maxLines <= 0 {
		maxLines = defaultGrepReadMaxLines
	}
	if lineCount <= 0 || lineCount > maxLines {
		lineCount = maxLines
	}
	maxChars := scope.Settings.ReadMaxChars
	if maxChars <= 0 {
		maxChars = defaultGrepReadMaxChars
	}

	res.DocumentID = doc.DocumentID
	res.Filename = doc.Filename
	res.TotalLines = total
	if total == 0 || startLine > total {
		res.StartLine = startLine
		res.EndLine = total
		res.Formatted = fmt.Sprintf("[1] %s (doc_id=%s, total_lines=%d)\n(Requested start_line=%d is beyond end of document.)", doc.Filename, doc.DocumentID, total, startLine)
		return res, nil, nil
	}

	endLine := startLine + lineCount - 1
	if endLine > total {
		endLine = total
	}
	var body strings.Builder
	actualEnd := startLine - 1
	for ln := startLine; ln <= endLine; ln++ {
		lineText := fmt.Sprintf("L%d: %s\n", ln, doc.Lines[ln-1])
		if body.Len() > 0 && body.Len()+len(lineText) > maxChars {
			break
		}
		if body.Len() == 0 && len(lineText) > maxChars {
			lineText = truncateRunes(lineText, maxChars-16) + "... [truncated]\n"
			body.WriteString(lineText)
			actualEnd = ln
			break
		}
		body.WriteString(lineText)
		actualEnd = ln
	}
	if actualEnd < startLine {
		actualEnd = startLine
	}
	res.StartLine = startLine
	res.EndLine = actualEnd
	res.HasMore = actualEnd < total

	excerpt := strings.TrimRight(body.String(), "\n")
	header := fmt.Sprintf("[1] %s (doc_id=%s, L%d-L%d of %d)\n", doc.Filename, doc.DocumentID, res.StartLine, res.EndLine, total)
	footer := ""
	if res.HasMore {
		footer = fmt.Sprintf("\n\nMore lines available: call doc_read with start_line=%d.", res.EndLine+1)
	}
	res.Formatted = header + excerpt + footer
	snippet := &Snippet{
		ID:      fmt.Sprintf("read:%s:%d-%d", doc.DocumentID, res.StartLine, res.EndLine),
		Index:   1,
		Title:   fmt.Sprintf("%s (L%d-L%d)", doc.Filename, res.StartLine, res.EndLine),
		URL:     doc.URL,
		Snippet: excerpt,
		Source:  doc.Source,
	}
	return res, snippet, nil
}

// FormatGrepDocumentList renders the lightweight metadata index for doc_list.
func FormatGrepDocumentList(scope *GrepScope) string {
	if scope == nil || len(scope.Documents) == 0 {
		return "No documents are available in the current scope."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Available documents (%d):\n", len(scope.Documents))
	for _, doc := range scope.Documents {
		status := "grep"
		if doc.Pinned {
			status = "injected_full"
		}
		turnMark := ""
		if doc.CurrentTurn {
			turnMark = ", current_turn=true"
		}
		fmt.Fprintf(&b, "- doc_id=%s | filename=%q | source=%s | lines=%d | est_tokens=%d | mode=%s%s\n",
			doc.DocumentID, doc.Filename, doc.Source, doc.LineCount, doc.TokenEstimate, status, turnMark)
		if len(doc.Outline) > 0 {
			b.WriteString("  outline:")
			shown := min(len(doc.Outline), 12)
			for i := 0; i < shown; i++ {
				h := doc.Outline[i]
				if i > 0 {
					b.WriteString(";")
				}
				fmt.Fprintf(&b, " L%d %s %s", h.Line, strings.Repeat("#", h.Level), h.Title)
			}
			if len(doc.Outline) > shown {
				fmt.Fprintf(&b, " (+%d more headings)", len(doc.Outline)-shown)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// FallbackGrep runs server-side keyword Grep over unpinned documents when
// tool calling is disabled or unavailable for the current turn.
func FallbackGrep(scope *GrepScope, userText string) []Snippet {
	if scope == nil || !scope.HasUnpinned {
		return nil
	}
	terms := tokenize(strings.ToLower(userText))
	if len(terms) == 0 {
		trimmed := strings.TrimSpace(userText)
		if trimmed == "" {
			return nil
		}
		terms = []string{strings.ToLower(trimmed)}
	}
	// Deduplicate terms and prefer more specific tokens first.
	seenTerms := make(map[string]bool, len(terms))
	uniqueTerms := make([]string, 0, len(terms))
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" || seenTerms[t] {
			continue
		}
		seenTerms[t] = true
		uniqueTerms = append(uniqueTerms, t)
	}
	sort.SliceStable(uniqueTerms, func(i, j int) bool {
		return utf8.RuneCountInString(uniqueTerms[i]) > utf8.RuneCountInString(uniqueTerms[j])
	})

	ctxLines := scope.Settings.ContextLines
	if ctxLines < 0 {
		ctxLines = 0
	}
	maxMatches := scope.Settings.MaxMatches
	if maxMatches <= 0 {
		maxMatches = defaultGrepMaxMatches
	}
	maxChars := scope.Settings.ReadMaxChars
	if maxChars <= 0 {
		maxChars = defaultGrepReadMaxChars
	}

	type scoredWindow struct {
		doc       *GrepDocument
		start     int
		end       int
		score     int
		matchLine int
	}
	candidates := []scoredWindow{}
	for i := range scope.Documents {
		doc := &scope.Documents[i]
		if doc.Pinned || len(doc.Lines) == 0 {
			continue
		}
		for idx, rawLine := range doc.Lines {
			low := strings.ToLower(rawLine)
			score := 0
			for _, term := range uniqueTerms {
				if strings.Contains(low, term) {
					score += max(1, utf8.RuneCountInString(term))
				}
			}
			if score == 0 {
				continue
			}
			start := max(0, idx-ctxLines)
			end := min(len(doc.Lines)-1, idx+ctxLines)
			if len(candidates) > 0 && candidates[len(candidates)-1].doc.DocumentID == doc.DocumentID && start <= candidates[len(candidates)-1].end+1 {
				prev := &candidates[len(candidates)-1]
				if end > prev.end {
					prev.end = end
				}
				prev.score += score
			} else {
				candidates = append(candidates, scoredWindow{
					doc:       doc,
					start:     start,
					end:       end,
					score:     score,
					matchLine: idx + 1,
				})
			}
		}
	}

	if len(candidates) == 0 {
		// If no keyword hit at all, fall back to the head of unpinned documents
		// within budget so the answer model still sees what documents exist.
		out := []Snippet{}
		usedChars := 0
		for i := range scope.Documents {
			doc := &scope.Documents[i]
			if doc.Pinned || len(doc.Lines) == 0 {
				continue
			}
			limit := min(len(doc.Lines), max(10, scope.Settings.ReadMaxLines/2))
			var b strings.Builder
			for li := 0; li < limit; li++ {
				fmt.Fprintf(&b, "L%d: %s\n", li+1, doc.Lines[li])
			}
			text := strings.TrimRight(b.String(), "\n")
			if usedChars+len(text) > maxChars {
				break
			}
			usedChars += len(text)
			out = append(out, Snippet{
				ID:      fmt.Sprintf("grep-head:%s:1-%d", doc.DocumentID, limit),
				Index:   len(out) + 1,
				Title:   fmt.Sprintf("%s (L1-L%d)", doc.Filename, limit),
				URL:     doc.URL,
				Snippet: text,
				Source:  doc.Source,
			})
		}
		return out
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].doc.CurrentTurn != candidates[j].doc.CurrentTurn {
			return candidates[i].doc.CurrentTurn
		}
		return candidates[i].matchLine < candidates[j].matchLine
	})

	out := []Snippet{}
	usedChars := 0
	for _, cand := range candidates {
		if len(out) >= maxMatches {
			break
		}
		var b strings.Builder
		for li := cand.start; li <= cand.end; li++ {
			fmt.Fprintf(&b, "L%d: %s\n", li+1, cand.doc.Lines[li])
		}
		text := strings.TrimRight(b.String(), "\n")
		if len(out) > 0 && usedChars+len(text) > maxChars {
			break
		}
		usedChars += len(text)
		out = append(out, Snippet{
			ID:      fmt.Sprintf("grep-fb:%s:%d-%d", cand.doc.DocumentID, cand.start+1, cand.end+1),
			Index:   len(out) + 1,
			Title:   fmt.Sprintf("%s (L%d-L%d)", cand.doc.Filename, cand.start+1, cand.end+1),
			URL:     cand.doc.URL,
			Snippet: text,
			Source:  cand.doc.Source,
		})
	}
	return out
}
