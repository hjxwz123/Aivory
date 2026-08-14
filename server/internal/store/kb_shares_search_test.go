package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSearchKnowledgeBaseShareCandidatesRequiresExactEmail(t *testing.T) {
	db := openKBPermissionTestDB(t)
	exec(t, db, `UPDATE users SET name=CASE id
		WHEN 'creator' THEN 'Library Owner'
		WHEN 'member' THEN 'Matching Member'
		WHEN 'outsider' THEN 'Outside Collaborator'
		ELSE name END`)
	seedPersonalKnowledgeBaseShares(t, db)

	tests := []struct {
		name     string
		query    string
		wantID   string
		wantRole string
	}{
		{name: "missing query"},
		{name: "whitespace query", query: " \t\n "},
		{name: "display name", query: "Matching Member"},
		{name: "partial email local part", query: "member"},
		{name: "partial email address", query: "member@example"},
		{name: "email substring", query: "example.test"},
		{name: "wildcard", query: "%"},
		{name: "owner exact email", query: "creator@example.test"},
		{name: "exact email", query: "member@example.test", wantID: "member", wantRole: "read"},
		{name: "trimmed case insensitive exact email", query: "  OUTSIDER@EXAMPLE.TEST  ", wantID: "outsider", wantRole: "write"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := SearchKnowledgeBaseShareCandidates(
				context.Background(), db, "personal-kb", "creator", tc.query, 20,
			)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantID == "" {
				if len(rows) != 0 {
					t.Fatalf("query %q returned users=%+v, want none", tc.query, rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("query %q returned users=%+v, want one", tc.query, rows)
			}
			if rows[0].UserID != tc.wantID || rows[0].Role != tc.wantRole {
				t.Fatalf("query %q returned user=%+v, want id=%q role=%q", tc.query, rows[0], tc.wantID, tc.wantRole)
			}
		})
	}
}

func TestSearchKnowledgeBaseShareCandidatesExcludesInactiveExactEmail(t *testing.T) {
	db := openKBPermissionTestDB(t)
	exec(t, db, `UPDATE users SET status='banned' WHERE id='outsider'`)

	rows, err := SearchKnowledgeBaseShareCandidates(
		context.Background(), db, "personal-kb", "creator", "outsider@example.test", 20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("inactive exact-email match returned users=%+v, want none", rows)
	}
}

func TestUpsertKnowledgeBaseShareRequiresExactTargetEmail(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		prepare func(*testing.T, *sql.DB)
		wantID  string
		wantErr error
	}{
		{name: "opaque user id", email: "member", wantErr: ErrInvalidKnowledgeBaseShare},
		{name: "display name", email: "Matching Member", wantErr: ErrInvalidKnowledgeBaseShare},
		{name: "blank email", wantErr: ErrInvalidKnowledgeBaseShare},
		{name: "partial registered email", email: "member@example", wantErr: ErrNotFound},
		{name: "unknown exact email", email: "missing@example.test", wantErr: ErrNotFound},
		{name: "owner exact email", email: "creator@example.test", wantErr: ErrNotFound},
		{
			name:  "inactive exact email",
			email: "member@example.test",
			prepare: func(t *testing.T, db *sql.DB) {
				t.Helper()
				exec(t, db, `UPDATE users SET status='banned' WHERE id='member'`)
			},
			wantErr: ErrNotFound,
		},
		{name: "trimmed case insensitive exact email", email: "  MEMBER@EXAMPLE.TEST  ", wantID: "member"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			if tc.prepare != nil {
				tc.prepare(t, db)
			}
			share, err := UpsertKnowledgeBaseShare(
				context.Background(), db, "personal-kb", "creator", tc.email, "read",
			)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("UpsertKnowledgeBaseShare(%q) error=%v, want %v", tc.email, err, tc.wantErr)
				}
				if share != nil {
					t.Fatalf("UpsertKnowledgeBaseShare(%q) share=%+v, want nil", tc.email, share)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if share == nil || share.UserID != tc.wantID || share.Email != "member@example.test" {
				t.Fatalf("UpsertKnowledgeBaseShare(%q) share=%+v, want member exact match", tc.email, share)
			}
		})
	}
}
