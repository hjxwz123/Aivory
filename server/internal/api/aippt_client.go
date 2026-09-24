package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Docmee V2 API client (§ AI PPT API mode).
//
// Everything here runs server-side: the Api-Key never leaves the deployment, and
// the browser only ever sees our own endpoints. Two upstream quirks drive the
// shape of this file and were confirmed against the live service:
//
//   - failures come back as HTTP 200 with a non-zero `code` in the envelope, so
//     every call must inspect `code` rather than the status line;
//   - `generateContent` streaming ends with status=4 carrying only the outline
//     tree, so the Markdown has to be accumulated from the `text` deltas.
const (
	aiPPTContentTimeout = 6 * time.Minute
	aiPPTRequestTimeout = 60 * time.Second
	// Upstream caps: 5 files / ~50MB per task (Docmee's own documentation).
	aiPPTMaxFiles        = 5
	aiPPTUpstreamReadCap = 64 << 20
	aiPPTSSELineCap      = 4 << 20
)

// aiPPTError is a non-zero upstream `code`, kept structured so handlers can log
// the vendor's message without leaking it to the browser.
type aiPPTError struct {
	Operation string
	Code      int
	Message   string
}

func (e *aiPPTError) Error() string {
	return fmt.Sprintf("docmee %s failed (code %d): %s", e.Operation, e.Code, e.Message)
}

// errAiPPTUpstream marks any transport/decoding failure talking to Docmee.
var errAiPPTUpstream = errors.New("AI PPT upstream error")

type aiPPTClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newAiPPTClient(d Deps, cfg docmeeConfig, timeout time.Duration) *aiPPTClient {
	client := d.DocmeeHTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &aiPPTClient{baseURL: strings.TrimRight(cfg.APIBaseURL, "/"), apiKey: cfg.APIKey, http: client}
}

// envelope is the shared Docmee response wrapper: `code` 0 means success, and
// `data` is either an object or a bare array depending on the endpoint.
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *aiPPTClient) do(ctx context.Context, method, path, token string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("token", token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errAiPPTUpstream, err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, aiPPTUpstreamReadCap))
	if readErr != nil {
		return nil, fmt.Errorf("%w: %v", errAiPPTUpstream, readErr)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: http %d: %s", errAiPPTUpstream, resp.StatusCode, docmeeSnippet(string(raw), docmeeMaxUpstreamMessageLen))
	}
	return raw, nil
}

// call performs a JSON request and decodes `data` into out. A non-zero `code`
// becomes an *aiPPTError.
func (c *aiPPTClient) call(ctx context.Context, method, path, token, operation string, body any, out any) error {
	var reader io.Reader
	contentType := ""
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
		contentType = "application/json"
	}
	raw, err := c.do(ctx, method, path, token, reader, contentType)
	if err != nil {
		return err
	}
	return decodeAiPPTEnvelope(operation, raw, out)
}

// decodeAiPPTEnvelope unwraps the shared `{code, message, data}` response shape.
// The vendor answers HTTP 200 even for failures, so `code` is the real status.
func decodeAiPPTEnvelope(operation string, raw []byte, out any) error {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: %s: %s", errAiPPTUpstream, operation, docmeeSnippet(string(raw), docmeeMaxUpstreamMessageLen))
	}
	if env.Code != 0 {
		return &aiPPTError{Operation: operation, Code: env.Code, Message: env.Message}
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("%w: %s: decode data: %v", errAiPPTUpstream, operation, err)
	}
	return nil
}

// ----- read endpoints -------------------------------------------------------

type aiPPTOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// options returns the vendor's own enumerations (language / audience / scene /
// reference …) so the UI never hardcodes a list that can drift.
func (c *aiPPTClient) options(ctx context.Context, token, lang string) (map[string][]aiPPTOption, error) {
	path := "/api/ppt/v2/options"
	if strings.TrimSpace(lang) != "" {
		path += "?lang=" + url.QueryEscape(strings.TrimSpace(lang))
	}
	out := map[string][]aiPPTOption{}
	if err := c.call(ctx, http.MethodGet, path, token, "options", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type aiPPTTemplate struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Category      string   `json:"category"`
	CoverURL      string   `json:"coverUrl"`
	PageCoverURLs []string `json:"pageCoverUrls"`
	Lang          string   `json:"lang"`
	Num           int      `json:"num"`
	// Owned marks a custom template that belongs to the calling user rather than
	// to the account-wide pool an administrator uploaded; only owned templates
	// may be renamed or deleted from the UI.
	Owned bool `json:"owned,omitempty"`
	// Shared marks a template the administrator published to every user of this
	// deployment (the vendor reports it with an empty owner id). Only the admin
	// template list sets it.
	Shared bool `json:"shared,omitempty"`
	// vendorUserID is the vendor's own owner id. It is read (a `type=4` listing
	// mixes the caller's uploads with the account-public ones, which carry an
	// empty id) but never echoed to the browser.
	vendorUserID string
}

// templates lists system (type=1) or user (type=4) templates. The response has no
// pagination envelope — `data` is the page itself.
func (c *aiPPTClient) templates(ctx context.Context, token string, typ, page, size int, category string) ([]aiPPTTemplate, error) {
	if typ != 1 && typ != 4 {
		typ = 1
	}
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 60 {
		size = 24
	}
	filters := map[string]any{"type": typ}
	if strings.TrimSpace(category) != "" {
		filters["category"] = strings.TrimSpace(category)
	}
	var rows []struct {
		aiPPTTemplate
		UserID string `json:"userId"`
	}
	err := c.call(ctx, http.MethodPost, "/api/ppt/templates", token, "templates",
		map[string]any{"page": page, "size": size, "filters": filters}, &rows)
	if err != nil {
		return nil, err
	}
	list := make([]aiPPTTemplate, 0, len(rows))
	for _, row := range rows {
		tmpl := row.aiPPTTemplate
		tmpl.vendorUserID = row.UserID
		list = append(list, tmpl)
	}
	return list, nil
}

type aiPPTAccount struct {
	AvailableCount int `json:"availableCount"`
	UsedCount      int `json:"usedCount"`
}

// callAdmin performs a JSON request authenticated with the Api-Key rather than a
// user token. Only the account-level template routes need this: a deployment
// template lives outside every uid.
func (c *aiPPTClient) callAdmin(ctx context.Context, path, operation string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Api-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errAiPPTUpstream, err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, aiPPTUpstreamReadCap))
	if readErr != nil {
		return fmt.Errorf("%w: %v", errAiPPTUpstream, readErr)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%w: http %d: %s", errAiPPTUpstream, resp.StatusCode,
			docmeeSnippet(string(raw), docmeeMaxUpstreamMessageLen))
	}
	return decodeAiPPTEnvelope(operation, raw, out)
}

// accountInfo reports the deployment's vendor balance (Api-Key authenticated) so
// an administrator can see when the upstream account runs dry.
func (c *aiPPTClient) accountInfo(ctx context.Context) (aiPPTAccount, error) {
	var out aiPPTAccount
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/user/apiInfo", nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Api-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("%w: %v", errAiPPTUpstream, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, fmt.Errorf("%w: %v", errAiPPTUpstream, err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Code != 0 {
		return out, fmt.Errorf("%w: apiInfo: %s", errAiPPTUpstream, docmeeSnippet(string(raw), docmeeMaxUpstreamMessageLen))
	}
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return out, fmt.Errorf("%w: apiInfo: %v", errAiPPTUpstream, err)
	}
	return out, nil
}

// ----- generation -----------------------------------------------------------

