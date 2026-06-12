// Package pgcompat adapts the SQLite-flavoured SQL used throughout the store
// layer to PostgreSQL without rewriting hundreds of call sites.
//
// Every query in this codebase uses `?` positional placeholders (the SQLite /
// MySQL convention). PostgreSQL wants `$1, $2, …`. Rather than touch every
// query string, this package registers a thin database/sql driver that wraps
// pgx's stdlib driver and rewrites placeholders on the way through — the same
// trick sqlx.Rebind performs, but at the driver boundary so application code
// keeps passing `?`.
//
// The rewrite is placeholder-aware: `?` inside single-quoted string literals,
// line comments (`--`) and block comments (`/* */`) is left untouched. Doubled
// single quotes (`”`) inside a literal are handled as escapes.
package pgcompat

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Open returns a *sql.DB backed by Postgres that accepts `?` placeholders.
// dsn is a standard libpq / pgx connection string or URL.
func Open(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	base := stdlib.GetConnector(*cfg)
	return sql.OpenDB(&connector{base: base}), nil
}

// Rebind rewrites `?` placeholders to `$1, $2, …`. Exported for testing.
func Rebind(query string) string {
	// Fast path: nothing to do.
	if !strings.ContainsRune(query, '?') {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch c {
		case '\'':
			// Single-quoted string literal — copy verbatim until the closing
			// quote, treating '' as an escaped quote.
			b.WriteByte(c)
			i++
			for i < len(query) {
				b.WriteByte(query[i])
				if query[i] == '\'' {
					if i+1 < len(query) && query[i+1] == '\'' {
						b.WriteByte('\'')
						i += 2
						continue
					}
					break
				}
				i++
			}
		case '-':
			// Line comment: -- … to end of line.
			if i+1 < len(query) && query[i+1] == '-' {
				for i < len(query) && query[i] != '\n' {
					b.WriteByte(query[i])
					i++
				}
				// Step back one so the loop's i++ lands on the newline (or end).
				i--
			} else {
				b.WriteByte(c)
			}
		case '/':
			// Block comment: /* … */.
			if i+1 < len(query) && query[i+1] == '*' {
				b.WriteByte(query[i])
				b.WriteByte(query[i+1])
				i += 2
				for i < len(query) {
					b.WriteByte(query[i])
					if query[i] == '*' && i+1 < len(query) && query[i+1] == '/' {
						b.WriteByte('/')
						i++
						break
					}
					i++
				}
			} else {
				b.WriteByte(c)
			}
		case '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// --- driver.Connector ------------------------------------------------------

type connector struct{ base driver.Connector }

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	inner, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &conn{inner: inner}, nil
}

func (c *connector) Driver() driver.Driver { return c.base.Driver() }

// --- driver.Conn -----------------------------------------------------------

type conn struct{ inner driver.Conn }

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.inner.Prepare(Rebind(query))
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, Rebind(query))
	}
	return c.inner.Prepare(Rebind(query))
}

func (c *conn) Close() error { return c.inner.Close() }

func (c *conn) Begin() (driver.Tx, error) { //nolint:staticcheck // required by driver.Conn
	return c.inner.Begin() //nolint:staticcheck
}

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.inner.Begin() //nolint:staticcheck
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := c.inner.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, Rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := c.inner.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, Rebind(query), args)
	}
	return nil, driver.ErrSkip
}

// CheckNamedValue delegates to pgx's value checker so the broad set of Go types
// pgx accepts (e.g. plain int) keep working; falls back to the default check.
func (c *conn) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := c.inner.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (c *conn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *conn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *conn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}
