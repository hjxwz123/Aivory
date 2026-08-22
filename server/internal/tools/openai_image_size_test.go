package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

func sizedPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return encoded.Bytes()
}

func TestImageGenerateSchemaDoesNotDefaultToSquare(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal((&imageGenerateTool{}).InputSchema(), &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	size, _ := properties["size"].(map[string]any)
	if _, exists := size["default"]; exists {
		t.Fatalf("size schema still has a default: %#v", size)
	}
	if _, exists := size["enum"]; exists {
		t.Fatalf("size schema must allow GPT Image 2 WIDTHxHEIGHT values: %#v", size)
	}
	if _, exists := properties["input_images"]; exists {
		t.Fatal("internal artifact ids must not be exposed to the chat model")
	}
	action, _ := properties["action"].(map[string]any)
	if got := action["enum"]; fmt.Sprint(got) != "[generate edit]" {
		t.Fatalf("action enum = %#v, want explicit generate/edit", got)
	}
	baseImage, _ := properties["base_image"].(map[string]any)
	if got := baseImage["enum"]; fmt.Sprint(got) != "[none previous_generation current_attachment]" {
		t.Fatalf("base_image enum = %#v", got)
	}
	required, _ := schema["required"].([]any)
	for _, want := range []string{"prompt", "action", "base_image"} {
		found := false
		for _, field := range required {
			found = found || field == want
		}
		if !found {
			t.Fatalf("schema required fields = %#v, missing %q", required, want)
		}
	}
}

func TestImageGenerateRequiresExplicitConsistentOperation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "missing action", input: `{"prompt":"draw"}`, want: "action must be generate or edit"},
		{name: "generation with base", input: `{"prompt":"draw","action":"generate","base_image":"previous_generation"}`, want: "requires base_image=none"},
		{name: "edit without base", input: `{"prompt":"change title","action":"edit","base_image":"none"}`, want: "specify whether to edit the previous generated image"},
	}
	tool := &imageGenerateTool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := tool.Execute(context.Background(), []byte(tt.input), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Execute error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestClosestGPTImage1Size(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		want          string
	}{
		{name: "square", width: 1000, height: 1000, want: "1024x1024"},
		{name: "landscape", width: 1600, height: 900, want: "1536x1024"},
		{name: "portrait", width: 900, height: 1600, want: "1024x1536"},
		{name: "invalid", width: 0, height: 1000, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := closestGPTImage1Size(tt.width, tt.height); got != tt.want {
				t.Fatalf("closestGPTImage1Size(%d, %d) = %q, want %q", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

func TestClosestGPTImage2SizePreservesLegalAspect(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		want          string
		wantRatio     float64
	}{
		{name: "landscape 16:9", width: 1920, height: 1080, want: "2048x1152", wantRatio: 16.0 / 9.0},
		{name: "portrait 9:16", width: 1080, height: 1920, want: "1152x2048", wantRatio: 9.0 / 16.0},
		{name: "standard 4:3 defaults to 2K", width: 1600, height: 1200, want: "2048x1536", wantRatio: 4.0 / 3.0},
		{name: "extreme ratio clamps to 3:1", width: 5000, height: 1000, want: "2016x672", wantRatio: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := closestGPTImage2Size(tt.width, tt.height)
			if got != tt.want {
				t.Fatalf("closestGPTImage2Size(%d, %d) = %q, want %q", tt.width, tt.height, got, tt.want)
			}
			var width, height int
			if _, err := fmt.Sscanf(got, "%dx%d", &width, &height); err != nil {
				t.Fatalf("parse inferred size %q: %v", got, err)
			}
			pixels := width * height
			if width%16 != 0 || height%16 != 0 || width > gptImage2MaxEdge || height > gptImage2MaxEdge {
				t.Fatalf("inferred size violates edge constraints: %s", got)
			}
			if pixels < gptImage2MinPixels || pixels > gptImage2MaxPixels {
				t.Fatalf("inferred size has %d pixels, outside official bounds", pixels)
			}
			if ratio := float64(width) / float64(height); math.Abs(math.Log(ratio/tt.wantRatio)) > 0.001 {
				t.Fatalf("inferred ratio %f is not close to %f", ratio, tt.wantRatio)
			}
		})
	}
	if got := closestGPTImage2Size(0, 100); got != "" {
		t.Fatalf("invalid dimensions inferred size %q", got)
	}
}

func TestInferredOpenAIEditSizeUsesKnownModelsAndFallsBack(t *testing.T) {
	landscape := imageBytes{data: sizedPNG(t, 1600, 900), mime: "image/png"}
	if got := inferredOpenAIEditSize("gpt-image-2-2026-04-21", landscape); got != "2048x1152" {
		t.Fatalf("GPT Image 2 snapshot size = %q", got)
	}
	if got := inferredOpenAIEditSize("gpt-image-1.5", landscape); got != "1536x1024" {
		t.Fatalf("GPT Image 1.5 size = %q", got)
	}
	if got := inferredOpenAIEditSize("chatgpt-image-latest", landscape); got != "" {
		t.Fatalf("unknown model inferred unsupported size %q", got)
	}
	if got := inferredOpenAIEditSize("gpt-image-2-preview", landscape); got != "" {
		t.Fatalf("undocumented GPT Image 2 variant inferred unsupported size %q", got)
	}
	if got := inferredOpenAIEditSize("gpt-image-2", imageBytes{data: []byte("not an image"), mime: "image/png"}); got != "" {
		t.Fatalf("invalid image inferred size %q", got)
	}
}

func TestResolveOpenAIImageSizeRecognizesAspectAndResolution(t *testing.T) {
	landscape := []imageBytes{{data: sizedPNG(t, 1600, 1200), mime: "image/png"}}
	tests := []struct {
		name       string
		model      string
		prompt     string
		configured string
		inputs     []imageBytes
		want       string
	}{
		{name: "generation defaults to 2K square", model: "gpt-image-2", want: "2048x2048"},
		{name: "clock time is not treated as aspect ratio", model: "gpt-image-2", prompt: "让画面里的钟显示 12:30", want: "2048x2048"},
		{name: "ratio defaults to 2K", model: "gpt-image-2", prompt: "生成一张 16:9 横版海报", want: "2048x1152"},
		{name: "portrait ratio defaults to 2K", model: "gpt-image-2", prompt: "做成9：16竖版", want: "1152x2048"},
		{name: "Chinese ratio separator", model: "gpt-image-2", prompt: "宽高比4比3", want: "2048x1536"},
		{name: "explicit 1K tier", model: "gpt-image-2", prompt: "1K 正方形", want: "1024x1024"},
		{name: "explicit 2K tier", model: "gpt-image-2", prompt: "2K 正方形", want: "2048x2048"},
		{name: "explicit 4K", model: "gpt-image-2", prompt: "16:9，4K 超清", want: "3840x2160"},
		{name: "unsupported 8K clamps to 4K tier", model: "gpt-image-2", prompt: "16:9，8K", want: "3840x2160"},
		{name: "4K square respects pixel cap", model: "gpt-image-2", prompt: "4K 正方形", want: "2880x2880"},
		{name: "exact pixels map to supported 2K tier", model: "gpt-image-2", prompt: "输出 1920x1080 像素", want: "2048x1152"},
		{name: "1080p is not a separate resolution tier", model: "gpt-image-2", prompt: "16:9 1080p", want: "2048x1152"},
		{name: "1440p is not a separate resolution tier", model: "gpt-image-2", prompt: "16:9 1440p", want: "2048x1152"},
		{name: "edit inherits base ratio at 2K", model: "gpt-image-2", inputs: landscape, want: "2048x1536"},
		{name: "configured size remains when prompt has no sizing", model: "gpt-image-2", configured: "1024x1536", want: "1024x1536"},
		{name: "prompt ratio overrides configured size and uses 2K", model: "gpt-image-2", prompt: "改为 16:9", configured: "1024x1536", want: "2048x1152"},
		{name: "older GPT Image maps to supported landscape size", model: "gpt-image-1.5", prompt: "16:9 4K", want: "1536x1024"},
		{name: "DALL-E 3 maps to supported landscape size", model: "dall-e-3", prompt: "16:9 4K", want: "1792x1024"},
		{name: "OpenAI-compatible alias receives 2K", model: "provider-image-alias", prompt: "3:4", want: "1536x2048"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveOpenAIImageSize(tt.model, tt.prompt, tt.configured, tt.inputs); got != tt.want {
				t.Fatalf("resolveOpenAIImageSize() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenAIImageGenerationDefaultsTo2K(t *testing.T) {
	var captured map[string]any
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"aW1hZ2U="}]}`), nil
	})

	images, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"gpt-image-2",
		imgInput{Prompt: "server prompt", N: 1},
		nil,
		nil,
	)
	if err != nil || len(images) != 1 {
		t.Fatalf("openaiGenerateImages: images=%d err=%v", len(images), err)
	}
	if got := captured["size"]; got != "2048x2048" {
		t.Fatalf("default generation size = %#v, want 2048x2048", got)
	}
}

func TestOpenAIImageGenerationUsesPromptAspectAndClarity(t *testing.T) {
	var captured map[string]any
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"aW1hZ2U="}]}`), nil
	})

	_, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"gpt-image-2",
		imgInput{Prompt: "optimized prompt", UserPrompt: "生成一张 16:9、4K 的产品海报", N: 1},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("openaiGenerateImages: %v", err)
	}
	if got := captured["size"]; got != "3840x2160" {
		t.Fatalf("prompt-derived size = %#v, want 3840x2160", got)
	}
}

