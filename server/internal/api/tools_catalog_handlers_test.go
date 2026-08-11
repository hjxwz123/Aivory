package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
	toolregistry "aivory/server/internal/tools"
)

func TestSelectableToolsCatalogIsFlatSafeAndDeduplicatesMCPService(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "tools-catalog.db"))
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','catalog@example.test','hash','user','active')`); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(ctx, db, "Catalog", "openai", "responses", "https://provider.example.test", "provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "catalog-model", Label: "Catalog model",
		Enabled: true, Stream: true, ToolMode: "native",
		BuiltinTools: json.RawMessage(`["aivory_web_search"]`),
		OfficialTools: json.RawMessage(`[
			{"name":"web_search","icon":"Search","request":{"tools":[{"type":"web_search","private":"must-not-leak"}]}}
		]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	const headerSecret = "Bearer catalog-admin-secret"
	remote, err := store.CreateMCPServer(ctx, db, store.MCPServer{
		ID: "mcp_rail", Name: "Train schedules", Icon: "Train",
		Description: "Look up railway trips", URL: "https://mcp.example.test/private-endpoint",
		Headers: map[string]string{"Authorization": headerSecret}, Enabled: true,
		DiscoveredTools: json.RawMessage(`[
			{"name":"search-trains","description":"Search trains","inputSchema":{"type":"object"}},
			{"name":"get-current-date","description":"Get the current date","inputSchema":{"type":"object"}}
		]`),
	})
	if err != nil {
		t.Fatal(err)
	}

	registry := toolregistry.NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodGet, "/api/tools?model_id="+model.ID, nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{
		ID: "u1", Role: "user", Status: "active",
	}))
	recorder := httptest.NewRecorder()
	listSelectableToolsHandler(Deps{DB: db, Tools: registry}, recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, secret := range []string{headerSecret, remote.URL, "must-not-leak", "Authorization"} {
		if strings.Contains(body, secret) {
			t.Fatalf("catalog leaked %q: %s", secret, body)
		}
	}
	var rows []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, row := range rows {
		for key := range row {
			switch key {
			case "id", "name", "description", "icon":
			default:
				t.Fatalf("catalog exposed unexpected field %q: %#v", key, row)
			}
		}
		id, _ := row["id"].(string)
		counts[id]++
	}
	for _, id := range []string{"builtin:aivory_web_search", "hosted:web_search", "mcp:" + remote.ID} {
		if counts[id] != 1 {
			t.Fatalf("catalog count for %q=%d, rows=%#v", id, counts[id], rows)
		}
	}
	if counts["mcp:"+remote.ID] != 1 {
		t.Fatalf("MCP service with multiple methods was not deduplicated: %#v", rows)
	}
}

func TestSelectableToolsCatalogRejectsMissingOrDisabledModel(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "tools-catalog-invalid.db"))
	t.Cleanup(func() { _ = db.Close() })
	for _, target := range []struct {
		path string
		want int
	}{
		{path: "/api/tools", want: http.StatusBadRequest},
		{path: "/api/tools?model_id=missing", want: http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, target.path, nil)
		recorder := httptest.NewRecorder()
		listSelectableToolsHandler(Deps{DB: db}, recorder, req)
		if recorder.Code != target.want {
			t.Fatalf("%s status=%d want=%d body=%s", target.path, recorder.Code, target.want, recorder.Body.String())
		}
	}
}
