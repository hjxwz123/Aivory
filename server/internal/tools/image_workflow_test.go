package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func sizedJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatalf("encode test JPEG: %v", err)
	}
	return encoded.Bytes()
}

func seedImageWorkflow(t *testing.T, channelType, requestID string) (*imageGenerateTool, string) {
	t.Helper()
	db := openToolsTestDB(t)
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_flow','Image Flow',0)`,
		`INSERT INTO users(id,email,password_hash,name,group_id) VALUES('u_flow','flow@example.test','hash','Flow User','ug_flow')`,
		`INSERT INTO channels(id,name,type,base_url,api_key) VALUES('ch_flow','Image Channel','` + channelType + `','https://images.example.test','server-secret')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m_flow','ch_flow','image','` + requestID + `','Image Model')`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('m_flow','ug_flow',604800,'count',0)`,
		`INSERT INTO conversations(id,user_id,title,model_id) VALUES('c_flow','u_flow','Image flow','m_flow')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	return &imageGenerateTool{db: db, uploadDir: t.TempDir(), artifactDir: t.TempDir()}, "c_flow"
}

func TestOpenAIImageContinuationUsesNearestBranchAndRegenerateIgnoresSibling(t *testing.T) {
	tool, convID := seedImageWorkflow(t, "openai", "gpt-image-2")
	for _, query := range []string{
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('u_root','c_flow',NULL,'user','m_flow')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,status) VALUES('a_old','c_flow','u_root','assistant','m_flow','complete')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('u_follow','c_flow','a_old','user','m_flow')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,author_id,status) VALUES('a_follow','c_flow','u_follow','assistant','m_flow','u_flow','streaming')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,author_id,status) VALUES('a_regen','c_flow','u_root','assistant','m_flow','u_flow','streaming')`,
	} {
		if _, err := tool.db.Exec(query); err != nil {
			t.Fatalf("seed message %q: %v", query, err)
		}
	}
	priorImage := sizedPNG(t, 640, 360)
	priorPath := filepath.Join(tool.artifactDir, "prior.png")
	if err := os.WriteFile(priorPath, priorImage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateArtifact(context.Background(), tool.db, store.Artifact{
		ID: "art_prior", MessageID: "a_old", Filename: "prior.png", StoragePath: priorPath,
		MimeType: "image/png", SizeBytes: int64(len(priorImage)),
	}); err != nil {
		t.Fatal(err)
	}
	pythonImage := sizedPNG(t, 48, 48)
	pythonPath := filepath.Join(tool.artifactDir, "python-output.png")
	if err := os.WriteFile(pythonPath, pythonImage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateArtifact(context.Background(), tool.db, store.Artifact{
		ID: "art_python", MessageID: "a_old", Filename: "python-output.png", StoragePath: pythonPath,
		MimeType: "image/png", SizeBytes: int64(len(pythonImage)), Source: store.ArtifactSourcePythonExecute,
	}); err != nil {
		t.Fatal(err)
	}

	if got := tool.loadNearestBranchImage(context.Background(), &llm.ToolContext{DB: tool.db, ConvID: convID, MessageID: "a_regen"}); got != nil {
		t.Fatal("regenerate assistant selected an image from its sibling response")
	}
	if got := tool.loadNearestBranchImage(context.Background(), &llm.ToolContext{DB: tool.db, ConvID: convID, MessageID: "a_follow"}); got == nil || !bytes.Equal(got.data, priorImage) {
		t.Fatal("follow-up assistant did not select the nearest image on its parent branch")
	}

	responseImage := sizedPNG(t, 32, 32)
	requestCount := 0
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			if req.URL.Path != "/v1/images/edits" {
				t.Fatalf("branch continuation path = %q, want /v1/images/edits", req.URL.Path)
			}
			if err := req.ParseMultipartForm(8 << 20); err != nil {
				t.Fatalf("parse continuation edit: %v", err)
			}
			files := req.MultipartForm.File["image"]
			if len(files) != 1 {
				t.Fatalf("continuation image count = %d", len(files))
			}
			file, err := files[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(file)
			_ = file.Close()
			if readErr != nil || !bytes.Equal(got, priorImage) {
				t.Fatalf("continuation source changed, err=%v", readErr)
			}
			if prompt := req.FormValue("prompt"); !strings.Contains(prompt, "只把天空改成蓝色") || strings.Contains(prompt, "replace everything") {
				t.Fatalf("continuation prompt = %q", prompt)
			}
		case 2:
			if req.URL.Path != "/v1/images/generations" {
				t.Fatalf("regenerate path = %q, want /v1/images/generations", req.URL.Path)
			}
		default:
			t.Fatalf("unexpected image request %d", requestCount)
		}
		body, _ := json.Marshal(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(responseImage)}}})
		return imageSuccessResponse(string(body)), nil
	})

	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"replace everything","action":"edit","base_image":"previous_generation","input_images":["stale-artifact-id"]}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_follow", ImageModelID: "m_flow", DB: tool.db,
		ImageUserPrompt: "只把天空改成蓝色",
	}); err != nil {
		t.Fatalf("branch continuation: %v", err)
	}
	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"draw a fresh variation","action":"generate","base_image":"none"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_regen", ImageModelID: "m_flow", DB: tool.db,
	}); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("image request count = %d", requestCount)
	}
}

func seedImageBaseSelectionWorkflow(t *testing.T) (*imageGenerateTool, string, []byte, []string, [][]byte) {
	t.Helper()
	tool, convID := seedImageWorkflow(t, "openai", "gpt-image-2")
	for _, query := range []string{
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('u_root','c_flow',NULL,'user','m_flow')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,status) VALUES('a_prior','c_flow','u_root','assistant','m_flow','complete')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('u_edit','c_flow','a_prior','user','m_flow')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,author_id,status) VALUES('a_edit','c_flow','u_edit','assistant','m_flow','u_flow','streaming')`,
	} {
		if _, err := tool.db.Exec(query); err != nil {
			t.Fatalf("seed message %q: %v", query, err)
		}
	}

	priorCanvas := sizedPNG(t, 1600, 900)
	priorPath := filepath.Join(tool.artifactDir, "prior-canvas.png")
	if err := os.WriteFile(priorPath, priorCanvas, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateArtifact(context.Background(), tool.db, store.Artifact{
		ID: "art_canvas", MessageID: "a_prior", Filename: "prior-canvas.png", StoragePath: priorPath,
		MimeType: "image/png", SizeBytes: int64(len(priorCanvas)), Source: store.ArtifactSourceImageGenerate,
	}); err != nil {
		t.Fatal(err)
	}

	references := [][]byte{sizedPNG(t, 700, 500), sizedPNG(t, 500, 700)}
	referenceIDs := []string{"f_web_page", "f_hardware"}
	for i, id := range referenceIDs {
		path := filepath.Join(tool.uploadDir, id+".png")
		if err := os.WriteFile(path, references[i], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateFile(context.Background(), tool.db, store.File{
			ID: id, UserID: "u_flow", ConversationID: convID, Filename: id + ".png", StoragePath: path,
			MimeType: "image/png", Kind: "image", SizeBytes: int64(len(references[i])),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return tool, convID, priorCanvas, referenceIDs, references
}

func TestOpenAIImageEditCanSelectPriorCanvasBeforeCurrentReferences(t *testing.T) {
	tool, convID, priorCanvas, referenceIDs, references := seedImageBaseSelectionWorkflow(t)

	responseImage := sizedPNG(t, 32, 32)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/images/edits" {
			t.Fatalf("continuation path = %q, want /v1/images/edits", req.URL.Path)
		}
		if err := req.ParseMultipartForm(12 << 20); err != nil {
			t.Fatalf("parse continuation edit: %v", err)
		}
		files := req.MultipartForm.File["image[]"]
		if len(files) != 3 {
			t.Fatalf("continuation image count = %d, want canvas + 2 references", len(files))
		}
		wantImages := [][]byte{priorCanvas, references[0], references[1]}
		for i, want := range wantImages {
			file, err := files[i].Open()
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(file)
			_ = file.Close()
			if readErr != nil || !bytes.Equal(got, want) {
				t.Fatalf("continuation image[%d] is not the expected canvas/reference, err=%v", i, readErr)
			}
		}
		prompt := req.FormValue("prompt")
		if !strings.Contains(prompt, "右侧换成参考图中的网站页面") ||
			!strings.Contains(prompt, "first supplied image as the authoritative base canvas") {
			t.Fatalf("continuation prompt = %q", prompt)
		}
		body, _ := json.Marshal(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(responseImage)}}})
		return imageSuccessResponse(string(body)), nil
	})

	// A chat model can retain the first turn's attachment index when it
	// continues editing the previous generated result. The index is stale and
	// must be ignored because previous_generation has no attachment position.
	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"redesign everything","action":"edit","base_image":"previous_generation","base_image_index":1}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_edit", ImageModelID: "m_flow", DB: tool.db,
		ImageInputIDs: referenceIDs, ImageUserPrompt: "右侧换成参考图中的网站页面，右下角硬件参考另一张图，其他不变",
	}); err != nil {
		t.Fatalf("continuation with references: %v", err)
	}
}

