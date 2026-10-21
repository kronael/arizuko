package webdocs

// The sidebar has one declaration, template/web/nav.json, and one renderer,
// renderNav. Every page keeps a static copy of the sidebar, so the site needs
// no script to show its navigation and an agent that greps the HTML sees it.
// TestWebDocsNavIsGenerated fails on a copy that differs from the render;
// `make docs-nav` writes the render into every page.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const navFile = "../../template/web/nav.json"

var writeNav = flag.Bool("write-nav", false, "rewrite each page's sidebar from template/web/nav.json")

type navPage struct {
	Href  string `json:"href"`
	Label string `json:"label"`
}

type navSection struct {
	Dir   string    `json:"dir"`
	Label string    `json:"label"`
	Pages []navPage `json:"pages"`
}

type navDecl struct {
	Sections []navSection `json:"sections"`
}

var navBlockRe = regexp.MustCompile(`(?s)\n[ \t]*<nav class="docs-nav".*?</nav>`)

func loadNav(t *testing.T) navDecl {
	t.Helper()
	b, err := os.ReadFile(navFile)
	if err != nil {
		t.Fatalf("read %s: %v", navFile, err)
	}
	var decl navDecl
	if err := json.Unmarshal(b, &decl); err != nil {
		t.Fatalf("parse %s: %v", navFile, err)
	}
	if len(decl.Sections) < 5 {
		t.Fatalf("%s declares %d sections; the file is wrong", navFile, len(decl.Sections))
	}
	return decl
}

// renderNav returns the sidebar of the page at path page under the site root.
// The page's own section, when the sidebar lists it, is the one expanded.
func renderNav(decl navDecl, page string) string {
	depth := strings.Count(page, "/")
	toRoot := strings.Repeat("../", depth)
	dir, _, _ := strings.Cut(page, "/")
	toSection := strings.Repeat("../", depth-1)

	label := "Docs"
	for _, s := range decl.Sections {
		if s.Dir == dir {
			label = s.Label
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "    <nav class=\"docs-nav\" aria-label=%q>\n", label)
	b.WriteString("      <ul>\n")
	for _, s := range decl.Sections {
		if s.Dir != dir {
			fmt.Fprintf(&b, "        <li><a class=\"nav-section\" href=\"%s%s/\">%s</a></li>\n", toRoot, s.Dir, s.Label)
			continue
		}
		self := toSection
		if self == "" {
			self = "./"
		}
		if len(s.Pages) == 0 {
			fmt.Fprintf(&b, "        <li><a class=\"nav-section\" href=\"%s\">%s</a></li>\n", self, s.Label)
			continue
		}
		fmt.Fprintf(&b, "        <li><a class=\"nav-section\" href=\"%s\">%s</a>\n", self, s.Label)
		b.WriteString("          <ul class=\"nav-children\">\n")
		for _, p := range s.Pages {
			href := toSection + p.Href
			if href == "" {
				href = "./"
			}
			fmt.Fprintf(&b, "            <li><a href=\"%s\">%s</a></li>\n", href, p.Label)
		}
		b.WriteString("          </ul>\n")
		b.WriteString("        </li>\n")
	}
	b.WriteString("      </ul>\n")
	b.WriteString("    </nav>")
	return b.String()
}

// withNav returns body with its sidebar replaced by nav, and false when body
// carries no sidebar.
func withNav(body, nav string) (string, bool) {
	loc := navBlockRe.FindStringIndex(body)
	if loc == nil {
		return body, false
	}
	return body[:loc[0]] + "\n" + nav + body[loc[1]:], true
}

// TestWebDocsNavIsGenerated requires every sidebar to equal its render from
// template/web/nav.json, and a second render to change nothing. Run with
// -write-nav (`make docs-nav`) to write the render into the pages.
func TestWebDocsNavIsGenerated(t *testing.T) {
	decl := loadNav(t)
	pages := 0
	for _, f := range htmlFiles(t) {
		name := rel(t, f)
		if name == shellLess {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		body := string(raw)
		nav := renderNav(decl, name)
		want, ok := withNav(body, nav)
		if !ok {
			t.Errorf("%s has no docs-nav sidebar", name)
			continue
		}
		pages++
		if again, _ := withNav(want, nav); again != want {
			t.Errorf("%s: a second render changes the page; the rewrite is not a fixed point", name)
		}
		if want == body {
			continue
		}
		if *writeNav {
			if err := os.WriteFile(f, []byte(want), 0o644); err != nil {
				t.Fatalf("write %s: %v", f, err)
			}
			continue
		}
		t.Errorf("%s: sidebar differs from template/web/nav.json — run `make docs-nav`", name)
	}
	if pages < 100 {
		t.Fatalf("only %d sidebars rendered; the walk is broken", pages)
	}
}

// TestNavDeclaresEveryPage requires each section the sidebar expands to list
// every page in its directory and every subdirectory, and nothing else. A
// page missing here exists on disk and no sidebar links it.
func TestNavDeclaresEveryPage(t *testing.T) {
	root := filepath.Join(pubDir, siteRoot)
	for _, s := range loadNav(t).Sections {
		dir := filepath.Join(root, s.Dir)
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			t.Errorf("nav.json names section %q, which has no index.html", s.Dir)
			continue
		}
		if len(s.Pages) == 0 {
			continue
		}
		declared := map[string]bool{"index.html": true}
		for _, p := range s.Pages {
			target := p.Href
			if target == "" || strings.HasSuffix(target, "/") {
				target += "index.html"
			}
			if _, err := os.Stat(filepath.Join(dir, target)); err != nil {
				t.Errorf("nav.json %s lists %q, which does not exist", s.Dir, p.Href)
			}
			declared[target] = true
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			target := e.Name()
			if e.IsDir() {
				target += "/index.html"
			} else if !strings.HasSuffix(target, ".html") {
				continue
			}
			if !declared[target] {
				t.Errorf("%s/%s exists and nav.json does not list it", s.Dir, target)
			}
		}
	}
}
