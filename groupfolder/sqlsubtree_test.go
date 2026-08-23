package groupfolder

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSQLSubtree_NoWildcardsNoCaseFolding runs the real SQL, because the bug it
// replaces was a property of sqlite's LIKE, not of Go. `DELETE ... LIKE
// 'a_b'||'/%'` also deleted axb's rows (`_` is a wildcard) and `coach` also
// deleted Coach's (LIKE is case-insensitive for ASCII); a live instance has
// carried a Coach/coach pair (BUGS L66).
func TestSQLSubtree_NoWildcardsNoCaseFolding(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	rows := []string{
		"a_b", "a_b/kid", "axb/kid",
		"coach#observe", "Coach#observe",
		"acme", "acmecorp/x", "a%/kid", "azz/kid",
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, r); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		folder, sep string
		want        []string
	}{
		{"a_b", "/", []string{"a_b", "a_b/kid"}},
		{"coach", "#", []string{"coach#observe"}},
		{"acme", "/", []string{"acme"}},
		{"a%", "/", []string{"a%/kid"}},
	} {
		q := `SELECT v FROM t WHERE ` + SQLSubtree("v", c.sep) + ` ORDER BY v`
		res, err := db.Query(q, SubtreeArgs(c.folder, c.sep)...)
		if err != nil {
			t.Fatalf("%s: %v", c.folder, err)
		}
		var got []string
		for res.Next() {
			var v string
			if err := res.Scan(&v); err != nil {
				t.Fatal(err)
			}
			got = append(got, v)
		}
		res.Close()
		if len(got) != len(c.want) {
			t.Errorf("subtree(%q, %q) matched %v, want %v", c.folder, c.sep, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("subtree(%q, %q) matched %v, want %v", c.folder, c.sep, got, c.want)
				break
			}
		}
	}
}
