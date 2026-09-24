package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

// uploadCall is what the template-upload stub observed.
type uploadCall struct {
	Type     string
	File     string
	Filename string
	Token    string
	APIKey   string
}

// aipptStubTransport emulates the Docmee V2 surface we depend on. It routes by
// path so one fixture can serve the whole flow, and records how often each
// endpoint was called (which is how the "charged exactly once" tests are stated).
type aipptStubTransport struct {
	t          *testing.T
	mu         sync.Mutex
	calls      map[string]int
	markdown   string
	pptID      string
	fileBytes  []byte
	templateID string
	failWith   *aiPPTError
	// failPath fails only the named upstream path (the others answer normally).
	failPath string
	// statusOverride lets a test force an HTTP failure.
	statusOverride int
	// lastUpload records the most recent uploadTemplate call.
	lastUpload uploadCall
	// lastTemplateUpdate / lastDownload capture the JSON bodies of the two
	// endpoints whose parameter names are easy to get wrong (`pptId` vs `id`,
	// `refresh`).
	lastTemplateUpdate map[string]any
	lastDownload       map[string]any
	// templateOwner is the vendor `userId` a type=4 listing reports. Empty means
	// "account-public": shared by the administrator, and not the caller's to
	// rename or delete.
	templateOwner string
	// lastTemplateRename / lastTemplateDelete record the management calls.
	lastTemplateRename map[string]any
	lastTemplateDelete map[string]any
	// lastTemplatePublic records updateUserTemplate (publishing a template).
	lastTemplatePublic map[string]any
}

func newAiPPTStub(t *testing.T) *aipptStubTransport {
	return &aipptStubTransport{
		t:          t,
		calls:      map[string]int{},
		markdown:   "# AI 办公趋势\n## 章节一\n### 页面一\n#### 段落\n- 内容",
		pptID:      "ppt_stub_1",
		fileBytes:  []byte("PK\u0003\u0004stub-pptx-bytes"),
		templateID: "tpl_stub_1",
	}
}

func (s *aipptStubTransport) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[path]
}

func (s *aipptStubTransport) upload() uploadCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUpload
}

// templateUpdate / download expose the recorded request bodies.
func (s *aipptStubTransport) templateUpdate() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTemplateUpdate
}

func (s *aipptStubTransport) download() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastDownload
}

// templateRename / templateDelete expose the recorded management bodies.
func (s *aipptStubTransport) templateRename() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTemplateRename
}

func (s *aipptStubTransport) templateDelete() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTemplateDelete
}

func (s *aipptStubTransport) templatePublic() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTemplatePublic
}

// body decodes a JSON request body inside the stub (best effort: the tests that
// need it always send JSON).
func body(r *http.Request) map[string]any {
	var out map[string]any
	_ = json.NewDecoder(r.Body).Decode(&out)
	return out
}

// withPathParam injects a router path parameter, which handlers read through
// pathParam — the handler tests call the handlers directly, so the mux never
// fills the context in.
func withPathParam(r *http.Request, name, value string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), pathCtxKey{}, map[string]string{name: value}))
}

func (s *aipptStubTransport) hit(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[path]++
}

func jsonEnvelope(data any) string {
	raw, _ := json.Marshal(map[string]any{"code": 0, "message": "ok", "data": data})
	return string(raw)
}

