package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// commonImageScenario provides a realistic conversation branch with optional
// generated history and three small local uploads. Provider calls remain fully
// mocked by each test.
type commonImageScenario struct {
	tool      *imageGenerateTool
	toolCtx   *llm.ToolContext
	previous  []byte
	uploads   map[string][]byte
	artifacts []llm.ArtifactRef
}

func newCommonImageScenario(t *testing.T, withPrevious bool) *commonImageScenario {
	t.Helper()
	tool, convID := seedImageWorkflow(t, "openai", "gpt-image-2")
	if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,author_id,status) VALUES('common_user_root',? ,NULL,'user','m_flow','u_flow','complete')`, convID); err != nil {
		t.Fatal(err)
	}
	parentID := "common_user_root"
	var previous []byte
	if withPrevious {
		if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,author_id,status) VALUES('common_previous',?,'common_user_root','assistant','m_flow','u_flow','complete')`, convID); err != nil {
			t.Fatal(err)
		}
		parentID = "common_previous"
		previous = sizedPNG(t, 160, 90)
		path := filepath.Join(tool.artifactDir, "common-previous.png")
		if err := os.WriteFile(path, previous, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateArtifact(context.Background(), tool.db, store.Artifact{
			ID: "common_previous_artifact", MessageID: "common_previous", Filename: "previous.png",
			StoragePath: path, MimeType: "image/png", SizeBytes: int64(len(previous)),
			Source: store.ArtifactSourceImageGenerate,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,author_id,status) VALUES('common_user_current',?,?,'user','m_flow','u_flow','complete')`, convID, parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,model_id,author_id,status) VALUES('common_assistant',?,'common_user_current','assistant','m_flow','u_flow','streaming')`, convID); err != nil {
		t.Fatal(err)
	}

	uploads := map[string][]byte{
		"common_landscape": sizedPNG(t, 160, 90),
		"common_portrait":  sizedPNG(t, 90, 160),
		"common_jpeg":      sizedJPEG(t, 120, 120),
	}
	for id, data := range uploads {
		ext := ".png"
		mime := "image/png"
		if id == "common_jpeg" {
			ext = ".jpg"
			mime = "image/jpeg"
		}
		path := filepath.Join(tool.uploadDir, id+ext)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateFile(context.Background(), tool.db, store.File{
			ID: id, UserID: "u_flow", ConversationID: convID, Filename: id + ext,
			StoragePath: path, MimeType: mime, Kind: "image", SizeBytes: int64(len(data)),
		}); err != nil {
			t.Fatal(err)
		}
	}

	scenario := &commonImageScenario{
		tool: tool, previous: previous, uploads: uploads,
		toolCtx: &llm.ToolContext{
			UserID: "u_flow", ConvID: convID, MessageID: "common_assistant",
			ImageModelID: "m_flow", DB: tool.db,
		},
	}
	scenario.toolCtx.OnArtifact = func(artifact llm.ArtifactRef) {
		scenario.artifacts = append(scenario.artifacts, artifact)
	}
	return scenario
}

func commonMockImageResponse(t *testing.T, count int) *http.Response {
	t.Helper()
	data := make([]map[string]string, count)
	for index := range data {
		imageData := sizedPNG(t, 32+index, 24+index)
		data[index] = map[string]string{"b64_json": base64.StdEncoding.EncodeToString(imageData)}
	}
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	return imageSuccessResponse(string(body))
}

func readCommonMultipartImages(t *testing.T, req *http.Request) [][]byte {
	t.Helper()
	if err := req.ParseMultipartForm(16 << 20); err != nil {
		t.Fatalf("parse multipart image request: %v", err)
	}
	files := req.MultipartForm.File["image"]
	if len(files) == 0 {
		files = req.MultipartForm.File["image[]"]
	}
	images := make([][]byte, 0, len(files))
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		images = append(images, data)
	}
	return images
}

