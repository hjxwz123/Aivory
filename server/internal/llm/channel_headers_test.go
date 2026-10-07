package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestChannelHeadersAreAppliedToPrimaryAndIsolatedFallback(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("A") != "primary" || r.Header.Get("Authorization") != "Bearer override" {
			t.Errorf("primary headers = %v", r.Header)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("A") != "fallback" || r.Header.Get("X-Primary") != "" || r.Header.Get("Authorization") != "Bearer fallback-key" {
			t.Errorf("fallback headers = %v", r.Header)
		}
	}))
	defer fallback.Close()
	m := ModelInfo{BaseURL: primary.URL, APIKey: "primary-key", Headers: map[string]string{"A": "primary", "X-Primary": "yes", "Authorization": "Bearer override"},
		Fallback: &ChannelCreds{BaseURL: fallback.URL, APIKey: "fallback-key", Headers: map[string]string{"A": "fallback"}}}
	var used atomic.Bool
	resp, err := doProviderRequest(context.Background(), m, &used, func(baseURL, apiKey string) (*http.Request, error) {
		req, err := http.NewRequest("POST", baseURL, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		return req, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !used.Load() || resp.StatusCode != http.StatusOK {
		t.Fatalf("fallback=%v status=%d", used.Load(), resp.StatusCode)
	}
}

func TestChannelHeadersSurviveParsedRepairAndStickyFallback(t *testing.T) {
	for _, sticky := range []bool{false, true} {
		t.Run(map[bool]string{false: "primary", true: "sticky-fallback"}[sticky], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				want := "primary"
				if sticky {
					want = "fallback"
				}
				if r.Header.Get("A") != want {
					t.Errorf("header = %q, want %s", r.Header.Get("A"), want)
				}
			}))
			defer server.Close()
			m := ModelInfo{BaseURL: server.URL, APIKey: "key", Headers: map[string]string{"A": "primary"}}
			var used atomic.Bool
			if sticky {
				m.Fallback = &ChannelCreds{BaseURL: server.URL, APIKey: "fallback-key", Headers: map[string]string{"A": "fallback"}}
				used.Store(true)
			}
			consumed := 0
			err := doProviderParsedRequestWithRepair(context.Background(), m, &used,
				func(baseURL, _ string) (*http.Request, error) { return http.NewRequest("POST", baseURL, nil) },
				func(_ *http.Response, _ func(SseEvent)) error {
					consumed++
					if consumed == 1 {
						return errors.New("repairable request")
					}
					return nil
				}, nil, func(error) bool { return true })
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