func (s *aipptStubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	path := r.URL.Path
	s.hit(path)
	respond := func(status int, body, contentType string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{contentType}},
		}, nil
	}
	if s.statusOverride >= 400 {
		return respond(s.statusOverride, `{"code":500,"message":"boom"}`, "application/json")
	}
	if s.failWith != nil {
		payload, _ := json.Marshal(map[string]any{"code": s.failWith.Code, "message": s.failWith.Message})
		return respond(http.StatusOK, string(payload), "application/json")
	}
	if s.failPath != "" && path == s.failPath {
		return respond(http.StatusOK, `{"code":1003,"message":"无权限访问"}`, "application/json")
	}

	switch {
	case path == "/api/user/createApiToken":
		return respond(200, jsonEnvelope(map[string]any{"token": "tok_stub", "expireTime": 7200}), "application/json")
	case path == "/api/ppt/v2/options":
		return respond(200, jsonEnvelope(map[string]any{
			"lang":     []map[string]string{{"name": "简体中文", "value": "zh"}},
			"audience": []map[string]string{{"name": "客户", "value": "客户"}},
		}), "application/json")
	case path == "/api/ppt/templates":
		// `filters.type` selects the listing: 1 = system, 4 = the caller's own
		// uploads (which the vendor keeps in a separate, uid-scoped table).
		typ := 1
		if filters, ok := body(r)["filters"].(map[string]any); ok {
			if v, ok := filters["type"].(float64); ok {
				typ = int(v)
			}
		}
		if typ == 4 {
			return respond(200, jsonEnvelope([]map[string]any{
				{"id": "tpl_custom_1", "name": "我的自定义模板", "coverUrl": "https://docmee.cn/mine.png",
					"userId": s.templateOwner},
			}), "application/json")
		}
		return respond(200, jsonEnvelope([]map[string]any{
			{"id": s.templateID, "name": "办公简约", "category": "办公报告",
				"coverUrl": "https://chatmee.cn/cover.png"},
		}), "application/json")
	case path == "/api/ppt/uploadTemplate":
		_ = r.ParseMultipartForm(8 << 20)
		s.mu.Lock()
		s.lastUpload = uploadCall{
			Type:   r.FormValue("type"),
			File:   r.FormValue("templateId"),
			Token:  r.Header.Get("token"),
			APIKey: r.Header.Get("Api-Key"),
		}
		if file, header, err := r.FormFile("file"); err == nil {
			_ = file.Close()
			s.lastUpload.Filename = header.Filename
		}
		s.mu.Unlock()
		return respond(200, jsonEnvelope(map[string]any{"id": "tpl_new_1"}), "application/json")
	case path == "/api/ppt/v2/createTask":
		return respond(200, jsonEnvelope(map[string]any{"id": "task_stub_1"}), "application/json")
	case path == "/api/ppt/v2/generateContent":
		// SSE: deltas split across fragments, then the tree-only tail.
		var out bytes.Buffer
		half := len(s.markdown) / 2
		for _, part := range []string{s.markdown[:half], s.markdown[half:]} {
			chunk, _ := json.Marshal(map[string]any{"outlineType": "MD", "status": 3, "text": part})
			out.WriteString("data: " + string(chunk) + "\n\n")
		}
		tail, _ := json.Marshal(map[string]any{
			"outlineType": "MD", "status": 4,
			"result": map[string]any{"children": []any{}},
		})
		out.WriteString("data: " + string(tail) + "\n\n")
		return respond(200, out.String(), "text/event-stream")
	case path == "/api/ppt/v2/updateContent":
		chunk, _ := json.Marshal(map[string]any{"status": 4, "markdown": s.markdown + "\n## 追加章节"})
		return respond(200, "data: "+string(chunk)+"\n\n", "text/event-stream")
	case path == "/api/ppt/v2/generatePptx":
		return respond(200, jsonEnvelope(map[string]any{"pptInfo": map[string]any{
			"id": s.pptID, "subject": "AI 办公趋势", "coverUrl": "https://docmee.cn/cover.png",
			"fileUrl": "https://docmee.cn/rendered.pptx", "templateId": s.templateID, "totalPage": 12,
		}}), "application/json")
	case path == "/api/ppt/downloadPptx":
		s.mu.Lock()
		s.lastDownload = body(r)
		s.mu.Unlock()
		return respond(200, jsonEnvelope(map[string]any{
			"id": s.pptID, "name": "AI 办公趋势", "subject": "AI 办公趋势",
			"fileUrl":  "https://docmee.cn/rendered.pptx",
			"coverUrl": "https://docmee.cn/cover.png",
		}), "application/json")
	case path == "/api/ppt/loadPptxMarkdown":
		return respond(200, jsonEnvelope(map[string]any{"markdownText": s.markdown}), "application/json")
	case path == "/api/ppt/updatePptTemplate":
		s.mu.Lock()
		s.lastTemplateUpdate = body(r)
		s.mu.Unlock()
		return respond(200, jsonEnvelope(map[string]any{}), "application/json")
	case path == "/api/ppt/updateUserTemplate":
		s.mu.Lock()
		s.lastTemplatePublic = body(r)
		s.mu.Unlock()
		return respond(200, jsonEnvelope(map[string]any{}), "application/json")
	case path == "/api/ppt/updateTemplate":
		s.mu.Lock()
		s.lastTemplateRename = body(r)
		s.mu.Unlock()
		return respond(200, jsonEnvelope(map[string]any{}), "application/json")
	case path == "/api/ppt/delTemplateId":
		s.mu.Lock()
		s.lastTemplateDelete = body(r)
		s.mu.Unlock()
		return respond(200, jsonEnvelope(map[string]any{}), "application/json")
	case path == "/api/ppt/updatePptxAttr", path == "/api/ppt/delete":
		return respond(200, jsonEnvelope(map[string]any{}), "application/json")
	case path == "/rendered.pptx", path == "/cover.png":
		return respond(200, string(s.fileBytes), "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	default:
		s.t.Errorf("unexpected upstream path %s", path)
		return respond(404, `{"code":404,"message":"not found"}`, "application/json")
	}
}

type aipptFixture struct {
	deps  Deps
	db    *sql.DB
	user  *store.User
	stub  *aipptStubTransport
	other *store.User
}

