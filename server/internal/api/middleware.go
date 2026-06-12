package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"aurelia/server/internal/store"
)

type handler func(d Deps, w http.ResponseWriter, r *http.Request)

type userCtxKey struct{}

// wrap converts our (d, w, r) handler signature to http.HandlerFunc without
// requiring auth.
func wrap(d Deps, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h(d, w, r)
	}
}

// authUser returns the authenticated user from context (panics if missing —
// only called by the auth wrapper).
func authUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userCtxKey{}).(*store.User)
	return u
}

// requireAuth wraps a handler with access-token validation. Token is read from
// the "auth_token" httpOnly cookie or the Authorization header (Bearer).
//
// Named `requireAuth` (not `auth`) so we don't shadow the imported `auth`
// package — Go won't let a func and an import share a name in the same file.
func requireAuth(d Deps, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := readAccessToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, errAuthRequired)
			return
		}
		claims, err := d.Auth.ParseAccess(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, errAuthRequired)
			return
		}
		user, err := store.FindUserByID(r.Context(), d.DB, claims.UID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, errAuthRequired)
			return
		}
		if user.Status != "active" {
			writeError(w, http.StatusForbidden, errAccountBlocked)
			return
		}
		if user.TokenVer != claims.TV {
			writeError(w, http.StatusUnauthorized, errSessionExpired)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey{}, user)
		h(d, w, r.WithContext(ctx))
	}
}

// requireAdmin wraps a handler with both auth and admin-role enforcement.
func requireAdmin(d Deps, h handler) http.HandlerFunc {
	return requireAuth(d, func(d Deps, w http.ResponseWriter, r *http.Request) {
		u := authUser(r)
		if u.Role != "admin" {
			writeError(w, http.StatusForbidden, errAdminOnly)
			return
		}
		h(d, w, r)
	})
}

func readAccessToken(r *http.Request) string {
	if c, err := r.Cookie("auth_token"); err == nil {
		return c.Value
	}
	a := r.Header.Get("Authorization")
	if strings.HasPrefix(a, "Bearer ") {
		return strings.TrimPrefix(a, "Bearer ")
	}
	return ""
}

// clientIP extracts the request's source address from X-Forwarded-For (first
// hop), X-Real-IP, or RemoteAddr. Used by the IP-scoped rate limits.
func clientIP(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		// First IP is the originator; subsequent are proxies.
		if i := strings.Index(h, ","); i > 0 {
			return strings.TrimSpace(h[:i])
		}
		return strings.TrimSpace(h)
	}
	if h := r.Header.Get("X-Real-IP"); h != "" {
		return strings.TrimSpace(h)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimitIP applies a fixed-window per-IP request budget — used to block
// brute-force attempts on auth endpoints (login/register/refresh). We use the
// cache's Incr-with-TTL primitive (Redis-backed in prod, in-memory in dev) so
// the limit is shared across replicas.
//
// scope namespaces the counter (e.g. "auth.login"), perWindow is the budget,
// window is the fixed-window length. Returns true if the request should be
// allowed.
func rateLimitIP(d Deps, r *http.Request, scope string, perWindow int, window time.Duration) bool {
	ip := clientIP(r)
	if ip == "" {
		return true
	}
	key := "rl:ip:" + scope + ":" + ip + ":" + r.URL.Path
	n := d.Cache.Incr(key, window)
	return int(n) <= perWindow
}

// rateLimitedIP wraps a handler with rateLimitIP enforcement.
func rateLimitedIP(d Deps, scope string, perWindow int, window time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rateLimitIP(d, r, scope, perWindow, window) {
			writeError(w, http.StatusTooManyRequests, errors.New("rate limit exceeded — try again later"))
			return
		}
		next(w, r)
	}
}

// reserveConcurrentGen takes one slot in the per-user concurrent-generation
// budget (§8 — gen:active). Default cap is 3; admins can override via the
// `max_concurrent_generations` setting. The returned release MUST be called in
// a defer once the SSE stream ends, otherwise the slot leaks until restart.
func reserveConcurrentGen(d Deps, userID string) (release func(), ok bool) {
	cap := 3
	if raw, gerr := store.GetSetting(d.DB, "max_concurrent_generations"); gerr == nil {
		// best-effort: ignore parse errors and keep cap=3
		_ = jsonUnmarshalInt(raw, &cap)
	}
	if cap <= 0 {
		return func() {}, true
	}
	key := "gen:active:" + userID
	// Incr first; if we're over the cap, decrement immediately and refuse.
	n := d.Cache.Incr(key, 30*time.Minute) // safety TTL so dead slots clear themselves
	if int(n) > cap {
		// Decrement by re-Incr-ing the negative; we don't have Decr so we
		// emulate it by SET-ing to the lower value when possible. The 30-min
		// TTL above means the slot self-clears even if we miss the release.
		d.Cache.Set(key, intToString(int(n)-1), 30*time.Minute)
		return func() {}, false
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		if v, hit := d.Cache.Get(key); hit {
			cur := 0
			for _, c := range v {
				if c < '0' || c > '9' {
					cur = 0
					break
				}
				cur = cur*10 + int(c-'0')
			}
			if cur > 0 {
				d.Cache.Set(key, intToString(cur-1), 30*time.Minute)
			}
		}
	}, true
}

// jsonUnmarshalInt accepts a json.RawMessage holding an integer (or a JSON
// number cast as int) and writes it into out.
func jsonUnmarshalInt(raw []byte, out *int) error {
	if len(raw) == 0 {
		return errors.New("empty")
	}
	n := 0
	neg := false
	i := 0
	if raw[0] == '-' {
		neg = true
		i = 1
	}
	for ; i < len(raw); i++ {
		c := raw[i]
		if c < '0' || c > '9' {
			return errors.New("not int")
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	*out = n
	return nil
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	if neg {
		out = append([]byte{'-'}, out...)
	}
	return string(out)
}

// checkDailyTokenQuota enforces a per-user daily token ceiling (§8 hard rule).
// Default is 0 (disabled); set settings.daily_token_limit to a positive int to
// turn it on. Tokens are counted via usage_logs (input+output), summed for the
// current UTC day.
func checkDailyTokenQuota(d Deps, userID string) bool {
	limit := 0
	if raw, err := store.GetSetting(d.DB, "daily_token_limit"); err == nil {
		_ = jsonUnmarshalInt(raw, &limit)
	}
	if limit <= 0 {
		return true
	}
	dayStart := time.Now().Truncate(24 * time.Hour).Unix()
	var used int
	_ = d.DB.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM usage_logs WHERE user_id=? AND created_at>=?`,
		userID, dayStart).Scan(&used)
	return used < limit
}