// createTask opens an upstream task. `file` (with its filename) is only used for
// the upload-based input types; everything else travels as `content`.
func (c *aiPPTClient) createTask(ctx context.Context, token string, typ int, content, filename string, file io.Reader) (string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("type", fmt.Sprintf("%d", typ)); err != nil {
		return "", err
	}
	if strings.TrimSpace(content) != "" {
		if err := writer.WriteField("content", content); err != nil {
			return "", err
		}
	}
	if file != nil && filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(part, io.LimitReader(file, aiPPTUpstreamReadCap)); err != nil {
			return "", err
		}
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	raw, err := c.do(ctx, http.MethodPost, "/api/ppt/v2/createTask", token, &buf, writer.FormDataContentType())
	if err != nil {
		return "", err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("%w: createTask: %s", errAiPPTUpstream, docmeeSnippet(string(raw), docmeeMaxUpstreamMessageLen))
	}
	if env.Code != 0 {
		return "", &aiPPTError{Operation: "createTask", Code: env.Code, Message: env.Message}
	}
	var data struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || data.ID == "" {
		return "", fmt.Errorf("%w: createTask: missing task id", errAiPPTUpstream)
	}
	return data.ID, nil
}

// aiPPTGenerateRequest mirrors generateContent's documented options.
type aiPPTGenerateRequest struct {
	Length       string `json:"length,omitempty"`
	Scene        string `json:"scene,omitempty"`
	Audience     string `json:"audience,omitempty"`
	Lang         string `json:"lang,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	AISearch     bool   `json:"aiSearch"`
	IsGenImg     bool   `json:"isGenImg"`
	OutlineType  string `json:"outlineType,omitempty"`
	QuestionMode bool   `json:"questionMode"`
	IsNeedAsk    bool   `json:"isNeedAsk"`
}

type aiPPTContentResult struct {
	// Markdown is the accumulated outline/content text (stream deltas joined).
	Markdown string
	// Tree is the final structural outline (level/name nodes), kept for future use.
	Tree json.RawMessage
}

type aiPPTContentData struct {
	OutlineType string          `json:"outlineType"`
	Status      int             `json:"status"`
	Text        string          `json:"text"`
	Result      json.RawMessage `json:"result"`
}

// generateContent runs the non-streaming variant: one JSON response whose `text`
// is the complete Markdown. Simple and atomic, but the caller waits ~30s.
func (c *aiPPTClient) generateContent(ctx context.Context, token, taskID string, opts aiPPTGenerateRequest) (aiPPTContentResult, error) {
	opts.OutlineType = "MD"
	opts.QuestionMode = false
	opts.IsNeedAsk = false
	body := map[string]any{"id": taskID, "stream": false}
	mergeAiPPTOptions(body, opts)
	var data aiPPTContentData
	if err := c.call(ctx, http.MethodPost, "/api/ppt/v2/generateContent", token, "generateContent", body, &data); err != nil {
		return aiPPTContentResult{}, err
	}
	return aiPPTContentResult{Markdown: data.Text, Tree: data.Result}, nil
}

// streamContent relays generateContent's SSE deltas through onDelta (nil-safe)
// and returns the assembled Markdown plus the final tree. The upstream tail
// carries only the tree, so the Markdown IS the concatenation of the deltas.
func (c *aiPPTClient) streamContent(ctx context.Context, token, taskID string, opts aiPPTGenerateRequest, onDelta func(string) error) (aiPPTContentResult, error) {
	opts.OutlineType = "MD"
	opts.QuestionMode = false
	opts.IsNeedAsk = false
	body := map[string]any{"id": taskID, "stream": true}
	mergeAiPPTOptions(body, opts)
	return c.stream(ctx, "/api/ppt/v2/generateContent", token, "generateContent", body, onDelta)
}

// rewriteContent asks the vendor to rework an existing Markdown outline
// according to a user instruction (1 Docmee credit per call).
func (c *aiPPTClient) rewriteContent(ctx context.Context, token, taskID, markdown, question string, onDelta func(string) error) (aiPPTContentResult, error) {
	body := map[string]any{"id": taskID, "stream": true, "markdown": markdown, "question": question}
	return c.stream(ctx, "/api/ppt/v2/updateContent", token, "updateContent", body, onDelta)
}

func mergeAiPPTOptions(body map[string]any, opts aiPPTGenerateRequest) {
	payload, _ := json.Marshal(opts)
	var merged map[string]any
	_ = json.Unmarshal(payload, &merged)
	for k, v := range merged {
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		body[k] = v
	}
}

// stream consumes a Docmee SSE response, joining `text` deltas. Some builds also
// return a final `markdown` field (updateContent), which wins when present.
func (c *aiPPTClient) stream(ctx context.Context, path, token, operation string, body any, onDelta func(string) error) (aiPPTContentResult, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return aiPPTContentResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return aiPPTContentResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("token", token)
	resp, err := c.http.Do(req)
	if err != nil {
		return aiPPTContentResult{}, fmt.Errorf("%w: %v", errAiPPTUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return aiPPTContentResult{}, fmt.Errorf("%w: http %d: %s", errAiPPTUpstream, resp.StatusCode,
			docmeeSnippet(string(raw), docmeeMaxUpstreamMessageLen))
	}

	// A non-SSE error body (or a JSON answer despite stream=true) still parses as
	// an envelope: detect it before treating the body as a stream.
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "event-stream") {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, aiPPTUpstreamReadCap))
		if err != nil {
			return aiPPTContentResult{}, fmt.Errorf("%w: %v", errAiPPTUpstream, err)
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return aiPPTContentResult{}, fmt.Errorf("%w: %s: %s", errAiPPTUpstream, operation,
				docmeeSnippet(string(raw), docmeeMaxUpstreamMessageLen))
		}
		if env.Code != 0 {
			return aiPPTContentResult{}, &aiPPTError{Operation: operation, Code: env.Code, Message: env.Message}
		}
		var data aiPPTContentData
		_ = json.Unmarshal(env.Data, &data)
		if data.Text != "" && onDelta != nil {
			if err := onDelta(data.Text); err != nil {
				return aiPPTContentResult{}, err
			}
		}
		return aiPPTContentResult{Markdown: data.Text, Tree: data.Result}, nil
	}

	var markdown strings.Builder
	var final string
	var tree json.RawMessage
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), aiPPTSSELineCap)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		chunk := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if chunk == "" || chunk == "[DONE]" {
			continue
		}
		var data aiPPTContentData
		if json.Unmarshal([]byte(chunk), &data) != nil {
			continue
		}
		if data.Text != "" {
			markdown.WriteString(data.Text)
			if onDelta != nil {
				if err := onDelta(data.Text); err != nil {
					return aiPPTContentResult{}, err
				}
			}
		}
		if len(data.Result) > 0 && string(data.Result) != "null" {
			tree = data.Result
		}
		if data.Status == 4 {
			final = chunk
		}
	}
	if err := scanner.Err(); err != nil {
		// A cut stream still yields the Markdown collected so far; the caller
		// decides whether a partial outline is worth keeping.
		if markdown.Len() == 0 {
			return aiPPTContentResult{}, fmt.Errorf("%w: %s: stream: %v", errAiPPTUpstream, operation, err)
		}
	}
	if final != "" {
		var tail struct {
			Markdown string `json:"markdown"`
		}
		_ = json.Unmarshal([]byte(final), &tail)
		if strings.TrimSpace(tail.Markdown) != "" {
			return aiPPTContentResult{Markdown: tail.Markdown, Tree: tree}, nil
		}
	}
	return aiPPTContentResult{Markdown: markdown.String(), Tree: tree}, nil
}

type aiPPTPptInfo struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	CoverURL   string `json:"coverUrl"`
	FileURL    string `json:"fileUrl"`
	TemplateID string `json:"templateId"`
	TotalPage  int    `json:"totalPage"`
}

// generatePptx renders the deck (1 Docmee credit per call).
func (c *aiPPTClient) generatePptx(ctx context.Context, token, taskID, templateID, markdown string) (aiPPTPptInfo, error) {
	body := map[string]any{"id": taskID, "markdown": markdown}
	if strings.TrimSpace(templateID) != "" {
		body["templateId"] = templateID
	}
	var data struct {
		PptInfo aiPPTPptInfo `json:"pptInfo"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/ppt/v2/generatePptx", token, "generatePptx", body, &data); err != nil {
		return aiPPTPptInfo{}, err
	}
	if data.PptInfo.ID == "" {
		return aiPPTPptInfo{}, fmt.Errorf("%w: generatePptx: missing ppt id", errAiPPTUpstream)
	}
	return data.PptInfo, nil
}

