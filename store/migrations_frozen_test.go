package store

// store/migrations is FROZEN. It builds the pre-split messages.db — the schema an
// instance had before `arizuko migrate-split` — and nothing else. No daemon runs
// it: routd, onbod, authd, runed and chanlib each embed, own and migrate their own
// chain, and the only writers of this one are the split tool and the tests that
// simulate an old instance for it.
//
// So a file added here reaches no live database, and a test that reads a table
// through it is reading a schema production does not have. That is not
// hypothetical: `arizuko budget set` wrote its cap into this chain while routd's
// gate read routd.db, so every cap the verb reported as set was invisible to
// enforcement (BUGS Q1). New schema belongs in the owning daemon's chain.
//
// This test is the enforcement. It parses the chain out of the tree — one entry
// per file, version taken from the name — and compares the result against
// frozenChain below, which is the whole expectation. TestSQLLivesOnlyInTheStorageLayer
// and TestEveryOpenGoesThroughThisPackage guard their seams the same way.

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// frozenChain is what store/migrations held when it was frozen: 88 files
// numbered 0001..0088, dense and unique. Nothing may be added, so both numbers
// are constants rather than something the test derives from the same directory
// it is checking.
var frozenChain = struct {
	files int
	last  int
}{files: 88, last: 88}

// migrationName is the only shape db_utils.Migrate orders by: a four-digit
// version, a dash, a slug, `.sql`.
var migrationName = regexp.MustCompile(`^(\d{4})-[a-z0-9-]+\.sql$`)

func TestMigrationsAreFrozen(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	const why = "store/migrations is the FROZEN pre-split messages.db schema — no " +
		"daemon runs it, so a table added here reaches no live database. Put the " +
		"migration in the chain of the daemon that owns the table (routd/migrations, " +
		"onbod/migrations, authd/migrations, runed/migrations, chanlib/migrations)"

	seen := map[int]string{}
	var versions []int
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("migrations/%s is a directory — %s", e.Name(), why)
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("migrations/%s is not a migration file (want NNNN-slug.sql) — %s",
				e.Name(), why)
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("migrations/%s: %v", e.Name(), err)
		}
		if prev, dup := seen[v]; dup {
			t.Errorf("migrations/%s reuses version %04d, already taken by %s — %s",
				e.Name(), v, prev, why)
			continue
		}
		seen[v] = e.Name()
		versions = append(versions, v)
	}
	sort.Ints(versions)

	if len(versions) != frozenChain.files {
		t.Errorf("store/migrations holds %d files, frozen at %d%s — %s",
			len(versions), frozenChain.files, addedSince(versions, frozenChain.last), why)
	}
	if n := len(versions); n > 0 && versions[n-1] != frozenChain.last {
		t.Errorf("store/migrations ends at %04d, frozen at %04d — %s",
			versions[n-1], frozenChain.last, why)
	}
	for i, v := range versions {
		if v != i+1 {
			t.Errorf("store/migrations skips a version at %04d (%s) — the chain must stay "+
				"dense so db_utils.Migrate's version count means what it says",
				v, seen[v])
			break
		}
	}
}

// addedSince names the files past the frozen version, so the failure says which
// file to move rather than only that the count is wrong.
func addedSince(versions []int, last int) string {
	var added []string
	for _, v := range versions {
		if v > last {
			added = append(added, fmt.Sprintf("%04d", v))
		}
	}
	if len(added) == 0 {
		return ""
	}
	return " (added: " + fmt.Sprint(added) + ")"
}
