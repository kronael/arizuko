// Package bugsindex checks that BUGS.md's "What needs you" section names
// exactly the entries that are still open.
//
// The section exists because the file is 5,000 lines and chronological, so a
// reader cannot see what waits on them without scanning it. That makes it a
// second declaration of the open set, and a second declaration drifts — an
// entry closes and stays listed, or opens and is never surfaced. Either way
// the section stops being the thing it was added to be.
package bugsindex

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const bugsPath = "../../BUGS.md"

// idInHeading pulls the entry id out of a heading. Ids are Lnnn, plus a few
// older shapes (V2, F5, P3b, A1, Q5) and some entries titled in prose.
var idInHeading = regexp.MustCompile(`^## (?:✅ FIXED [0-9-]+ |📋 RECORD )?([A-Z]+[0-9]+[a-z]?) — `)

// idInIndex pulls the ids a bullet is ABOUT — the backticked ids before its em
// dash. An id cited in the prose after it is a cross-reference (`L104` is why
// L347 is a decision, not an entry awaiting one) and must not count as listed,
// or closing it would fail this test for no reason.
var idInIndex = regexp.MustCompile("(?m)^- ((?:`[A-Z]+[0-9]+[a-z]?`(?:, )?)+)")
var idInBullet = regexp.MustCompile("`([A-Z]+[0-9]+[a-z]?)`")

func TestOpenEntriesAreListedHere(t *testing.T) {
	raw, err := os.ReadFile(bugsPath)
	if err != nil {
		t.Fatalf("read BUGS.md: %v", err)
	}
	body := string(raw)

	start := strings.Index(body, "\n## What needs you\n")
	if start < 0 {
		t.Fatal("BUGS.md has no 'What needs you' section; the queue is unreadable without one")
	}
	rest := body[start+1:]
	end := strings.Index(rest[1:], "\n## ")
	if end < 0 {
		t.Fatal("the 'What needs you' section never ends")
	}
	section := rest[:end+1]

	listed := map[string]bool{}
	for _, m := range idInIndex.FindAllStringSubmatch(section, -1) {
		for _, one := range idInBullet.FindAllStringSubmatch(m[1], -1) {
			listed[one[1]] = true
		}
	}

	// An entry is open when its heading is followed by a `Status: open` line
	// before the next heading.
	var open []string
	var id string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			if m := idInHeading.FindStringSubmatch(line); m != nil {
				id = m[1]
			} else {
				id = ""
			}
			continue
		}
		if id != "" && strings.HasPrefix(line, "- **Status:** open") {
			if !slices.Contains(open, id) {
				open = append(open, id)
			}
			id = ""
		}
	}
	if len(open) == 0 {
		t.Fatal("no open entry found; this test is reading BUGS.md wrong, not celebrating")
	}

	var missing []string
	for _, entry := range open {
		if !listed[entry] {
			missing = append(missing, entry)
		}
	}
	if len(missing) > 0 {
		t.Errorf("open and NOT in 'What needs you': %v — the section is what a reader "+
			"scans instead of the file, so an unlisted entry waits invisibly", missing)
	}

	openSet := map[string]bool{}
	for _, entry := range open {
		openSet[entry] = true
	}
	var stale []string
	for entry := range listed {
		if !openSet[entry] {
			stale = append(stale, entry)
		}
	}
	slices.Sort(stale)
	if len(stale) > 0 {
		t.Errorf("listed in 'What needs you' and NOT open: %v — a closed entry that "+
			"still asks for a decision spends the reader's attention on nothing", stale)
	}
}
