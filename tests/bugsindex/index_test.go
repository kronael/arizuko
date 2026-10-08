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

// openMarker matches the words a heading uses to say an entry still waits on
// someone. `open` was the only one until 2026-10-08, when `S1b` — "PROPOSED —
// redesign, needs sign-off" — turned out to be open, unlisted, and invisible to this
// guard for two months. I then filed it a second time as `L399` without finding it.
//
// A proposal awaiting sign-off IS an open item: it is the shape most of this queue
// takes.
var openMarker = regexp.MustCompile(`(?i)\bopen\b|\bPROPOSED\b|\bneeds sign-off\b|\bawaiting sign-off\b`)

// headingSaysOpen reads the parenthesised marker a heading ends with. "fixed
// `pending`" and "one of two fixed" both contain no bare `open`, and
// "open question", "open — needs sign-off" and "PROPOSED — needs sign-off" all do.
func headingSaysOpen(heading string) bool {
	i := strings.LastIndex(heading, "(")
	if i < 0 {
		return false
	}
	return openMarker.MatchString(heading[i:])
}

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

	// An entry is open when its HEADING says so. The heading is what a reader
	// sees, and it is the only marker every entry carries — ten entries had a
	// heading saying open and no `Status: open` line at all, so a Status-only
	// reading counted 22 when the queue held 32. A `Status: open` line without
	// an open heading counts too, so neither spelling can hide an entry.
	var open []string
	var id string
	inBody := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			id = ""
			if strings.Contains(line, "✅ FIXED") || strings.Contains(line, "📋 RECORD") {
				continue
			}
			m := idInHeading.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			id = m[1]
			if headingSaysOpen(line) && !slices.Contains(open, id) {
				open = append(open, id)
			}
			continue
		}
		if id != "" && strings.HasPrefix(line, "- **Status:** open") {
			inBody[id] = true
			if !slices.Contains(open, id) {
				open = append(open, id)
			}
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