type aiPPTDownload struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Subject  string `json:"subject"`
	FileURL  string `json:"fileUrl"`
	CoverURL string `json:"coverUrl"`
}

// downloadPptx returns a short-lived (2h) signed URL for the rendered file.
func (c *aiPPTClient) downloadPptx(ctx context.Context, token, pptID string, refresh bool) (aiPPTDownload, error) {
	var out aiPPTDownload
	if err := c.call(ctx, http.MethodPost, "/api/ppt/downloadPptx", token, "downloadPptx",
		map[string]any{"id": pptID, "refresh": refresh}, &out); err != nil {
		return out, err
	}
	if strings.TrimSpace(out.FileURL) == "" {
		return out, fmt.Errorf("%w: downloadPptx: empty fileUrl", errAiPPTUpstream)
	}
	return out, nil
}

// updatePptTemplate re-lays out an already-rendered deck with another template,
// keeping the pages the deck currently has (including slide-level edits made in
// Docmee's editor).
//
// Two details of this endpoint are easy to get wrong and were confirmed against
// the live service: the ppt id travels as `pptId` (sending `id` answers
// `{"code":-1,"message":"参数错误"}`), and `sync` defaults to false, in which case
// the call returns before the re-layout is finished and the downloadable file is
// still the old template. We always ask for the synchronous form.
func (c *aiPPTClient) updatePptTemplate(ctx context.Context, token, pptID, templateID string) error {
	return c.call(ctx, http.MethodPost, "/api/ppt/updatePptTemplate", token, "updatePptTemplate",
		map[string]any{"pptId": pptID, "templateId": templateID, "sync": true}, nil)
}