func aipptTestDeps(t *testing.T, allowance float64, settings map[string]any) aipptFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "aippt.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db,
		`INSERT INTO user_groups(id,name,is_default,is_public,credit_allowance,credit_period_seconds) VALUES('ug_free','Free',1,1,?,86400)`,
		allowance)
	for _, id := range []string{"u1", "u2"} {
		mustExec(t, db,
			`INSERT INTO users(id,email,password_hash,group_id,credit_cycle_anchor) VALUES(?,?,'hash','ug_free',?)`,
			id, id+"@example.test", time.Now().Unix()-60)
	}
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	base := map[string]any{
		"docmee_api_key":         "sk_test_key",
		"credits_per_usd":        100.0,
		"docmee_credits_per_ppt": 10.0,
	}
	for k, v := range settings {
		base[k] = v
	}
	for key, value := range base {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	store.InvalidateConfig()
	stub := newAiPPTStub(t)
	return aipptFixture{
		deps: Deps{
			DB:               db,
			Cache:            cache.NewMemory(),
			DocmeeHTTPClient: &http.Client{Transport: stub},
			Config:           config.Config{UploadDir: t.TempDir()},
		},
		db:    db,
		user:  &store.User{ID: "u1", GroupID: "ug_free"},
		other: &store.User{ID: "u2", GroupID: "ug_free"},
		stub:  stub,
	}
}

func aipptReq(t *testing.T, f aipptFixture, user *store.User, method, path string, body any) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	// Deck routes read their id through the router's path-param context; the
	// handler tests call handlers directly, so inject it here.
	if parts := strings.Split(strings.Trim(path, "/"), "/"); len(parts) >= 5 && parts[3] == "decks" {
		req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": parts[4]}))
	}
	return httptest.NewRecorder(), req
}

// aipptUploadTemplate builds a multipart template-upload request plus its recorder.
func aipptUploadTemplate(
	t *testing.T, user *store.User, path, filename string, body []byte,
) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	return aipptUploadTemplateFields(t, user, path, filename, body, nil)
}

// aipptUploadTemplateFields additionally sends form fields (e.g. `public=true`).
func aipptUploadTemplateFields(
	t *testing.T, user *store.User, path, filename string, body []byte, fields map[string]string,
) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("form field: %v", err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	_, _ = part.Write(body)
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/"+path, &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	return httptest.NewRecorder(), req
}

// createDeck runs the create-task handler and returns the deck id.
func createDeck(t *testing.T, f aipptFixture, user *store.User) string {
	t.Helper()
	rec, req := aipptReq(t, f, user, http.MethodPost, "/api/me/ppt/tasks", map[string]any{
		"type": 1, "content": "AI 办公趋势",
	})
	meAiPPTCreateTaskHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Deck store.AiPPTDeck `json:"deck"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Deck.ID == "" || body.Deck.TaskID != "task_stub_1" {
		t.Fatalf("deck = %+v, want an id and the upstream task", body.Deck)
	}
	return body.Deck.ID
}

func TestAiPPTOptionsAndTemplatesAreProxied(t *testing.T) {
	f := aipptTestDeps(t, 50, nil)

	rec, req := aipptReq(t, f, f.user, http.MethodGet, "/api/me/ppt/options?lang=zh", nil)
	meAiPPTOptionsHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("options status=%d body=%s", rec.Code, rec.Body.String())
	}
	var options struct {
		Options map[string][]aiPPTOption `json:"options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if len(options.Options["lang"]) != 1 || options.Options["lang"][0].Value != "zh" {
		t.Fatalf("options = %+v, want the upstream language list", options.Options)
	}

	rec, req = aipptReq(t, f, f.user, http.MethodGet, "/api/me/ppt/templates?type=1&size=12", nil)
	meAiPPTTemplatesHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("templates status=%d body=%s", rec.Code, rec.Body.String())
	}
	var tmpl struct {
		Templates []aiPPTTemplate `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tmpl); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	if len(tmpl.Templates) != 1 || tmpl.Templates[0].ID != "tpl_stub_1" {
		t.Fatalf("templates = %+v, want the upstream array", tmpl.Templates)
	}
}

func TestAiPPTResourceProxyRejectsForeignHosts(t *testing.T) {
	f := aipptTestDeps(t, 50, nil)

	rec, req := aipptReq(t, f, f.user, http.MethodGet,
		"/api/me/ppt/resource?url="+urlQuery("https://evil.example.com/x.png"), nil)
	meAiPPTResourceHandler(f.deps, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign host status=%d, want 400", rec.Code)
	}

	rec, req = aipptReq(t, f, f.user, http.MethodGet,
		"/api/me/ppt/resource?url="+urlQuery("https://chatmee.cn/cover.png"), nil)
	meAiPPTResourceHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("vendor cover status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("vendor cover returned no bytes")
	}
}

func urlQuery(raw string) string {
	return strings.ReplaceAll(strings.ReplaceAll(raw, ":", "%3A"), "/", "%2F")
}

func TestAiPPTOutlineStreamsDeltasAndPersistsOutline(t *testing.T) {
	f := aipptTestDeps(t, 50, nil)
	deckID := createDeck(t, f, f.user)

	rec, req := aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/outline",
		map[string]any{"length": "short", "lang": "zh", "prompt": "语气专业"})
	meAiPPTOutlineHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("outline status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"delta"`) || !strings.Contains(body, `"type":"done"`) {
		t.Fatalf("stream body missing our events: %s", body)
	}
	if !strings.Contains(body, "AI 办公趋势") {
		t.Fatalf("stream body lost the outline text: %s", body)
	}

	deck, err := store.GetAiPPTDeck(context.Background(), f.db, deckID, f.user.ID)
	if err != nil {
		t.Fatalf("get deck: %v", err)
	}
	if deck.Status != store.AiPPTDeckOutlineReady {
		t.Fatalf("deck status = %s, want outline_ready", deck.Status)
	}
	if !strings.Contains(deck.Outline, "AI 办公趋势") {
		t.Fatalf("stored outline = %q, want the assembled markdown", deck.Outline)
	}
	// Nothing is charged for an outline (generation carries the price).
	balance, err := store.GetCreditBalance(context.Background(), f.db, f.user.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.Reserved != 0 || balance.Available != 50 {
		t.Fatalf("balance = reserved %v available %v, want untouched 50", balance.Reserved, balance.Available)
	}
}

