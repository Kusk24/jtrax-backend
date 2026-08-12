package db

import "testing"

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "trailing semicolon does not yield an empty statement",
			in:   "CREATE TABLE a (id TEXT);",
			want: []string{"CREATE TABLE a (id TEXT)"},
		},
		{
			name: "line comments are stripped",
			in:   "-- a comment\nCREATE TABLE a (id TEXT); -- trailing\nCREATE TABLE b (id TEXT);",
			want: []string{"CREATE TABLE a (id TEXT)", "CREATE TABLE b (id TEXT)"},
		},
		{
			name: "semicolon inside a string literal does not split",
			in:   "INSERT INTO a VALUES ('one;two');",
			want: []string{"INSERT INTO a VALUES ('one;two')"},
		},
		{
			name: "doubled quote inside a string keeps the split correct",
			in:   "INSERT INTO a VALUES ('it''s; fine'); INSERT INTO b VALUES (1);",
			want: []string{"INSERT INTO a VALUES ('it''s; fine')", "INSERT INTO b VALUES (1)"},
		},
		{
			name: "final statement without a semicolon is kept",
			in:   "CREATE TABLE a (id TEXT)",
			want: []string{"CREATE TABLE a (id TEXT)"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitStatements(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d statements %q, want %d %q", len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("statement %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// The real migrations must survive the splitter, since every deployment
// applies them through it.
func TestSplitStatementsOnRealMigrations(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		stmts := splitStatements(string(body))
		if len(stmts) == 0 {
			t.Errorf("%s split into no statements", e.Name())
		}
		for _, s := range stmts {
			if len(s) < 5 {
				t.Errorf("%s produced a suspiciously short statement %q", e.Name(), s)
			}
		}
	}
}
