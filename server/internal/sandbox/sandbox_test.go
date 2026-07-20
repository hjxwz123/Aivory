package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPSandboxResetInputsUsesFixedEndpoint(t *testing.T) {
	var gotSession string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/files/reset-inputs" {
			t.Errorf("path = %s, want /files/reset-inputs", r.URL.Path)
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		gotSession = body.SessionID
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	svc := New(server.URL, "")
	if err := svc.ResetInputs(context.Background(), "session-123"); err != nil {
		t.Fatalf("ResetInputs: %v", err)
	}
	if gotSession != "session-123" {
		t.Fatalf("session_id = %q, want session-123", gotSession)
	}
}