func TestAiPPTGenerateChargesOnceAndMirrorsTheFile(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	deckID := createDeck(t, f, f.user)

	// Outline first so the render has content to use.
	rec, req := aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/outline", nil)
	meAiPPTOutlineHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("outline status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec, req = aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/pptx",
		map[string]any{"template_id": "tpl_stub_1"})
	meAiPPTGenerateHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("generate status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deck             store.AiPPTDeck `json:"deck"`
		Credits          float64         `json:"credits"`
		CreditsAvailable float64         `json:"credits_available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Credits != 10 || out.CreditsAvailable != 15 {
		t.Fatalf("charge = %v credits, %v available; want 10 / 15", out.Credits, out.CreditsAvailable)
	}
	if out.Deck.Status != store.AiPPTDeckReady || out.Deck.PptID != "ppt_stub_1" {
		t.Fatalf("deck = %+v, want ready with the upstream ppt id", out.Deck)
	}
	if out.Deck.FileID == "" {
		t.Fatalf("deck has no mirrored file: %+v", out.Deck)
	}
	// The mirrored bytes are a real user file, reachable through the normal path.
	file, err := store.GetFile(context.Background(), f.db, out.Deck.FileID, f.user.ID)
	if err != nil {
		t.Fatalf("mirrored file: %v", err)
	}
	if file.Kind != "doc" || file.SizeBytes != int64(len(f.stub.fileBytes)) {
		t.Fatalf("mirrored file = kind %s size %d, want doc / %d bytes", file.Kind, file.SizeBytes, len(f.stub.fileBytes))
	}
	if !strings.HasSuffix(file.Filename, ".pptx") {
		t.Fatalf("mirrored filename = %q, want a .pptx", file.Filename)
	}

	// A second render of the SAME deck must not charge again.
	rec, req = aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/pptx",
		map[string]any{"template_id": "tpl_stub_1"})
	meAiPPTGenerateHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second generate status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if out.CreditsAvailable != 15 {
		t.Fatalf("available after re-render = %v, want 15 (charged once)", out.CreditsAvailable)
	}
	balance, err := store.GetCreditBalance(context.Background(), f.db, f.user.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.TimedRemaining != 15 {
		t.Fatalf("timed remaining = %v, want 15", balance.TimedRemaining)
	}
}

func TestAiPPTGenerateFailsClosedWithoutCredits(t *testing.T) {
	f := aipptTestDeps(t, 5, nil)
	deckID := createDeck(t, f, f.user)

	rec, req := aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/pptx",
		map[string]any{"markdown": "# 主题", "template_id": "tpl_stub_1"})
	meAiPPTGenerateHandler(f.deps, rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status=%d, want 402; body=%s", rec.Code, rec.Body.String())
	}
	if f.stub.count("/api/ppt/v2/generatePptx") != 0 {
		t.Fatal("upstream render was called despite an unaffordable balance")
	}
	balance, err := store.GetCreditBalance(context.Background(), f.db, f.user.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.Reserved != 0 || balance.Available != 5 {
		t.Fatalf("balance = reserved %v available %v, want 0/5", balance.Reserved, balance.Available)
	}
}

func TestAiPPTDecksAreScopedToTheirOwner(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	deckID := createDeck(t, f, f.user)

	rec, req := aipptReq(t, f, f.other, http.MethodGet, "/api/me/ppt/decks/"+deckID, nil)
	meAiPPTDeckHandler(f.deps, rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign deck status=%d, want 404", rec.Code)
	}

	rec, req = aipptReq(t, f, f.other, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/pptx",
		map[string]any{"markdown": "# 主题"})
	meAiPPTGenerateHandler(f.deps, rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign generate status=%d, want 404", rec.Code)
	}
}

func TestAiPPTUpstreamCodeBecomesTypedError(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	deckID := createDeck(t, f, f.user)
	f.stub.failWith = &aiPPTError{Operation: "generatePptx", Code: 1010, Message: "not supported"}

	rec, req := aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/pptx",
		map[string]any{"markdown": "# 主题", "template_id": "tpl_stub_1"})
	meAiPPTGenerateHandler(f.deps, rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_code") {
		t.Fatalf("body = %s, want the upstream code surfaced", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "not supported") {
		t.Fatalf("body leaked the vendor message: %s", rec.Body.String())
	}
	// The failed render must release its hold and be recorded as failed.
	balance, err := store.GetCreditBalance(context.Background(), f.db, f.user.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.Reserved != 0 || balance.Available != 25 {
		t.Fatalf("balance = reserved %v available %v, want the hold released", balance.Reserved, balance.Available)
	}
	deck, err := store.GetAiPPTDeck(context.Background(), f.db, deckID, f.user.ID)
	if err != nil {
		t.Fatalf("deck: %v", err)
	}
	if deck.Status != store.AiPPTDeckFailed {
		t.Fatalf("deck status = %s, want failed", deck.Status)
	}
}

func TestAiPPTUploadTaskForwardsTheFile(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("type", "2")
	part, err := writer.CreateFormFile("file", "quarter.docx")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	_, _ = part.Write([]byte("docx-bytes"))
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/me/ppt/tasks", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, f.user))
	rec := httptest.NewRecorder()
	meAiPPTCreateTaskHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Deck store.AiPPTDeck `json:"deck"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Deck.SourceType != 2 {
		t.Fatalf("source type = %d, want 2 (upload)", body.Deck.SourceType)
	}
	if body.Deck.Subject != "quarter" {
		t.Fatalf("subject = %q, want the uploaded file's base name", body.Deck.Subject)
	}
}

func TestAiPPTStoreDeckLifecycle(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	created, err := store.CreateAiPPTDeck(context.Background(), f.db, store.AiPPTDeck{
		UserID: f.user.ID, TaskID: "t1", Subject: "主题",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Status != store.AiPPTDeckDraft {
		t.Fatalf("created = %+v", created)
	}
	created.Status = store.AiPPTDeckReady
	created.PptID = "p1"
	created.Credits = 10
	if err := store.UpdateAiPPTDeck(context.Background(), f.db, *created); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.GetAiPPTDeck(context.Background(), f.db, created.ID, f.user.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PptID != "p1" || got.Credits != 10 || got.Status != store.AiPPTDeckReady {
		t.Fatalf("round trip = %+v", got)
	}
	if _, err := store.GetAiPPTDeck(context.Background(), f.db, created.ID, f.other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-user get err = %v, want ErrNotFound", err)
	}
	decks, err := store.ListAiPPTDecks(context.Background(), f.db, f.user.ID, 10, 0)
	if err != nil || len(decks) != 1 {
		t.Fatalf("list = %v decks, err %v", len(decks), err)
	}
	if n, err := store.CountAiPPTDecks(context.Background(), f.db, f.user.ID); err != nil || n != 1 {
		t.Fatalf("count = %d, err %v", n, err)
	}
	if err := store.DeleteAiPPTDeck(context.Background(), f.db, created.ID, f.user.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := store.CountAiPPTDecks(context.Background(), f.db, f.user.ID); n != 0 {
		t.Fatalf("count after delete = %d, want 0", n)
	}
}

// The editor hand-off exists so slide-level editing (which we do not rebuild) is
// possible: it must hand out a token + the deck id, and refuse a deck that has no
// rendered file yet.
func TestAiPPTEditorSessionHandsOffTokenForRenderedDecksOnly(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	deckID := createDeck(t, f, f.user)

	rec, req := aipptReq(t, f, f.user, http.MethodGet, "/api/me/ppt/decks/"+deckID+"/editor", nil)
	meAiPPTDeckEditorHandler(f.deps, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unrendered deck status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	aipptRender(t, f, deckID)
	rec, req = aipptReq(t, f, f.user, http.MethodGet, "/api/me/ppt/decks/"+deckID+"/editor", nil)
	meAiPPTDeckEditorHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("editor status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var session struct {
		Token   string `json:"token"`
		PptID   string `json:"ppt_id"`
		SDKURL  string `json:"sdk_url"`
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if session.Token == "" || session.PptID != "ppt_stub_1" {
		t.Fatalf("session = %+v, want a token and the upstream deck id", session)
	}
	// The Api-Key must never appear in a browser-facing payload.
	if strings.Contains(rec.Body.String(), "sk_test_key") {
		t.Fatalf("editor session leaked the Api-Key: %s", rec.Body.String())
	}
	if session.SDKURL == "" {
		t.Fatalf("session is missing the editor SDK URL: %s", rec.Body.String())
	}

	// Another user cannot open (or even confirm) someone else's deck.
	other := httptest.NewRecorder()
	_, otherReq := aipptReq(t, f, f.other, http.MethodGet, "/api/me/ppt/decks/"+deckID+"/editor", nil)
	meAiPPTDeckEditorHandler(f.deps, other, otherReq)
	if other.Code != http.StatusNotFound {
		t.Fatalf("foreign editor status = %d, want 404", other.Code)
	}
}

// Editing happens in the vendor editor, which saves upstream: the sync endpoint
// must pull the file again and retire the previous mirrored copy.
func TestAiPPTRefreshFileReplacesTheMirroredCopy(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	deckID := createDeck(t, f, f.user)
	aipptRender(t, f, deckID)

	deck, err := store.GetAiPPTDeck(context.Background(), f.db, deckID, f.user.ID)
	if err != nil {
		t.Fatalf("deck: %v", err)
	}
	previous := deck.FileID
	if previous == "" {
		t.Fatal("render did not mirror a file")
	}
	// Give the second pull different bytes so "replaced" is observable.
	f.stub.fileBytes = []byte("PK\u0003\u0004edited-pptx-bytes")

	rec, req := aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/refresh-file", nil)
	meAiPPTDeckRefreshFileHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deck store.AiPPTDeck `json:"deck"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Deck.FileID == "" || out.Deck.FileID == previous {
		t.Fatalf("file id = %q, want a fresh mirror (previous %q)", out.Deck.FileID, previous)
	}
	if _, err := store.GetFile(context.Background(), f.db, out.Deck.FileID, f.user.ID); err != nil {
		t.Fatalf("refreshed file: %v", err)
	}
	if _, err := store.GetFile(context.Background(), f.db, previous, f.user.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("previous file still present (err=%v), want it retired", err)
	}
}

// Switching templates goes through the vendor's re-layout endpoint, whose pid is
// spelled `pptId` — sending `id` makes Docmee answer `{"code":-1,"message":"参数错误"}`,
// which reached the user as a bare "AI PPT service rejected the request" even
// though the chosen template (system or freshly uploaded) was perfectly usable.
// The deck must also not be re-rendered from the outline: that would discard any
// slide-level edits made in the vendor editor.
func TestAiPPTDeckTemplateSwitchRelaysOutAndRemirrors(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	deckID := createDeck(t, f, f.user)
	aipptRender(t, f, deckID)

	deck, err := store.GetAiPPTDeck(context.Background(), f.db, deckID, f.user.ID)
	if err != nil {
		t.Fatalf("deck: %v", err)
	}
	previous := deck.FileID
	if previous == "" {
		t.Fatal("render did not mirror a file")
	}
	rendersBefore := f.stub.count("/api/ppt/v2/generatePptx")

	rec, req := aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/template",
		map[string]any{"template_id": "tpl_custom_1"})
	meAiPPTDeckTemplateHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("template switch status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	sent := f.stub.templateUpdate()
	if sent["pptId"] != "ppt_stub_1" {
		t.Fatalf("updatePptTemplate body = %v, want pptId", sent)
	}
	if _, legacy := sent["id"]; legacy {
		t.Fatalf("updatePptTemplate body = %v, want `pptId` rather than `id`", sent)
	}
	if sent["templateId"] != "tpl_custom_1" {
		t.Fatalf("updatePptTemplate templateId = %v, want the requested template", sent["templateId"])
	}
	if sent["sync"] != true {
		t.Fatalf("updatePptTemplate sync = %v, want true (the re-layout must be finished)", sent["sync"])
	}
	if call := f.stub.download(); call["refresh"] != true {
		t.Fatalf("downloadPptx body = %v, want refresh=true so the file is re-rendered", call)
	}
	if got := f.stub.count("/api/ppt/v2/generatePptx"); got != rendersBefore {
		t.Fatalf("generatePptx calls = %d, want %d: a template switch re-lays out, it does not re-render", got, rendersBefore)
	}

	var out struct {
		Deck store.AiPPTDeck `json:"deck"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Deck.TemplateID != "tpl_custom_1" || out.Deck.Status != store.AiPPTDeckReady {
		t.Fatalf("deck = %+v, want ready on the new template", out.Deck)
	}
	// The name comes from the vendor's `type=4` listing: custom templates never
	// appear among the system ones.
	if out.Deck.TemplateName != "我的自定义模板" {
		t.Fatalf("template name = %q, want the custom template's name", out.Deck.TemplateName)
	}
	if out.Deck.FileID == "" || out.Deck.FileID == previous {
		t.Fatalf("file id = %q, want a fresh mirror (previous %q)", out.Deck.FileID, previous)
	}
	if _, err := store.GetFile(context.Background(), f.db, previous, f.user.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("previous file still present (err=%v), want it retired", err)
	}
}

// A rejected template switch must not bill the user and must leave the deck on
// the template it actually has.
func TestAiPPTDeckTemplateSwitchReleasesTheHoldOnFailure(t *testing.T) {
	f := aipptTestDeps(t, 25, map[string]any{"docmee_edit_credits": 5.0})
	deckID := createDeck(t, f, f.user)
	aipptRender(t, f, deckID)
	f.stub.failWith = &aiPPTError{Operation: "updatePptTemplate", Code: -1, Message: "参数错误"}

	rec, req := aipptReq(t, f, f.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/template",
		map[string]any{"template_id": "tpl_custom_1"})
	meAiPPTDeckTemplateHandler(f.deps, rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_error") {
		t.Fatalf("body = %s, want the typed upstream error", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "参数错误") {
		t.Fatalf("body leaked the vendor message: %s", rec.Body.String())
	}
	balance, err := store.GetCreditBalance(context.Background(), f.db, f.user.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.Reserved != 0 || balance.Available != 15 {
		t.Fatalf("balance = reserved %v available %v, want the edit hold released (15 = 25 - 10 render)",
			balance.Reserved, balance.Available)
	}
	deck, err := store.GetAiPPTDeck(context.Background(), f.db, deckID, f.user.ID)
	if err != nil {
		t.Fatalf("deck: %v", err)
	}
	if deck.TemplateID != "tpl_stub_1" {
		t.Fatalf("deck template = %q, want the previous one kept", deck.TemplateID)
	}
}

// A user may rename and delete their own uploads, and the picker is told which
// entries those are: the vendor's `type=4` page also lists the account-public
// templates an administrator shared.
func TestAiPPTTemplateRenameAndDeleteOwnUploads(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	f.stub.templateOwner = "2742_" + docmeeUIDForUser(f.user.ID)

	// The listing marks it as the caller's own.
	rec, req := aipptReq(t, f, f.user, http.MethodGet, "/api/me/ppt/templates?type=4", nil)
	meAiPPTTemplatesHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("templates status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Templates []aiPPTTemplate `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Templates) != 1 || !listed.Templates[0].Owned {
		t.Fatalf("templates = %+v, want one owned custom template", listed.Templates)
	}

	rec, req = aipptReq(t, f, f.user, http.MethodPost,
		"/api/me/ppt/templates/tpl_custom_1/rename", map[string]any{"name": "品牌模板"})
	req = withPathParam(req, "id", "tpl_custom_1")
	meAiPPTTemplateRenameHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if sent := f.stub.templateRename(); sent["id"] != "tpl_custom_1" || sent["name"] != "品牌模板" {
		t.Fatalf("updateTemplate body = %v, want {id, name}", sent)
	}

	rec, req = aipptReq(t, f, f.user, http.MethodDelete, "/api/me/ppt/templates/tpl_custom_1", nil)
	req = withPathParam(req, "id", "tpl_custom_1")
	meAiPPTTemplateDeleteHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if sent := f.stub.templateDelete(); sent["id"] != "tpl_custom_1" {
		t.Fatalf("delTemplateId body = %v, want {id}", sent)
	}
}

// Someone else's template — including a shared, account-public one — must not be
// renameable or deletable, and the refusal must not reach the vendor.
func TestAiPPTTemplateManagementRefusesForeignTemplates(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)

	// Account-public (empty owner) and a template this uid never uploaded.
	for _, templateID := range []string{"tpl_custom_1", "tpl_someone_else"} {
		for _, call := range []struct {
			name   string
			method string
			run    func(Deps, http.ResponseWriter, *http.Request)
			body   any
		}{
			{"rename", http.MethodPost, meAiPPTTemplateRenameHandler, map[string]any{"name": "偷来的名字"}},
			{"delete", http.MethodDelete, meAiPPTTemplateDeleteHandler, nil},
		} {
			path := "/api/me/ppt/templates/" + templateID
			if call.name == "rename" {
				path += "/rename"
			}
			rec, req := aipptReq(t, f, f.user, call.method, path, call.body)
			req = withPathParam(req, "id", templateID)
			call.run(f.deps, rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404; body=%s", call.name, templateID, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "template_not_found") {
				t.Fatalf("%s body = %s, want the template_not_found code", call.name, rec.Body.String())
			}
		}
	}
	if f.stub.count("/api/ppt/updateTemplate") != 0 || f.stub.count("/api/ppt/delTemplateId") != 0 {
		t.Fatal("a foreign template reached the vendor")
	}
}

// The handler tests call handlers directly, so nothing above proves the mux
// actually resolves a route. An unauthenticated request must reach `requireAuth`
// (401) instead of falling through to "not found" (404).
func TestAiPPTRoutesAreWiredThroughTheRouter(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	router := NewRouter(f.deps)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/me/ppt/templates"},
		{http.MethodPost, "/api/me/ppt/templates"},
		{http.MethodPost, "/api/me/ppt/templates/tpl_1/rename"},
		{http.MethodDelete, "/api/me/ppt/templates/tpl_1"},
		{http.MethodPost, "/api/me/ppt/decks/ppt_1/template"},
		{http.MethodGet, "/api/admin/aippt/templates"},
		{http.MethodPost, "/api/admin/aippt/templates"},
		{http.MethodPost, "/api/admin/aippt/templates/tpl_1/public"},
		{http.MethodDelete, "/api/admin/aippt/templates/tpl_1"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 (is the route registered?)",
				route.method, route.path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me/ppt/templates/nope/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unregistered route status = %d, want 404", rec.Code)
	}
}

// Custom templates: a user upload goes through the user token with type=4, while
// an Api-Key-level (public) template may only be overwritten with the Api-Key.
func TestAiPPTTemplateUploadRegistersAUserTemplate(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)

	rec, req := aipptUploadTemplate(t, f.user, "me/ppt/templates", "brand.pptx", []byte("PK\u0003\u0004template"))
	meAiPPTTemplateUploadHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		TemplateID string `json:"template_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.TemplateID != "tpl_new_1" {
		t.Fatalf("template_id = %q, want the upstream id", out.TemplateID)
	}
	call := f.stub.upload()
	if call.Type != "4" || call.Filename != "brand.pptx" {
		t.Fatalf("upstream call = %+v, want type=4 with the uploaded name", call)
	}
	if call.Token == "" || call.APIKey != "" {
		t.Fatalf("upload auth = token %q api-key %q, want the user token only", call.Token, call.APIKey)
	}
}

func TestAiPPTTemplateUploadRejectsNonPptx(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)

	rec, req := aipptUploadTemplate(t, f.user, "me/ppt/templates", "notes.docx", []byte("not-a-deck"))
	meAiPPTTemplateUploadHandler(f.deps, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pptx_only") {
		t.Fatalf("body = %s, want the pptx_only code", rec.Body.String())
	}
	if call := f.stub.upload(); call.Type != "" {
		t.Fatalf("upstream was called for a rejected file: %+v", call)
	}
}

func TestAiPPTAdminTemplateUploadUsesTheApiKey(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)

	rec, req := aipptUploadTemplate(t, f.user, "admin/aippt/templates", "public.pptx", []byte("PK\u0003\u0004public"))
	adminAiPPTTemplateUploadHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin upload status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	call := f.stub.upload()
	if call.APIKey == "" || call.Token != "" {
		t.Fatalf("admin upload auth = api-key %q token %q, want the Api-Key only", call.APIKey, call.Token)
	}
}

// An Api-Key upload is account-owned, and users never see it until it is
// published — so the admin list reports which templates are shared, and the
// publish/withdraw/delete actions run on the Api-Key.
func TestAiPPTAdminTemplatesListPublishAndDelete(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)
	f.stub.templateOwner = "2742" // account-owned: not yet shared

	list := func() (templates []aiPPTTemplate, shared int) {
		t.Helper()
		rec, req := aipptReq(t, f, f.user, http.MethodGet, "/api/admin/aippt/templates", nil)
		adminAiPPTTemplatesHandler(f.deps, rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("admin list status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var out struct {
			Templates []aiPPTTemplate `json:"templates"`
			Shared    int             `json:"shared"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Templates, out.Shared
	}

	templates, shared := list()
	if len(templates) != 1 || templates[0].Shared || shared != 0 {
		t.Fatalf("templates = %+v shared = %d, want one unshared account template", templates, shared)
	}

	rec, req := aipptReq(t, f, f.user, http.MethodPost,
		"/api/admin/aippt/templates/tpl_custom_1/public", map[string]any{"is_public": true})
	req = withPathParam(req, "id", "tpl_custom_1")
	adminAiPPTTemplatePublicHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if sent := f.stub.templatePublic(); sent["templateId"] != "tpl_custom_1" || sent["isPublic"] != true {
		t.Fatalf("updateUserTemplate body = %v, want {templateId, isPublic:true}", sent)
	}

	// The vendor reports a published template with an empty owner id.
	f.stub.templateOwner = ""
	templates, shared = list()
	if len(templates) != 1 || !templates[0].Shared || shared != 1 {
		t.Fatalf("templates = %+v shared = %d, want the template reported as shared", templates, shared)
	}

	rec, req = aipptReq(t, f, f.user, http.MethodDelete, "/api/admin/aippt/templates/tpl_custom_1", nil)
	req = withPathParam(req, "id", "tpl_custom_1")
	adminAiPPTTemplateDeleteHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if sent := f.stub.templateDelete(); sent["id"] != "tpl_custom_1" {
		t.Fatalf("delTemplateId body = %v, want {id}", sent)
	}
}

// "Upload a template for everyone" is one admin action: the file goes up with
// the Api-Key, then the new id is published. A publish failure still reports the
// id, so the template is not lost.
func TestAiPPTAdminTemplateUploadCanPublishInOneStep(t *testing.T) {
	f := aipptTestDeps(t, 25, nil)

	rec, req := aipptUploadTemplateFields(t, f.user, "admin/aippt/templates", "brand.pptx",
		[]byte("PK\u0003\u0004brand"), map[string]string{"public": "true"})
	adminAiPPTTemplateUploadHandler(f.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		TemplateID string `json:"template_id"`
		Shared     bool   `json:"shared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.TemplateID != "tpl_new_1" || !out.Shared {
		t.Fatalf("upload = %+v, want the new template shared", out)
	}
	if sent := f.stub.templatePublic(); sent["templateId"] != "tpl_new_1" || sent["isPublic"] != true {
		t.Fatalf("updateUserTemplate body = %v, want the uploaded id published", sent)
	}

	// The publish can fail on its own; the upload then still reports its id.
	f.stub.failPath = "/api/ppt/updateUserTemplate"
	rec, req = aipptUploadTemplateFields(t, f.user, "admin/aippt/templates", "brand2.pptx",
		[]byte("PK\u0003\u0004brand2"), map[string]string{"public": "true"})
	adminAiPPTTemplateUploadHandler(f.deps, rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("publish failure status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tpl_new_1") {
		t.Fatalf("body = %s, want the uploaded template id kept", rec.Body.String())
	}
}
