package routd

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// declares finds every table this chain builds, whatever the spelling.
var declares = regexp.MustCompile(`(?i)CREATE\s+(?:VIRTUAL\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_]+)`)

// chainDeclares is what routd's migration chain is allowed to build.
//
// Everything else routd.db holds is an objstore entity: the driver makes it from
// the declaration beside the code writing it (store.EnsureEntities), so a table
// named here as well would be built empty at every fresh open for CreateEntity
// to reclaim, which is one statement each and no row anywhere.
//
// `messages` and its FTS index are the two left, and they are the two a document
// store has no answer for: ranked search leaves for its own service and
// `messages` moves with it (spec 6/37 §"Search is the one that is answered by
// leaving"). When that lands this chain builds nothing, `routd/db.go` runs none
// of it, and this test goes with the directory it reads.
var chainDeclares = []string{"messages", "messages_fts"}

// TestChainDeclaresOnlyTheTablesAnEntityCannotBe is the boundary between what
// routd's schema chain still owns and what the driver owns.
//
// A new file that builds a table is either the one table left or a table that
// should have been an entity, and the second is what this catches: the five it
// used to build — turn_context, turn_results, group_watchers, task_run_logs and
// user_profiles — were all entities already, and nothing failed, because a table
// built empty for the driver to take over looks exactly like a healthy one.
func TestChainDeclaresOnlyTheTablesAnEntityCannotBe(t *testing.T) {
	files, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read the chain: %v", err)
	}
	for _, f := range files {
		raw, err := migrationFS.ReadFile("migrations/" + f.Name())
		if err != nil {
			t.Fatalf("read %s: %v", f.Name(), err)
		}
		for _, m := range declares.FindAllStringSubmatch(uncommented(string(raw)), -1) {
			table := strings.ToLower(m[1])
			if slices.Contains(chainDeclares, table) {
				continue
			}
			t.Errorf("%s builds %q; routd's chain builds %s and nothing else. A table that "+
				"is not one of those is an objstore entity: declare it in store/entities.go, "+
				"where the code that writes it declares its coordinate, and let the driver "+
				"make it", f.Name(), table, strings.Join(chainDeclares, " and "))
		}
	}
}

// uncommented drops the `--` comments, so a file that NAMES a table in the
// paragraph explaining why it does not build one is not read as building it.
func uncommented(sql string) string {
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		if at := strings.Index(line, "--"); at >= 0 {
			line = line[:at]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