func TestOpenAIImageGenerateIgnoresPriorAndCurrentImages(t *testing.T) {
	tool, convID, _, referenceIDs, _ := seedImageBaseSelectionWorkflow(t)
	responseImage := sizedPNG(t, 32, 32)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/images/generations" {
			t.Fatalf("generation path = %q, want /v1/images/generations", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode generation request: %v", err)
		}
		if body["prompt"] != "create a completely new poster" {
			t.Fatalf("generation prompt = %#v", body["prompt"])
		}
		if body["size"] != "3840x2160" {
			t.Fatalf("generation size = %#v, want user-requested 16:9 4K", body["size"])
		}
		encoded := base64.StdEncoding.EncodeToString(responseImage)
		return imageSuccessResponse(`{"data":[{"b64_json":"` + encoded + `"}]}`), nil
	})

	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"create a completely new poster","action":"generate","base_image":"none"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_edit", ImageModelID: "m_flow", DB: tool.db,
		ImageInputIDs: referenceIDs, ImageUserPrompt: "ignore the old poster and create a new one in 16:9 at 4K",
	}); err != nil {
		t.Fatalf("generation with image context: %v", err)
	}
}

func TestOpenAIImageEditCanSelectCurrentAttachmentWithoutPriorImage(t *testing.T) {
	tool, convID, priorCanvas, referenceIDs, references := seedImageBaseSelectionWorkflow(t)
	responseImage := sizedPNG(t, 32, 32)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/images/edits" {
			t.Fatalf("edit path = %q, want /v1/images/edits", req.URL.Path)
		}
		if err := req.ParseMultipartForm(12 << 20); err != nil {
			t.Fatalf("parse current-attachment edit: %v", err)
		}
		files := req.MultipartForm.File["image[]"]
		if len(files) != 2 {
			t.Fatalf("edit image count = %d, want selected base + other current reference", len(files))
		}
		wantImages := [][]byte{references[1], references[0]}
		for i, want := range wantImages {
			file, err := files[i].Open()
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(file)
			_ = file.Close()
			if readErr != nil || !bytes.Equal(got, want) {
				t.Fatalf("current-attachment edit image[%d] is incorrect, err=%v", i, readErr)
			}
			if bytes.Equal(got, priorCanvas) {
				t.Fatalf("current-attachment edit unexpectedly included the prior generated image at position %d", i)
			}
		}
		if prompt := req.FormValue("prompt"); !strings.Contains(prompt, "只修改第二张上传图") {
			t.Fatalf("current-attachment edit prompt = %q", prompt)
		}
		encoded := base64.StdEncoding.EncodeToString(responseImage)
		return imageSuccessResponse(`{"data":[{"b64_json":"` + encoded + `"}]}`), nil
	})

	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"rewrite everything","action":"edit","base_image":"current_attachment","base_image_index":2}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_edit", ImageModelID: "m_flow", DB: tool.db,
		ImageInputIDs: referenceIDs, ImageUserPrompt: "只修改第二张上传图，第一张作为参考",
	}); err != nil {
		t.Fatalf("edit selected current attachment: %v", err)
	}
}

