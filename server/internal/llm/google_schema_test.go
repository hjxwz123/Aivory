package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

var unsupportedGeminiSchemaKeys = map[string]bool{
	"$schema": true, "$id": true, "$anchor": true, "$comment": true,
	"$defs": true, "definitions": true, "$ref": true, "$dynamicRef": true,
	"additionalProperties": true, "patternProperties": true, "unevaluatedProperties": true,
	"oneOf": true, "allOf": true, "const": true, "examples": true,
	"not": true, "if": true, "then": true, "else": true,
	"exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true,
	"uniqueItems": true, "contains": true, "prefixItems": true,
}

func TestNormalizeGeminiFunctionSchema(t *testing.T) {
	raw := json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://example.test/train-tool.schema.json",
  "$defs": {
    "Station": {
      "type": ["string", "null"],
      "minLength": 1,
      "maxLength": 12,
      "additionalProperties": false,
      "examples": ["BJP"]
    },
    "Options": {
      "allOf": [
        {
          "type": "object",
          "properties": {"date": {"type": "string", "format": "date"}},
          "required": ["date"]
        },
        {
          "type": "object",
          "properties": {
            "kind": {"oneOf": [{"const": "G"}, {"const": "D"}]}
          },
          "required": ["kind"]
        }
      ]
    },
    "BaseQuery": {
      "type": "object",
      "properties": {"query": {"type": "string", "minLength": 2}},
      "required": ["query"]
    }
  },
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "station": {"$ref": "#/$defs/Station"},
    "options": {"$ref": "#/$defs/Options"},
    "tags": {
      "type": "array",
      "minItems": 1,
      "maxItems": 3,
      "items": {"type": "string"}
    },
    "dynamic": {
      "type": "object",
      "additionalProperties": {"type": "integer", "minimum": 0}
    },
    "choice": {"type": ["string", "integer", "null"]},
    "count": {"type": "integer", "enum": [1, 2]},
    "tuple": {"type": "array", "items": [{"type": "string"}, {"type": "integer"}]},
    "external": {"$ref": "https://example.test/external.schema.json"},
    "merged": {
      "allOf": [{"$ref": "#/$defs/BaseQuery"}],
      "properties": {
        "query": {"description": "User search text"},
        "limit": {"type": "integer"}
      },
      "required": ["limit"]
    },
    "crossRequired": {
      "allOf": [
        {"type": "object", "required": ["cross"]},
        {"type": "object", "properties": {"cross": {"type": "boolean"}}}
      ]
    },
    "numericEnum": {"enum": [1, 2]},
    "boolConst": {"const": true},
    "unionEnum": {"type": ["string", "integer"], "enum": ["auto"]},
    "modernTuple": {
      "type": "array",
      "prefixItems": [{"type": "string"}, {"type": "integer"}],
      "items": false
    }
  },
  "required": ["station", "options", "missing"],
  "propertyOrdering": ["options", "station", "missing"]
}`)
	original := append(json.RawMessage(nil), raw...)

	normalized := normalizeGeminiFunctionSchema(raw)
	assertNoUnsupportedGeminiSchemaKeys(t, normalized)
	if string(raw) != string(original) {
		t.Fatal("normalization mutated the provider-neutral source schema")
	}
	if normalized["type"] != "object" {
		t.Fatalf("root type = %#v, want object", normalized["type"])
	}

	properties := mustSchemaMap(t, normalized["properties"])
	station := mustSchemaMap(t, properties["station"])
	if station["type"] != "string" || station["nullable"] != true {
		t.Fatalf("nullable ref was not normalized: %#v", station)
	}
	if station["minLength"] != "1" || station["maxLength"] != "12" || station["example"] != "BJP" {
		t.Fatalf("station constraints were not retained in Gemini wire form: %#v", station)
	}

	options := mustSchemaMap(t, properties["options"])
	optionProperties := mustSchemaMap(t, options["properties"])
	if _, ok := optionProperties["date"]; !ok {
		t.Fatalf("allOf date property was lost: %#v", options)
	}
	kind := mustSchemaMap(t, optionProperties["kind"])
	if branches, ok := kind["anyOf"].([]any); !ok || len(branches) != 2 {
		t.Fatalf("oneOf was not converted to anyOf: %#v", kind)
	}
	assertStringSet(t, options["required"], "date", "kind")

	tags := mustSchemaMap(t, properties["tags"])
	if tags["minItems"] != "1" || tags["maxItems"] != "3" {
		t.Fatalf("array int64 constraints were not string encoded: %#v", tags)
	}
	if mustSchemaMap(t, tags["items"])["type"] != "string" {
		t.Fatalf("array item schema was lost: %#v", tags)
	}

	dynamic := mustSchemaMap(t, properties["dynamic"])
	if description, _ := dynamic["description"].(string); !strings.Contains(description, "Additional object-property values") {
		t.Fatalf("dynamic object constraint was not preserved as guidance: %#v", dynamic)
	}
	choice := mustSchemaMap(t, properties["choice"])
	if choice["nullable"] != true {
		t.Fatalf("nullable union lost nullability: %#v", choice)
	}
	if branches, ok := choice["anyOf"].([]any); !ok || len(branches) != 2 {
		t.Fatalf("multi-type union was not converted to anyOf: %#v", choice)
	}
	count := mustSchemaMap(t, properties["count"])
	if _, exists := count["enum"]; exists {
		t.Fatalf("non-string enum would be rejected by Gemini: %#v", count)
	}
	if description, _ := count["description"].(string); !strings.Contains(description, "Allowed values") {
		t.Fatalf("non-string enum guidance was dropped: %#v", count)
	}
	tuple := mustSchemaMap(t, properties["tuple"])
	if branches, ok := mustSchemaMap(t, tuple["items"])["anyOf"].([]any); !ok || len(branches) != 2 {
		t.Fatalf("tuple items were not conservatively normalized: %#v", tuple)
	}
	external := mustSchemaMap(t, properties["external"])
	if external["type"] != "string" {
		t.Fatalf("external ref did not receive a safe type fallback: %#v", external)
	}

	merged := mustSchemaMap(t, properties["merged"])
	mergedProperties := mustSchemaMap(t, merged["properties"])
	query := mustSchemaMap(t, mergedProperties["query"])
	if query["type"] != "string" || query["minLength"] != "2" || query["description"] != "User search text" {
		t.Fatalf("allOf/ref property constraints were overwritten: %#v", merged)
	}
	if mustSchemaMap(t, mergedProperties["limit"])["type"] != "integer" {
		t.Fatalf("sibling property was lost while merging allOf: %#v", merged)
	}
	assertStringSet(t, merged["required"], "query", "limit")
	crossRequired := mustSchemaMap(t, properties["crossRequired"])
	assertStringSet(t, crossRequired["required"], "cross")

	numericEnum := mustSchemaMap(t, properties["numericEnum"])
	if numericEnum["type"] != "integer" {
		t.Fatalf("numeric enum type was not inferred: %#v", numericEnum)
	}
	if _, exists := numericEnum["enum"]; exists {
		t.Fatalf("numeric enum would be invalid in Gemini's string enum field: %#v", numericEnum)
	}
	boolConst := mustSchemaMap(t, properties["boolConst"])
	if boolConst["type"] != "boolean" {
		t.Fatalf("boolean const type was not inferred: %#v", boolConst)
	}
	unionEnum := mustSchemaMap(t, properties["unionEnum"])
	if unionEnum["type"] != "string" {
		t.Fatalf("string enum did not narrow its union type: %#v", unionEnum)
	}
	assertStringSet(t, unionEnum["enum"], "auto")
	if _, exists := unionEnum["anyOf"]; exists {
		t.Fatalf("narrowed enum retained a conflicting anyOf: %#v", unionEnum)
	}

	modernTuple := mustSchemaMap(t, properties["modernTuple"])
	modernItems := mustSchemaMap(t, modernTuple["items"])
	if branches, ok := modernItems["anyOf"].([]any); !ok || len(branches) != 2 {
		t.Fatalf("prefixItems tuple was not converted to item alternatives: %#v", modernTuple)
	}
	if description, _ := modernTuple["description"].(string); !strings.Contains(description, "Tuple item positions") {
		t.Fatalf("tuple ordering guidance was lost: %#v", modernTuple)
	}

	assertStringSet(t, normalized["required"], "station", "options")
	assertStringSet(t, normalized["propertyOrdering"], "options", "station")
}

func TestNormalizeGeminiFunctionSchemaFallbacks(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{`), json.RawMessage(`[]`)} {
		normalized := normalizeGeminiFunctionSchema(raw)
		if normalized["type"] != "object" {
			t.Fatalf("invalid schema fallback type = %#v", normalized["type"])
		}
		if properties := mustSchemaMap(t, normalized["properties"]); len(properties) != 0 {
			t.Fatalf("invalid schema fallback properties = %#v", properties)
		}
	}

	cyclic := normalizeGeminiFunctionSchema(json.RawMessage(`{
  "type":"object",
  "$defs":{"Loop":{"$ref":"#/$defs/Loop"}},
  "properties":{"loop":{"$ref":"#/$defs/Loop"}}
}`))
	loop := mustSchemaMap(t, mustSchemaMap(t, cyclic["properties"])["loop"])
	if loop["type"] != "string" {
		t.Fatalf("cyclic ref did not terminate with a safe schema: %#v", loop)
	}

	encodedRef := normalizeGeminiFunctionSchema(json.RawMessage(`{
  "type":"object",
  "$defs":{"A B":{"anyOf":[{"type":"boolean"}]}},
  "properties":{"flag":{"$ref":"#/$defs/A%20B/anyOf/0"}}
}`))
	flag := mustSchemaMap(t, mustSchemaMap(t, encodedRef["properties"])["flag"])
	if flag["type"] != "boolean" {
		t.Fatalf("encoded ref with an array index was not resolved: %#v", flag)
	}
}

