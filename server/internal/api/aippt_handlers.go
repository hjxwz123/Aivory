package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/sse"
	"aivory/server/internal/store"
)

// AI PPT API mode (§ AI PPT rebuild).
//
// The browser talks only to these endpoints; every Docmee call happens here, so
// the Api-Key never leaves the deployment and the vendor's temporary token can be
// reused freely. The user-facing flow is four steps:
//
//	POST   /api/me/ppt/tasks            open a task (type + content, or a file)
//	POST   /api/me/ppt/decks/:id/outline  stream the outline (our own SSE)
//	GET    /api/me/ppt/templates         pick a template
//	POST   /api/me/ppt/decks/:id/pptx    render, charge, mirror into our files
//
// Decks are rows in aippt_decks (see store.AiPPTDeck): we never depend on the
// vendor's own listing, and the rendered .pptx is mirrored into the user's files
// so it outlives Docmee's 2-hour download links.

const (
	aiPPTOutlineRateLimit = 20
	aiPPTMirrorMaxBytes   = 64 << 20
	aiPPTMaxPromptRunes   = 50
)

func newAiPPTFor(d Deps, cfg docmeeConfig) *aiPPTClient {
	return newAiPPTClient(d, cfg, aiPPTContentTimeout)
}

// aiPPTReady resolves the config and answers the caller when the integration is
// unusable, so every API-mode handler reports the same typed states.
func aiPPTReady(d Deps, w http.ResponseWriter) (docmeeConfig, *aiPPTClient, bool) {
	cfg, ok := docmeeReadyConfig(d, w)
	if !ok {
		return cfg, nil, false
	}
	return cfg, newAiPPTFor(d, cfg), true
}

// aiPPTToken returns the cached temporary token used for server-side calls.
func aiPPTToken(ctx context.Context, d Deps, cfg docmeeConfig, userID string) (string, error) {
	return docmeeToken(ctx, d, cfg, userID)
}

// writeAiPPTUpstreamError maps an upstream failure onto our API without leaking
// the vendor's message (which can echo configuration details).
func writeAiPPTUpstreamError(d Deps, w http.ResponseWriter, operation string, err error) {
	var apiErr *aiPPTError
	if errors.As(err, &apiErr) {
		if d.Logger != nil {
			d.Logger.Printf("aippt %s upstream code=%d message=%s", operation, apiErr.Code, apiErr.Message)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "AI PPT service rejected the request", "code": "upstream_error",
			"upstream_code": apiErr.Code,
		})
		return
	}
	if d.Logger != nil {
		d.Logger.Printf("aippt %s failed: %v", operation, err)
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"error": "AI PPT service is temporarily unavailable", "code": "upstream_error",
	})
}

// ----- options / templates / resources --------------------------------------

// meAiPPTOptionsHandler proxies the vendor's enumerations. Cached per language
// because the lists change rarely and the UI reads them on every open.
func meAiPPTOptionsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	cacheKey := "aippt:options:" + lang
	if d.Cache != nil {
		if cached, hit := d.Cache.Get(cacheKey); hit {
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(cached))
			return
		}
	}
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	options, err := client.options(r.Context(), token, lang)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "options", err)
		return
	}
	payload, err := json.Marshal(map[string]any{"options": options})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if d.Cache != nil {
		d.Cache.Set(cacheKey, string(payload), 30*time.Minute)
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write(payload)
}

// meAiPPTTemplatesHandler proxies the template page (system or user templates).
// The upstream response has no pagination envelope, so we add one for the UI.
func meAiPPTTemplatesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	q := r.URL.Query()
	typ := 1
	if q.Get("type") == "4" {
		typ = 4
	}
	page := atoiOr(q.Get("page"), 1)
	size := atoiOr(q.Get("size"), 24)
	// System templates are stable and cheap to cache; a user's own templates
	// change the moment they upload one, so those are always read through.
	cacheKey := fmt.Sprintf("aippt:templates:%d:%d:%d:%s", typ, page, size, q.Get("category"))
	if d.Cache != nil && typ == 1 {
		if cached, hit := d.Cache.Get(cacheKey); hit {
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(cached))
			return
		}
	}
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	list, err := client.templates(r.Context(), token, typ, page, size, q.Get("category"))
	if err != nil {
		writeAiPPTUpstreamError(d, w, "templates", err)
		return
	}
	// A `type=4` page mixes the caller's own uploads with the account-public
	// templates an administrator shared; only the former may be renamed/deleted,
	// so mark them for the UI instead of letting it guess.
	if typ == 4 {
		uid := docmeeUIDForUser(u.ID)
		for i := range list {
			list[i].Owned = list[i].vendorUserID != "" && strings.HasSuffix(list[i].vendorUserID, uid)
		}
	}
	payload, err := json.Marshal(map[string]any{
		"templates": list, "page": page, "size": size,
		"has_more": len(list) >= size,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if d.Cache != nil {
		d.Cache.Set(cacheKey, string(payload), 15*time.Minute)
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write(payload)
}

// meAiPPTResourceHandler proxies vendor-hosted images (template covers, deck
// covers). They are 403 without the temporary token, and that token must not be
// handed to the browser — so the browser asks us, and we pass it through.
func meAiPPTResourceHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	target := strings.TrimSpace(r.URL.Query().Get("url"))
	if target == "" || !aiPPTResourceAllowed(target) {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	raw, contentType, err := client.fetchResource(r.Context(), target, token)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "resource", err)
		return
	}
	if contentType == "" {
		contentType = "image/png"
	}
	w.Header().Set("content-type", contentType)
	w.Header().Set("cache-control", "private, max-age=1800")
	_, _ = w.Write(raw)
}