// uploadTemplate registers a user template (type=4) from a .pptx, or overwrites
// an existing one when templateID is supplied. Docmee only accepts an overwrite
// of an Api-Key-level (public) template with the Api-Key itself, hence useAPIKey.
func (c *aiPPTClient) uploadTemplate(
	ctx context.Context, token, templateID, filename string, file io.Reader, useAPIKey bool,
) (string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("type", "4"); err != nil {
		return "", err
	}
	if strings.TrimSpace(templateID) != "" {
		if err := writer.WriteField("templateId", strings.TrimSpace(templateID)); err != nil {
			return "", err
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, io.LimitReader(file, aiPPTUpstreamReadCap)); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/ppt/uploadTemplate", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if useAPIKey {
		req.Header.Set("Api-Key", c.apiKey)
	} else {
		req.Header.Set("token", token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errAiPPTUpstream, err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, aiPPTUpstreamReadCap))
	if readErr != nil {
		return "", fmt.Errorf("%w: %v", errAiPPTUpstream, readErr)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: http %d: %s", errAiPPTUpstream, resp.StatusCode,
			docmeeSnippet(string(raw), docmeeMaxUpstreamMessageLen))
	}
	var data struct {
		ID string `json:"id"`
	}
	if err := decodeAiPPTEnvelope("uploadTemplate", raw, &data); err != nil {
		return "", err
	}
	if strings.TrimSpace(data.ID) == "" {
		return "", fmt.Errorf("%w: uploadTemplate: missing template id", errAiPPTUpstream)
	}
	return data.ID, nil
}

// updateTemplateName renames a custom template.
//
// Docmee's documentation lists the Api-Key as the credential for this route, but
// the Api-Key cannot see a uid-level template — it answers `{"code":-1,
// "message":"模板不存在"}`. The user's temporary token is the credential that
// works, and it also keeps one deployment user from renaming another's template
// (a *system* template is refused either way, with `1003 无权限访问`).
func (c *aiPPTClient) updateTemplateName(ctx context.Context, token, templateID, name string) error {
	return c.call(ctx, http.MethodPost, "/api/ppt/updateTemplate", token, "updateTemplate",
		map[string]any{"id": templateID, "name": name}, nil)
}

