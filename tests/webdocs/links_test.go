// Package webdocs tests the static operator site in template/web/pub as
// content: every internal link resolves, every spec the pages cite is in the
// corpus, and the hand-copied product nav in every page names exactly the
// pages that exist.
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

	// specPathRe matches a spec named by filename — in an href
	// ("…/blob/main/specs/5/17-openapi-mcp.md"), in prose, or in a JS
	// comment. specIDRe matches the shorter form the pages also use, where
	// the slug is left off: "spec 5/17", "spec&nbsp;5/17", "specs/5/17".
	// All three are in the tree, so all three are checked.
	specPathRe = regexp.MustCompile(`specs/([1-9][0-9]?)/([0-9A-Za-z][0-9A-Za-z_.-]*)\.md`)
	// The separator class carries a COLON as well as whitespace: a page writes
	// "Spec:&nbsp;<phase>/<id>." as readily as "spec <phase>/<id>", and a class
	// without it let five dead IDs through on the one page a public reader hits
	// (reference/mcp.html, BUGS L43).
	//
	// The example is spelled with placeholders on purpose: a real-looking number in
	// a comment is a citation to every grep that scans for them, and
	// TestEverySpecCitationInGoResolves counted this line as a dead `7/F`.
	specIDRe = regexp.MustCompile(`(?i)\bspecs?(?::?(?:[ \t\x{00A0}]+|&nbsp;)|/)([1-9][0-9]?)/([0-9]{1,2}|[A-Za-z][0-9]?)\b`)
)

// specsDir is the corpus the site cites. It sits outside pubDir, so the
// site's own link check cannot see these: they are written as absolute
// github.com URLs, as prose, or as both.
const specsDir = "../../specs"

func htmlFiles(t *testing.T) []string {
	t.Helper()
	out := siteFiles(t, ".html")
	if len(out) < 50 {
		t.Fatalf("only %d html files under %s — the path is wrong and every "+
			"assertion below would pass vacuously", len(out), pubDir)
	}
	return out
}

// siteFiles walks everything the site ships with one of the given
// extensions. The spec checks want .js too: the browser SDK carries its
// protocol pointer in a header comment, and that pointer went stale with
// the rest (BUGS L220).
func siteFiles(t *testing.T, exts ...string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(pubDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		for _, e := range exts {
			if strings.HasSuffix(path, e) {
				out = append(out, path)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", pubDir, err)
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

// TestWebDocsSpecPathsResolve requires every spec the site names by filename
// to exist in the corpus. TestWebDocsInternalLinksResolve cannot see these:
// the site writes them as absolute github.com URLs, which resolve() skips as
// off-site, and as bare prose in a <code> span, which carries no href at all.
// Sixteen were dead when this test was written, one of them cited ten times
// on a single page (BUGS L220).
func TestWebDocsSpecPathsResolve(t *testing.T) {
	checked := 0
	for _, f := range siteFiles(t, ".html", ".js") {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range specPathRe.FindAllStringSubmatch(string(body), -1) {
			checked++
			target := filepath.Join(specsDir, m[1], m[2]+".md")
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s cites %s, which is not in the corpus — repoint it "+
					"at the spec that holds the claim", f, m[0])
			}
		}
	}
	t.Logf("resolved %d spec paths", checked)
	// 99 at the C5 docs fold, 69 at C7, 52 at C9 and 37 at C13; each fold moves
	// spec links out of the pages. The floor exists so a regex that stops
	// matching fails loudly instead of passing over nothing; it is not a target.
	if checked < 37 {
		t.Fatalf("only %d spec paths found; the extraction is broken", checked)
	}
}

// TestWebDocsSpecIDsResolve covers the shorter form the same pages use —
// "spec 5/17", "spec&nbsp;5/17", "specs/5/17" — where the slug is left off
// and only a glob can resolve it. A renumbered phase leaves this form
// dangling while every filename beside it is correct; nine were dead
// alongside the sixteen paths.
//
// A phase directory that does not exist is an error here, unlike in
// tests/specrefs: the corpus may cite a phase that is planned and unwritten,
// but a published page that does so hands its reader a 404.
func TestWebDocsSpecIDsResolve(t *testing.T) {
	checked := 0
	for _, f := range siteFiles(t, ".html", ".js") {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		s := string(body)
		for _, loc := range specIDRe.FindAllStringSubmatchIndex(s, -1) {
			// Skip an ID that is the head of a real filename: the path check
			// above already owns "specs/5/17-openapi-mcp.md", and reporting
			// it twice hides which form actually needs the edit. A trailing
			// slug that is NOT a filename stays this check's business —
			// "specs/7/D-*" is a glob two pages wrote, and skipping on the
			// bare dash would let it through.
			if headOfSpecFilename(s, loc[1]) {
				continue
			}
			id, phase, num := s[loc[0]:loc[1]], s[loc[2]:loc[3]], s[loc[4]:loc[5]]
			checked++
			hits, err := filepath.Glob(filepath.Join(specsDir, phase, num+"-*.md"))
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) == 0 {
				t.Errorf("%s cites %q, and specs/%s holds no %s-*.md — repoint it "+
					"at the spec that holds the claim", f, id, phase, num)
			}
		}
	}
	t.Logf("resolved %d prose spec IDs", checked)
	// 36 at the C4 docs fold and 26 at C9; each fold moves spec IDs out of the
	// pages, so the floor follows the count down. Same reasoning as the path
	// floor above.
	if checked < 20 {
		t.Fatalf("only %d prose spec IDs found; the extraction is broken", checked)
	}
}

// headOfSpecFilename reports whether the spec ID ending at i is followed by
// the rest of a "<num>-<slug>.md" filename, which specPathRe already checks.
func headOfSpecFilename(s string, i int) bool {
	j := i
	for j < len(s) && (s[j] == '-' || s[j] == '.' || s[j] == '_' ||
		s[j] >= '0' && s[j] <= '9' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
		j++
	}
	return strings.HasSuffix(s[i:j], ".md")
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

	// The chain starts and ends at the products index; links to it are
	// terminators and have nothing to point back. The test checks that every
	// pager link is reciprocated, not that every product is on the chain.
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
