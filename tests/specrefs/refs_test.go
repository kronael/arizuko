// Package specrefs tests the spec reference graph: every `<phase>/<id>` a
// document names resolves to a live spec file or to a row in specs/RETIRED.md.
//
// Phases 6 and 7 have been renumbered three times. `specs/6/1` has been five
// different specs, `specs/7/2` six. A stale ID in a phase whose numbers were
// reused is not a dangling pointer a reader notices — it silently addresses a
// spec about something else, which is what BUGS L43 measured (44 IDs) and why
// the graph is checked rather than trusted.
package specrefs

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const root = "../.."

// refRe matches the two forms a reader actually follows: a backticked ID
// (`6/7`, `specs/5/33-paths-roles.md`) and a bare path (specs/5/33). Both
// carry the phase and the ID; anything else is prose.
var refRe = regexp.MustCompile("`(?:\\.\\./|specs/)?([0-9]{1,2})/([0-9A-Za-z]{1,4})(?:[-/][^`]*)?`" +
	`|specs?/([0-9]{1,2})/([0-9A-Za-z]{1,4})\b`)

var retiredRe = regexp.MustCompile("^\\| `([0-9]{1,2})/([0-9A-Za-z]{1,4})` \\|")

type ref struct{ phase, id string }

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

// liveSpecs is the set of IDs a spec file currently claims.
func liveSpecs(t *testing.T) map[ref]bool {
	t.Helper()
	live := map[ref]bool{}
	phases, err := os.ReadDir(filepath.Join(root, "specs"))
	if err != nil {
		t.Fatalf("read specs: %v", err)
	}
	for _, p := range phases {
		if !p.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, "specs", p.Name()))
		if err != nil {
			t.Fatalf("read specs/%s: %v", p.Name(), err)
		}
		for _, f := range files {
			name := f.Name()
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			live[ref{p.Name(), strings.SplitN(strings.TrimSuffix(name, ".md"), "-", 2)[0]}] = true
		}
	}
	return live
}

// retiredSpecs is the set of IDs specs/RETIRED.md accounts for.
func retiredSpecs(t *testing.T) map[ref]bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "specs", "RETIRED.md"))
	if err != nil {
		t.Fatalf("read RETIRED.md: %v", err)
	}
	out := map[ref]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		if m := retiredRe.FindStringSubmatch(line); m != nil {
			out[ref{m[1], m[2]}] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("RETIRED.md parsed to zero rows — the table format changed")
	}
	return out
}

func TestEverySpecReferenceResolves(t *testing.T) {
	live, retired := liveSpecs(t), retiredSpecs(t)
	bad := map[string][]string{}
	for _, f := range sourceFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		text := string(body)
		for _, m := range refRe.FindAllStringSubmatchIndex(text, -1) {
			r := ref{group(text, m, 1), group(text, m, 2)}
			if r.phase == "" {
				r = ref{group(text, m, 3), group(text, m, 4)}
			}
			if live[r] || retired[r] {
				continue
			}
			key := r.phase + "/" + r.id
			line := 1 + strings.Count(text[:m[0]], "\n")
			bad[key] = append(bad[key], rel(f)+":"+itoa(line))
		}
	}
	if len(bad) == 0 {
		return
	}
	keys := make([]string, 0, len(bad))
	for k := range bad {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Errorf("spec %s resolves to no live file and has no specs/RETIRED.md row: %s",
			k, strings.Join(bad[k], " "))
	}
}

// TestEverySpecLinkTargetExists checks the other half: an ID can be live while
// the filename beside it is wrong, which a link a reader clicks still 404s on.
func TestEverySpecLinkTargetExists(t *testing.T) {
	linkRe := regexp.MustCompile(`\]\(([^)]*specs?/[0-9]{1,2}/[^)]+\.md)\)|\]\(([0-9A-Za-z]{1,4}-[^)]+\.md)\)`)
	for _, f := range sourceFiles(t) {
		if filepath.Ext(f) != ".md" {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range linkRe.FindAllStringSubmatch(string(body), -1) {
			target := m[1]
			if target == "" {
				target = m[2]
			}
			if strings.HasPrefix(target, "http") {
				continue
			}
			p := filepath.Join(filepath.Dir(f), target)
			if !strings.HasPrefix(target, ".") && !strings.HasPrefix(target, "specs/") &&
				strings.Contains(target, "/") {
				p = filepath.Join(root, target)
			}
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: link target %q does not exist", rel(f), target)
			}
		}
	}
}

func group(text string, m []int, n int) string {
	if 2*n+1 >= len(m) || m[2*n] < 0 {
		return ""
	}
	return text[m[2*n]:m[2*n+1]]
}

func rel(p string) string { return strings.TrimPrefix(p, root+"/") }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
