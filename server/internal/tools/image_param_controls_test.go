package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

type imageRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn imageRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func useImageTestHTTPClient(t *testing.T, fn imageRoundTripFunc) {
	t.Helper()
	previous := toolHTTPClient
	toolHTTPClient = &http.Client{Transport: fn}
	t.Cleanup(func() { toolHTTPClient = previous })
}

func imageSuccessResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type recordingImageBiller struct {
	checkedN int
}

func (b *recordingImageBiller) CheckImageCredits(_ context.Context, _ string, _ *store.Model, n int) (bool, bool, string) {
	b.checkedN = n
	return true, false, ""
}

func (b *recordingImageBiller) ChargeImageCredits(context.Context, string, float64) (float64, float64) {
	return 0, 0
}

func TestImageGenerateToolUsesOneClampedCountForQuotaRequestAndUsage(t *testing.T) {
	db := openToolsTestDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,name) VALUES('u_image','image@example.test','hash','Image User')`,
		`INSERT INTO channels(id,name,type,base_url,api_key) VALUES('ch_image','Image Channel','openai','https://images.example.test','server-secret')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,price_per_image) VALUES('m_image','ch_image','image','gpt-image-1','Image Model',0.25)`,
		`INSERT INTO conversations(id,user_id,title,model_id) VALUES('c_image','u_image','Image','m_image')`,
		`INSERT INTO messages(id,conversation_id,role,model_id) VALUES('msg_image','c_image','assistant','m_image')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	controls := json.RawMessage(`[
		{
			"key":"render",
			"type":"select",
			"options":[{"value":"studio"}],
			"map":{"studio":{"n":999,"size":"1536x1024","quality":"high"}}
		}
	]`)
	requestParams := llm.MergeParamControls(nil, controls, map[string]any{
		"render":  "studio",
		"unknown": "attacker",
	})
	clampedN := llm.ClampImageGenerationCount(999)

	var captured map[string]any
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		// A provider can misbehave and return more candidates than requested. The
		// server must retain only the clamped, preflighted batch.
		data := make([]map[string]string, clampedN+2)
		for i := range data {
			data[i] = map[string]string{"b64_json": base64.StdEncoding.EncodeToString([]byte("image"))}
		}
		body, _ := json.Marshal(map[string]any{"data": data})
		return imageSuccessResponse(string(body)), nil
	})

	biller := &recordingImageBiller{}
	tool := &imageGenerateTool{db: db, artifactDir: t.TempDir()}
	output, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"draw","n":999,"size":"1024x1024"}`), &llm.ToolContext{
		UserID:             "u_image",
		ConvID:             "c_image",
		MessageID:          "msg_image",
		ImageModelID:       "m_image",
		ImageBilling:       biller,
		ImageRequestParams: requestParams,
		DB:                 db,
	})
	if err != nil {
		t.Fatalf("image_generate Execute: %v", err)
	}
	if biller.checkedN != clampedN {
		t.Fatalf("quota checked n=%d, want %d", biller.checkedN, clampedN)
	}
	if captured["n"] != float64(clampedN) {
		t.Fatalf("upstream n=%#v, want %d", captured["n"], clampedN)
	}
	if captured["size"] != "1536x1024" || captured["quality"] != "high" {
		t.Fatalf("mapped image params missing upstream: %#v", captured)
	}
	if !strings.Contains(output, "Generated "+strconv.Itoa(clampedN)+" image(s)") {
		t.Fatalf("output = %q", output)
	}
	var loggedCount int
	if err := db.QueryRow(`SELECT images_count FROM usage_logs WHERE user_id='u_image' AND model_id='m_image' AND purpose='image'`).Scan(&loggedCount); err != nil {
		t.Fatalf("read image usage: %v", err)
	}
	if loggedCount != clampedN {
		t.Fatalf("usage images_count=%d, want %d", loggedCount, clampedN)
	}
}

func TestImageGenerateToolFaithfullyEditsCurrentAttachmentWithModelDefaults(t *testing.T) {
	db := openToolsTestDB(t)
	controls := `[{"key":"render","type":"select","default":"faithful","options":[{"value":"faithful"}],"map":{"faithful":{"quality":"high","background":"opaque","input_fidelity":"low"}}}]`
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,name) VALUES('u_edit','edit@example.test','hash','Edit User')`,
		`INSERT INTO channels(id,name,type,base_url,api_key) VALUES('ch_edit','Edit Channel','openai','https://images.example.test','server-secret')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO models(id,channel_id,kind,request_id,label,param_controls) VALUES('m_edit','ch_edit','image','gpt-image-2','GPT Image 2',?)`, controls); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id,user_id,title,model_id) VALUES('c_edit','u_edit','Edit','m_edit')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,role,model_id) VALUES('msg_edit','c_edit','assistant','m_edit')`); err != nil {
		t.Fatal(err)
	}

	inputData := sizedPNG(t, 1600, 900)
	inputPath := filepath.Join(t.TempDir(), "terminal.png")
	if err := os.WriteFile(inputPath, inputData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFile(context.Background(), db, store.File{
		ID: "f_terminal", UserID: "u_edit", ConversationID: "c_edit", Filename: "terminal.png",
		MimeType: "image/png", Kind: "image", SizeBytes: int64(len(inputData)), StoragePath: inputPath,
	}); err != nil {
		t.Fatal(err)
	}

	const exactInstruction = "把所有 07 点改成 09 点，08 点改成 10 点，其他内容一律不变"
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != "https://images.example.test/v1/images/edits" {
			t.Fatalf("request URL = %q", got)
		}
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := req.FormValue("quality"); got != "high" {
			t.Fatalf("quality = %q, want selected image-model default", got)
		}
		if got := req.FormValue("background"); got != "opaque" {
			t.Fatalf("background = %q, want selected image-model default", got)
		}
		if got := req.FormValue("input_fidelity"); got != "" {
			t.Fatalf("GPT Image 2 must omit unsupported input_fidelity, got %q", got)
		}
		if got := req.FormValue("size"); got != "1280x720" {
			t.Fatalf("source-ratio size = %q, want 1280x720", got)
		}
		prompt := req.FormValue("prompt")
		if !strings.Contains(prompt, exactInstruction) || !strings.Contains(prompt, "Preserve every other detail") {
			t.Fatalf("faithful edit prompt = %q", prompt)
		}
		if strings.Contains(prompt, "rewrite the whole terminal") {
			t.Fatalf("chat-model paraphrase replaced exact user instruction: %q", prompt)
		}
		files := req.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("bound current attachment count = %d, want 1", len(files))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		gotImage, err := io.ReadAll(file)
		if err != nil || string(gotImage) != string(inputData) {
			t.Fatalf("bound current attachment changed, err=%v", err)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"ZWRpdGVk"}]}`), nil
	})

	tool := &imageGenerateTool{db: db, artifactDir: t.TempDir()}
	_, _, err := tool.Execute(
		context.Background(),
		[]byte(`{"prompt":"rewrite the whole terminal","n":1}`),
		&llm.ToolContext{
			UserID: "u_edit", ConvID: "c_edit", MessageID: "msg_edit", ImageModelID: "m_edit", DB: db,
			ImageInputIDs: []string{"f_terminal"}, ImageUserPrompt: exactInstruction,
		},
	)
	if err != nil {
		t.Fatalf("image_generate Execute: %v", err)
	}
}

