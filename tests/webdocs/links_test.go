// Package webdocs tests the static operator site in template/web/pub as
// content: every internal link resolves, and the hand-copied product nav in
// every page names exactly the pages that exist.
//
// The site has no build step, so nothing but a test can catch a dangling
// link. Retiring the aws-devops product (242b7d10) deleted its two pages and
// left 28 `../aws-devops/` nav entries plus one landing card behind — 29 live
// 404s that survived four months and a `products` catalog test, because that
// test compares directory sets and never reads an href.
package webdocs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// pubDir is the source of truth for the site; `arizuko create` copies it
// verbatim into each instance's <data-dir>/web/pub.
const pubDir = "../../template/web/pub"

// siteRoot is the one site in this repo. /pub/ also serves sibling sites
// (krons/, agents/) that live in other trees, so an absolute link into one of
// those is legitimate and unverifiable from here.
const siteRoot = "arizuko"

var (
	linkRe = regexp.MustCompile(`(?i)(?:href|src)\s*=\s*"([^"]*)"`)
	idRe   = regexp.MustCompile(`(?i)\b(?:id|name)\s*=\s*"([^"]+)"`)
)

func htmlFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(pubDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".html") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", pubDir, err)
	}
	if len(out) < 50 {
		t.Fatalf("only %d html files under %s — the path is wrong and every "+
			"assertion below would pass vacuously", len(out), pubDir)
	}
	return out
}

// TestWebDocsInternalLinksResolve walks every href and src on the site and
// requires the target to exist on disk. A directory link ("../github/") means
// that directory's index.html, which is how vited serves it.
func TestWebDocsInternalLinksResolve(t *testing.T) {
	checked := 0
	for _, f := range htmlFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range linkRe.FindAllStringSubmatch(string(body), -1) {
			target, ok := resolve(f, m[1])
			if !ok {
				continue
			}
			checked++
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s links to %q — no such file (%s)", f, m[1], target)
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d internal links found; the extraction is broken", checked)
	}
}

// TestWebDocsFragmentsResolve requires every "page.html#anchor" to land on an
// id that page actually carries. A renamed heading otherwise scrolls the
// reader to the top of a long reference page with no error.
func TestWebDocsFragmentsResolve(t *testing.T) {
	ids := map[string]map[string]bool{}
	idsOf := func(path string) map[string]bool {
		if s, ok := ids[path]; ok {
			return s
		}
		s := map[string]bool{}
		if body, err := os.ReadFile(path); err == nil {
			for _, m := range idRe.FindAllStringSubmatch(string(body), -1) {
				s[m[1]] = true
			}
		}
		ids[path] = s
		return s
	}

	checked := 0
	for _, f := range htmlFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range linkRe.FindAllStringSubmatch(string(body), -1) {
			raw := strings.TrimSpace(m[1])
			path, frag, found := strings.Cut(raw, "#")
			if !found || frag == "" {
				continue
			}
			target := f
			if path != "" {
				var ok bool
				if target, ok = resolve(f, path); !ok {
					continue
				}
				if _, err := os.Stat(target); err != nil {
					continue // already reported by TestWebDocsInternalLinksResolve
				}
			}
			checked++
			if !idsOf(target)[frag] {
				t.Errorf("%s links to %q — %s carries no id=%q", f, raw, target, frag)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d fragment links found; the extraction is broken", checked)
	}
}

// TestWebDocsUseRelativeLinks enforces template/web/CLAUDE.md §"Style rules":
// the site is served from a subpath, so an absolute "/pub/arizuko/..." breaks
// the moment it is mounted anywhere else. Absolute links into a SIBLING site
// under /pub/ are fine — those trees are not in this repo.
func TestWebDocsUseRelativeLinks(t *testing.T) {
	for _, f := range htmlFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range linkRe.FindAllStringSubmatch(string(body), -1) {
			u := strings.TrimSpace(m[1])
			if strings.HasPrefix(u, "/pub/"+siteRoot+"/") || u == "/pub/"+siteRoot+"/" {
				t.Errorf("%s links to %q — use a relative path; the site is "+
					"served from a subpath (template/web/CLAUDE.md §Style rules)", f, u)
			}
		}
	}
}

