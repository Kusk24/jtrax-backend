// Splitting migration files into individual SQL statements. Turso's libSQL
// driver sends one statement per request, so a migration cannot be handed to
// Exec as a single blob the way the local SQLite driver allows.
package db

import "strings"

// splitStatements breaks migration SQL into individual statements, ignoring
// semicolons inside quoted strings and `--` comments. Statements with an inner
// semicolon (trigger bodies) are not supported; no migration uses one.
func splitStatements(text string) []string {
	var out []string
	var cur strings.Builder
	inString, inComment := false, false

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inComment:
			if c == '\n' {
				inComment = false
				cur.WriteRune(c)
			}
		case !inString && c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			inComment = true
			i++
		case c == ';' && !inString:
			appendStatement(&out, cur.String())
			cur.Reset()
		default:
			// A doubled '' inside a string toggles twice, which nets out correctly.
			if c == '\'' {
				inString = !inString
			}
			cur.WriteRune(c)
		}
	}
	appendStatement(&out, cur.String())
	return out
}

// appendStatement adds stmt unless it is blank once trimmed, so trailing
// semicolons and comment-only tails do not produce empty statements.
func appendStatement(out *[]string, stmt string) {
	if s := strings.TrimSpace(stmt); s != "" {
		*out = append(*out, s)
	}
}
