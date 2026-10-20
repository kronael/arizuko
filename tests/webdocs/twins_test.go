package webdocs

// The twin-pin keeper. A figure or a shared block is drawn once and copied at
// most once more; the copy drifts the day one side is edited alone. Each twin
// is registered here by name, and every file that carries it must hold the
// same bytes.
//
// A Markdown side wraps its fenced block in "<!-- twin:<name> -->" and
// "<!-- /twin -->". An HTML side is "<pre id=\"twin-<name>\">", with an
// optional <code> inside; the six entities a figure may need are decoded
// before the compare.
//
// Mutation proof, run when the test was written: "rows written" changed to
// "rows writen" in README.md turned TestTwinsAreByteEqual red.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// twin names one shared block and the repo-relative files that carry it.
type twin struct {
	name  string
	files []string
}

var twins = []twin{
	{name: "six-steps", files: []string{
		"README.md",
		"template/web/pub/arizuko/concepts/primitives.html",
	}},
	{name: "install", files: []string{
		"INSTALL.md",
		"README.md",
		"template/web/pub/arizuko/howto/index.html",
	}},
	{name: "turn", files: []string{
		"ARCHITECTURE.md",
		"template/web/pub/arizuko/concepts/turn.html",
	}},
	{name: "topics", files: []string{
		"ROUTING.md",
		"template/web/pub/arizuko/concepts/topics.html",
	}},
	{name: "store", files: []string{
		"objstore/README.md",
		"template/web/pub/arizuko/concepts/store.html",
	}},
	{name: "mounts", files: []string{
		"runed/README.md",
		"template/web/pub/arizuko/components/runed.html",
	}},
	{name: "secrets", files: []string{
		"SECURITY.md",
		"template/web/pub/arizuko/concepts/secrets.html",
	}},
	{name: "egress", files: []string{
		"SECURITY.md",
		"template/web/pub/arizuko/components/egress.html",
	}},
	{name: "grants", files: []string{
		"auth/README.md",
		"template/web/pub/arizuko/concepts/grants.html",
	}},
}

var (
	fenceRe   = regexp.MustCompile("(?s)^\\s*```[^\\n]*\\n(.*?\\n)```\\s*$")
	codeTagRe = regexp.MustCompile(`(?s)^<code[^>]*>(.*)</code>$`)
	entityRe  = regexp.MustCompile(`&[#0-9A-Za-z]+;`)
)

var figureEntities = strings.NewReplacer(
	"&lt;", "<", "&gt;", ">", "&amp;", "&",
	"&rarr;", "→", "&larr;", "←", "&mdash;", "—",
)

// markdownTwin returns the fenced block between the twin markers in body.
func markdownTwin(body, name string) (string, error) {
	open := "<!-- twin:" + name + " -->"
	if n := strings.Count(body, open); n != 1 {
		return "", fmt.Errorf("%d %q markers, want 1", n, open)
	}
	rest := body[strings.Index(body, open)+len(open):]
	end := strings.Index(rest, "<!-- /twin -->")
	if end < 0 {
		return "", fmt.Errorf("%q has no closing <!-- /twin -->", open)
	}
	m := fenceRe.FindStringSubmatch(rest[:end])
	if m == nil {
		return "", fmt.Errorf("%q does not wrap exactly one fenced block", open)
	}
	return strings.TrimSuffix(m[1], "\n"), nil
}

// htmlTwin returns the decoded text of <pre id="twin-<name>"> in body.
func htmlTwin(body, name string) (string, error) {
	re := regexp.MustCompile(`(?s)<pre id="twin-` + regexp.QuoteMeta(name) + `"[^>]*>(.*?)</pre>`)
	all := re.FindAllStringSubmatch(body, -1)
	if len(all) != 1 {
		return "", fmt.Errorf("%d <pre id=\"twin-%s\"> blocks, want 1", len(all), name)
	}
	text := all[0][1]
	if m := codeTagRe.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	if strings.Contains(text, "<") {
		return "", fmt.Errorf("twin-%s carries a tag; a figure is plain text", name)
	}
	for _, e := range entityRe.FindAllString(text, -1) {
		if figureEntities.Replace(e) == e {
			return "", fmt.Errorf("twin-%s carries %s, which the twin test does not decode", name, e)
		}
	}
	return strings.TrimSuffix(figureEntities.Replace(text), "\n"), nil
}

func twinText(file, name string) (string, error) {
	body, err := os.ReadFile(filepath.Join(repoRoot, file))
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(file, ".md") {
		return markdownTwin(string(body), name)
	}
	return htmlTwin(string(body), name)
}

// TestTwinsAreByteEqual requires every registered twin to exist in each file
// that carries it, with the same bytes everywhere.
func TestTwinsAreByteEqual(t *testing.T) {
	for _, tw := range twins {
		if len(tw.files) < 2 {
			t.Errorf("twin %q names %d files; a twin has two sides", tw.name, len(tw.files))
			continue
		}
		want, err := twinText(tw.files[0], tw.name)
		if err != nil {
			t.Errorf("%s: %v", tw.files[0], err)
			continue
		}
		for _, f := range tw.files[1:] {
			got, err := twinText(f, tw.name)
			if err != nil {
				t.Errorf("%s: %v", f, err)
				continue
			}
			if got != want {
				t.Errorf("twin %q differs:\n--- %s\n%s\n--- %s\n%s", tw.name, tw.files[0], want, f, got)
			}
		}
	}
}

// TestTwinExtractorsSeeWhatTheyGuard checks both extractors on samples, so a
// regex that stops matching fails here instead of comparing two empty strings.
func TestTwinExtractorsSeeWhatTheyGuard(t *testing.T) {
	md := "x\n<!-- twin:f -->\n```text\na → b &lt;\n```\n<!-- /twin -->\n"
	got, err := markdownTwin(md, "f")
	if err != nil || got != "a → b &lt;" {
		t.Errorf("markdownTwin = %q, %v", got, err)
	}
	html := "<pre id=\"twin-f\"><code>a &rarr; b &amp;lt;\n</code></pre>"
	got, err = htmlTwin(html, "f")
	if err != nil || got != "a → b &lt;" {
		t.Errorf("htmlTwin = %q, %v", got, err)
	}
	for _, bad := range []string{
		"<pre id=\"twin-f\">a &nbsp; b</pre>",
		"<pre id=\"twin-f\">a <b>b</b></pre>",
		"<pre id=\"twin-g\">a</pre>",
	} {
		if _, err := htmlTwin(bad, "f"); err == nil {
			t.Errorf("htmlTwin accepted %q", bad)
		}
	}
	for _, bad := range []string{
		"<!-- twin:f -->\nno fence\n<!-- /twin -->",
		"<!-- twin:f -->\n```\na\n```\n",
		"no marker",
	} {
		if _, err := markdownTwin(bad, "f"); err == nil {
			t.Errorf("markdownTwin accepted %q", bad)
		}
	}
}