// resolve turns one href into a filesystem path, or reports that it is not
// ours to check. Anything off-site (scheme, protocol-relative, mailto, a bare
// fragment) and any absolute path into a sibling /pub/ site is skipped.
func resolve(from, raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	if u == "" || strings.HasPrefix(u, "#") {
		return "", false
	}
	for _, p := range []string{"http://", "https://", "//", "mailto:", "data:", "javascript:", "tel:"} {
		if strings.HasPrefix(u, p) {
			return "", false
		}
	}
	path, _, _ := strings.Cut(u, "#")
	path, _, _ = strings.Cut(path, "?")
	if path == "" {
		return "", false
	}
	var target string
	if strings.HasPrefix(path, "/") {
		if !strings.HasPrefix(path, "/pub/"+siteRoot+"/") {
			return "", false // a sibling site, not in this repo
		}
		target = filepath.Join(pubDir, strings.TrimPrefix(path, "/pub/"))
	} else {
		target = filepath.Join(filepath.Dir(from), path)
	}
	if strings.HasSuffix(path, "/") {
		target = filepath.Join(target, "index.html")
	}
	return target, true
}

// TestProductNavMatchesCatalog gates step 2 of template/web/CLAUDE.md
// §"Adding a new product": the nav-children list is hand-copied into every
// page under products/, so a new product means 30 edits and a retired one
// means 30 deletions. TestProductCatalogMatchesCorpus checks the directories;
// this checks that all 30 copies agree with them.
func TestProductNavMatchesCatalog(t *testing.T) {
	productsDir := filepath.Join(pubDir, siteRoot, "products")
	entries, err := os.ReadDir(productsDir)
	if err != nil {
		t.Fatalf("read %s: %v", productsDir, err)
	}
	want := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			want[e.Name()] = true
		}
	}
	if len(want) < 5 {
		t.Fatalf("only %d product pages found — the path is wrong", len(want))
	}

	navRe := regexp.MustCompile(`(?s)<ul class="nav-children">(.*?)</ul>`)
	hrefRe := regexp.MustCompile(`href="(?:\.\./)?([a-z0-9-]+)/"`)

	pages := 0
	for _, f := range htmlFiles(t) {
		if !strings.HasPrefix(filepath.ToSlash(f), filepath.ToSlash(productsDir)+"/") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		nav := navRe.FindStringSubmatch(string(body))
		if nav == nil {
			t.Errorf("%s has no <ul class=\"nav-children\"> — every product page carries the list", f)
			continue
		}
		pages++
		got := map[string]bool{}
		for _, m := range hrefRe.FindAllStringSubmatch(nav[1], -1) {
			got[m[1]] = true
		}
		for name := range want {
			if !got[name] {
				t.Errorf("%s: nav omits product %q", f, name)
			}
		}
		for name := range got {
			if !want[name] {
				t.Errorf("%s: nav names %q, which has no page — a retired product "+
					"leaves one dead entry per page", f, name)
			}
		}
	}
	if pages < len(want) {
		t.Fatalf("only %d product pages carried a nav list, want at least %d", pages, len(want))
	}
}

// TestProductPagerChainIsWhole requires every rel="prev"/rel="next" on a
// product page to point at a page that exists and to point BACK. A broken
// pager is how the aws-devops removal left `discord-team` pointing forward
// into nothing and `reality` pointing back into nothing.
func TestProductPagerChainIsWhole(t *testing.T) {
	productsDir := filepath.Join(pubDir, siteRoot, "products")
	pagerRe := regexp.MustCompile(`(?i)<a\s+rel="(prev|next)"\s+href="([^"]+)"`)

	type link struct{ from, rel, target string }
	var links []link
	for _, f := range htmlFiles(t) {
		if !strings.HasPrefix(filepath.ToSlash(f), filepath.ToSlash(productsDir)+"/") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range pagerRe.FindAllStringSubmatch(string(body), -1) {
			target, ok := resolve(f, m[2])
			if !ok {
				continue
			}
			links = append(links, link{from: filepath.Clean(f), rel: m[1], target: filepath.Clean(target)})
		}
	}
	if len(links) < 10 {
		t.Fatalf("only %d pager links found under %s; the extraction is broken", len(links), productsDir)
	}

	// The chain starts and ends at the products index; those two links are
	// terminators and have nothing to point back. Seven products carry no
	// pager at all — that gap is not this test's business, only the
	// soundness of the chain that does exist.
	terminator := filepath.Clean(filepath.Join(productsDir, "index.html"))

	have := map[string]bool{}
	for _, l := range links {
		have[l.rel+" "+l.from+" -> "+l.target] = true
	}
	var broken []string
	for _, l := range links {
		if _, err := os.Stat(l.target); err != nil {
			broken = append(broken, l.from+" "+l.rel+" -> "+l.target+" (missing)")
			continue
		}
		if l.target == terminator {
			continue
		}
		back := "next"
		if l.rel == "next" {
			back = "prev"
		}
		if !have[back+" "+l.target+" -> "+l.from] {
			broken = append(broken, l.from+" "+l.rel+" -> "+l.target+" (not reciprocated)")
		}
	}
	sort.Strings(broken)
	for _, b := range broken {
		t.Errorf("pager chain broken: %s", b)
	}
}
