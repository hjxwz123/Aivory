package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestImageGenerationSendsCurrentReferences(t *testing.T) {
	for _, provider := range []string{"openai", "gemini"} {
		for _, direct := range []bool{false, true} {
			name := provider + "/chat"
			if direct {
				name = provider + "/direct"
			}
			t.Run(name, func(t *testing.T) {
				scenario := newCommonImageScenario(t, true)
				model := "gpt-image-2.5-sunburst"
				if provider == "gemini" {
					model = "gemini-3-pro-image-preview"
				}
				if _, err := scenario.tool.db.Exec(`UPDATE channels SET type=? WHERE id='ch_flow'`, provider); err != nil {
					t.Fatal(err)
				}
				if _, err := scenario.tool.db.Exec(`UPDATE models SET request_id=? WHERE id='m_flow'`, model); err != nil {
					t.Fatal(err)
				}
				scenario.toolCtx.DirectImageTurn = direct
				scenario.toolCtx.ImageInputIDs = []string{"common_portrait", "common_jpeg"}
				const userPrompt = "不背单词打卡全勤奖。请根据上面内容，模仿下面的图片生成一个证书"
				scenario.toolCtx.ImageUserPrompt = userPrompt
				calls := 0
				useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
					calls++
					var images [][]byte
					var prompt string
					if provider == "openai" {
						if req.URL.Path != "/v1/images/edits" {
							t.Fatalf("reference generation path = %q, want /v1/images/edits", req.URL.Path)
						}
						images = readCommonMultipartImages(t, req)
						prompt = req.FormValue("prompt")
						if req.FormValue("model") != model {
							t.Fatalf("request did not preserve selected model %q", model)
						}
					} else {
						var body struct {
							Contents []struct {
								Parts []struct {
									Text       string `json:"text"`
									InlineData *struct {
										Data []byte `json:"data"`
									} `json:"inlineData"`
								} `json:"parts"`
							} `json:"contents"`
						}
						if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
							t.Fatal(err)
						}
						for _, content := range body.Contents {
							for _, part := range content.Parts {
								prompt += part.Text
								if part.InlineData != nil {
									images = append(images, part.InlineData.Data)
								}
							}
						}
					}
					if len(images) != 2 {
						t.Fatalf("sent %d images, want exactly 2 current references without prior generation", len(images))
					}
					for i, id := range scenario.toolCtx.ImageInputIDs {
						if !bytes.Equal(images[i], scenario.uploads[id]) {
							t.Fatalf("reference %d bytes differ from uploaded file", i)
						}
					}
					if !strings.Contains(prompt, userPrompt) || strings.Contains(prompt, "invent a red and gold certificate") {
						t.Fatalf("reference generation lost user's literal instruction: %q", prompt)
					}
					if strings.Contains(prompt, "authoritative base canvas") {
						t.Fatal("reference generation was incorrectly forced into a faithful canvas edit")
					}
					if provider == "openai" {
						return commonMockImageResponse(t, 1), nil
					}
					encoded := base64.StdEncoding.EncodeToString(sizedPNG(t, 32, 32))
					return imageSuccessResponse(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + encoded + `"}}]}}]}`), nil
				})
				_, _, err := scenario.tool.Execute(context.Background(), []byte(`{"prompt":"invent a red and gold certificate","action":"generate","base_image":"none"}`), scenario.toolCtx)
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 || len(scenario.artifacts) != 1 {
					t.Fatalf("calls/artifacts = %d/%d, want 1/1", calls, len(scenario.artifacts))
				}
			})
		}
	}
}

func TestImageGenerationRejectsUnavailableOrExcessReferences(t *testing.T) {
	for _, test := range []struct {
		name      string
		ids       []string
		limit     int
		wantError string
	}{
		{"missing reference", []string{"missing_file"}, 2, "reference image is unavailable"},
		{"partially missing references", []string{"common_portrait", "missing_file"}, 2, "reference image is unavailable"},
		{"too many references", []string{"common_portrait", "common_jpeg"}, 1, "at most 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			scenario := newCommonImageScenario(t, false)
			previousCap := imageImageInputImageCap
			imageImageInputImageCap = test.limit
			t.Cleanup(func() { imageImageInputImageCap = previousCap })
			scenario.toolCtx.ImageInputIDs = test.ids
			useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
				t.Fatal("invalid reference request reached the provider")
				return nil, nil
			})
			_, _, err := scenario.tool.Execute(context.Background(), []byte(`{"prompt":"模仿参考图生成一个证书","action":"generate","base_image":"none"}`), scenario.toolCtx)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}
