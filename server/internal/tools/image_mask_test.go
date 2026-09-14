package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func testMaskPNG(t *testing.T, width, height int, selected bool) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	if selected {
		canvas.SetNRGBA(0, 0, color.NRGBA{})
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestPrepareImageMaskPreservesGeometryAndAlpha(t *testing.T) {
	base := imageBytes{data: sizedJPEG(t, 80, 40), mime: "image/jpeg"}
	mask := imageBytes{data: testMaskPNG(t, 80, 40, true), mime: "image/png"}
	converted, gotMask, err := prepareImageMask(base, mask)
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(converted.data))
	if err != nil || format != "png" || converted.mime != "image/png" || config.Width != 80 || config.Height != 40 {
		t.Fatalf("invalid normalized source: %v %s %v", config, format, err)
	}
	if !bytes.Equal(mask.data, gotMask.data) {
		t.Fatal("mask bytes were altered")
	}
	decoded, err := png.Decode(bytes.NewReader(gotMask.data))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, selected := decoded.At(0, 0).RGBA()
	_, _, _, untouched := decoded.At(1, 0).RGBA()
	if selected != 0 || untouched != 65535 {
		t.Fatalf("incorrect mask polarity: %d %d", selected, untouched)
	}
	for name, invalid := range map[string]imageBytes{
		"wrong dimensions": {data: testMaskPNG(t, 40, 40, true), mime: "image/png"},
		"opaque":           {data: testMaskPNG(t, 80, 40, false), mime: "image/png"},
		"jpeg":             {data: sizedJPEG(t, 80, 40), mime: "image/jpeg"},
		"corrupt":          {data: []byte("invalid"), mime: "image/png"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := prepareImageMask(base, invalid); err == nil {
				t.Fatal("invalid mask accepted")
			}
		})
	}
}

func TestMaskEditSendsExactSelectedArtifactAndSeparateMask(t *testing.T) {
	tool, convID, _, _, _ := seedImageBaseSelectionWorkflow(t)
	ctx := context.Background()
	// Select a different artifact in the same reply, not FirstImageArtifactForMessage.
	base := sizedJPEG(t, 80, 40)
	basePath := filepath.Join(tool.artifactDir, "chosen.jpg")
	if err := os.WriteFile(basePath, base, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateArtifact(ctx, tool.db, store.Artifact{ID: "art_chosen", MessageID: "a_prior", Filename: "chosen.jpg", StoragePath: basePath, MimeType: "image/jpeg", SizeBytes: int64(len(base)), Source: store.ArtifactSourceImageGenerate}); err != nil {
		t.Fatal(err)
	}
	mask := testMaskPNG(t, 80, 40, true)
	maskPath := filepath.Join(tool.uploadDir, "mask.png")
	if err := os.WriteFile(maskPath, mask, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFile(ctx, tool.db, store.File{ID: "mask", UserID: "u_flow", ConversationID: convID, Filename: "mask.png", StoragePath: maskPath, MimeType: "image/png", Kind: "image", SizeBytes: int64(len(mask)), BranchMessageID: "a_prior"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	useImageTestHTTPClient(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/v1/images/edits" {
			t.Fatalf("unexpected endpoint %s", req.URL.Path)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer req.MultipartForm.RemoveAll()
		if len(req.MultipartForm.File["image"]) != 1 || len(req.MultipartForm.File["mask"]) != 1 || len(req.MultipartForm.File["image[]"]) != 0 {
			t.Fatalf("unexpected parts: %v", req.MultipartForm.File)
		}
		file, _, err := req.FormFile("mask")
		if err != nil {
			t.Fatal(err)
		}
		gotMask, _ := io.ReadAll(file)
		file.Close()
		if !bytes.Equal(gotMask, mask) {
			t.Fatal("mask did not reach provider unchanged")
		}
		file, _, err = req.FormFile("image")
		if err != nil {
			t.Fatal(err)
		}
		config, format, err := image.DecodeConfig(file)
		file.Close()
		if err != nil || format != "png" || config.Width != 80 || config.Height != 40 {
			t.Fatalf("wrong edit base: %v %s %v", config, format, err)
		}
		return imageSuccessResponse(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(testMaskPNG(t, 80, 40, false)) + `"}]}`), nil
	})
	tc := &llm.ToolContext{UserID: "u_flow", ConvID: convID, MessageID: "a_edit", ImageModelID: "m_flow", DB: tool.db, ImageUserPrompt: "replace the selected area", ImageEdit: &llm.ImageEditRequest{BaseArtifactID: "art_chosen", MaskFileID: "mask"}}
	input := []byte(`{"prompt":"replace the selected area","action":"edit","base_image":"previous_generation"}`)
	if _, _, err := tool.Execute(ctx, input, tc); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	// Invalid inputs must not reach the provider.
	for name, query := range map[string]string{
		"foreign mask":         `UPDATE files SET conversation_id=NULL WHERE id='mask'`,
		"unsupported provider": `UPDATE channels SET type='google' WHERE id='ch_flow'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if _, _, err := tool.Execute(ctx, input, tc); err == nil {
				t.Fatal("invalid mask edit accepted")
			}
		})
	}
	if calls != 1 {
		t.Fatal("invalid request reached provider")
	}
	var parsed map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["properties"].(map[string]any)["mask"]; ok {
		t.Fatal("model-facing schema exposes server-owned mask")
	}
}
