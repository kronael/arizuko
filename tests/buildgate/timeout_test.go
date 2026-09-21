// Package buildgate pins the invariants of the Makefile targets that gate a
// release.
//
// go test's built-in per-package budget is 10m, and a package that reaches it
// dies on a goroutine dump with NO `--- FAIL:` line — only a bare
// `FAIL <pkg> 600s`. That signature is indistinguishable at a glance from a
// flaky assertion: BUGS F37 read it as a slakd race and hunted it for six
// weeks, and BUGS L26 hit the same wall under -race. A budget go picks silently
// is the one nobody can act on, so every invocation the gate runs states one.
package buildgate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// repoRoot is the tree this test reads; buildgate sits two levels under it.
const repoRoot = "../.."

// goDefaultTimeout is go test's built-in per-package budget. A stated budget
// that does not beat it has stated nothing.
const goDefaultTimeout = 10 * time.Minute

var (
	// makeListVar captures `DAEMONS = a b c`, the lists `make test` recurses into.
	makeListVar = regexp.MustCompile(`(?m)^(?:DAEMONS|COMPONENTS)\s*=\s*(.*)$`)
	// goflagsTimeout captures the budget the root Makefile exports to every go
	// test it runs, its own recipes and the sub-makes alike.
	goflagsTimeout = regexp.MustCompile(`(?m)^export\s+GOFLAGS\s*\+=.*?-timeout=(\S+)`)
	// inlineTimeout matches a budget stated on the recipe line itself. go's
	// command line beats GOFLAGS, so such a line keeps the budget it names.
	inlineTimeout = regexp.MustCompile(`-timeout[= ]`)
)

// exportedTimeout returns the budget the root Makefile exports, and whether it
// exports one at all.
func exportedTimeout(t *testing.T, makefile string) (time.Duration, bool) {
	t.Helper()
	m := goflagsTimeout.FindStringSubmatch(makefile)
	if m == nil {
		return 0, false
	}
	d, err := time.ParseDuration(m[1])
	if err != nil {
		t.Fatalf("GOFLAGS states -timeout=%s, which go cannot parse: %v", m[1], err)
	}
	return d, true
}

// goTestRecipes returns every recipe line in path that runs go test, keyed by
// line number. Recipe lines are tab-indented, which is what separates them from
// the comments discussing the same command.
func goTestRecipes(t *testing.T, path string) map[int]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[int]string{}
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "\t") && strings.Contains(line, "go test") {
			out[i+1] = line
		}
	}
	return out
}

// subMakefiles returns the per-daemon and per-component Makefiles `make test`
// recurses into, read out of the root Makefile's own lists rather than a copy
// here that can go stale.
func subMakefiles(t *testing.T, makefile string) []string {
	t.Helper()
	var out []string
	for _, m := range makeListVar.FindAllStringSubmatch(makefile, -1) {
		for _, dir := range strings.Fields(m[1]) {
			out = append(out, filepath.Join(repoRoot, dir, "Makefile"))
		}
	}
	if len(out) == 0 {
		t.Fatal("no DAEMONS or COMPONENTS list found in the root Makefile — this test reads them from it")
	}
	return out
}

func readRootMakefile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	return string(b)
}

// TestExportedTimeoutBeatsGoDefault fails when the root Makefile states no
// per-package budget, or states one go would have applied anyway.
func TestExportedTimeoutBeatsGoDefault(t *testing.T) {
	got, ok := exportedTimeout(t, readRootMakefile(t))
	if !ok {
		t.Fatalf("the root Makefile exports no GOFLAGS -timeout, so every recipe that names none "+
			"runs at go's silent %s default; reaching it prints a goroutine dump and no `--- FAIL:` line", goDefaultTimeout)
	}
	if got <= goDefaultTimeout {
		t.Errorf("GOFLAGS states -timeout=%s, which does not beat go's own %s default: "+
			"a budget that changes nothing leaves the suite where BUGS F37 found it", got, goDefaultTimeout)
	}
}

// TestEveryGoTestStatesATimeout fails when a go test the release gate runs would
// fall back to go's silent default.
func TestEveryGoTestStatesATimeout(t *testing.T) {
	root := readRootMakefile(t)
	_, exported := exportedTimeout(t, root)

	paths := append([]string{filepath.Join(repoRoot, "Makefile")}, subMakefiles(t, root)...)
	for _, path := range paths {
		for line, recipe := range goTestRecipes(t, path) {
			if inlineTimeout.MatchString(recipe) || exported {
				continue
			}
			t.Errorf("%s:%d runs go test with no -timeout and nothing exports one, so it inherits "+
				"go's silent %s default:\n\t%s", path, line, goDefaultTimeout, strings.TrimSpace(recipe))
		}
	}
}
