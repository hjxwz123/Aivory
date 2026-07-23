package llm

import (
	"encoding/json"
	"testing"
)

func TestImageParamControlsAllowlistAndQuantityClamp(t *testing.T) {
	controls := json.RawMessage(`[
		{
			"key":"render",
			"type":"select",
			"default":"standard",
			"options":[{"value":"standard"},{"value":"studio"}],
			"map":{
				"standard":{"quality":"standard","size":"1024x1024"},
				"studio":{"quality":"high","size":"1536x1024","n":99}
			}
		},
		{
			"key":"aspect",
			"type":"select",
			"options":[{"value":"wide"}],
			"map":{"wide":{"generationConfig":{"imageConfig":{"aspectRatio":"16:9"}}}}
		}
	]`)

	params := MergeParamControls(nil, controls, map[string]any{
		"render":  "studio",
		"aspect":  "wide",
		"unknown": "untrusted",
	})
	if params["quality"] != "high" || params["size"] != "1536x1024" {
		t.Fatalf("selected OpenAI mappings missing: %#v", params)
	}
	generationConfig, ok := params["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig = %#v, want object", params["generationConfig"])
	}
	imageConfig, ok := generationConfig["imageConfig"].(map[string]any)
	if !ok || imageConfig["aspectRatio"] != "16:9" {
		t.Fatalf("Gemini aspect mapping missing: %#v", generationConfig)
	}
	if _, exists := params["unknown"]; exists {
		t.Fatalf("unknown request key reached mapped params: %#v", params)
	}
	if got, want := imageGenerationCountFromParams(params), ClampImageGenerationCount(99); got != want {
		t.Fatalf("mapped generation count = %d, want capped %d", got, want)
	}
}

func TestImageParamControlsApplyDefaultsAndDropUnknownValues(t *testing.T) {
	controls := json.RawMessage(`[
		{
			"key":"quality",
			"type":"select",
			"default":"high",
			"options":[{"value":"standard"},{"value":"high"}],
			"map":{"standard":{"quality":"standard"},"high":{"quality":"high"}}
		}
	]`)

	if params := MergeParamControls(nil, controls, nil); params["quality"] != "high" {
		t.Fatalf("server did not apply the declared default: %#v", params)
	}
	if got := imageGenerationCountFromParams(nil); got != 1 {
		t.Fatalf("default generation count = %d, want 1", got)
	}
	if params := MergeParamControls(nil, controls, map[string]any{"quality": "ultra"}); len(params) != 0 {
		t.Fatalf("unknown option reached mapped params: %#v", params)
	}
	if params := MergeParamControls(nil, controls, map[string]any{"quality": "standard"}); params["quality"] != "standard" {
		t.Fatalf("explicit selection did not override the default: %#v", params)
	}
}

func TestParamControlDefaultsRespectShowIf(t *testing.T) {
	controls := json.RawMessage(`[
		{
			"key":"mode",
			"type":"select",
			"default":"basic",
			"options":[{"value":"basic"},{"value":"pro"}],
			"map":{"basic":{"mode":"basic"},"pro":{"mode":"pro"}}
		},
		{
			"key":"quality",
			"type":"select",
			"default":"high",
			"show_if":{"mode":"pro"},
			"options":[{"value":"high"}],
			"map":{"high":{"quality":"high"}}
		}
	]`)

	params := MergeParamControls(nil, controls, nil)
	if params["mode"] != "basic" {
		t.Fatalf("base default missing: %#v", params)
	}
	if _, exists := params["quality"]; exists {
		t.Fatalf("hidden dependent default was applied: %#v", params)
	}

	params = MergeParamControls(nil, controls, map[string]any{"mode": "pro"})
	if params["mode"] != "pro" || params["quality"] != "high" {
		t.Fatalf("visible dependent default missing: %#v", params)
	}
}

func TestClampImageGenerationCountUsesOneAsSafeMinimum(t *testing.T) {
	for _, input := range []int{-10, 0, 1, 100} {
		if got := clampImageGenerationCount(input, 0); got != 1 {
			t.Fatalf("clampImageGenerationCount(%d, 0) = %d, want 1", input, got)
		}
	}
}
