package webdocs

// Every service the release can deploy has a components page and a row on the
// components index. A package's compose fragment, template/services/<kind>.yml,
// is the declaration; a page that does not exist for it is a daemon an operator
// can run and cannot read about. A second account's fragment,
// <kind>-<label>.yml, is the same daemon and needs no page of its own.
// documentedElsewhere is a closed list: a kind leaves it when it gets a page
// or stops shipping, and the test fails on both.
//
// Mutation proof, run when the test was written: moving components/gitd.html
// aside, and separately deleting gitd's row from components/index.html, each
// turned TestEveryServiceKindHasAComponentPage red.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const servicesDir = "../../template/services"

var documentedElsewhere = map[string]string{
	"extenddb": "concepts/store.html — the store server, not an arizuko daemon",
	"kokoro":   "components/ttsd.html — the speech backend ttsd fronts",
}

func serviceKinds(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		t.Fatalf("read %s: %v", servicesDir, err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".yml"); ok && !e.IsDir() {
			names[n] = true
		}
	}
	kinds := map[string]bool{}
	for n := range names {
		if base, _, ok := strings.Cut(n, "-"); ok && names[base] {
			continue
		}
		kinds[n] = true
	}
	return kinds
}

func TestEveryServiceKindHasAComponentPage(t *testing.T) {
	compDir := filepath.Join(pubDir, siteRoot, "components")
	index, err := os.ReadFile(filepath.Join(compDir, "index.html"))
	if err != nil {
		t.Fatalf("read components index: %v", err)
	}
	kinds := serviceKinds(t)
	if len(kinds) < 10 {
		t.Fatalf("only %d service kinds found in %s; the path is wrong", len(kinds), servicesDir)
	}
	checked := 0
	for kind := range kinds {
		page := filepath.Join(compDir, kind+".html")
		_, statErr := os.Stat(page)
		if why, ok := documentedElsewhere[kind]; ok {
			if statErr == nil {
				t.Errorf("%s now has %s; drop it from documentedElsewhere (%s)", kind, page, why)
			}
			continue
		}
		checked++
		if statErr != nil {
			t.Errorf("template/services/%s.yml ships, and %s does not exist", kind, page)
		}
		if !strings.Contains(string(index), `href="`+kind+`.html"`) {
			t.Errorf("components/index.html has no row linking %s.html", kind)
		}
	}
	for kind := range documentedElsewhere {
		if !kinds[kind] {
			t.Errorf("documentedElsewhere names %q, which template/services no longer ships", kind)
		}
	}
	t.Logf("checked %d service kinds", checked)
}
