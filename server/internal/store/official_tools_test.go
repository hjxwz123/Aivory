package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeOfficialToolsSupportsLegacyAndCustomDefinitions(t *testing.T) {
	t.Setenv("AIVORY_LLM_OFFICIAL_TOOL_SPEC", "medium")
	normalized, err := NormalizeOfficialTools(json.RawMessage(`[
		"web_search",
		"vendor_lookup",
		{"name":" maps ","icon":" map ","request":{"type":"maps","options":{"lang":"en"}}}
	]`))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	definitions, err := ParseOfficialTools(normalized)
	if err != nil {
		t.Fatalf("parse normalized: %v", err)
	}
	if len(definitions) != 3 {
		t.Fatalf("definitions = %+v, want 3", definitions)
	}
	if definitions[0].Name != "web_search" || definitions[0].Icon != "search" || !strings.Contains(string(definitions[0].Request), `"search_context_size":"medium"`) {
		t.Fatalf("legacy OpenAI default not expanded: %+v", definitions[0])
	}
	if definitions[1].Name != "vendor_lookup" || definitions[1].Icon != "wrench" || string(definitions[1].Request) != `{"tools":[{"type":"vendor_lookup"}]}` {
		t.Fatalf("generic legacy tool not expanded: %+v", definitions[1])
	}
	if definitions[2].Name != "maps" || definitions[2].Icon != "map" || !strings.Contains(string(definitions[2].Request), `"lang":"en"`) {
		t.Fatalf("custom definition not normalized: %+v", definitions[2])
	}
}

func TestDefaultOpenAIResponsesOfficialToolsPreservesWebSearchContextEnv(t *testing.T) {
	t.Setenv("AIVORY_LLM_OFFICIAL_TOOL_SPEC", "high")

	definitions := DefaultOpenAIResponsesOfficialTools()
	if len(definitions) != 3 {
		t.Fatalf("default definitions = %d, want 3", len(definitions))
	}
	var request struct {
		Tools []struct {
			Type              string `json:"type"`
			SearchContextSize string `json:"search_context_size"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(definitions[0].Request, &request); err != nil {
		t.Fatalf("decode web search request: %v", err)
	}
	if len(request.Tools) != 1 || request.Tools[0].Type != "web_search" || request.Tools[0].SearchContextSize != "high" {
		t.Fatalf("web search request = %#v, want context size high", request.Tools)
	}
}

func TestNormalizeOfficialToolsRejectsInvalidDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "null", raw: `null`},
		{name: "object instead of array", raw: `{}`},
		{name: "empty legacy name", raw: `[" "]`},
		{name: "missing name", raw: `[{"icon":"search","request":{"type":"search"}}]`},
		{name: "missing request", raw: `[{"name":"search","icon":"search"}]`},
		{name: "request array", raw: `[{"name":"search","icon":"search","request":[]}]`},
		{name: "unknown field", raw: `[{"name":"search","icon":"search","request":{},"enabled":true}]`},
		{name: "duplicate", raw: `["search",{"name":"search","icon":"search","request":{}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeOfficialTools(json.RawMessage(tc.raw)); err == nil {
				t.Fatalf("NormalizeOfficialTools(%s) unexpectedly succeeded", tc.raw)
			}
		})
	}
}

func TestMigrateOfficialToolsUpgradesLegacyRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "official-tools.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO channels(id,name,type,api_format) VALUES('ch1','OpenAI','openai','responses')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,channel_id,kind,request_id,label,official_tools) VALUES('m1','ch1','chat','gpt-test','Test','["web_search","code_interpreter"]')`); err != nil {
		t.Fatalf("insert legacy model: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("upgrade migrate: %v", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT official_tools FROM models WHERE id='m1'`).Scan(&raw); err != nil {
		t.Fatalf("read migrated value: %v", err)
	}
	if strings.Contains(raw, `"web_search"`) && !strings.Contains(raw, `"name":"web_search"`) {
		t.Fatalf("legacy array was not upgraded: %s", raw)
	}
	definitions, err := ParseOfficialTools(json.RawMessage(raw))
	if err != nil || len(definitions) != 2 {
		t.Fatalf("migrated definitions = %+v, err=%v", definitions, err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("idempotent migrate: %v", err)
	}
	var again string
	if err := db.QueryRow(`SELECT official_tools FROM models WHERE id='m1'`).Scan(&again); err != nil {
		t.Fatalf("read idempotent value: %v", err)
	}
	if again != raw {
		t.Fatalf("second migration changed canonical value:\nfirst=%s\nagain=%s", raw, again)
	}
}