func TestImageGenerateToolInheritsSavedModelParamsAndDefaultCount(t *testing.T) {
	tool, convID := seedImageWorkflow(t, "openai", "gpt-image-1.5")
	controls := `[{"key":"render","type":"select","default":"draft","options":[{"value":"draft"},{"value":"studio"}],"map":{"draft":{"quality":"low","background":"transparent","n":1},"studio":{"quality":"high","background":"opaque","n":2}}}]`
	if _, err := tool.db.Exec(`UPDATE models SET param_controls=? WHERE id='m_flow'`, controls); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,role,model_id,author_id,status) VALUES('a_saved','c_flow','assistant','m_flow','u_flow','streaming')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUserSettings(context.Background(), tool.db, "u_flow", map[string]any{
		"image_model_id": "m_flow",
		"image_model_params": map[string]any{
			"model_id": "m_flow",
			"params":   map[string]any{"render": "studio"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	responseImage := sizedPNG(t, 24, 24)
	var captured map[string]any
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"data": []map[string]string{
			{"b64_json": base64.StdEncoding.EncodeToString(responseImage)},
			{"b64_json": base64.StdEncoding.EncodeToString(responseImage)},
		}})
		return imageSuccessResponse(string(body)), nil
	})

	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"same settings","action":"generate","base_image":"none"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_saved", ImageModelID: "m_flow", DB: tool.db,
	}); err != nil {
		t.Fatalf("image_generate: %v", err)
	}
	if captured["n"] != float64(2) || captured["quality"] != "high" || captured["background"] != "opaque" {
		t.Fatalf("saved image settings were not inherited: %#v", captured)
	}
}