// ----- tasks and decks ------------------------------------------------------

// meAiPPTCreateTaskHandler opens the deck: it creates the upstream task and
// records our own row, so a draft survives a page reload.
func meAiPPTCreateTaskHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	if !rateLimitUser(d, u.ID, "aippt", 30, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT requests — try again shortly"))
		return
	}

	// multipart for uploads, JSON for everything else.
	var (
		typ     = 1
		content string
		fname   string
		fileBuf *bytes.Buffer
	)
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		maxBytes := int64(cfg.MaxUploadMB) << 20
		if maxBytes <= 0 {
			maxBytes = int64(docmeeDefaultMaxUploadMB) << 20
		}
		if err := r.ParseMultipartForm(maxBytes + 1<<20); err != nil {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		typ = atoiOr(r.FormValue("type"), 1)
		content = strings.TrimSpace(r.FormValue("content"))
		if f, header, err := r.FormFile("file"); err == nil {
			defer f.Close()
			fileBuf = &bytes.Buffer{}
			if _, err := fileBuf.ReadFrom(io.LimitReader(f, maxBytes+1)); err != nil {
				writeError(w, http.StatusBadRequest, errInvalidInput)
				return
			}
			if int64(fileBuf.Len()) > maxBytes {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
					"error": fmt.Sprintf("file exceeds the %d MB limit", cfg.MaxUploadMB), "code": "file_too_large",
				})
				return
			}
			fname = sanitizeAiPPTUploadName(header.Filename)
		}
	} else {
		var body struct {
			Type    int    `json:"type"`
			Content string `json:"content"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		typ, content = body.Type, strings.TrimSpace(body.Content)
	}
	if typ < 1 || typ > 7 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if content == "" && fileBuf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "provide content or a file", "code": "missing_input",
		})
		return
	}
	if len(content) > 8000 {
		content = content[:8000]
	}

	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	var reader *bytes.Buffer
	if fileBuf != nil {
		reader = fileBuf
	}
	taskID, err := client.createTask(r.Context(), token, typ, content, fname, reader)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "createTask", err)
		return
	}

	subject := aiPPTSubjectFor(typ, content, fname)
	optionsJSON, _ := json.Marshal(map[string]any{"type": typ, "content": truncateAiPPT(content, 2000), "file_name": fname})
	deck, err := store.CreateAiPPTDeck(r.Context(), d.DB, store.AiPPTDeck{
		UserID: u.ID, TaskID: taskID, Subject: subject, SourceType: typ,
		Status: store.AiPPTDeckDraft, OptionsJSON: string(optionsJSON),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deck": deck})
}

// meAiPPTDecksHandler lists the caller's decks.
func meAiPPTDecksHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	q := r.URL.Query()
	limit := atoiOr(q.Get("limit"), 50)
	offset := atoiOr(q.Get("offset"), 0)
	decks, err := store.ListAiPPTDecks(r.Context(), d.DB, u.ID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	total, err := store.CountAiPPTDecks(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decks": decks, "total": total, "limit": limit, "offset": offset})
}

// meAiPPTDeckHandler reads or deletes one deck (optionally with its mirrored file).
func meAiPPTDeckHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	deck, err := store.GetAiPPTDeck(r.Context(), d.DB, id, u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"deck": deck})
	case http.MethodDelete:
		if deck.FileID != "" {
			dropAiPPTMirroredFile(d, u.ID, deck.FileID)
		}
		if err := store.DeleteAiPPTDeck(r.Context(), d.DB, id, u.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, errInvalidInput)
	}
}

// ----- outline streaming ----------------------------------------------------

// meAiPPTOutlineHandler streams the outline through our own SSE. It either
// generates content for a fresh task or rewrites an existing outline when a
// `question` is supplied.
type aiPPTOutlineRequest struct {
	Length   string `json:"length"`
	Scene    string `json:"scene"`
	Audience string `json:"audience"`
	Lang     string `json:"lang"`
	Prompt   string `json:"prompt"`
	AISearch bool   `json:"ai_search"`
	IsGenImg bool   `json:"is_gen_img"`
	Question string `json:"question"`
	Markdown string `json:"markdown"`
}

func meAiPPTOutlineHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	deck, err := store.GetAiPPTDeck(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if deck.TaskID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "deck has no upstream task", "code": "no_task"})
		return
	}
	var body aiPPTOutlineRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if !rateLimitUser(d, u.ID, "aippt-outline", aiPPTOutlineRateLimit, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT generations — try again shortly"))
		return
	}
	rewriting := strings.TrimSpace(body.Question) != ""
	if rewriting && strings.TrimSpace(body.Markdown) == "" {
		body.Markdown = deck.Outline
	}
	if rewriting && strings.TrimSpace(body.Markdown) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "nothing to rewrite yet", "code": "missing_outline"})
		return
	}

	writer := sse.New(w)
	if writer == nil {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unavailable"))
		return
	}

	// Edits can be charged (default free); the hold is taken before the upstream
	// call and settled by the deck id afterwards.
	attemptID := store.GenID("ppte")
	editBilling := rewriting && cfg.editBillingEnabled(d)
	if editBilling {
		if _, err := store.ReserveCredits(r.Context(), d.DB, u.ID, cfg.EditCredits,
			docmeeAttemptSourceType, attemptID, docmeeReservationTTL); err != nil {
			_ = writer.Send(map[string]any{"type": "error", "code": "insufficient_credits",
				"message": "insufficient credits"}, "")
			return
		}
	}

	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		if editBilling {
			_ = store.ReleaseCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID)
		}
		_ = writer.Send(map[string]any{"type": "error", "code": "upstream_error",
			"message": "AI PPT service is temporarily unavailable"}, "")
		return
	}

	onDelta := func(delta string) error {
		return writer.Send(map[string]any{"type": "delta", "text": delta}, "")
	}

	var result aiPPTContentResult
	lang := strings.TrimSpace(body.Lang)
	if lang == "" {
		lang = "zh"
	}
	if rewriting {
		result, err = client.rewriteContent(r.Context(), token, deck.TaskID, body.Markdown,
			strings.TrimSpace(body.Question), onDelta)
	} else {
		result, err = client.streamContent(r.Context(), token, deck.TaskID, aiPPTGenerateRequest{
			Length:   strings.TrimSpace(body.Length),
			Scene:    strings.TrimSpace(body.Scene),
			Audience: strings.TrimSpace(body.Audience),
			Lang:     lang,
			Prompt:   truncateAiPPT(strings.TrimSpace(body.Prompt), aiPPTMaxPromptRunes),
			AISearch: body.AISearch,
			IsGenImg: body.IsGenImg,
		}, onDelta)
	}
	if err != nil {
		if editBilling {
			_ = store.ReleaseCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID)
		}
		var apiErr *aiPPTError
		message := "AI PPT generation failed"
		code := "upstream_error"
		if errors.As(err, &apiErr) {
			if d.Logger != nil {
				d.Logger.Printf("aippt outline upstream code=%d message=%s", apiErr.Code, apiErr.Message)
			}
			message = "AI PPT service rejected the request"
		} else if d.Logger != nil {
			d.Logger.Printf("aippt outline failed: %v", err)
		}
		deck.Status = store.AiPPTDeckFailed
		deck.Error = message
		_ = store.UpdateAiPPTDeck(r.Context(), d.DB, *deck)
		_ = writer.Send(map[string]any{"type": "error", "code": code, "message": message}, "")
		return
	}

	markdown := strings.TrimSpace(result.Markdown)
	if markdown == "" {
		deck.Status = store.AiPPTDeckFailed
		deck.Error = "the content service returned an empty outline"
		_ = store.UpdateAiPPTDeck(r.Context(), d.DB, *deck)
		_ = writer.Send(map[string]any{"type": "error", "code": "empty_outline",
			"message": deck.Error}, "")
		return
	}
	deck.Outline = markdown
	deck.Status = store.AiPPTDeckOutlineReady
	deck.Error = ""
	if deck.Subject == "" || rewriting {
		if subject := aiPPTSubjectFromMarkdown(markdown); subject != "" {
			deck.Subject = subject
		}
	}
	if err := store.UpdateAiPPTDeck(r.Context(), d.DB, *deck); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if editBilling {
		if debit, _, err := store.SettleCreditReservationByKey(r.Context(), d.DB, u.ID,
			docmeeAttemptSourceType, attemptID, docmeeAttemptSourceType, "rewrite:"+deck.ID+":"+attemptID,
			cfg.EditCredits); err != nil {
			if d.Logger != nil {
				d.Logger.Printf("aippt edit charge failed (user=%s deck=%s): %v", u.ID, deck.ID, err)
			}
		} else if err := recordDocmeeUsage(r.Context(), d, u.ID,
			store.AiPPTEditUsageMemo(store.AiPPTUsageEventRewrite, deck.ID, attemptID), debit.Total); err != nil {
			// The charge already moved; only the report row is missing.
			if d.Logger != nil {
				d.Logger.Printf("aippt edit usage record failed (user=%s deck=%s): %v", u.ID, deck.ID, err)
			}
		}
	}

	balance, err := store.GetCreditBalance(r.Context(), d.DB, u.ID)
	available := 0.0
	if err == nil {
		available = balance.Available
	}
	_ = writer.Send(map[string]any{
		"type": "done", "deck": deck, "markdown": markdown, "credits_available": available,
	}, "")
}

// ----- render, mirror, charge ----------------------------------------------

type aiPPTGeneratePptxRequest struct {
	TemplateID string `json:"template_id"`
	Markdown   string `json:"markdown"`
}

// meAiPPTGenerateHandler renders the deck, mirrors the .pptx into the caller's
// files and settles the per-deck credit charge exactly once (the ledger is keyed
// by the upstream ppt id).
func meAiPPTGenerateHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	deck, err := store.GetAiPPTDeck(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var body aiPPTGeneratePptxRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if strings.TrimSpace(body.Markdown) == "" {
		body.Markdown = deck.Outline
	}
	if strings.TrimSpace(body.Markdown) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the outline is empty", "code": "missing_outline"})
		return
	}
	templateID := strings.TrimSpace(body.TemplateID)
	if templateID == "" {
		templateID = cfg.DefaultTemplateID
	}
	if deck.TaskID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "deck has no upstream task", "code": "no_task"})
		return
	}
	if deck.Status == store.AiPPTDeckGenerating {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this deck is already rendering", "code": "in_progress"})
		return
	}
	if !rateLimitUser(d, u.ID, "aippt", 30, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT requests — try again shortly"))
		return
	}

	// Open the credit hold first: a short balance must fail before the vendor
	// renders anything.
	billing := cfg.billingEnabled(d) && deck.Credits <= 0
	attemptID := store.GenID("ppt")
	if billing {
		if _, err := store.ReserveCredits(r.Context(), d.DB, u.ID, cfg.CreditsPerPPT,
			docmeeAttemptSourceType, attemptID, docmeeReservationTTL); err != nil {
			if errors.Is(err, store.ErrInsufficientCredits) {
				writeJSON(w, http.StatusPaymentRequired, map[string]any{
					"error": errDocmeeInsufficient.Error(), "code": "insufficient_credits",
					"credits_per_ppt": cfg.CreditsPerPPT,
				})
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	deck.Status = store.AiPPTDeckGenerating
	deck.Outline = body.Markdown
	deck.TemplateID = templateID
	_ = store.UpdateAiPPTDeck(r.Context(), d.DB, *deck)

	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		if billing {
			_ = store.ReleaseCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID)
		}
		deck.Status = store.AiPPTDeckFailed
		deck.Error = "AI PPT service is temporarily unavailable"
		_ = store.UpdateAiPPTDeck(r.Context(), d.DB, *deck)
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}

	info, err := client.generatePptx(r.Context(), token, deck.TaskID, templateID, body.Markdown)
	if err != nil {
		if billing {
			_ = store.ReleaseCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID)
		}
		deck.Status = store.AiPPTDeckFailed
		deck.Error = "rendering failed"
		if apiErr := new(aiPPTError); errors.As(err, &apiErr) {
			deck.Error = fmt.Sprintf("rendering failed (upstream code %d)", apiErr.Code)
		}
		_ = store.UpdateAiPPTDeck(r.Context(), d.DB, *deck)
		writeAiPPTUpstreamError(d, w, "generatePptx", err)
		return
	}

	// Settle under the upstream ppt id: repeated requests for the same deck can
	// never debit twice (see store.SettleCreditReservationByKey).
	credits := deck.Credits
	if billing {
		debit, _, settleErr := store.SettleCreditReservationByKey(r.Context(), d.DB, u.ID,
			docmeeAttemptSourceType, attemptID, docmeeAttemptSourceType, info.ID, cfg.CreditsPerPPT)
		if settleErr != nil {
			if d.Logger != nil {
				d.Logger.Printf("aippt charge failed (user=%s ppt=%s): %v", u.ID, info.ID, settleErr)
			}
			_ = store.ReleaseCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID)
		} else {
			credits = debit.Total
		}
	}
	// Publish the deck to usage reporting (the admin usage page reads
	// usage_logs/usage_stats, not credit_ledger). One row per generated deck —
	// billed or free — because the row is the record of the CALL; its credits
	// column carries what the ledger actually took (0 when the deployment does not
	// charge), so billing totals stay exact while every generation stays visible.
	// Idempotent per deck id, and it also backfills a row whose original write
	// failed.
	usageRecorded := true
	if err := recordDocmeeDeckUsage(r.Context(), d, u.ID, info.ID, credits); err != nil {
		usageRecorded = false
		if d.Logger != nil {
			d.Logger.Printf("aippt usage record failed (user=%s ppt=%s credits=%v): %v", u.ID, info.ID, credits, err)
		}
	}

	deck.PptID = info.ID
	deck.Subject = firstNonEmpty(info.Subject, deck.Subject)
	deck.CoverURL = info.CoverURL
	deck.TemplateID = firstNonEmpty(info.TemplateID, templateID)
	deck.Credits = credits
	deck.Status = store.AiPPTDeckReady
	deck.Error = ""
	if name := aiPPTTemplateName(r.Context(), d, cfg, u.ID, deck.TemplateID); name != "" {
		deck.TemplateName = name
	}
	// Mirror the rendered file into our own storage; a mirror failure keeps the
	// deck usable upstream (the UI can retry) but does not fail the generation.
	if fileID, mirrorErr := mirrorAiPPTDeckFile(r.Context(), d, cfg, client, u.ID, token, *deck, info); mirrorErr != nil {
		if d.Logger != nil {
			d.Logger.Printf("aippt mirror failed (user=%s ppt=%s): %v", u.ID, info.ID, mirrorErr)
		}
		deck.Error = "the file could not be saved to your files yet"
	} else {
		if deck.FileID != "" && deck.FileID != fileID {
			dropAiPPTMirroredFile(d, u.ID, deck.FileID)
		}
		deck.FileID = fileID
	}
	if err := store.UpdateAiPPTDeck(r.Context(), d.DB, *deck); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	balance, _ := store.GetCreditBalance(r.Context(), d.DB, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"deck": deck, "credits": credits, "credits_available": balance.Available,
		"usage_recorded": usageRecorded,
	})
}

// meAiPPTDeckTemplateHandler re-lays out an existing deck with another template
// and replaces the mirrored file.
//
// The template switch is the vendor's own re-layout (updatePptTemplate), not a
// generatePptx re-render: the latter rebuilds every page from the stored outline
// and would silently discard slide-level edits the user made in Docmee's editor.
func meAiPPTDeckTemplateHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	deck, err := store.GetAiPPTDeck(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if deck.TaskID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "deck has no upstream task", "code": "no_task"})
		return
	}
	if deck.PptID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "this deck has not been rendered yet", "code": "no_pptx",
		})
		return
	}
	var body struct {
		TemplateID string `json:"template_id"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.TemplateID) == "" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	templateID := strings.TrimSpace(body.TemplateID)
	if !rateLimitUser(d, u.ID, "aippt", 30, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT requests — try again shortly"))
		return
	}
	editBilling := cfg.editBillingEnabled(d)
	attemptID := store.GenID("ppte")
	if editBilling {
		if _, err := store.ReserveCredits(r.Context(), d.DB, u.ID, cfg.EditCredits,
			docmeeAttemptSourceType, attemptID, docmeeReservationTTL); err != nil {
			if errors.Is(err, store.ErrInsufficientCredits) {
				writeJSON(w, http.StatusPaymentRequired, map[string]any{
					"error": errDocmeeInsufficient.Error(), "code": "insufficient_credits",
				})
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		if editBilling {
			_ = store.ReleaseCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID)
		}
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	if err := client.updatePptTemplate(r.Context(), token, deck.PptID, templateID); err != nil {
		if editBilling {
			_ = store.ReleaseCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID)
		}
		writeAiPPTUpstreamError(d, w, "updatePptTemplate", err)
		return
	}
	if editBilling {
		if debit, _, err := store.SettleCreditReservationByKey(r.Context(), d.DB, u.ID,
			docmeeAttemptSourceType, attemptID, docmeeAttemptSourceType,
			"template:"+deck.PptID+":"+attemptID, cfg.EditCredits); err != nil {
			if d.Logger != nil {
				d.Logger.Printf("aippt template charge failed (user=%s deck=%s): %v", u.ID, deck.ID, err)
			}
		} else if err := recordDocmeeUsage(r.Context(), d, u.ID,
			store.AiPPTEditUsageMemo(store.AiPPTUsageEventTemplate, deck.ID, attemptID), debit.Total); err != nil {
			if d.Logger != nil {
				d.Logger.Printf("aippt template usage record failed (user=%s deck=%s): %v", u.ID, deck.ID, err)
			}
		}
	}
	deck.TemplateID = templateID
	deck.TemplateName = firstNonEmpty(
		aiPPTTemplateName(r.Context(), d, cfg, u.ID, templateID), deck.TemplateName)

	// The re-layout is done but the stored .pptx was rendered with the previous
	// template, so refresh it before mirroring: the user's download must match
	// what the preview shows.
	download, err := client.downloadPptx(r.Context(), token, deck.PptID, true)
	if err != nil {
		deck.Error = "the file could not be re-rendered with this template"
		_ = store.UpdateAiPPTDeck(r.Context(), d.DB, *deck)
		writeAiPPTUpstreamError(d, w, "downloadPptx", err)
		return
	}
	info := aiPPTPptInfo{
		ID:       deck.PptID,
		Subject:  firstNonEmpty(download.Subject, download.Name, deck.Subject),
		FileURL:  download.FileURL,
		CoverURL: firstNonEmpty(download.CoverURL, deck.CoverURL),
	}
	deck.Status = store.AiPPTDeckReady
	if fileID, mirrorErr := mirrorAiPPTDeckFile(r.Context(), d, cfg, client, u.ID, token, *deck, info); mirrorErr != nil {
		if d.Logger != nil {
			d.Logger.Printf("aippt re-mirror failed (deck=%s): %v", deck.ID, mirrorErr)
		}
		deck.Error = "the file could not be saved to your files yet"
	} else {
		if deck.FileID != "" && deck.FileID != fileID {
			dropAiPPTMirroredFile(d, u.ID, deck.FileID)
		}
		deck.FileID = fileID
		deck.Error = ""
	}
	if info.CoverURL != "" {
		deck.CoverURL = info.CoverURL
	}
	if info.Subject != "" {
		deck.Subject = info.Subject
	}
	if err := store.UpdateAiPPTDeck(r.Context(), d.DB, *deck); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deck": deck})
}

// meAiPPTDeckRenameHandler renames a deck in both places.
func meAiPPTDeckRenameHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	deck, err := store.GetAiPPTDeck(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Subject string `json:"subject"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	subject := strings.TrimSpace(body.Subject)
	if subject == "" || len(subject) > 200 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	deck.Subject = subject
	if deck.PptID != "" {
		if token, err := aiPPTToken(r.Context(), d, cfg, u.ID); err == nil {
			if err := client.renamePptx(r.Context(), token, deck.PptID, subject); err != nil && d.Logger != nil {
				d.Logger.Printf("aippt rename upstream failed (deck=%s): %v", deck.ID, err)
			}
		}
	}
	if err := store.UpdateAiPPTDeck(r.Context(), d.DB, *deck); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deck": deck})
}

// ----- admin: vendor balance ------------------------------------------------

// adminAiPPTVendorHandler exposes the deployment's Docmee balance so an
// administrator can top up before users hit "upstream rejected".
func adminAiPPTVendorHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	cfg, ok := docmeeReadyConfig(d, w)
	if !ok {
		return
	}
	client := newAiPPTFor(d, cfg)
	info, err := client.accountInfo(context.Background())
	if err != nil {
		writeAiPPTUpstreamError(d, w, "apiInfo", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available_count": info.AvailableCount,
		"used_count":      info.UsedCount,
	})
}

// meAiPPTTemplateUploadHandler registers a user template (type=4) from a .pptx.
// Docmee learns and marks up the file server-side, so the new template appears in
// the picker's "Mine" tab as soon as it finishes there.
func meAiPPTTemplateUploadHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	if !rateLimitUser(d, u.ID, "aippt-template", 10, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many template uploads — try again shortly"))
		return
	}
	file, filename, limit, ok := readAiPPTTemplateUpload(d, cfg, w, r)
	if !ok {
		return
	}
	defer file.Close()
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	templateID, err := client.uploadTemplate(r.Context(), token, r.FormValue("template_id"), filename, file, false)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "uploadTemplate", err)
		return
	}
	if d.Logger != nil {
		d.Logger.Printf("aippt template uploaded (user=%s id=%s bytes<=%d)", u.ID, templateID, limit)
	}
	writeJSON(w, http.StatusOK, map[string]any{"template_id": templateID})
}

// ----- custom-template management -------------------------------------------

// aiPPTOwnsTemplate reports whether a custom template belongs to the caller.
//
// The vendor scopes a `type=4` page to the caller's uid, but that page also lists
// the account-public templates an administrator shared (they carry an empty
// owner id). Those belong to the deployment, not to the person looking at them,
// so they are not renameable or deletable from a user's own picker.
func aiPPTOwnsTemplate(
	ctx context.Context, client *aiPPTClient, token, userID, templateID string,
) bool {
	want := docmeeUIDForUser(userID)
	list, err := client.templates(ctx, token, 4, 1, 60, "")
	if err != nil {
		return false
	}
	for _, tmpl := range list {
		if tmpl.ID == templateID {
			return tmpl.vendorUserID != "" && strings.HasSuffix(tmpl.vendorUserID, want)
		}
	}
	return false
}

// writeAiPPTTemplateNotFound answers a template the caller cannot act on. A
// foreign template and a missing one are reported identically so the endpoint
// cannot be used to probe someone else's ids.
func writeAiPPTTemplateNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": "this template does not exist or is not yours", "code": "template_not_found",
	})
}

// meAiPPTTemplateRenameHandler renames one of the caller's own templates.
func meAiPPTTemplateRenameHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	if !rateLimitUser(d, u.ID, "aippt-template", 10, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many template changes — try again shortly"))
		return
	}
	templateID := strings.TrimSpace(pathParam(r, "id"))
	var body struct {
		Name string `json:"name"`
	}
	if templateID == "" || decodeJSON(r, &body) != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len([]rune(name)) > 60 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	if !aiPPTOwnsTemplate(r.Context(), client, token, u.ID, templateID) {
		writeAiPPTTemplateNotFound(w)
		return
	}
	if err := client.updateTemplateName(r.Context(), token, templateID, name); err != nil {
		writeAiPPTUpstreamError(d, w, "updateTemplate", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template_id": templateID, "name": name})
}

// meAiPPTTemplateDeleteHandler removes one of the caller's own templates. Decks
// already rendered with it keep their file: the mirrored .pptx is ours, not the
// template's.
func meAiPPTTemplateDeleteHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	if !rateLimitUser(d, u.ID, "aippt-template", 10, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many template changes — try again shortly"))
		return
	}
	templateID := strings.TrimSpace(pathParam(r, "id"))
	if templateID == "" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	if !aiPPTOwnsTemplate(r.Context(), client, token, u.ID, templateID) {
		writeAiPPTTemplateNotFound(w)
		return
	}
	if err := client.deleteTemplate(r.Context(), token, templateID); err != nil {
		writeAiPPTUpstreamError(d, w, "delTemplateId", err)
		return
	}
	// A deck that still points at the deleted template keeps the id for display;
	// re-rendering with it is what would fail, and the UI asks for a new template.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminAiPPTTemplateUploadHandler uploads an Api-Key-level template, which Docmee
// only permits with the Api-Key rather than a user token.
//
// An Api-Key upload is *account-owned*, not shared: verified against the live
// service, other users' own-template listing does not show it until it is
// published with `updateUserTemplate` (`userId` goes from the account id to
// empty). A form field `public=true` therefore publishes it in the same request,
// which is what an administrator means by "upload a company template".
func adminAiPPTTemplateUploadHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	file, filename, _, ok := readAiPPTTemplateUpload(d, cfg, w, r)
	if !ok {
		return
	}
	defer file.Close()
	templateID, err := client.uploadTemplate(r.Context(), "", r.FormValue("template_id"), filename, file, true)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "uploadTemplate", err)
		return
	}
	shared := false
	if wantsPublic := strings.EqualFold(strings.TrimSpace(r.FormValue("public")), "true"); wantsPublic {
		if err := client.setTemplatePublic(r.Context(), templateID, true); err != nil {
			// The file is already uploaded; report the failure but keep the id so
			// the administrator can retry the publish from the list.
			if d.Logger != nil {
				d.Logger.Printf("aippt template publish failed (id=%s): %v", templateID, err)
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "the template was uploaded but could not be shared with users",
				"code":  "upstream_error", "template_id": templateID,
			})
			return
		}
		shared = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"template_id": templateID, "shared": shared})
}

// adminAiPPTTemplatesHandler lists the templates the deployment owns, so an
// administrator can see what is available to users and what is still private to
// the account.
func adminAiPPTTemplatesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	list, err := client.accountTemplates(r.Context())
	if err != nil {
		writeAiPPTUpstreamError(d, w, "templates", err)
		return
	}
	shared := 0
	for i := range list {
		list[i].Shared = list[i].vendorUserID == ""
		if list[i].Shared {
			shared++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"templates": list, "total": len(list), "shared": shared, "max_upload_mb": cfg.MaxUploadMB,
	})
}

// adminAiPPTTemplatePublicHandler publishes (or withdraws) a deployment template.
func adminAiPPTTemplatePublicHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	_, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	templateID := strings.TrimSpace(pathParam(r, "id"))
	var body struct {
		IsPublic *bool `json:"is_public"`
	}
	if templateID == "" || decodeJSON(r, &body) != nil || body.IsPublic == nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if err := client.setTemplatePublic(r.Context(), templateID, *body.IsPublic); err != nil {
		writeAiPPTUpstreamError(d, w, "updateUserTemplate", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template_id": templateID, "shared": *body.IsPublic})
}

// adminAiPPTTemplateDeleteHandler removes a deployment template.
func adminAiPPTTemplateDeleteHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	_, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	templateID := strings.TrimSpace(pathParam(r, "id"))
	if templateID == "" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if err := client.deleteAccountTemplate(r.Context(), templateID); err != nil {
		writeAiPPTUpstreamError(d, w, "delTemplateId", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// readAiPPTTemplateUpload validates and returns the uploaded template file. Only
// .pptx is accepted (Docmee's own rule), bounded by the configured cap.
func readAiPPTTemplateUpload(
	d Deps, cfg docmeeConfig, w http.ResponseWriter, r *http.Request,
) (multipart.File, string, int64, bool) {
	limit := int64(cfg.MaxUploadMB) << 20
	if limit <= 0 {
		limit = int64(docmeeDefaultMaxUploadMB) << 20
	}
	if err := r.ParseMultipartForm(limit + 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return nil, "", limit, false
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "a .pptx template file is required", "code": "missing_file",
		})
		return nil, "", limit, false
	}
	filename := sanitizeAiPPTUploadName(header.Filename)
	if !strings.EqualFold(filepath.Ext(filename), ".pptx") {
		_ = file.Close()
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "templates must be .pptx files", "code": "pptx_only",
		})
		return nil, "", limit, false
	}
	if header.Size > limit {
		_ = file.Close()
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": fmt.Sprintf("template exceeds the %d MB limit", cfg.MaxUploadMB), "code": "file_too_large",
		})
		return nil, "", limit, false
	}
	return file, filename, limit, true
}

// ----- editor hand-off ------------------------------------------------------

// meAiPPTDeckEditorHandler hands the browser the minimum it needs to open the
// vendor's editor for one finished deck: a short-lived token (never the Api-Key),
// the deck id and where the editor SDK lives.
//
// Creation deliberately stays on our own UI — this exists because slide-level
// editing is the one thing a bespoke front end cannot reasonably rebuild, and the
// user asked for Docmee's editor specifically.
func meAiPPTDeckEditorHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, ok := docmeeReadyConfig(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	deck, err := store.GetAiPPTDeck(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if deck.PptID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "this deck has not been rendered yet", "code": "no_pptx",
		})
		return
	}
	if !rateLimitUser(d, u.ID, "aippt", 30, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT requests — try again shortly"))
		return
	}
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"ppt_id":     deck.PptID,
		"subject":    deck.Subject,
		"sdk_url":    cfg.SDKURL,
		"domain":     cfg.Domain,
		"created_at": time.Now().Unix(),
	})
}

// meAiPPTDeckRefreshFileHandler re-downloads a deck from the vendor and replaces
// the mirrored .pptx. Called after the user edited the deck in Docmee's editor
// (their editor saves upstream, so our copy has to be pulled again).
func meAiPPTDeckRefreshFileHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	cfg, client, ok := aiPPTReady(d, w)
	if !ok {
		return
	}
	u := authUser(r)
	deck, err := store.GetAiPPTDeck(r.Context(), d.DB, pathParam(r, "id"), u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if deck.PptID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "this deck has not been rendered yet", "code": "no_pptx",
		})
		return
	}
	if !rateLimitUser(d, u.ID, "aippt", 30, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT requests — try again shortly"))
		return
	}
	token, err := aiPPTToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "token", err)
		return
	}
	download, err := client.downloadPptx(r.Context(), token, deck.PptID, true)
	if err != nil {
		writeAiPPTUpstreamError(d, w, "downloadPptx", err)
		return
	}
	info := aiPPTPptInfo{
		ID:       deck.PptID,
		Subject:  firstNonEmpty(download.Subject, download.Name, deck.Subject),
		FileURL:  download.FileURL,
		CoverURL: firstNonEmpty(download.CoverURL, deck.CoverURL),
	}
	fileID, err := mirrorAiPPTDeckFile(r.Context(), d, cfg, client, u.ID, token, *deck, info)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Printf("aippt re-sync failed (deck=%s): %v", deck.ID, err)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "the edited file could not be saved to your files", "code": "mirror_failed",
		})
		return
	}
	previous := deck.FileID
	deck.FileID = fileID
	deck.Subject = firstNonEmpty(info.Subject, deck.Subject)
	if info.CoverURL != "" {
		deck.CoverURL = info.CoverURL
	}
	deck.Status = store.AiPPTDeckReady
	deck.Error = ""
	if err := store.UpdateAiPPTDeck(r.Context(), d.DB, *deck); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if previous != "" && previous != fileID {
		dropAiPPTMirroredFile(d, u.ID, previous)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deck": deck})
}

// ----- helpers --------------------------------------------------------------

// mirrorAiPPTDeckFile downloads the freshly rendered .pptx and stores it as a
// normal user file, so it outlives the vendor's 2-hour signed URL and shows up in
// the files page with the usual preview/download/share paths.
func mirrorAiPPTDeckFile(
	ctx context.Context, d Deps, cfg docmeeConfig, client *aiPPTClient,
	userID, token string, deck store.AiPPTDeck, info aiPPTPptInfo,
) (string, error) {
	fileURL := strings.TrimSpace(info.FileURL)
	if fileURL == "" {
		// The render response does not always carry a file URL; ask explicitly.
		download, err := client.downloadPptx(ctx, token, info.ID, true)
		if err != nil {
			return "", err
		}
		fileURL = download.FileURL
	}
	raw, contentType, err := client.fetchResource(ctx, fileURL, token)
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > aiPPTMirrorMaxBytes {
		return "", fmt.Errorf("rendered file is too large (%d bytes)", len(raw))
	}
	if err := checkStorageQuotaCtx(ctx, d, userID, int64(len(raw))); err != nil {
		return "", err
	}
	name := aiPPTSafeFileName(firstNonEmpty(deck.Subject, info.Subject, "AI PPT")) + ".pptx"
	if strings.HasSuffix(strings.ToLower(name), "..pptx") {
		name = "AI PPT.pptx"
	}
	path, err := uploadDestPath(d, userID, "f", name)
	if err != nil {
		return "", err
	}
	size, err := writeUploadCopy(path, bytes.NewReader(raw), aiPPTMirrorMaxBytes)
	if err != nil {
		return "", err
	}
	if contentType == "" {
		contentType = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	}
	created, err := store.CreateFile(ctx, d.DB, store.File{
		UserID: userID, Filename: name, MimeType: contentType, SizeBytes: size,
		Kind: kindOf(contentType, name), StoragePath: path,
	})
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return created.ID, nil
}

func checkStorageQuotaCtx(ctx context.Context, d Deps, userID string, size int64) error {
	return store.CheckStorageQuota(ctx, d.DB, userID, size)
}

// dropAiPPTMirroredFile removes a mirrored file row and its bytes. Best-effort:
// the deck row is updated separately, and a leftover blob is preferable to
// failing the user's delete.
func dropAiPPTMirroredFile(d Deps, userID, fileID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f, err := store.GetFile(ctx, d.DB, fileID, userID)
	if err != nil {
		return
	}
	if f.StoragePath != "" {
		_ = os.Remove(f.StoragePath)
	}
	_ = store.AdminDeleteFile(ctx, d.DB, fileID)
}

// aiPPTSubjectFor derives a display name for a brand-new deck.
func aiPPTSubjectFor(typ int, content, filename string) string {
	switch typ {
	case 2, 4:
		if filename != "" {
			return strings.TrimSuffix(filename, filepath.Ext(filename))
		}
	}
	line := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
	line = strings.TrimPrefix(line, "#")
	line = strings.TrimSpace(line)
	if line == "" {
		return "AI PPT"
	}
	return truncateAiPPT(line, 60)
}

// aiPPTSubjectFromMarkdown takes the first heading of a generated outline.
func aiPPTSubjectFromMarkdown(markdown string) string {
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return truncateAiPPT(strings.TrimSpace(strings.TrimPrefix(line, "# ")), 60)
		}
	}
	return ""
}

// aiPPTSafeFileName keeps a mirrored file name usable on every filesystem.
func aiPPTSafeFileName(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\n', '\r', '\t':
			return '-'
		}
		return r
	}, strings.TrimSpace(raw))
	cleaned = strings.Trim(cleaned, ". -")
	if cleaned == "" {
		cleaned = "AI PPT"
	}
	if len(cleaned) > 80 {
		cleaned = cleaned[:80]
	}
	return cleaned
}

func sanitizeAiPPTUploadName(raw string) string {
	name := filepath.Base(strings.TrimSpace(raw))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "upload"
	}
	if ext := filepath.Ext(name); ext != "" {
		name = aiPPTSafeFileName(strings.TrimSuffix(name, ext)) + ext
	} else {
		name = aiPPTSafeFileName(name)
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// aiPPTTemplateName resolves a template's display name from the vendor's
// listings; an empty result is fine (the UI still has the id). System templates
// are searched first, then the caller's own uploads — a custom template never
// appears in the system listing.
func aiPPTTemplateName(ctx context.Context, d Deps, cfg docmeeConfig, userID, templateID string) string {
	if strings.TrimSpace(templateID) == "" {
		return ""
	}
	token, err := aiPPTToken(ctx, d, cfg, userID)
	if err != nil {
		return ""
	}
	client := newAiPPTFor(d, cfg)
	for _, typ := range []int{1, 4} {
		list, err := client.templates(ctx, token, typ, 1, 60, "")
		if err != nil {
			continue
		}
		for _, tmpl := range list {
			if tmpl.ID == templateID {
				return tmpl.Name
			}
		}
	}
	return ""
}

func truncateAiPPT(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}

// atoiOr parses a query/form integer, falling back to def for junk input.
func atoiOr(raw string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		return n
	}
	return def
}
