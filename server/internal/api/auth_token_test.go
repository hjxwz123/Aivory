package api

import (
	"net/http/httptest"
	"testing"
)

func TestReadAccessTokenPrefersBearerOverCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.Header.Set("Authorization", "Bearer fresh")
	r.Header.Set("Cookie", "auth_token=stale")

	if got := readAccessToken(r); got != "fresh" {
		t.Fatalf("readAccessToken = %q, want bearer token", got)
	}
}