// deleteTemplate removes a custom template the caller owns. Account-public
// templates (shared by the administrator) are refused with `1003 无权限访问`.
func (c *aiPPTClient) deleteTemplate(ctx context.Context, token, templateID string) error {
	return c.call(ctx, http.MethodPost, "/api/ppt/delTemplateId", token, "delTemplateId",
		map[string]any{"id": templateID}, nil)
}

// deleteAccountTemplate removes one of the deployment's own templates. Docmee
// refuses an account-level template to a user token, so this runs on the Api-Key.
func (c *aiPPTClient) deleteAccountTemplate(ctx context.Context, templateID string) error {
	return c.callAdmin(ctx, "/api/ppt/delTemplateId", "delTemplateId",
		map[string]any{"id": templateID}, nil)
}

// setTemplatePublic shares a custom template with every token under the Api-Key
// (Docmee calls this an "account-level public template"). Verified against the
// live service: this is the *only* thing that makes a custom template show up in
// other users' own-template listing — an Api-Key upload alone stays account-owned
// (`userId` = the account) and is invisible to a uid.
func (c *aiPPTClient) setTemplatePublic(ctx context.Context, templateID string, isPublic bool) error {
	return c.callAdmin(ctx, "/api/ppt/updateUserTemplate", "updateUserTemplate",
		map[string]any{"templateId": templateID, "isPublic": isPublic}, nil)
}

// accountTemplates lists the templates the deployment itself owns: the Api-Key
// scope sees only these (a uid's uploads never appear here).
func (c *aiPPTClient) accountTemplates(ctx context.Context) ([]aiPPTTemplate, error) {
	return c.templates(ctx, c.apiKey, 4, 1, 60, "")
}

// renamePptx updates the deck's display name.
func (c *aiPPTClient) renamePptx(ctx context.Context, token, pptID, name string) error {
	return c.call(ctx, http.MethodPost, "/api/ppt/updatePptxAttr", token, "updatePptxAttr",
		map[string]any{"id": pptID, "name": name}, nil)
}

// deletePptx removes the upstream deck. Best-effort: our own record is the
// source of truth for the user's library.
func (c *aiPPTClient) deletePptx(ctx context.Context, token, pptID string) error {
	return c.call(ctx, http.MethodPost, "/api/ppt/delete", token, "delete", map[string]any{"id": pptID}, nil)
}

// ----- resource proxy -------------------------------------------------------

// aiPPTResourceHosts is the allowlist for proxied covers/files. Without it the
// resource endpoint would be an open SSRF-capable proxy for any signed-in user.
var aiPPTResourceHosts = []string{
	"docmee.cn", "chatmee.cn", "xpptx.com", "aliyuncs.com", "myqcloud.com", "aliyun.com",
}

func aiPPTResourceAllowed(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range aiPPTResourceHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

// fetchResource downloads a vendor-hosted asset (template cover, deck cover,
// rendered file). Covers on the vendor's own hosts require the temporary token;
// pre-signed object-storage URLs must not have it appended.
func (c *aiPPTClient) fetchResource(ctx context.Context, rawURL, token string) ([]byte, string, error) {
	target := strings.TrimSpace(rawURL)
	if !aiPPTResourceAllowed(target) {
		return nil, "", fmt.Errorf("%w: resource host not allowed", errAiPPTUpstream)
	}
	if parsed, err := url.Parse(target); err == nil && parsed.RawQuery == "" && token != "" {
		q := parsed.Query()
		q.Set("token", token)
		parsed.RawQuery = q.Encode()
		target = parsed.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	// Cover/CDN hosts reject requests without a browser-ish agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Aivory/1.0)")
	client := c.http
	if d := client.Timeout; d < aiPPTRequestTimeout {
		// Resources are small; keep the caller's transport but bound the wait.
		clone := *client
		clone.Timeout = aiPPTRequestTimeout
		client = &clone
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", errAiPPTUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("%w: resource http %d", errAiPPTUpstream, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, aiPPTUpstreamReadCap))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", errAiPPTUpstream, err)
	}
	return raw, resp.Header.Get("Content-Type"), nil
}