func TestImageCommonOperationsUseExpectedMockedRequests(t *testing.T) {
	tests := []struct {
		name             string
		withPrevious     bool
		inputIDs         []string
		toolInput        string
		userPrompt       string
		wantPath         string
		wantPrompt       string
		unwantedPrompt   string
		wantImageSources []string
		wantSize         string
		wantCount        int
	}{
		{
			name: "generate a new image ignores old and uploaded images", withPrevious: true,
			inputIDs:   []string{"common_landscape"},
			toolInput:  `{"prompt":"生成一个全新的简洁产品图","action":"generate","base_image":"none"}`,
			userPrompt: "生成一个全新的简洁产品图", wantPath: "/v1/images/generations",
			wantPrompt: "生成一个全新的简洁产品图", wantSize: "2048x2048", wantCount: 1,
		},
		{
			name:       "generate a landscape image with requested resolution",
			toolInput:  `{"prompt":"生成横版活动海报","action":"generate","base_image":"none"}`,
			userPrompt: "生成一张 16:9、4K 横版活动海报", wantPath: "/v1/images/generations",
			wantPrompt: "生成横版活动海报", wantSize: "3840x2160", wantCount: 1,
		},
		{
			name:       "generate two image variations",
			toolInput:  `{"prompt":"生成两个图标方案","action":"generate","base_image":"none","n":2}`,
			userPrompt: "生成两个图标方案", wantPath: "/v1/images/generations",
			wantPrompt: "生成两个图标方案", wantSize: "2048x2048", wantCount: 2,
		},
		{
			name:       "edit one uploaded PNG without an explicit index",
			inputIDs:   []string{"common_landscape"},
			toolInput:  `{"prompt":"rewrite the entire picture","action":"edit","base_image":"current_attachment"}`,
			userPrompt: "只把背景改成白色，其他不变", wantPath: "/v1/images/edits",
			wantPrompt: "只把背景改成白色，其他不变", unwantedPrompt: "rewrite the entire picture",
			wantImageSources: []string{"common_landscape"}, wantSize: "2048x1152", wantCount: 1,
		},
		{
			name:       "edit the second upload and use the first as a reference",
			inputIDs:   []string{"common_landscape", "common_portrait"},
			toolInput:  `{"prompt":"replace all content","action":"edit","base_image":"current_attachment","base_image_index":2}`,
			userPrompt: "修改第二张图的人物服装，第一张图作为颜色参考", wantPath: "/v1/images/edits",
			wantPrompt: "修改第二张图的人物服装，第一张图作为颜色参考", unwantedPrompt: "replace all content",
			wantImageSources: []string{"common_portrait", "common_landscape"}, wantSize: "1152x2048", wantCount: 1,
		},
		{
			name:       "edit a JPEG upload",
			inputIDs:   []string{"common_jpeg"},
			toolInput:  `{"prompt":"change the photo","action":"edit","base_image":"current_attachment","base_image_index":1}`,
			userPrompt: "轻微提高照片亮度，保持构图", wantPath: "/v1/images/edits",
			wantPrompt: "轻微提高照片亮度，保持构图", unwantedPrompt: "change the photo",
			wantImageSources: []string{"common_jpeg"}, wantSize: "2048x2048", wantCount: 1,
		},
		{
			name: "continue editing the previous result with a stale upload index", withPrevious: true,
			toolInput:  `{"prompt":"replace everything","action":"edit","base_image":"previous_generation","base_image_index":1}`,
			userPrompt: "只把标题颜色改成红色，其他不变", wantPath: "/v1/images/edits",
			wantPrompt: "只把标题颜色改成红色，其他不变", unwantedPrompt: "replace everything",
			wantImageSources: []string{"previous"}, wantSize: "2048x1152", wantCount: 1,
		},
		{
			name: "continue editing the previous result with a new reference", withPrevious: true,
			inputIDs:   []string{"common_jpeg"},
			toolInput:  `{"prompt":"redesign the image","action":"edit","base_image":"previous_generation"}`,
			userPrompt: "保持上一张构图，只参考附件中的配色", wantPath: "/v1/images/edits",
			wantPrompt: "保持上一张构图，只参考附件中的配色", unwantedPrompt: "redesign the image",
			wantImageSources: []string{"previous", "common_jpeg"}, wantSize: "2048x1152", wantCount: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := newCommonImageScenario(t, test.withPrevious)
			scenario.toolCtx.ImageInputIDs = append([]string(nil), test.inputIDs...)
			scenario.toolCtx.ImageUserPrompt = test.userPrompt
			providerCalls := 0
			useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
				providerCalls++
				if req.URL.Path != test.wantPath {
					t.Fatalf("provider path = %q, want %q", req.URL.Path, test.wantPath)
				}
				if req.Header.Get("authorization") != "Bearer server-secret" {
					t.Fatalf("missing mocked provider authorization header")
				}
				if test.wantPath == "/v1/images/generations" {
					var request map[string]any
					if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					if request["prompt"] != test.wantPrompt || request["size"] != test.wantSize || request["n"] != float64(test.wantCount) {
						t.Fatalf("generation request = %#v", request)
					}
				} else {
					images := readCommonMultipartImages(t, req)
					if len(images) != len(test.wantImageSources) {
						t.Fatalf("edit image count = %d, want %d", len(images), len(test.wantImageSources))
					}
					for index, source := range test.wantImageSources {
						want := scenario.uploads[source]
						if source == "previous" {
							want = scenario.previous
						}
						if !bytes.Equal(images[index], want) {
							t.Fatalf("edit image %d did not use source %q", index, source)
						}
					}
					prompt := req.FormValue("prompt")
					if !strings.Contains(prompt, test.wantPrompt) || (test.unwantedPrompt != "" && strings.Contains(prompt, test.unwantedPrompt)) {
						t.Fatalf("edit prompt = %q", prompt)
					}
					if size := req.FormValue("size"); size != test.wantSize {
						t.Fatalf("edit size = %q, want %q", size, test.wantSize)
					}
				}
				return commonMockImageResponse(t, test.wantCount), nil
			})

			output, _, err := scenario.tool.Execute(context.Background(), []byte(test.toolInput), scenario.toolCtx)
			if err != nil {
				t.Fatalf("image operation failed: %v", err)
			}
			if providerCalls != 1 {
				t.Fatalf("provider calls = %d, want 1", providerCalls)
			}
			if !strings.Contains(output, "Generated "+strconv.Itoa(test.wantCount)+" image(s)") {
				t.Fatalf("tool output = %q", output)
			}
			if len(scenario.artifacts) != test.wantCount {
				t.Fatalf("artifact callbacks = %d, want %d", len(scenario.artifacts), test.wantCount)
			}
			for _, artifact := range scenario.artifacts {
				if artifact.MimeType != "image/png" || artifact.Size == 0 || artifact.URL == "" {
					t.Fatalf("persisted artifact = %+v", artifact)
				}
			}
			var artifactCount int
			if err := scenario.tool.db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE message_id='common_assistant' AND source='image_generate'`).Scan(&artifactCount); err != nil {
				t.Fatal(err)
			}
			if artifactCount != test.wantCount {
				t.Fatalf("stored artifacts = %d, want %d", artifactCount, test.wantCount)
			}
			var usageCount int
			if err := scenario.tool.db.QueryRow(`SELECT images_count FROM usage_logs WHERE message_id='common_assistant' AND purpose='image'`).Scan(&usageCount); err != nil {
				t.Fatal(err)
			}
			if usageCount != test.wantCount {
				t.Fatalf("logged image count = %d, want %d", usageCount, test.wantCount)
			}
		})
	}
}

func TestImageCommonInvalidOperationsStopBeforeProvider(t *testing.T) {
	tests := []struct {
		name         string
		withPrevious bool
		inputIDs     []string
		toolInput    string
		wantError    string
	}{
		{name: "empty prompt", toolInput: `{"prompt":" ","action":"generate","base_image":"none"}`, wantError: "prompt required"},
		{name: "missing action", toolInput: `{"prompt":"draw","base_image":"none"}`, wantError: "action must be generate or edit"},
		{name: "generation cannot select a previous base", withPrevious: true, toolInput: `{"prompt":"draw","action":"generate","base_image":"previous_generation"}`, wantError: "requires base_image=none"},
		{name: "generation cannot include an attachment index", toolInput: `{"prompt":"draw","action":"generate","base_image":"none","base_image_index":1}`, wantError: "must not set base_image_index"},
		{name: "edit must select a base", toolInput: `{"prompt":"edit","action":"edit","base_image":"none"}`, wantError: "Please specify whether to edit"},
		{name: "current attachment edit needs an upload", toolInput: `{"prompt":"edit","action":"edit","base_image":"current_attachment","base_image_index":1}`, wantError: "no current-turn image attachment"},
		{name: "multiple uploads need a valid index", inputIDs: []string{"common_landscape", "common_portrait"}, toolInput: `{"prompt":"edit","action":"edit","base_image":"current_attachment"}`, wantError: "must select one of the 2"},
		{name: "attachment index cannot be out of range", inputIDs: []string{"common_landscape"}, toolInput: `{"prompt":"edit","action":"edit","base_image":"current_attachment","base_image_index":2}`, wantError: "must select one of the 1"},
		{name: "selected upload must be a valid image", inputIDs: []string{"missing_file"}, toolInput: `{"prompt":"edit","action":"edit","base_image":"current_attachment","base_image_index":1}`, wantError: "selected current-turn base image is unavailable"},
		{name: "previous edit needs a generated image on the branch", toolInput: `{"prompt":"edit","action":"edit","base_image":"previous_generation"}`, wantError: "no previous generated image"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := newCommonImageScenario(t, test.withPrevious)
			scenario.toolCtx.ImageInputIDs = append([]string(nil), test.inputIDs...)
			providerCalls := 0
			useImageTestHTTPClient(t, func(*http.Request) (*http.Response, error) {
				providerCalls++
				return commonMockImageResponse(t, 1), nil
			})

			_, _, err := scenario.tool.Execute(context.Background(), []byte(test.toolInput), scenario.toolCtx)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("operation error = %v, want %q", err, test.wantError)
			}
			var userErr *llm.ToolUserError
			if !errors.As(err, &userErr) {
				t.Fatalf("operation error type = %T, want ToolUserError", err)
			}
			if providerCalls != 0 {
				t.Fatalf("invalid operation reached provider %d time(s)", providerCalls)
			}
			var artifacts int
			if err := scenario.tool.db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE message_id='common_assistant'`).Scan(&artifacts); err != nil {
				t.Fatal(err)
			}
			if artifacts != 0 {
				t.Fatalf("invalid operation persisted %d artifact(s)", artifacts)
			}
		})
	}
}