func TestImageGenerationFallsBackOnceAndLogsBothChannelAttempts(t *testing.T) {
	tool, convID := seedImageWorkflow(t, "openai", "gpt-image-1.5")
	for _, query := range []string{
		`INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch_flow_fallback','Image Fallback','openai','','https://fallback.images.test','fallback-secret',1)`,
		`UPDATE channels SET base_url='https://primary.images.test', api_format='' WHERE id='ch_flow'`,
		`UPDATE models SET fallback_channel_id='ch_flow_fallback', price_per_image=0.25 WHERE id='m_flow'`,
		`INSERT INTO messages(id,conversation_id,role,model_id,author_id,status) VALUES('a_fallback','c_flow','assistant','m_flow','u_flow','streaming')`,
	} {
		if _, err := tool.db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	if err := store.SetSetting(tool.db, "log_full_requests", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(tool.db, "log_errors_only", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(tool.db, "log_request_bodies", false); err != nil {
		t.Fatal(err)
	}

	imageData := sizedPNG(t, 24, 24)
	primaryCalls, fallbackCalls := 0, 0
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "primary.images.test":
			primaryCalls++
			if got := req.Header.Get("authorization"); got != "Bearer server-secret" {
				t.Fatalf("primary authorization = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"primary unavailable"}`)),
			}, nil
		case "fallback.images.test":
			fallbackCalls++
			if got := req.Header.Get("authorization"); got != "Bearer fallback-secret" {
				t.Fatalf("fallback authorization = %q", got)
			}
			return imageSuccessResponse(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(imageData) + `"}]}`), nil
		default:
			t.Fatalf("unexpected image host %q", req.URL.Host)
			return nil, nil
		}
	})

	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"draw a lighthouse","action":"generate","base_image":"none"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_fallback", ImageModelID: "m_flow", DB: tool.db,
	}); err != nil {
		t.Fatalf("fallback image generation: %v", err)
	}
	if primaryCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("attempts primary/fallback = %d/%d, want 1/1", primaryCalls, fallbackCalls)
	}

	rows, err := tool.db.Query(`
		SELECT status, channel_id, fallback, error, request_method, request_url, request_body, images_count, cost
		FROM usage_logs WHERE message_id='a_fallback' AND purpose='image' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type usageRow struct {
		status, channelID, errorText, method, requestURL, requestBody string
		fallback, images                                              int
		cost                                                          float64
	}
	var got []usageRow
	for rows.Next() {
		var row usageRow
		if err := rows.Scan(&row.status, &row.channelID, &row.fallback, &row.errorText, &row.method, &row.requestURL, &row.requestBody, &row.images, &row.cost); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("usage rows = %+v, want primary error + fallback success", got)
	}
	if got[0].status != "error" || got[0].channelID != "ch_flow" || got[0].fallback != 0 ||
		!strings.Contains(got[0].errorText, "503") || got[0].method != "POST" ||
		!strings.Contains(got[0].requestURL, "primary.images.test") || got[0].requestBody != "" || got[0].images != 0 || got[0].cost != 0 {
		t.Fatalf("primary failure row = %+v", got[0])
	}
	if got[1].status != "ok" || got[1].channelID != "ch_flow_fallback" || got[1].fallback != 1 ||
		got[1].errorText != "" || got[1].method != "POST" || !strings.Contains(got[1].requestURL, "fallback.images.test") ||
		got[1].requestBody != "" || got[1].images != 1 || got[1].cost != 0.25 {
		t.Fatalf("fallback success row = %+v", got[1])
	}
}

func TestGeminiReferenceLimitsAreModelSpecificAndRejectOverflow(t *testing.T) {
	previousCap := imageImageInputImageCap
	imageImageInputImageCap = 0
	t.Cleanup(func() { imageImageInputImageCap = previousCap })

	tool, convID := seedImageWorkflow(t, "gemini", "gemini-2.5-flash-image")
	if _, err := tool.db.Exec(`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m_gemini3','ch_flow','image','gemini-3-pro-image-preview','Gemini 3')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.db.Exec(`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('m_gemini3','ug_flow',604800,'count',0)`); err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"a_gemini25", "a_gemini3"} {
		if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,role,model_id,author_id,status) VALUES(?,'c_flow','assistant','m_flow','u_flow','streaming')`, messageID); err != nil {
			t.Fatal(err)
		}
	}
	imageData := sizedPNG(t, 20, 20)
	inputIDs := []string{}
	for i := 1; i <= 4; i++ {
		id := "f_ref_" + string(rune('0'+i))
		path := filepath.Join(tool.uploadDir, id+".png")
		if err := os.WriteFile(path, imageData, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateFile(context.Background(), tool.db, store.File{
			ID: id, UserID: "u_flow", ConversationID: convID, Filename: id + ".png", StoragePath: path,
			MimeType: "image/png", Kind: "image", SizeBytes: int64(len(imageData)),
		}); err != nil {
			t.Fatal(err)
		}
		inputIDs = append(inputIDs, id)
	}

	httpCalls := 0
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		httpCalls++
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		contents, _ := body["contents"].([]any)
		content, _ := contents[0].(map[string]any)
		parts, _ := content["parts"].([]any)
		if len(parts) != 5 {
			t.Fatalf("Gemini 3 parts = %d, want 4 images + prompt", len(parts))
		}
		encoded := base64.StdEncoding.EncodeToString(imageData)
		return imageSuccessResponse(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + encoded + `"}}]}}]}`), nil
	})

	_, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"combine","action":"edit","base_image":"current_attachment","base_image_index":1}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_gemini25", ImageModelID: "m_flow", ImageInputIDs: inputIDs, DB: tool.db,
	})
	if err == nil || !strings.Contains(err.Error(), "at most 3") {
		t.Fatalf("Gemini 2.5 overflow error = %v", err)
	}
	if httpCalls != 0 {
		t.Fatal("Gemini 2.5 overflow reached the provider")
	}
	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"combine","action":"edit","base_image":"current_attachment","base_image_index":1}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_gemini3", ImageModelID: "m_gemini3", ImageInputIDs: inputIDs, DB: tool.db,
	}); err != nil {
		t.Fatalf("Gemini 3 four-reference request: %v", err)
	}
	if httpCalls != 1 {
		t.Fatalf("Gemini 3 provider calls = %d", httpCalls)
	}
}

