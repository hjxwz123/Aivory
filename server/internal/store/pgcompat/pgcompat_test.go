package pgcompat

import "testing"

func TestRebind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"SELECT * FROM users WHERE id=?", "SELECT * FROM users WHERE id=$1"},
		{"INSERT INTO t(a,b,c) VALUES(?,?,?)", "INSERT INTO t(a,b,c) VALUES($1,$2,$3)"},
		{"UPDATE t SET a=?, b=? WHERE id=?", "UPDATE t SET a=$1, b=$2 WHERE id=$3"},
		// '?' inside a string literal must be preserved.
		{"SELECT '新对话?' , ? FROM t", "SELECT '新对话?' , $1 FROM t"},
		// Escaped quote inside literal.
		{"SELECT 'it''s ok?', ?", "SELECT 'it''s ok?', $1"},
		// Line comment.
		{"SELECT ? -- trailing ? comment\n, ?", "SELECT $1 -- trailing ? comment\n, $2"},
		// Block comment.
		{"SELECT ? /* a ? b */ , ?", "SELECT $1 /* a ? b */ , $2"},
		// Real mock-seed literal: placeholder among quoted literals.
		{
			"VALUES('m_mock_chat', ?, 'chat', 'No external calls.', 'native', 1, 1)",
			"VALUES('m_mock_chat', $1, 'chat', 'No external calls.', 'native', 1, 1)",
		},
	}
	for _, c := range cases {
		if got := Rebind(c.in); got != c.want {
			t.Errorf("Rebind(%q)\n  got  %q\n  want %q", c.in, got, c.want)
		}
	}
}
