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
var specIDRe = regexp.MustCompile(
	"`(?:specs/)?([1-9][0-9]?)/([0-9]{1,2}|[A-Z])`" +
		"|\\bspecs? ([1-9][0-9]?)/([0-9]{1,2}|[A-Z])\\b")

// discussesTheForm names a file that writes spec IDs as EXAMPLES of a pattern
// rather than as citations. Its own matcher is the subject, so a retired ID in
// it is the point. There is one, and a second entry needs the same argument.
func discussesTheForm(rel string) bool {
	return rel == "tests/webdocs/links_test.go"
}

// historyFiles record what was true when they were written, so a retired ID in
// them is correct and must stay. Everything else states the present.
func isHistory(rel string) bool {
	return rel == "BUGS.md" || rel == "bugs.md" || rel == "CHANGELOG.md" ||
		strings.Contains(rel, "/migrations/") || strings.HasPrefix(rel, ".diary/")
}

// TestEveryProseSpecIDResolves is the guard for prose pointers. A phase
// directory that does not exist is not an error — phases are planned before
// they are written — but a phase that exists and does not hold the ID is: that
// is a reader sent to a spec nobody can open.
//
// IT READS THE CODE TOO. sourceFiles collects .md, .go, .ts and .html, and this
// loop skipped everything that was not .md — so eight ids in Go named specs
// nobody could open, two of them in dashd and timed production files (BUGS
// L354). The pattern reads the unbackticked prose form for the same reason:
// that is how code cites a spec, and requiring backticks hid every one.
func TestEveryProseSpecIDResolves(t *testing.T) {
	checked := 0
	for _, f := range sourceFiles(t) {
		if isHistory(rel(f)) || discussesTheForm(rel(f)) {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range specIDRe.FindAllStringSubmatch(string(body), -1) {
			// One alternation, two shapes: the backtick groups or the prose
			// groups carry the match, never both.
			phase, id := m[1], m[2]
			if phase == "" {
				phase, id = m[3], m[4]
			}
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
				// A letter spec can be `A-name.md` with no second dash.
				if bare, gerr := filepath.Glob(filepath.Join(phaseDir, id+".md")); gerr == nil && len(bare) > 0 {
					continue
				}
				t.Errorf("%s: cites %s, and specs/%s holds no %s-*.md",
					rel(f), m[0], phase, id)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no spec IDs found — the pattern stopped matching the corpus")
	}
	t.Logf("resolved %d prose spec IDs", checked)
}

// sectionRe matches a quoted section pointer — `spec 5/8 §"Surface"` and
// `spec 6/26-personal-mode § "The mechanics"`. BOTH spacings, because the tree
// writes both and two earlier sweeps each covered one: three stale pointers sat
// in the gap between them (BUGS L356).
var sectionRe = regexp.MustCompile(`spec[s]? ([1-9][0-9]?)/([0-9A-Z][0-9A-Za-z-]*) § ?"([^"]+)"`)

// despan strips markdown backticks so a heading written “ ## Read-path: `/v1`
// only “ matches a citation that quotes it without them. Leaving them in
// reported six correct pointers as dead.
func despan(s string) string { return strings.ReplaceAll(s, "`", "") }

// unwrap joins a citation that spans two comment lines. A quoted section long
// enough to wrap carries "\n// " inside the capture, which matches no spec.
var unwrapRe = regexp.MustCompile(`\s*\n\s*(//|\*)?\s*`)

func unwrap(s string) string { return strings.TrimSpace(unwrapRe.ReplaceAllString(s, " ")) }

// TestEverySectionCitationResolves is the half no guard read. The other tests
// stop at the number, so a pointer could name a live spec and a section that
// spec dropped — 30 of them did, 13 in `5/8` alone, the most-cited spec in the
// tree.
//
// It asserts the quoted text appears ANYWHERE in the spec, not that it is a
// heading: a citation may quote a bold phrase, and demanding `^## ` would fail
// honest pointers. The claim checked is "a reader who searches for this string
// finds it".
func TestEverySectionCitationResolves(t *testing.T) {
	checked := 0
	for _, f := range sourceFiles(t) {
		if isHistory(rel(f)) || discussesTheForm(rel(f)) {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range sectionRe.FindAllStringSubmatch(string(body), -1) {
			phase, id, section := m[1], m[2], m[3]
			hits, gerr := filepath.Glob(filepath.Join(root, "specs", phase, id+"*.md"))
			if gerr != nil {
				t.Fatal(gerr)
			}
			if len(hits) == 0 {
				continue // the number itself is TestEveryProseSpecIDResolves's job
			}
			spec, rerr := os.ReadFile(hits[0])
			if rerr != nil {
				t.Fatalf("read %s: %v", hits[0], rerr)
			}
			checked++
			if !strings.Contains(strings.ToLower(despan(unwrap(string(spec)))),
				strings.ToLower(despan(unwrap(section)))) {
				t.Errorf("%s: cites %s/%s §%q, and that text is nowhere in %s",
					rel(f), phase, id, unwrap(section), filepath.Base(hits[0]))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no section citations found — the pattern stopped matching the corpus, " +
			"and an empty set is a guard that cannot fail")
	}
	t.Logf("resolved %d section citations", checked)
}