func TestProviderImageOutputMIMEUsesActualBytes(t *testing.T) {
	jpegData := sizedJPEG(t, 16, 16)
	webpData := []byte{'R', 'I', 'F', 'F', 16, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}

	t.Run("OpenAI", func(t *testing.T) {
		useImageTestHTTPClient(t, func(*http.Request) (*http.Response, error) {
			body, _ := json.Marshal(map[string]any{"data": []map[string]string{
				{"b64_json": base64.StdEncoding.EncodeToString(jpegData)},
				{"b64_json": base64.StdEncoding.EncodeToString(webpData)},
			}})
			return imageSuccessResponse(string(body)), nil
		})
		images, err := openaiGenerateImages(context.Background(), "https://images.example.test", "key", "gpt-image-1.5", imgInput{Prompt: "draw", N: 2}, nil, map[string]any{"output_format": "png"})
		if err != nil || len(images) != 2 || images[0].mime != "image/jpeg" || images[1].mime != "image/webp" {
			t.Fatalf("OpenAI images=%#v err=%v", images, err)
		}
	})

	t.Run("Gemini", func(t *testing.T) {
		useImageTestHTTPClient(t, func(*http.Request) (*http.Response, error) {
			return imageSuccessResponse(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(jpegData) + `"}}]}}]}`), nil
		})
		images, err := geminiGenerateImages(context.Background(), "https://images.example.test", "key", "gemini-3-pro-image-preview", imgInput{Prompt: "draw", N: 1}, nil, nil)
		if err != nil || len(images) != 1 || images[0].mime != "image/jpeg" {
			t.Fatalf("Gemini images=%#v err=%v", images, err)
		}
	})
}

