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

// shellLess is the one page that does not carry the docs shell: the landing
// page, by design.
const shellLess = "index.html"

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

// TestWebDocsOnlyTheLandingIsShellLess requires every page except the landing
// page to carry the docs shell, and the landing page to exist without it.
func TestWebDocsOnlyTheLandingIsShellLess(t *testing.T) {
	pages, landing := 0, false
	for _, f := range htmlFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		pages++
		hasShell := strings.Contains(string(body), `class="docs-layout"`)
		if rel(t, f) == shellLess {
			landing = true
			if hasShell {
				t.Errorf("%s carries the docs shell; the landing page has none by design", f)
			}
			continue
		}
		if !hasShell {
			t.Errorf("%s has no docs-layout shell — copy the shell from "+
				"concepts/primitives.html (template/web/CLAUDE.md §Visual design system)", f)
		}
	}
	if !landing {
		t.Errorf("the landing page %s does not exist", shellLess)
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

// childSections are the sections whose pages carry their siblings in the
// sidebar. Each page holds its own copy of the list, so a page added to one
// copy and not the others is a drift no reader of the index sees.
var childSections = []string{"concepts", "reference", "howto", "components", "products"}

var sectionChildrenRe = regexp.MustCompile(`(?s)class="nav-section"\s+href="([^"]*)"[^>]*>[^<]*</a>\s*<ul class="nav-children">(.*?)</ul>`)

// sectionChildren returns the resolved targets of the nav-children list that
// sits under the sidebar entry for section, in order, and whether one exists.
func sectionChildren(f, body, section string) ([]string, bool) {
	want := filepath.Join(pubDir, siteRoot, section, "index.html")
	for _, m := range sectionChildrenRe.FindAllStringSubmatch(body, -1) {
		if target, ok := resolve(f, m[1]); !ok || filepath.Clean(target) != want {
			continue
		}
		var out []string
		for _, h := range linkRe.FindAllStringSubmatch(m[2], -1) {
			if target, ok := resolve(f, h[1]); ok {
				out = append(out, filepath.Clean(target))
			}
		}
		return out, true
	}
	return nil, false
}

// TestWebDocsSectionNavListsEverySibling requires every page in a section to
// carry the section index's nav-children list: the same targets, in the same
// order. Mutation proof, run when the test was written: one <li> removed from
// concepts/topics.html turned it red.
func TestWebDocsSectionNavListsEverySibling(t *testing.T) {
	root := filepath.Join(pubDir, siteRoot)
	index := map[string][]string{}
	for _, s := range childSections {
		f := filepath.Join(root, s, "index.html")
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		list, ok := sectionChildren(f, string(body), s)
		if !ok || len(list) < 5 {
			t.Fatalf("%s: section nav has %d children; the extraction is broken", f, len(list))
		}
		index[s] = list
	}
	pages := 0
	for _, f := range htmlFiles(t) {
		name := rel(t, f)
		section, _, _ := strings.Cut(name, "/")
		want, inSection := index[section]
		if !inSection || name == shellLess {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		pages++
		got, ok := sectionChildren(f, string(body), section)
		if !ok {
			t.Errorf("%s: sidebar has no nav-children list under %s", name, section)
			continue
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: section nav differs from %s/index.html — copy the "+
				"index's list (%d entries, want %d)", name, section, len(got), len(want))
		}
	}
	if pages < 100 {
		t.Fatalf("only %d section pages checked; the walk is broken", pages)
	}
}
