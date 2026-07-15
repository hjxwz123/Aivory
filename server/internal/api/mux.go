package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"aivory/server/internal/envcfg"
)

// jsonRequestBodySizeCap caps JSON request bodies (§FIX-2); env-overridable.
var jsonRequestBodySizeCap = envcfg.Int64("AIVORY_API_JSON_REQUEST_BODY_SIZE_CAP", 4<<20)

// mux is a tiny path-param router. Routes use the `:name` syntax which is
// captured into the request context.
type mux struct {
	routes []route
}

type route struct {
	method      string
	patternText string
	pattern     []string // path segments
	handler     http.HandlerFunc
}

func newMux() *mux { return &mux{} }

func (m *mux) handle(method, pattern string, h http.HandlerFunc) {
	segs := splitPath(pattern)
	m.routes = append(m.routes, route{method: method, patternText: pattern, pattern: segs, handler: h})
}

type pathCtxKey struct{}

// pathParam reads a captured parameter from r.Context.
func pathParam(r *http.Request, name string) string {
	v, _ := r.Context().Value(pathCtxKey{}).(map[string]string)
	return v[name]
}

func (m *mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	segs := splitPath(r.URL.Path)
	methodMismatchPattern := ""
	for _, rt := range m.routes {
		params, ok := matchPath(rt.pattern, segs)
		if !ok {
			continue
		}
		if rt.method != r.Method {
			if methodMismatchPattern == "" {
				methodMismatchPattern = rt.patternText
			}
			continue
		}
		recordResponseRoute(w, rt.patternText)
		ctx := context.WithValue(r.Context(), pathCtxKey{}, params)
		rt.handler.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	if methodMismatchPattern != "" {
		recordResponseRoute(w, methodMismatchPattern)
	}
	writeJSON(w, 404, map[string]string{"error": "not found"})
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func matchPath(pattern, actual []string) (map[string]string, bool) {
	if len(pattern) != len(actual) {
		return nil, false
	}
	params := map[string]string{}
	for i, p := range pattern {
		if strings.HasPrefix(p, ":") {
			params[p[1:]] = actual[i]
			continue
		}
		if p != actual[i] {
			return nil, false
		}
	}
	return params, true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	if err == nil {
		err = errors.New("unknown error")
	}
	msg := err.Error()
	recorded := recordResponseError(w, err)
	if status >= 500 {
		// Log the real error server-side but never expose internal details to
		// the client — stack traces, SQL, file paths, etc. (§ FIX-3).
		if !recorded {
			slog.Error("internal error", "err", err)
		}
		msg = "internal server error"
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON decodes a JSON request body into dst.
// Body reads are capped at 4 MB to prevent memory exhaustion (§ FIX-2).
// Backup import has its own 2 GB limit in admin_backup_handlers.go.
func decodeJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, jsonRequestBodySizeCap) // 4 MB
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}
