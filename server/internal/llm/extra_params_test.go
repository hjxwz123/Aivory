package llm

import (
	"encoding/json"
	"testing"
)

func TestMergeRequestParamsPrecedenceAndNoAliasing(t *testing.T) {
	nativeMetadata := map[string]any{"source": "native"}
	native := map[string]any{
		"model":    "native-model",
		"stream":   true,
		"metadata": nativeMetadata,
		"generationConfig": map[string]any{
			"maxOutputTokens": 4096,
			"candidateCount":  1,
		},
	}
	extra := json.RawMessage(`{
		"model":"extra-model",
		"stream":false,
		"vendor_only":"kept",
		"generationConfig":{"maxOutputTokens":12,"temperature":0.9,"topP":0.2}
	}`)
	controls := json.RawMessage(`[
		{
			"key":"mode",
			"type":"select",
			"options":[{"value":"selected"}],
			"map":{"selected":{"model":"control-model","stream":false,"generationConfig":{"maxOutputTokens":64,"temperature":0.5,"topK":40}}}
		}
	]`)

	got := MergeRequestParams(native, extra, controls, map[string]any{"mode": "selected"})
	if got["model"] != "native-model" {
		t.Fatalf("native model lost precedence: %#v", got["model"])
	}
	if got["stream"] != true {
		t.Fatalf("native stream lost precedence: %#v", got["stream"])
	}
	if got["vendor_only"] != "kept" {
		t.Fatalf("extra-only field missing: %#v", got["vendor_only"])
	}
	gc, ok := got["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig = %#v, want object", got["generationConfig"])
	}
	if gc["maxOutputTokens"] != 4096 {
		t.Fatalf("native nested maxOutputTokens lost precedence: %#v", gc["maxOutputTokens"])
	}
	if gc["candidateCount"] != 1 {
		t.Fatalf("native nested candidateCount missing: %#v", gc["candidateCount"])
	}
	if gc["temperature"] != 0.5 {
		t.Fatalf("param-control temperature should override extra: %#v", gc["temperature"])
	}
	if gc["topK"] != float64(40) {
		t.Fatalf("param-control topK missing: %#v", gc["topK"])
	}
	if gc["topP"] != 0.2 {
		t.Fatalf("extra nested topP missing: %#v", gc["topP"])
	}

	metadata, ok := got["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v, want object", got["metadata"])
	}
	metadata["source"] = "mutated"
	if nativeMetadata["source"] != "native" {
		t.Fatalf("merged body aliased native nested map: %#v", nativeMetadata)
	}
}
