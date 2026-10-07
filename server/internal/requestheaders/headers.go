package requestheaders

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type Headers map[string]string

func (h *Headers) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("headers must be a JSON object with string values")
	}
	parsed := Headers{}
	for name, value := range fields {
		var text string
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil {
			return errors.New("header values must be strings")
		}
		parsed[name] = text
	}
	normalized, err := Normalize(parsed)
	if err != nil {
		return err
	}
	*h = normalized
	return nil
}

func Normalize(headers Headers) (Headers, error) {
	if len(headers) > 64 {
		return nil, errors.New("too many custom headers (maximum 64)")
	}
	normalized := Headers{}
	size := 0
	for name, value := range headers {
		if name == "" || strings.IndexFunc(name, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c))
		}) >= 0 {
			return nil, errors.New("invalid HTTP header name")
		}
		for i := 0; i < len(value); i++ {
			if value[i] == 127 || value[i] < 32 && value[i] != '\t' {
				return nil, errors.New("invalid HTTP header value")
			}
		}
		key := http.CanonicalHeaderKey(name)
		switch key {
		case "Content-Length", "Transfer-Encoding", "Trailer":
			return nil, fmt.Errorf("%s is managed by the HTTP transport", key)
		}
		if _, exists := normalized[key]; exists {
			return nil, errors.New("duplicate case-insensitive header name")
		}
		size += len(name) + len(value)
		if size > 16*1024 {
			return nil, errors.New("custom headers exceed 16 KiB")
		}
		normalized[key] = value
	}
	return normalized, nil
}

// Apply after provider defaults so administrator values take precedence.
func Apply(req *http.Request, headers Headers) {
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	for name, value := range headers {
		if strings.EqualFold(name, "Host") {
			req.Host = value
			continue
		}
		req.Header.Set(name, value)
	}
}
