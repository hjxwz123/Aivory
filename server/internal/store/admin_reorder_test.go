package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAdminCatalogReordersAreCompleteAndAtomic(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "admin-reorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	statements := []string{
		`INSERT INTO image_styles(id,name,sort_order) VALUES ('style-a','A',0),('style-b','B',1),('style-c','C',2)`,
		`INSERT INTO skills(id,name,description,instructions,sort_order) VALUES ('skill-a','A','d','i',0),('skill-b','B','d','i',1),('skill-c','C','d','i',2)`,
		`INSERT INTO prompts(id,name,content,sort_order) VALUES ('prompt-a','A','c',0),('prompt-b','B','c',1),('prompt-c','C','c',2)`,
		`INSERT INTO oauth_providers(id,kind,name,sort_order) VALUES ('oauth-a','google','A',0),('oauth-b','github','B',1),('oauth-c','oidc','C',2)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name    string
		table   string
		ids     []string
		reorder func(context.Context, *sql.DB, []string) error
	}{
		{"image styles", "image_styles", []string{"style-a", "style-b", "style-c"}, ReorderImageStyles},
		{"skills", "skills", []string{"skill-a", "skill-b", "skill-c"}, ReorderSkills},
		{"prompts", "prompts", []string{"prompt-a", "prompt-b", "prompt-c"}, ReorderPrompts},
		{"oauth providers", "oauth_providers", []string{"oauth-a", "oauth-b", "oauth-c"}, ReorderOAuthProviders},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			expected := []string{test.ids[2], test.ids[0], test.ids[1]}
			if err := test.reorder(ctx, db, expected); err != nil {
				t.Fatalf("reorder: %v", err)
			}
			if got := adminRecordOrder(t, db, test.table); !reflect.DeepEqual(got, expected) {
				t.Fatalf("order = %v, want %v", got, expected)
			}

			invalidOrders := [][]string{
				{test.ids[0], test.ids[0], test.ids[1]},
				{test.ids[0], test.ids[1]},
				{test.ids[0], test.ids[1], "unknown"},
			}
			for _, invalid := range invalidOrders {
				if err := test.reorder(ctx, db, invalid); !errors.Is(err, ErrInvalidReorder) {
					t.Fatalf("invalid order %v error = %v, want ErrInvalidReorder", invalid, err)
				}
				if got := adminRecordOrder(t, db, test.table); !reflect.DeepEqual(got, expected) {
					t.Fatalf("invalid reorder changed order to %v, want %v", got, expected)
				}
			}
		})
	}
}

func adminRecordOrder(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM ` + table + ` ORDER BY sort_order, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}
