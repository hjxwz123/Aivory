// Package envcfg reads optional AIVORY_* environment overrides for values that
// would otherwise be compiled-in defaults (timeouts, concurrency, batch sizes,
// cache TTLs, size caps, internal model params, …).
//
// Every getter returns the supplied default when the variable is unset, empty,
// or unparseable — so behaviour is IDENTICAL to the old hardcoded constant
// unless an operator explicitly sets the variable. This lets deployments tune
// the knobs documented in docs/config-reference.md without a code change, while
// a fresh install with no extra env still runs on the exact same defaults.
//
// These variables are intentionally NOT listed in .env.example: they are an
// advanced, rarely-needed surface. Read docs/config-reference.md and add the
// ones you need.
package envcfg

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// lookup returns the trimmed value and whether a non-empty override was set.
func lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

// Str returns the env override for key, or def if unset/empty.
func Str(key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}

// Int returns the integer env override for key, or def if unset/unparseable.
func Int(key string, def int) int {
	if v, ok := lookup(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Int64 returns the int64 env override for key (e.g. a byte size), or def.
func Int64(key string, def int64) int64 {
	if v, ok := lookup(key); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// F64 returns the float64 env override for key, or def.
func F64(key string, def float64) float64 {
	if v, ok := lookup(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// Bool returns the boolean env override for key, or def. Accepts 1/0, t/f,
// true/false, yes/no, on/off (case-insensitive).
func Bool(key string, def bool) bool {
	if v, ok := lookup(key); ok {
		switch strings.ToLower(v) {
		case "1", "t", "true", "y", "yes", "on":
			return true
		case "0", "f", "false", "n", "no", "off":
			return false
		}
	}
	return def
}

// Dur returns the duration env override for key, or def. The value is parsed
// with time.ParseDuration, so operators write e.g. "90s", "5m", "2h", "500ms".
func Dur(key string, def time.Duration) time.Duration {
	if v, ok := lookup(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