func TestArtifactPersistenceFailureReturnsErrorWithoutImageUsage(t *testing.T) {
	tool, convID := seedImageWorkflow(t, "openai", "gpt-image-1.5")
	if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,role,model_id,author_id,status) VALUES('a_fail','c_flow','assistant','m_flow','u_flow','streaming')`); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool.artifactDir = filepath.Join(blocker, "artifacts")
	imageData := sizedPNG(t, 16, 16)
	useImageTestHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return imageSuccessResponse(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(imageData) + `"}]}`), nil
	})

	_, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"draw","action":"generate","base_image":"none"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_fail", ImageModelID: "m_flow", DB: tool.db,
	})
	if err == nil || !strings.Contains(err.Error(), "persist generated image") {
		t.Fatalf("artifact failure error = %v", err)
	}
	var usageRows int
	if err := tool.db.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE purpose='image' AND message_id='a_fail'`).Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if usageRows != 0 {
		t.Fatalf("artifact failure wrote %d image usage rows", usageRows)
	}

	cleanupDir := t.TempDir()
	if _, err := saveArtifact(context.Background(), &llm.ToolContext{DB: tool.db, MessageID: "missing_message"}, cleanupDir, "orphan.png", "image/png", store.ArtifactSourceImageGenerate, imageData); err == nil {
		t.Fatal("missing artifact message must fail the database insert")
	}
	entries, readErr := os.ReadDir(cleanupDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("database failure left orphan files: entries=%d err=%v", len(entries), readErr)
	}
}
