package requestheaders

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeadersJSONValidation(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `"headers"`, `{"A":null}`, `{"A":1}`, `{"A":true}`, `{"A":{}}`,
		`{"bad name":"a"}`, `{"A":"a\r\nb"}`, `{"A":"a\u0000b"}`, `{"a":"a","A":"b"}`,
		`{"Content-Length":"100"}`, `{"Transfer-Encoding":"chunked"}`, `{"Trailer":"A"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var headers Headers
			if err := json.Unmarshal([]byte(raw), &headers); err == nil {
				t.Fatalf("accepted invalid headers %s", raw)
			}
		})
	}
	var headers Headers
	if err := json.Unmarshal([]byte(`{"A":"a","x-tenant":"team","X-Empty":""}`), &headers); err != nil {
		t.Fatal(err)
	}
	if headers["A"] != "a" || headers["X-Tenant"] != "team" {
		t.Fatalf("headers = %v", headers)
	}
	if _, err := Normalize(Headers{"A": strings.Repeat("a", 16*1024)}); err == nil {
		t.Fatal("oversized configuration accepted")
	}
}

func TestApplyHeadersOverridesDefaultsAndHost(t *testing.T) {
	req := httptest.NewRequest("POST", "https://provider.example/chat", nil)
	req.Header.Set("Authorization", "Bearer default")
	req.Header.Set("Content-Type", "application/json")
	Apply(req, Headers{"A": "a", "authorization": "Bearer custom", "Host": "gateway.example"})
	if req.Header.Get("A") != "a" || req.Header.Get("Authorization") != "Bearer custom" || req.Host != "gateway.example" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request host=%s headers=%v", req.Host, req.Header)
	}
}

func BenchmarkApplyHeaders(b *testing.B) {
	req := httptest.NewRequest("POST", "https://provider.example/chat", nil)
	headers := Headers{"A": "a", "X-Tenant": "team"}
	b.ReportAllocs()
	for b.Loop() {
		Apply(req, headers)
	}
}