func TestOpenAIImageGenerationUsesHighQualityForKnownModels(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		params    map[string]any
		want      string
		wantField bool
	}{
		{name: "gpt image 2", model: "gpt-image-2", want: "high", wantField: true},
		{name: "gpt image snapshot", model: "gpt-image-1.5-2026-04-21", want: "high", wantField: true},
		{name: "dall-e 3", model: "dall-e-3", want: "hd", wantField: true},
		{name: "admin override", model: "gpt-image-2", params: map[string]any{"quality": "low"}, want: "low", wantField: true},
		{name: "unknown compatible model", model: "provider-image-alias", wantField: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured map[string]any
			useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
				if err := json.NewDecoder(req.Body).Decode(&captured); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				return imageSuccessResponse(`{"data":[{"b64_json":"aW1hZ2U="}]}`), nil
			})

			if _, err := openaiGenerateImages(
				context.Background(),
				"https://images.example.test",
				"server-secret",
				tt.model,
				imgInput{Prompt: "server prompt", N: 1},
				nil,
				tt.params,
			); err != nil {
				t.Fatalf("openaiGenerateImages: %v", err)
			}
			got, exists := captured["quality"]
			if exists != tt.wantField || (tt.wantField && got != tt.want) {
				t.Fatalf("quality = %#v (exists=%v), want %q (exists=%v)", got, exists, tt.want, tt.wantField)
			}
		})
	}
}

