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
		{name: "landscape 16:9", width: 1920, height: 1080, want: "1280x720", wantRatio: 16.0 / 9.0},
		{name: "portrait 9:16", width: 1080, height: 1920, want: "720x1280", wantRatio: 9.0 / 16.0},
		{name: "standard 4:3", width: 1600, height: 1200, want: "1152x864", wantRatio: 4.0 / 3.0},
		{name: "extreme ratio clamps to 3:1", width: 5000, height: 1000, want: "1776x592", wantRatio: 3},
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
	if got := inferredOpenAIEditSize("gpt-image-2-2026-04-21", landscape); got != "1280x720" {
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

func TestOpenAIImageGenerationOmitsUnconfiguredSize(t *testing.T) {
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
	if _, exists := captured["size"]; exists {
		t.Fatalf("unconfigured generation forced size upstream: %#v", captured)
	}
}

func TestOpenAIImageEditInfersSourceRatio(t *testing.T) {
	inputData := sizedPNG(t, 1920, 1080)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := req.FormValue("size"); got != "1280x720" {
			t.Fatalf("inferred multipart size = %q, want 1280x720", got)
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

func TestOpenAIImageEditUnknownModelLetsProviderChoose(t *testing.T) {
	inputData := sizedPNG(t, 1920, 1080)
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		if err := req.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := req.FormValue("size"); got != "" {
			t.Fatalf("unknown model received inferred size %q", got)
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
