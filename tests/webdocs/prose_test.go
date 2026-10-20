package webdocs

// The banned-phrase keeper. A doc states what is true now: history lives in
// git, CHANGELOG.md, BUGS.md and the migration notes, so a doc that narrates
// it goes stale the day the next change lands. A doc also carries no
// marketing words. The scope is every web page except the changelog, the root
// Markdown except the history files, and every README below the root except
// vendored code and the agent's own skills.
//
// Mutation proof, run when the test was written: "The store is no longer
// SQLite." planted in concepts/primitives.html and "a robust design" planted
// in README.md each turned TestDocsCarryNoHistoryOrMarketingWords red.

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is the module root as seen from this package.
const repoRoot = "../.."

// bannedRe matches a history phrase or a marketing word, case-insensitive and
// word-bounded; a line break between two words still matches. Bare
// "previously" and "legacy" stay out: "a previously-seen message" and Slack's
// "legacy mpim" are present-tense facts.
var bannedRe = regexp.MustCompile(`(?i)\b(?:` +
	`used\s+to\s+be|no\s+longer|formerly|renamed\s+from|backwards?[-\s]compat\w*|` +
	`was\s+removed|got\s+removed|has\s+been\s+(?:replaced|removed|renamed|dropped)|` +
	`as\s+before|instead\s+of\s+the\s+old|` +
	`powerful|robust|seamless(?:ly)?|elegant|scalable|intuitive|blazing|effortless|` +
	`cutting-edge|state-of-the-art|best-in-class` +
	`)\b`)

// historyFiles are the root Markdown files whose job is to record history or
// open work.
var historyFiles = map[string]bool{
	"CHANGELOG.md": true,
	"BUGS.md":      true,
	"bugs.md":      true,
	"TODO.md":      true,
	"MIGRATION.md": true,
	"USELESS.md":   true,
}

// readmeSkip holds the prefixes whose READMEs are not project docs: vendored
// code, and the agent's own skills, which route an editor.
var readmeSkip = []string{"third_party/", "ant/skills/", ".claude/"}

// bannedPhrases returns every banned phrase in body as "line N: phrase".
func bannedPhrases(body string) []string {
	var out []string
	for _, loc := range bannedRe.FindAllStringIndex(body, -1) {
		phrase := strings.Join(strings.Fields(body[loc[0]:loc[1]]), " ")
		out = append(out, fmt.Sprintf("line %d: %s", lineOf(body, loc[0]), phrase))
	}
	return out
}

func isNestedReadme(f string) bool {
	return strings.Contains(f, "/") && path.Base(f) == "README.md"
}

// proseFiles lists the tracked files the keeper reads, repo-relative.
func proseFiles(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "*.md", "*.html").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	site := "template/web/pub/" + siteRoot + "/"
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch {
		case strings.HasPrefix(f, site):
			if strings.HasSuffix(f, ".html") && !strings.HasPrefix(f, site+"changelog/") {
				files = append(files, f)
			}
		case !strings.Contains(f, "/"):
			if strings.HasSuffix(f, ".md") && !historyFiles[f] {
				files = append(files, f)
			}
		case isNestedReadme(f):
			skip := false
			for _, p := range readmeSkip {
				skip = skip || strings.HasPrefix(f, p)
			}
			if !skip {
				files = append(files, f)
			}
		}
	}
	return files
}

// TestDocsCarryNoHistoryOrMarketingWords fails on a banned phrase in any doc
// in scope. Rewrite the sentence in the present tense; the fact stays.
func TestDocsCarryNoHistoryOrMarketingWords(t *testing.T) {
	pages, readmes := 0, 0
	for _, f := range proseFiles(t) {
		body, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.HasSuffix(f, ".html") {
			pages++
		} else if isNestedReadme(f) {
			readmes++
		}
		for _, v := range bannedPhrases(string(body)) {
			t.Errorf("%s %s — state what is true now; history belongs in git "+
				"and CHANGELOG.md, and a doc carries no marketing words", f, v)
		}
	}
	if pages < 100 || readmes < 40 {
		t.Fatalf("scanned %d pages and %d READMEs; the file list is broken", pages, readmes)
	}
}

// TestBannedPhraseDetectorSeesWhatItGuards runs the detector over samples
// that must trip it and samples that must not.
func TestBannedPhraseDetectorSeesWhatItGuards(t *testing.T) {
	bad := []string{
		"The store is no longer SQLite.",
		"It is No\n  Longer read.",
		"This used to be a table.",
		"formerly gated",
		"renamed from routes",
		"kept for backwards compatibility",
		"a backward-compat shim",
		"the flag was removed",
		"the column has been dropped",
		"works as before",
		"instead of the old path",
		"a robust design",
		"Seamlessly connects",
		"state-of-the-art routing",
	}
	for _, s := range bad {
		if len(bannedPhrases(s)) == 0 {
			t.Errorf("detector missed %q", s)
		}
	}
	ok := []string{
		"a previously-seen message",
		"Slack's legacy mpim",
		"the robustness of the parser",
		"no longest prefix",
	}
	for _, s := range ok {
		if v := bannedPhrases(s); len(v) != 0 {
			t.Errorf("detector flagged %q: %v", s, v)
		}
	}
	if got := bannedPhrases("a\nb\nno longer"); len(got) != 1 || !strings.HasPrefix(got[0], "line 3:") {
		t.Errorf("line number wrong: %v", got)
	}
}
