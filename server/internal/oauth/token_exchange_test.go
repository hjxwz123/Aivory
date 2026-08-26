package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDecodeTokenEndpointResponseSupportsFormEncoding(t *testing.T) {
	got, err := decodeTokenEndpointResponse([]byte("access_token=token-value&id_token=id-value"))
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "token-value" || got.IDToken != "id-value" {
		t.Fatalf("decoded form response = %+v", got)
	}
}

func TestTokenExchangeRetriesTransientTransportFailure(t *testing.T) {
	previousClient := httpClient
	requests := 0
	httpClient = &http.Client{Transport: oauth2RoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return nil, context.DeadlineExceeded
		}
		return oauth2JSONResponse(`{"access_token":"recovered-token"}`), nil
	})}
	t.Cleanup(func() {
		httpClient = previousClient
	})

	tokens, _, err := (Config{TokenURL: "https://identity.example.test/token"}).postToken(
		context.Background(), nil, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "recovered-token" || requests != 2 {
		t.Fatalf("tokens=%+v requests=%d, want a successful second attempt", tokens, requests)
	}
}

func TestTokenExchangeDoesNotRetryProviderProtocolError(t *testing.T) {
	previousClient := httpClient
	requests := 0
	httpClient = &http.Client{Transport: oauth2RoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		return oauth2JSONResponse(`{"error":"bad_verification_code"}`), nil
	})}
	t.Cleanup(func() {
		httpClient = previousClient
	})

	_, _, err := (Config{TokenURL: "https://identity.example.test/token"}).postToken(
		context.Background(), nil, "",
	)
	if got := TokenExchangeFailureReason(err); got != "oauth_code_invalid" {
		t.Fatalf("failure reason = %q, want oauth_code_invalid (err=%v)", got, err)
	}
	if requests != 1 {
		t.Fatalf("provider protocol error requests=%d, want 1", requests)
	}
}

func TestTokenExchangeFailureReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "GitHub credentials", err: &tokenEndpointError{Status: http.StatusOK, Code: "incorrect_client_credentials"}, want: "oauth_credentials_invalid"},
		{name: "expired or reused code", err: &tokenEndpointError{Status: http.StatusOK, Code: "bad_verification_code"}, want: "oauth_code_invalid"},
		{name: "redirect URI", err: &tokenEndpointError{Status: http.StatusBadRequest, Code: "redirect_uri_mismatch"}, want: "oauth_redirect_uri_mismatch"},
		{name: "rate limit code", err: &tokenEndpointError{Status: http.StatusOK, Code: "slow_down"}, want: "oauth_provider_rate_limited"},
		{name: "rate limit status", err: &tokenEndpointError{Status: http.StatusTooManyRequests}, want: "oauth_provider_rate_limited"},
		{name: "provider server error", err: &tokenEndpointError{Status: http.StatusBadGateway}, want: "oauth_provider_unreachable"},
		{name: "deadline", err: context.DeadlineExceeded, want: "oauth_provider_timeout"},
		{name: "unknown", err: errors.New("unknown exchange failure"), want: "token_exchange_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := TokenExchangeFailureReason(test.err); got != test.want {
				t.Fatalf("TokenExchangeFailureReason() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTokenEndpointErrorDoesNotLeakReturnedTokenFields(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"error":"redirect_uri_mismatch","error_description":"callback differs","access_token":"must-not-leak"}`,
		)),
	}
	_, _, err := parseTokenEndpointResponse(resp)
	if got := TokenExchangeFailureReason(err); got != "oauth_redirect_uri_mismatch" {
		t.Fatalf("failure reason = %q, want oauth_redirect_uri_mismatch (err=%v)", got, err)
	}
	if strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("exchange error leaked token response field: %v", err)
	}
}