func TestNormalizeGeminiFunctionSchemaComplexityBudget(t *testing.T) {
	definitions := map[string]any{
		"Level0": map[string]any{"type": "string"},
	}
	for index := 1; index <= 20; index++ {
		previous := "#/$defs/Level" + strconv.Itoa(index-1)
		definitions["Level"+strconv.Itoa(index)] = map[string]any{
			"allOf": []any{map[string]any{"$ref": previous}, map[string]any{"$ref": previous}},
		}
	}
	raw, err := json.Marshal(map[string]any{
		"type":       "object",
		"$defs":      definitions,
		"properties": map[string]any{"value": map[string]any{"$ref": "#/$defs/Level20"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	normalized := normalizeGeminiFunctionSchema(raw)
	assertNoUnsupportedGeminiSchemaKeys(t, normalized)
	value := mustSchemaMap(t, mustSchemaMap(t, normalized["properties"])["value"])
	if !schemaDescriptionContains(value, "complexity limit") {
		t.Fatalf("schema expansion did not expose its bounded fallback: %#v", value)
	}
}

func TestGeminiInt64String(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "0", want: "0", ok: true},
		{input: "1.0", want: "1", ok: true},
		{input: "1e3", want: "1000", ok: true},
		{input: "9223372036854775807", want: "9223372036854775807", ok: true},
		{input: "9223372036854775808", ok: false},
		{input: "1.5", ok: false},
		{input: "-1", ok: false},
	}
	for _, test := range tests {
		got, ok := geminiInt64String(json.Number(test.input))
		if ok != test.ok || got != test.want {
			t.Fatalf("geminiInt64String(%q) = %q, %v; want %q, %v", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestGoogleProviderNormalizesFunctionSchemaOnWire(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}` + "\n\n"))
	}))
	defer srv.Close()

	schema := json.RawMessage(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "additionalProperties":false,
  "properties":{"query":{"type":["string","null"],"minLength":1}},
  "required":["query"]
}`)
	original := append(json.RawMessage(nil), schema...)
	provider := &GoogleProvider{}
	_, err := provider.Stream(context.Background(), UnifiedChatRequest{
		SystemPrompt: "sys",
		Model:        ModelInfo{RequestID: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k"},
		History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hi"}}}},
		Tools:        []ToolDef{{Name: "mcp_train_lookup", Description: "lookup", InputSchema: schema}},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if string(schema) != string(original) {
		t.Fatal("Google request construction mutated the shared tool schema")
	}

	var body struct {
		Tools []struct {
			FunctionDeclarations []struct {
				Parameters map[string]any `json:"parameters"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("decode captured request: %v\nbody: %s", err, captured)
	}
	if len(body.Tools) != 1 || len(body.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("unexpected function declarations: %s", captured)
	}
	parameters := body.Tools[0].FunctionDeclarations[0].Parameters
	assertNoUnsupportedGeminiSchemaKeys(t, parameters)
	query := mustSchemaMap(t, mustSchemaMap(t, parameters["properties"])["query"])
	if query["type"] != "string" || query["nullable"] != true || query["minLength"] != "1" {
		t.Fatalf("wire schema was not normalized: %#v", query)
	}
}

func assertNoUnsupportedGeminiSchemaKeys(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if unsupportedGeminiSchemaKeys[key] {
				t.Fatalf("unsupported Gemini schema key %q survived in %#v", key, typed)
			}
			assertNoUnsupportedGeminiSchemaKeys(t, item)
		}
	case []any:
		for _, item := range typed {
			assertNoUnsupportedGeminiSchemaKeys(t, item)
		}
	}
}

func schemaDescriptionContains(value any, needle string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if description, _ := typed["description"].(string); strings.Contains(description, needle) {
			return true
		}
		for _, item := range typed {
			if schemaDescriptionContains(item, needle) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if schemaDescriptionContains(item, needle) {
				return true
			}
		}
	}
	return false
}

func mustSchemaMap(t *testing.T, value any) map[string]any {
	t.Helper()
	schema, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value is not a schema object: %#v", value)
	}
	return schema
}

func assertStringSet(t *testing.T, value any, expected ...string) {
	t.Helper()
	actual, ok := value.([]string)
	if !ok {
		t.Fatalf("value is not []string: %#v", value)
	}
	if len(actual) != len(expected) {
		t.Fatalf("strings = %#v, want %#v", actual, expected)
	}
	seen := make(map[string]bool, len(actual))
	for _, item := range actual {
		seen[item] = true
	}
	for _, item := range expected {
		if !seen[item] {
			t.Fatalf("strings = %#v, missing %q", actual, item)
		}
	}
}