func TestOpenAIImageEditInfersSourceRatio(t *testing.T) {
	inputData := sizedPNG(t, 1920, 1080)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := req.FormValue("size"); got != "2048x1152" {
			t.Fatalf("inferred multipart size = %q, want 2048x1152", got)
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
		if err != nil || !bytes.Equal(gotImage, inputData) {
			t.Fatalf("multipart image changed, err=%v", err)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"ZWRpdGVk"}]}`), nil
	})

	images, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"gpt-image-2",
		imgInput{Prompt: "edit prompt", N: 1},
		[]imageBytes{{data: inputData, mime: "image/png"}},
		nil,
	)
	if err != nil || len(images) != 1 || string(images[0].data) != "edited" {
		t.Fatalf("openaiGenerateImages: images=%#v err=%v", images, err)
	}
}

func TestOpenAIImageEditExplicitSizeOverridesInference(t *testing.T) {
	inputData := sizedPNG(t, 1920, 1080)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := req.FormValue("size"); got != "1024x1536" {
			t.Fatalf("explicit multipart size = %q, want 1024x1536", got)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"ZWRpdGVk"}]}`), nil
	})

	_, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"gpt-image-2",
		imgInput{Prompt: "edit prompt", N: 1},
		[]imageBytes{{data: inputData, mime: "image/png"}},
		map[string]any{"size": "1024x1536"},
	)
	if err != nil {
		t.Fatalf("openaiGenerateImages: %v", err)
	}
}

func TestOpenAIImageEditSendsEveryGPTImageReference(t *testing.T) {
	first := sizedPNG(t, 1600, 900)
	second := sizedPNG(t, 800, 800)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(8 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		files := req.MultipartForm.File["image[]"]
		if len(files) != 2 {
			t.Fatalf("multipart image[] count = %d, want 2", len(files))
		}
		for i, want := range [][]byte{first, second} {
			file, err := files[i].Open()
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(file)
			_ = file.Close()
			if readErr != nil || !bytes.Equal(got, want) {
				t.Fatalf("multipart image[%d] changed, err=%v", i, readErr)
			}
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"ZWRpdGVk"}]}`), nil
	})

	_, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"gpt-image-2",
		imgInput{Prompt: "combine the references", N: 1},
		[]imageBytes{{data: first, mime: "image/png"}, {data: second, mime: "image/png"}},
		nil,
	)
	if err != nil {
		t.Fatalf("openaiGenerateImages: %v", err)
	}
}

func TestOpenAIImage1EditDefaultsToHighInputFidelity(t *testing.T) {
	inputData := sizedPNG(t, 1200, 800)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := req.FormValue("input_fidelity"); got != "high" {
			t.Fatalf("input_fidelity = %q, want high", got)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"ZWRpdGVk"}]}`), nil
	})

	_, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"gpt-image-1.5",
		imgInput{Prompt: "small local edit", N: 1},
		[]imageBytes{{data: inputData, mime: "image/png"}},
		nil,
	)
	if err != nil {
		t.Fatalf("openaiGenerateImages: %v", err)
	}
}

func TestOpenAIImageEditUnknownCompatibleModelDefaultsTo2K(t *testing.T) {
	inputData := sizedPNG(t, 1920, 1080)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := req.FormValue("size"); got != "2048x1152" {
			t.Fatalf("compatible model size = %q, want 2048x1152", got)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"ZWRpdGVk"}]}`), nil
	})

	_, err := openaiGenerateImages(
		context.Background(),
		"https://images.example.test",
		"server-secret",
		"provider-image-alias",
		imgInput{Prompt: "edit prompt", N: 1},
		[]imageBytes{{data: inputData, mime: "image/png"}},
		nil,
	)
	if err != nil {
		t.Fatalf("openaiGenerateImages: %v", err)
	}
}
