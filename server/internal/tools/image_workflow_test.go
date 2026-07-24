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
		`INSERT INTO users(id,email,password_hash,name) VALUES('u_flow','flow@example.test','hash','Flow User')`,
		`INSERT INTO channels(id,name,type,base_url,api_key) VALUES('ch_flow','Image Channel','` + channelType + `','https://images.example.test','server-secret')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m_flow','ch_flow','image','` + requestID + `','Image Model')`,
		`INSERT INTO conversations(id,user_id,title,model_id) VALUES('c_flow','u_flow','Image flow','m_flow')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	return &imageGenerateTool{db: db, artifactDir: t.TempDir()}, "c_flow"
}

func TestOpenAIImageContinuationUsesNearestBranchAndRegenerateIgnoresSibling(t *testing.T) {
	tool, convID := seedImageWorkflow(t, "openai", "gpt-image-2")
	for _, query := range []string{
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('u_root','c_flow',NULL,'user','m_flow')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('a_old','c_flow','u_root','assistant','m_flow')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('u_follow','c_flow','a_old','user','m_flow')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('a_follow','c_flow','u_follow','assistant','m_flow')`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,model_id) VALUES('a_regen','c_flow','u_root','assistant','m_flow')`,
	} {
		if _, err := tool.db.Exec(query); err != nil {
			t.Fatalf("seed message %q: %v", query, err)
		}
	}
	priorImage := sizedPNG(t, 640, 360)
	priorPath := filepath.Join(t.TempDir(), "prior.png")
	if err := os.WriteFile(priorPath, priorImage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateArtifact(context.Background(), tool.db, store.Artifact{
		ID: "art_prior", MessageID: "a_old", Filename: "prior.png", StoragePath: priorPath,
		MimeType: "image/png", SizeBytes: int64(len(priorImage)),
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

	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"replace everything"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_follow", ImageModelID: "m_flow", DB: tool.db,
		ImageUserPrompt: "只把天空改成蓝色",
	}); err != nil {
		t.Fatalf("branch continuation: %v", err)
	}
	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"draw a fresh variation"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_regen", ImageModelID: "m_flow", DB: tool.db,
	}); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("image request count = %d", requestCount)
	}
}

func TestImageGenerateToolInheritsSavedModelParamsAndDefaultCount(t *testing.T) {
	tool, convID := seedImageWorkflow(t, "openai", "gpt-image-1.5")
	controls := `[{"key":"render","type":"select","default":"draft","options":[{"value":"draft"},{"value":"studio"}],"map":{"draft":{"quality":"low","background":"transparent","n":1},"studio":{"quality":"high","background":"opaque","n":2}}}]`
	if _, err := tool.db.Exec(`UPDATE models SET param_controls=? WHERE id='m_flow'`, controls); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,role,model_id) VALUES('a_saved','c_flow','assistant','m_flow')`); err != nil {
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

	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"same settings"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_saved", ImageModelID: "m_flow", DB: tool.db,
	}); err != nil {
		t.Fatalf("image_generate: %v", err)
	}
	if captured["n"] != float64(2) || captured["quality"] != "high" || captured["background"] != "opaque" {
		t.Fatalf("saved image settings were not inherited: %#v", captured)
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
	for _, messageID := range []string{"a_gemini25", "a_gemini3"} {
		if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,role,model_id) VALUES(?,'c_flow','assistant','m_flow')`, messageID); err != nil {
			t.Fatal(err)
		}
	}
	imageData := sizedPNG(t, 20, 20)
	inputIDs := []string{}
	for i := 1; i <= 4; i++ {
		id := "f_ref_" + string(rune('0'+i))
		path := filepath.Join(t.TempDir(), id+".png")
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

	_, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"combine"}`), &llm.ToolContext{
		UserID: "u_flow", ConvID: convID, MessageID: "a_gemini25", ImageModelID: "m_flow", ImageInputIDs: inputIDs, DB: tool.db,
	})
	if err == nil || !strings.Contains(err.Error(), "at most 3") {
		t.Fatalf("Gemini 2.5 overflow error = %v", err)
	}
	if httpCalls != 0 {
		t.Fatal("Gemini 2.5 overflow reached the provider")
	}
	if _, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"combine"}`), &llm.ToolContext{
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
	if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,role,model_id) VALUES('a_fail','c_flow','assistant','m_flow')`); err != nil {
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

	_, _, err := tool.Execute(context.Background(), []byte(`{"prompt":"draw"}`), &llm.ToolContext{
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
	if _, err := saveArtifact(context.Background(), &llm.ToolContext{DB: tool.db, MessageID: "missing_message"}, cleanupDir, "orphan.png", "image/png", imageData); err == nil {
		t.Fatal("missing artifact message must fail the database insert")
	}
	entries, readErr := os.ReadDir(cleanupDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("database failure left orphan files: entries=%d err=%v", len(entries), readErr)
	}
}
