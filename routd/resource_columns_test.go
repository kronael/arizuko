package routd

// The precondition of the object-path cutover, checked against the schema the
// resources actually live on (spec 6/40 §"What a registered resource still
// needs").
//
// A RowType that declares FEWER columns than its table has is survivable on the
// column path — an INSERT names the columns it binds and the rest take their
// defaults — and it is DATA LOSS under an entity, because a Put carries the
// item's WHOLE attribute set and an attribute the row does not name is gone
// (objstore.Write). So an under-declared registration is a silent truncation
// waiting for the day its table moves, and the check has to run before the move
// rather than during it.
//
// It runs HERE and not beside the registrations for the reason
// TestFolderScope_ClassifiesEveryRoutdTable does: routd.db is the schema these
// rows live on, and store's frozen chain builds a database no daemon opens.

import (
	"database/sql"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
)

// tableColumns reads one table's column names from the engine under test.
func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	q := `SELECT name FROM pragma_table_info(?)`
	if db_utils.IsPostgres(db) {
		q = `SELECT column_name FROM information_schema.columns ` +
			`WHERE table_schema = 'public' AND table_name = $1`
	}
	rows, err := db.Query(q, table)
	if err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	sort.Strings(out)
	return out
}

// declaredColumns is the `db:` tag set of one RowType — exactly the columns the
// engine reads and writes.
func declaredColumns(rt reflect.Type) []string {
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	var out []string
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		col, _, _ := strings.Cut(tag, ",")
		out = append(out, col)
	}
	sort.Strings(out)
	return out
}

// rowTypeGaps names, per resource, the columns its RowType deliberately does
// NOT name, and what makes that safe — or what still blocks it.
//
// It is a TOTAL classification with the check below, the shape
// store.folderScopeKeep already has: a column in neither the declaration nor
// the RowType fails the test, so a column joining a table cannot skip the
// question of whether the resource owes it a field. A verdict here is checked
// both ways — a gap that has been closed fails too, because a claim that
// outlives its cause is the L253 shape.
var rowTypeGaps = map[string][]string{
	// SkipApplyRebuild: an apply never writes these rows, so the engine issues
	// no Put over them on either shape, and the columns hold material a manifest
	// must not carry. `secret` is sealed under SECRETS_KEY (migration 0041), and
	// putting it in a RowType would put a live webhook credential in the export
	// an operator reads — the boundary objstore.Query.Project draws for
	// authd's private keys.
	"github_sources": {"secret"},
	// SkipApplyRebuild, and both columns are the same boundary: `token_hash` is
	// the ingress credential, and `kind` is the resource's own RowFilter
	// predicate (`kind = 'route'`), so a row that reached the projection at all
	// already carries the one value it can have.
	"route_tokens": {"kind", "token_hash"},
	// SkipApplyRebuild — "enc_value blobs are set imperatively, never via apply"
	// — and `value`/`refresh_val` are the sealed secret itself.
	"secrets": {"expires_at", "provider", "refresh_val", "scope_list", "value"},
}

// TestResourceRowTypesCoverTheirTable is the check: every column of every
// routd resource's table is either named by its RowType or declared above.
func TestResourceRowTypesCoverTheirTable(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	seen := map[string]bool{}
	for _, r := range resreg.BySubsystem(resreg.SubsystemRoutd) {
		if r.RowType == nil {
			continue
		}
		seen[r.Name] = true
		have := tableColumns(t, db.SQL(), r.Table)
		if len(have) == 0 {
			t.Errorf("%s: table %s is not in routd.db", r.Name, r.Table)
			continue
		}
		declared := map[string]bool{}
		for _, c := range declaredColumns(r.RowType) {
			declared[c] = true
		}
		known := map[string]bool{}
		for _, c := range rowTypeGaps[r.Name] {
			known[c] = true
		}
		for _, c := range have {
			if declared[c] || known[c] {
				continue
			}
			t.Errorf("%s: column %s.%s is in neither the RowType nor rowTypeGaps.\n"+
				"A Put replaces the item's whole attribute set, so a column the RowType "+
				"does not name is data loss the day the resource moves to an entity. "+
				"Add the field, or record here what makes the gap safe.", r.Name, r.Table, c)
		}
		for c := range known {
			if declared[c] {
				t.Errorf("%s: rowTypeGaps still claims %s.%s is undeclared, and the RowType "+
					"names it; drop the entry", r.Name, r.Table, c)
			}
			if !slices.Contains(have, c) {
				t.Errorf("%s: rowTypeGaps names %s.%s and the table has no such column; "+
					"drop the entry", r.Name, r.Table, c)
			}
		}
	}
	for name := range rowTypeGaps {
		if !seen[name] {
			t.Errorf("rowTypeGaps names %q, which is not a routd resource with a RowType", name)
		}
	}
}
