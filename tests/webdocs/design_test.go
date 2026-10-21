package webdocs

// The design-system guard. template/web/CLAUDE.md §"Visual design system"
// says every value lives in assets/hub.css and a page carries classes only.
// These tests fail on the two ways a page carries a value of its own (a
// <style> tag, a colour in a style attribute) and on a page without the shell.
// nav_test.go owns the sidebar.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// rootOverflowRe finds an overflow declaration in a rule whose selector list
// names only html and body.
var rootOverflowRe = regexp.MustCompile(`(?m)^\s*((?:html|body)(?:\s*,\s*(?:html|body))*)\s*\{[^}]*\boverflow(?:-x|-y)?\s*:\s*([a-z]+)`)

// TestShellStaysSticky is BUGS L504: `overflow-x: hidden` on html and body made
// the body the scroll container, so the sticky top bar, sidebar and contents
// rail scrolled away with the page. Only `clip` and `visible` keep the viewport
// as the container that the sticky elements bind to.
func TestShellStaysSticky(t *testing.T) {
	if !rootOverflowRe.MatchString("html, body { max-width: 100vw; overflow-x: hidden; }") {
		t.Fatal("rootOverflowRe no longer matches the rule it guards")
	}
	css, err := os.ReadFile(filepath.Join(pubDir, siteRoot, "assets", "hub.css"))
	if err != nil {
		t.Fatalf("read hub.css: %v", err)
	}
	for _, m := range rootOverflowRe.FindAllStringSubmatch(string(css), -1) {
		if m[2] != "clip" && m[2] != "visible" {
			t.Errorf("hub.css sets overflow %s on %q — the body becomes the scroll "+
				"container and the sticky shell stops sticking; use clip", m[2], m[1])
		}
	}
}
