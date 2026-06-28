package store

import (
	"regexp"
	"testing"
)

var (
	createTableRe = regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS\s+(\w+)\s*\((.*?)\n\);`)
	referencesRe  = regexp.MustCompile(`REFERENCES\s+(\w+)\s*\(`)
)

// TestPostgresSchemaNoForwardFK guards against the class of bug where a table is
// declared before a table it has a foreign key to. Postgres resolves FK targets
// eagerly within the single-batch Migrate Exec, so a forward reference aborts the
// whole migration and a brand-new Postgres install can't start. (SQLite is lazy,
// so this only ever bites in production.) This test parses the embedded pg schema
// in declaration order and fails if any REFERENCES points at a not-yet-defined
// table — no live database required.
func TestPostgresSchemaNoForwardFK(t *testing.T) {
	defined := map[string]bool{}
	for _, m := range createTableRe.FindAllStringSubmatch(schemaPGSQL, -1) {
		table, body := m[1], m[2]
		for _, ref := range referencesRe.FindAllStringSubmatch(body, -1) {
			target := ref[1]
			if target != table && !defined[target] {
				t.Errorf("table %q references %q before it is declared (forward FK) — Postgres migration would abort; move %q after %q in schema_pg.sql",
					table, target, table, target)
			}
		}
		defined[table] = true
	}
	if len(defined) == 0 {
		t.Fatal("parsed no CREATE TABLE statements from schema_pg.sql — parser or schema changed")
	}
}