func TestOpenAIImageGenerationMergesAllowedJSONParamsAndProtectsNativeFields(t *testing.T) {
	var captured map[string]any
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != "https://images.example.test/v1/images/generations" {
			t.Fatalf("request URL = %q", got)
		}
		if got := req.Header.Get("authorization"); got != "Bearer server-secret" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"aW1hZ2U="}]}`), nil
	})

	params := map[string]any{
		"quality":             "high",
		"background":          "transparent",
		"vendor":              map[string]any{"mode": "cinematic"},
		"model":               "attacker-model",
		"prompt":              "attacker prompt",
		"n":                   1000,
		"contents":            []any{"attacker content"},
		"input_images":        []any{"attacker image"},
		"response_modalities": []any{"TEXT"},
		"api_key":             "attacker-secret",
		"base_url":            "https://attacker.invalid",
		"url":                 "https://attacker.invalid/collect",
		"headers":             map[string]any{"authorization": "attacker"},
	}
	images, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"gpt-image-1",
		imgInput{Prompt: "server prompt", N: 999, Size: "1536x1024"},
		nil,
		params,
	)
	if err != nil {
		t.Fatalf("openaiGenerateImages: %v", err)
	}
	if len(images) != 1 || string(images[0].data) != "image" {
		t.Fatalf("images = %#v", images)
	}

	if captured["model"] != "gpt-image-1" || captured["prompt"] != "server prompt" {
		t.Fatalf("native identity/content overwritten: %#v", captured)
	}
	if captured["n"] != float64(llm.ClampImageGenerationCount(999)) {
		t.Fatalf("n = %#v, want shared clamp", captured["n"])
	}
	if captured["size"] != "1536x1024" || captured["quality"] != "high" || captured["background"] != "transparent" {
		t.Fatalf("allowed image params missing: %#v", captured)
	}
	vendor, ok := captured["vendor"].(map[string]any)
	if !ok || vendor["mode"] != "cinematic" {
		t.Fatalf("provider-specific object missing: %#v", captured["vendor"])
	}
	for _, forbidden := range []string{"contents", "input_images", "response_modalities", "api_key", "base_url", "url", "headers"} {
		if _, exists := captured[forbidden]; exists {
			t.Fatalf("forbidden field %q reached OpenAI body: %#v", forbidden, captured)
		}
	}
}

func TestOpenAIImageEditMergesAllowedMultipartParamsAndProtectsInputImage(t *testing.T) {
	inputData := []byte("trusted-input-image")
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != "https://images.example.test/v1/images/edits" {
			t.Fatalf("request URL = %q", got)
		}
		if got := req.Header.Get("authorization"); got != "Bearer server-secret" {
			t.Fatalf("authorization = %q", got)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		wantFields := map[string]string{
			"model":           "dall-e-3",
			"prompt":          "server edit prompt",
			"n":               "2",
			"size":            "1024x1792",
			"quality":         "hd",
			"background":      "transparent",
			"response_format": "b64_json",
		}
		for key, want := range wantFields {
			if got := req.FormValue(key); got != want {
				t.Fatalf("multipart %s = %q, want %q", key, got, want)
			}
		}
		for _, forbidden := range []string{"api_key", "base_url", "url", "contents", "input_images", "headers", "nested"} {
			if got := req.FormValue(forbidden); got != "" {
				t.Fatalf("forbidden multipart field %s = %q", forbidden, got)
			}
		}
		files := req.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("image file count = %d, want 1", len(files))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatalf("open multipart image: %v", err)
		}
		defer file.Close()
		gotImage, err := io.ReadAll(file)
		if err != nil || string(gotImage) != string(inputData) {
			t.Fatalf("multipart image = %q, err=%v", gotImage, err)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"ZWRpdGVk"}]}`), nil
	})

	params := map[string]any{
		"quality":      "hd",
		"background":   "transparent",
		"nested":       map[string]any{"not": "representable in multipart"},
		"model":        "attacker-model",
		"prompt":       "attacker prompt",
		"n":            999,
		"image":        "attacker image",
		"input_images": []any{"attacker image"},
		"contents":     []any{"attacker content"},
		"api_key":      "attacker-secret",
		"base_url":     "https://attacker.invalid",
		"url":          "https://attacker.invalid/collect",
		"headers":      map[string]any{"authorization": "attacker"},
	}
	images, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"dall-e-3",
		imgInput{Prompt: "server edit prompt", N: 2, Size: "1024x1792"},
		[]imageBytes{{data: inputData, mime: "image/png"}},
		params,
	)
	if err != nil {
		t.Fatalf("openaiGenerateImages edit: %v", err)
	}
	if len(images) != 1 || string(images[0].data) != "edited" {
		t.Fatalf("images = %#v", images)
	}
}