func TestImageCommonOperationCanEditMockedOutputCreatedEarlierInSameTurn(t *testing.T) {
	scenario := newCommonImageScenario(t, false)
	firstOutput := sizedPNG(t, 128, 72)
	secondOutput := sizedPNG(t, 64, 64)
	providerCalls := 0
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		providerCalls++
		switch providerCalls {
		case 1:
			if req.URL.Path != "/v1/images/generations" {
				t.Fatalf("first request path = %q", req.URL.Path)
			}
			body, _ := json.Marshal(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(firstOutput)}}})
			return imageSuccessResponse(string(body)), nil
		case 2:
			if req.URL.Path != "/v1/images/edits" {
				t.Fatalf("second request path = %q", req.URL.Path)
			}
			images := readCommonMultipartImages(t, req)
			if len(images) != 1 || !bytes.Equal(images[0], firstOutput) {
				t.Fatal("second request did not edit the first mocked output")
			}
			body, _ := json.Marshal(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(secondOutput)}}})
			return imageSuccessResponse(string(body)), nil
		default:
			t.Fatalf("unexpected provider call %d", providerCalls)
			return nil, nil
		}
	})

	scenario.toolCtx.ImageUserPrompt = "生成一张横版封面"
	if _, _, err := scenario.tool.Execute(context.Background(), []byte(`{"prompt":"生成一张横版封面","action":"generate","base_image":"none"}`), scenario.toolCtx); err != nil {
		t.Fatalf("first image operation: %v", err)
	}
	scenario.toolCtx.ImageUserPrompt = "只把封面标题改小"
	if _, _, err := scenario.tool.Execute(context.Background(), []byte(`{"prompt":"change everything","action":"edit","base_image":"previous_generation","base_image_index":1}`), scenario.toolCtx); err != nil {
		t.Fatalf("follow-up image operation: %v", err)
	}
	if providerCalls != 2 || len(scenario.artifacts) != 2 {
		t.Fatalf("provider calls/artifacts = %d/%d, want 2/2", providerCalls, len(scenario.artifacts))
	}
}
