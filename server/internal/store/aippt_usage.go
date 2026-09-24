package store

import (
	"context"
	"database/sql"
	"strings"
)

// AI PPT calls in the admin usage log (§ AI PPT / Docmee).
//
// Settling a credit reservation writes credit_ledger only, while the admin
// "Usage & billing → Usage" page reads usage_logs — so every AI PPT event also
// publishes one usage_logs row here. That row records the CALL (subject, vendor
// deck id, template, status) and carries, in its `credits` column, the amount
// the ledger actually took. A free generation therefore shows up as a call with
// 0 credits rather than disappearing, while every billing total stays exact.
//
// usage_logs has no AI-PPT columns, so the deck is identified by the row's
// message_id — the same memo that makes the write idempotent — and resolved in
// one follow-up query after the usage rows have been scanned.
const (
	// AiPPTUsagePurpose is the usage_logs/usage_stats purpose of an AI PPT event.
	AiPPTUsagePurpose = "ppt"

	// Event kinds, written into the memo and reported to the admin page.
	AiPPTUsageEventGenerate = "generate"
	AiPPTUsageEventRewrite  = "rewrite"
	AiPPTUsageEventTemplate = "template"

	aiPPTUsageGeneratePrefix = "ppt:"
	aiPPTUsageEditPrefix     = "ppt-edit:"
)

// AiPPTGenerateUsageMemo keys the usage row of one generated deck. The upstream
// ppt id is the same key the credit settlement re-keys its reservation onto, so
// the ledger entry and the usage row share one identity and a replayed charge
// cannot log the same deck twice.
func AiPPTGenerateUsageMemo(pptID string) string {
	return aiPPTUsageGeneratePrefix + strings.TrimSpace(pptID)
}

// AiPPTEditUsageMemo keys the usage row of one charged edit (AI rewrite or
// template re-layout). Edits are charged per call, so the attempt id — not the
// deck — is what makes the memo unique.
func AiPPTEditUsageMemo(event, deckID, attemptID string) string {
	return aiPPTUsageEditPrefix + event + ":" + deckID + ":" + attemptID
}

// aiPPTUsageMemoRef says which deck one usage memo points at: `byPPT` memos carry
// the vendor deck id of a generation, the others carry our own deck id.
type aiPPTUsageMemoRef struct {
	event  string
	byPPT  bool
	deckID string
	pptID  string
}

// parseAiPPTUsageMemo decodes a memo written by the two constructors above. An
// unrecognized memo is not an AI PPT row as far as this reader is concerned.
func parseAiPPTUsageMemo(memo string) (aiPPTUsageMemoRef, bool) {
	if rest, ok := strings.CutPrefix(memo, aiPPTUsageGeneratePrefix); ok {
		if rest = strings.TrimSpace(rest); rest == "" {
			return aiPPTUsageMemoRef{}, false
		}
		return aiPPTUsageMemoRef{event: AiPPTUsageEventGenerate, byPPT: true, pptID: rest}, true
	}
	rest, ok := strings.CutPrefix(memo, aiPPTUsageEditPrefix)
	if !ok {
		return aiPPTUsageMemoRef{}, false
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return aiPPTUsageMemoRef{}, false
	}
	return aiPPTUsageMemoRef{event: parts[0], deckID: parts[1]}, true
}

// AdminUsageAiPPT is the AI PPT call detail attached to a purpose="ppt" usage
// row. Every field is optional: the row survives its deck, so a deleted deck
// still reports the event and the credits that were charged.
type AdminUsageAiPPT struct {
	// Event is generate | rewrite | template.
	Event string `json:"event"`
	// DeckID is our aippt_decks id (empty when the record is gone).
	DeckID string `json:"deck_id,omitempty"`
	// Subject is the deck title.
	Subject string `json:"subject,omitempty"`
	// PptID is the vendor's deck id.
	PptID string `json:"ppt_id,omitempty"`
	// TemplateName is the template the deck was laid out with.
	TemplateName string `json:"template_name,omitempty"`
	// Status is the deck lifecycle state (draft/ready/failed/…).
	Status string `json:"status,omitempty"`
}

// attachAiPPTUsageDetails resolves the deck behind every AI PPT row of one page
// in a single extra query and fills in the call detail. Rows whose deck is gone
// keep their event plus whatever the memo itself identifies, so a charge never
// disappears from the report because the deck was deleted.
func attachAiPPTUsageDetails(ctx context.Context, db *sql.DB, rows []AdminUsageRecord) {
	refs := make([]aiPPTUsageMemoRef, len(rows))
	var pptIDs, deckIDs []string
	for i := range rows {
		if rows[i].Purpose != AiPPTUsagePurpose {
			continue
		}
		ref, ok := parseAiPPTUsageMemo(rows[i].messageID)
		if !ok {
			continue
		}
		refs[i] = ref
		rows[i].AiPPT = &AdminUsageAiPPT{Event: ref.event}
		if ref.byPPT {
			pptIDs = append(pptIDs, ref.pptID)
		} else {
			deckIDs = append(deckIDs, ref.deckID)
		}
	}
	pptIDs, deckIDs = cleanIDs(pptIDs), cleanIDs(deckIDs)
	if len(pptIDs) == 0 && len(deckIDs) == 0 {
		return
	}

	conds := make([]string, 0, 2)
	args := make([]any, 0, len(pptIDs)+len(deckIDs))
	if len(pptIDs) > 0 {
		conds = append(conds, "ppt_id IN ("+idPlaceholders(len(pptIDs))+")")
		args = append(args, anySlice(pptIDs)...)
	}
	if len(deckIDs) > 0 {
		conds = append(conds, "id IN ("+idPlaceholders(len(deckIDs))+")")
		args = append(args, anySlice(deckIDs)...)
	}
	res, err := db.QueryContext(ctx,
		`SELECT id, ppt_id, subject, template_name, status FROM aippt_decks WHERE `+
			strings.Join(conds, " OR "), args...)
	if err != nil {
		// The usage rows themselves are the reportable fact; a failed lookup only
		// costs the extra deck detail.
		return
	}
	defer res.Close()
	byPPT := map[string]AdminUsageAiPPT{}
	byDeck := map[string]AdminUsageAiPPT{}
	for res.Next() {
		var deck AdminUsageAiPPT
		if err := res.Scan(&deck.DeckID, &deck.PptID, &deck.Subject, &deck.TemplateName, &deck.Status); err != nil {
			return
		}
		if deck.PptID != "" {
			byPPT[deck.PptID] = deck
		}
		byDeck[deck.DeckID] = deck
	}
	for i := range rows {
		detail := rows[i].AiPPT
		if detail == nil {
			continue
		}
		ref := refs[i]
		var src AdminUsageAiPPT
		if ref.byPPT {
			src = byPPT[ref.pptID]
			if src.PptID == "" {
				// The deck row is gone (or never stored): the memo still names the
				// vendor deck the charge belongs to.
				src.PptID = ref.pptID
			}
		} else {
			src = byDeck[ref.deckID]
			if src.DeckID == "" {
				src.DeckID = ref.deckID
			}
		}
		src.Event = detail.Event
		*detail = src
	}
}
