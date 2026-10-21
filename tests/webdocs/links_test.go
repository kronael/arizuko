// Package webdocs tests the static operator site in template/web/pub as
// content: every internal link resolves, every spec the pages cite is in the
// corpus, and every sidebar is the render of template/web/nav.json.
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

// specCitationExempt lists the pages that may still name a spec, keyed by the
// path under pubDir/siteRoot. Every other page states its facts and carries no
// spec path and no spec ID: a spec points at what it changes and nothing
// points back, so deleting a spec never breaks a page.
//
// changelog/index.html is a record of what was true when it was written.
// research/index.html sits outside the sidebar and is linked from the landing
// page: it is a dated write-up.
var specCitationExempt = map[string]bool{
	"changelog/index.html": true,
	"research/index.html":  true,
}

// citesSpecAllowed reports whether the site file f may name a spec.
func citesSpecAllowed(f string) bool {
	rel, err := filepath.Rel(filepath.Join(pubDir, siteRoot), f)
	return err == nil && specCitationExempt[filepath.ToSlash(rel)]
}

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
	// A regex that stops matching passes over every page. Run it over a planted
	// sample first: the first must match, the second must not.
	if !specPathRe.MatchString(`<a href="https://github.com/o/r/blob/main/specs/5/17-openapi-mcp.md">`) ||
		specPathRe.MatchString(`the specs/ directory and the 5/17 handle`) {
		t.Fatal("specPathRe no longer separates a spec path from prose; the extraction is broken")
	}
	checked := 0
	for _, f := range siteFiles(t, ".html", ".js") {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range specPathRe.FindAllStringSubmatch(string(body), -1) {
			if !citesSpecAllowed(f) {
				t.Errorf("%s cites %s — state the fact in the page instead of "+
					"naming a spec", f, m[0])
				continue
			}
			checked++
			target := filepath.Join(specsDir, m[1], m[2]+".md")
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s cites %s, which is not in the corpus — repoint it "+
					"at the spec that holds the claim", f, m[0])
			}
		}
	}
	t.Logf("resolved %d spec paths on exempt pages", checked)
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
	// Same self-check as the path test, over the three spellings the pages use.
	for _, sample := range []string{"spec 5/17", "spec&nbsp;5/17", "see Spec: 5/17."} {
		if !specIDRe.MatchString(sample) {
			t.Fatalf("specIDRe does not match %q; the extraction is broken", sample)
		}
	}
	if specIDRe.MatchString("chapter 5/17 of the guide, 3/4 of the pages") {
		t.Fatal("specIDRe matches a plain fraction; the extraction is broken")
	}
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
			if !citesSpecAllowed(f) {
				t.Errorf("%s cites %q — state the fact in the page instead of "+
					"naming a spec", f, id)
				continue
			}
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
	t.Logf("resolved %d prose spec IDs on exempt pages", checked)
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
