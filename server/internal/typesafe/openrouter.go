package typesafe

import (
	"net/url"
	"strings"
)

const OpenRouterDecisionsProtocol = "openrouter.decisions"

// OpenRouterDecisionsEndpoint accepts a full resource URL or an API root.
// The standard /api/v1 root is shared with chat, but Decisions lives in /api/alpha.
func OpenRouterDecisionsEndpoint(baseURL string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://openrouter.ai"
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", failure(ErrConfiguration, "invalid OpenRouter API root or Decisions endpoint")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/systemone")
	switch {
	case strings.HasSuffix(u.Path, "/decisions"):
		// Use complete custom endpoints without adding another suffix.
	case u.Path == "":
		u.Path = "/api/alpha/decisions"
	case strings.HasSuffix(u.Path, "/api/v1"):
		u.Path = strings.TrimSuffix(u.Path, "/v1") + "/alpha/decisions"
	case strings.HasSuffix(u.Path, "/api"):
		u.Path += "/alpha/decisions"
	case strings.HasSuffix(u.Path, "/alpha"):
		u.Path += "/decisions"
	default:
		u.Path += "/decisions"
	}
	u.RawPath = ""
	return u.String(), nil
}

// Model discovery always uses the catalog, never /decisions/models.
func OpenRouterModelsEndpoint(baseURL string) (string, error) {
	endpoint, err := OpenRouterDecisionsEndpoint(baseURL)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(endpoint)
	base := strings.TrimSuffix(u.Path, "/decisions")
	if strings.HasSuffix(base, "/alpha") {
		base = strings.TrimSuffix(base, "/alpha") + "/v1"
	}
	u.Path = base + "/models"
	u.RawPath = ""
	return u.String(), nil
}