func TestGeminiImageGenerationMergesAspectParamsAndProtectsNativeFields(t *testing.T) {
	inputData := []byte("trusted-reference")
	var captured map[string]any
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path; got != "https://gemini.example.test/v1beta/models/gemini-image:generateContent" {
			t.Fatalf("request URL = %q", req.URL.String())
		}
		if got := req.URL.Query().Get("key"); got != "server-secret" {
			t.Fatalf("API key query = %q", got)
		}
		if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte("gemini-image"))
		return imageSuccessResponse(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/webp","data":"` + encoded + `"}}]}}]}`), nil
	})

	params := map[string]any{
		"generationConfig": map[string]any{
			"temperature":        0.4,
			"candidateCount":     999,
			"responseModalities": []any{"TEXT"},
			"imageConfig": map[string]any{
				"aspectRatio": "16:9",
				"imageSize":   "2K",
			},
		},
		"contents":            []any{map[string]any{"role": "attacker"}},
		"response_modalities": []any{"TEXT"},
		"model":               "attacker-model",
		"prompt":              "attacker prompt",
		"input_images":        []any{"attacker image"},
		"api_key":             "attacker-secret",
		"base_url":            "https://attacker.invalid",
		"url":                 "https://attacker.invalid/collect",
	}
	images, err := geminiGenerateImages(
		context.Background(),
		"https://gemini.example.test",
		"server-secret",
		"gemini-image",
		imgInput{Prompt: "server prompt", N: 999, Size: "1024x1024"},
		[]imageBytes{{data: inputData, mime: "image/png"}},
		params,
	)
	if err != nil {
		t.Fatalf("geminiGenerateImages: %v", err)
	}
	if len(images) != 1 || string(images[0].data) != "gemini-image" || images[0].mime != "image/webp" {
		t.Fatalf("images = %#v", images)
	}

	config, ok := captured["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig = %#v", captured["generationConfig"])
	}
	modalities, ok := config["responseModalities"].([]any)
	if !ok || len(modalities) != 1 || modalities[0] != "IMAGE" {
		t.Fatalf("native responseModalities overwritten: %#v", config["responseModalities"])
	}
	if config["candidateCount"] != float64(llm.ClampImageGenerationCount(999)) {
		t.Fatalf("candidateCount = %#v, want shared clamp", config["candidateCount"])
	}
	imageConfig, ok := config["imageConfig"].(map[string]any)
	if !ok || imageConfig["aspectRatio"] != "16:9" || imageConfig["imageSize"] != "2K" {
		t.Fatalf("Gemini image controls missing: %#v", config)
	}
	if config["temperature"] != 0.4 {
		t.Fatalf("provider-specific generation option missing: %#v", config)
	}
	contents, ok := captured["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("native contents missing: %#v", captured["contents"])
	}
	content, _ := contents[0].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("native reference/prompt parts = %#v", parts)
	}
	inline, _ := parts[0].(map[string]any)
	inlineData, _ := inline["inlineData"].(map[string]any)
	if inlineData["data"] != base64.StdEncoding.EncodeToString(inputData) {
		t.Fatalf("reference image was overwritten: %#v", inlineData)
	}
	textPart, _ := parts[1].(map[string]any)
	if textPart["text"] != "server prompt" {
		t.Fatalf("prompt was overwritten: %#v", textPart)
	}
	for _, forbidden := range []string{"model", "prompt", "input_images", "response_modalities", "api_key", "base_url", "url"} {
		if _, exists := captured[forbidden]; exists {
			t.Fatalf("forbidden field %q reached Gemini body: %#v", forbidden, captured)
		}
	}
}
