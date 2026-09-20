// Package specrefs checks that every markdown link under specs/ resolves.
//
// A spec that moves takes its filename with it, and nothing else in the repo
// notices: the corpus has no build step, so a link is only ever verified by a
// reader clicking it. The 2026-08-27 cockpit move rewrote 22 files by hand.
package specrefs

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../.."

// sourceFiles is the tracked set — the same boundary BUGS L43 measured. Asking
// git rather than walking keeps it identical to .gitignore: the vendored
// reference repos under refs/ and the agent worktrees under .claude/ are not
// this repo's reference graph, and a hand-kept skip list drifts from the file
// that decides.
func sourceFiles(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		switch filepath.Ext(f) {
		case ".md", ".go", ".ts", ".html":
			files = append(files, filepath.Join(root, f))
		}
	}
	return files
}

// TestEverySpecLinkTargetExists checks the other half: an ID can be live while
// the filename beside it is wrong, and a link a reader clicks still 404s.
// Anchors and query strings are cut before the check — the file is what has to
// exist. Only markdown under specs/ is checked; that is the graph this package
// owns, and every other tree has its own test.
func TestEverySpecLinkTargetExists(t *testing.T) {
	linkRe := regexp.MustCompile(`\]\(([^)\s]+\.md)(?:#[^)\s]*)?\)`)
	specDir := filepath.Join(root, "specs") + string(filepath.Separator)
	for _, f := range sourceFiles(t) {
		if filepath.Ext(f) != ".md" || !strings.HasPrefix(f, specDir) {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range linkRe.FindAllStringSubmatch(string(body), -1) {
			target := m[1]
			if strings.HasPrefix(target, "http") {
				continue
			}
			// A link written from the repo root resolves from there; every
			// other form is relative to the file that carries it.
			p := filepath.Join(filepath.Dir(f), target)
			if strings.HasPrefix(target, "specs/") {
				p = filepath.Join(root, target)
			}
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: link target %q does not exist", rel(f), target)
			}
		}
	}
}

func rel(p string) string { return strings.TrimPrefix(p, root+"/") }

// TestEveryDependsEntryResolves checks the frontmatter the link check cannot
// see. `depends:` names specs by stem, not by markdown link, so a renumber
// updates every visible pointer and leaves this one silently naming the old
// ID — which is exactly what the 2026-08-27 cockpit move did to four files.
//
// Four forms are in use: a bare stem in the same phase (`28-cockpit-index`), a
// phase-qualified stem (`5/13-ext-mcp`), a repo-rooted one
// (`specs/5/32-acl-unified`) and a relative one (`../5/14-credentials`).
func TestEveryDependsEntryResolves(t *testing.T) {
	dependsRe := regexp.MustCompile(`(?m)^depends:\s*\[([^\]]*)\]`)
	specDir := filepath.Join(root, "specs") + string(filepath.Separator)
	checked := 0
	for _, f := range sourceFiles(t) {
		if filepath.Ext(f) != ".md" || !strings.HasPrefix(f, specDir) {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		m := dependsRe.FindSubmatch(body)
		if m == nil {
			continue
		}
		for _, raw := range strings.Split(string(m[1]), ",") {
			entry := strings.Trim(strings.TrimSpace(raw), `"'`)
			if entry == "" {
				continue
			}
			var p string
			entry = strings.TrimPrefix(entry, "specs/")
			switch {
			case strings.HasPrefix(entry, "../"):
				p = filepath.Join(filepath.Dir(f), entry+".md")
			case strings.Contains(entry, "/"):
				p = filepath.Join(root, "specs", entry+".md")
			default:
				p = filepath.Join(filepath.Dir(f), entry+".md")
			}
			checked++
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: depends entry %q resolves to no spec", rel(f), entry)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no depends: entries parsed — the frontmatter format changed")
	}
}

// specIDRe matches a spec ID written as prose in backticks — `6/16`, `5/A`,
// `specs/5/33`. That is how most of the corpus cites a spec, and it is the form
// no other check sees: a link check reads `](path.md)` and the depends check
// reads frontmatter, so a prose ID can name a retired number while the build
// stays green. One retirement on 2026-09-05 left 25 such pointers across eight
// files.
var specIDRe = regexp.MustCompile("`(?:specs/)?([1-9][0-9]?)/([0-9]{1,2}|[A-Z])`")

// historyFiles record what was true when they were written, so a retired ID in
// them is correct and must stay. Everything else states the present.
func isHistory(rel string) bool {
	return rel == "BUGS.md" || rel == "CHANGELOG.md" ||
		strings.Contains(rel, "/migrations/") || strings.HasPrefix(rel, ".diary/")
}

// TestEveryBacktickSpecIDResolves is the guard for prose pointers. A phase
// directory that does not exist is not an error — phases are planned before
// they are written — but a phase that exists and does not hold the ID is: that
// is a reader sent to a spec nobody can open.
func TestEveryBacktickSpecIDResolves(t *testing.T) {
	checked := 0
	for _, f := range sourceFiles(t) {
		if filepath.Ext(f) != ".md" || isHistory(rel(f)) {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range specIDRe.FindAllStringSubmatch(string(body), -1) {
			phase, id := m[1], m[2]
			phaseDir := filepath.Join(root, "specs", phase)
			if _, err := os.Stat(phaseDir); err != nil {
				continue
			}
			checked++
			hits, err := filepath.Glob(filepath.Join(phaseDir, id+"-*.md"))
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) == 0 {
				t.Errorf("%s: cites %s, and specs/%s holds no %s-*.md",
					rel(f), m[0], phase, id)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no backtick spec IDs found — the pattern stopped matching the corpus")
	}
	t.Logf("resolved %d backtick spec IDs", checked)
}
