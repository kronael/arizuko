package webdocs

// The design-system guard. template/web/CLAUDE.md §"Visual design system"
// says every value lives in assets/hub.css and a page carries classes only.
// These tests fail on the two ways a page carries a value of its own (a
// <style> tag, a colour in a style attribute) and on the two ways the shell
// wears away (a page with no sidebar, a sidebar missing a section).

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	styleTagRe = regexp.MustCompile(`(?i)<style[\s>]`)

	// styleAttrRe captures the value of an inline style attribute, in either
	// quote style.
	styleAttrRe = regexp.MustCompile(`(?is)\bstyle\s*=\s*(?:"([^"]*)"|'([^']*)')`)

	// colourRe is what makes an inline style a defect: a colour written on a
	// page does not follow the theme toggle. A style that carries only
	// geometry is not matched. The var() alternative lists the colour tokens
	// of hub.css :root.
	colourRe = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|\brgba?\(|\bhsla?\(|\bcolor-mix\(|var\(\s*--(?:bg|card|code-bg|border|bright|fg|dim|accent[23]?)\b`)
)

// styleExceptions maps a page, by its path under the site root, to the reason
// it may break the rule. An entry is a decision, so the list starts empty.
var styleExceptions = map[string]string{}

// shellLess is every page that does not carry the docs shell: the landing page
// by design, and four zero-second redirects that no reader sees.
var shellLess = map[string]bool{
	"index.html":                    true,
	"components/crackbox.html":      true,
	"crackbox/index.html":           true,
	"crackbox/reference/index.html": true,
	"examples/chat-sdk.html":        true,
}

// navSections is the top-level list every sidebar carries, in order. The site
// has no build step, so each page holds its own copy of the sidebar.
var navSections = []string{
	"concepts", "reference", "howto", "components", "products",
	"security", "strategy", "research", "changelog",
}

// lineOf returns the 1-based line of byte offset i in s.
func lineOf(s string, i int) int {
	return 1 + strings.Count(s[:i], "\n")
}

// rel names a page by its path under the site root.
func rel(t *testing.T, f string) string {
	t.Helper()
	r, err := filepath.Rel(filepath.Join(pubDir, siteRoot), f)
	if err != nil {
		t.Fatalf("rel %s: %v", f, err)
	}
	return filepath.ToSlash(r)
}

// styleViolations reports every <style> tag and every colour-carrying inline
// style attribute in body, as "line N: ..." strings.
func styleViolations(body string) []string {
	var out []string
	for _, loc := range styleTagRe.FindAllStringIndex(body, -1) {
		out = append(out, fmt.Sprintf("line %d: <style> block", lineOf(body, loc[0])))
	}
	for _, loc := range styleAttrRe.FindAllStringSubmatchIndex(body, -1) {
		val := ""
		for _, g := range []int{2, 4} {
			if loc[g] >= 0 {
				val = body[loc[g]:loc[g+1]]
			}
		}
		if c := colourRe.FindString(val); c != "" {
			out = append(out, fmt.Sprintf("line %d: inline style carries a colour (%s)", lineOf(body, loc[0]), c))
		}
	}
	return out
}

// TestWebDocsPagesCarryNoStyleOfTheirOwn fails on a <style> tag, or on an
// inline style attribute carrying a colour, in any page.
func TestWebDocsPagesCarryNoStyleOfTheirOwn(t *testing.T) {
	scanned := 0
	for _, f := range htmlFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		scanned++
		name := rel(t, f)
		if _, ok := styleExceptions[name]; ok {
			continue
		}
		for _, v := range styleViolations(string(body)) {
			t.Errorf("%s %s — the value belongs in assets/hub.css under a class, "+
				"or the page does not need it (template/web/CLAUDE.md §Forbidden)", f, v)
		}
	}
	// 120 today.
	if scanned < 100 {
		t.Fatalf("only %d pages scanned for style rules; the walk is broken", scanned)
	}
}

// TestStyleGuardSeesWhatItGuards runs the detector over samples that must
// trip it and samples that must not, so a regex that stops matching fails
// here instead of reporting a clean site.
func TestStyleGuardSeesWhatItGuards(t *testing.T) {
	bad := []string{
		"<style>.x{}</style>",
		"<STYLE type=\"text/css\">",
		`<p style="color: #fff">`,
		`<p style="color:#8b6914">`,
		`<p style='background: rgb(1,2,3)'>`,
		`<p style="background: rgba(1,2,3,.5)">`,
		`<p style="color: hsl(10 20% 30%)">`,
		`<p style="background: color-mix(in srgb, red 10%, blue)">`,
		`<p style="color: var(--dim)">`,
		`<p style="border: 1px solid var(--border)">`,
		`<p style="color: var(--accent2)">`,
		"<p\n style=\"margin: 0;\n color: var(--fg)\">",
	}
	for _, s := range bad {
		if len(styleViolations(s)) == 0 {
			t.Errorf("detector missed %q", s)
		}
	}
	ok := []string{
		`<p class="dim">`,
		`<p style="margin-top: 2em">`,
		`<a href="#a" id="style">`,
		`<link rel="stylesheet" href="../assets/hub.css">`,
		`<p style="width: var(--w-nav)">`,
	}
	for _, s := range ok {
		if v := styleViolations(s); len(v) != 0 {
			t.Errorf("detector flagged %q: %v", s, v)
		}
	}
	if got := styleViolations("a\nb\n<style>"); len(got) != 1 || !strings.HasPrefix(got[0], "line 3:") {
		t.Errorf("line number wrong: %v", got)
	}
}

// TestWebDocsShellLessPagesAreTheKnownFive requires every page without the
// docs shell to be one of the five named above, and every one of those to
// exist and to lack the shell.
func TestWebDocsShellLessPagesAreTheKnownFive(t *testing.T) {
	root := filepath.Join(pubDir, siteRoot)
	seen := map[string]bool{}
	pages := 0
	for _, f := range htmlFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		pages++
		name := rel(t, f)
		if strings.Contains(string(body), `class="docs-layout"`) {
			continue
		}
		seen[name] = true
		if !shellLess[name] {
			t.Errorf("%s has no docs-layout shell — copy the shell from "+
				"concepts/primitives.html (template/web/CLAUDE.md §Visual design system)", f)
		}
	}
	for name := range shellLess {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("shellLess names %s, which does not exist: %v", name, err)
		} else if !seen[name] {
			t.Errorf("shellLess names %s, but it now carries the shell — drop it from the list", name)
		}
	}
	if pages < 100 {
		t.Fatalf("only %d pages scanned for the shell; the walk is broken", pages)
	}
}

// TestWebDocsNavListsEverySection requires every sidebar to carry all nine
// top-level sections, in order.
func TestWebDocsNavListsEverySection(t *testing.T) {
	root := filepath.Join(pubDir, siteRoot)
	for _, s := range navSections {
		if _, err := os.Stat(filepath.Join(root, s, "index.html")); err != nil {
			t.Fatalf("navSections names %q, which has no index.html: %v", s, err)
		}
	}
	navRe := regexp.MustCompile(`(?s)<nav class="docs-nav".*?</nav>`)
	sectionRe := regexp.MustCompile(`class="nav-section"\s+href="([^"]*)"`)

	pages := 0
	var bad []string
	for _, f := range htmlFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		nav := navRe.FindString(string(body))
		if nav == "" {
			continue // the shell-less pages; the test above owns those
		}
		pages++
		var got []string
		for _, m := range sectionRe.FindAllStringSubmatch(nav, -1) {
			target, ok := resolve(f, m[1])
			if !ok {
				continue
			}
			r, err := filepath.Rel(root, filepath.Dir(target))
			if err != nil {
				t.Fatalf("rel %s: %v", target, err)
			}
			got = append(got, filepath.ToSlash(r))
		}
		if strings.Join(got, ",") != strings.Join(navSections, ",") {
			bad = append(bad, rel(t, f)+": "+strings.Join(got, ","))
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("sidebar does not list the nine sections in order (%s)", b)
	}
	// 115 today.
	if pages < 100 {
		t.Fatalf("only %d sidebars found; the extraction is broken", pages)
	}
}
