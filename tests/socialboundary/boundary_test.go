// Package socialboundary enforces spec 6/33's adapter/observer split.
//
// A social adapter covers the bot's immediate interaction landscape —
// mentions, replies, DMs, reactions on its own posts. Broad monitoring
// (hashtags, keywords, competitor accounts, trending, thread crawling) belongs
// to the observer, a different system with its own auth, rate budget and
// failure modes. Bundling them puts unbounded polling on the path that
// delivers a user's message.
//
// The boundary is a source fact, so this reads it from source — the same way
// tests/shutdown reads signal ordering.
package socialboundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../.."

// socialAdapters are the social adapters 6/33 names. twitd is here with
// the Go ones: the markers below are plain strings and the boundary is
// the same one, so the implementation language decides nothing.
var socialAdapters = []string{"bskyd", "twitd"}

// observerMarkers are the shapes that mean broad monitoring rather than
// bot-directed notification. Deliberately blunt: a false positive costs a
// comment explaining why a name is innocent, while a false negative is
// unbounded polling nobody noticed.
var observerMarkers = []string{"hashtag", "trending", "competitor"}

// feedPollExceptions records the adapters that cross the boundary, with the
// reason. It is a CLOSED list: an adapter cannot start polling a feed without
// editing this map and reading why. It is empty.
var feedPollExceptions = map[string]string{}

// adapterSource concatenates one adapter's non-test source. A Go adapter keeps
// its source in the package directory and a TypeScript one under `src/`, so both
// are read; an adapter that ships neither returns the empty string, which every
// test below reads as "carries nothing", so a missing directory can never make
// a boundary look clean.
func adapterSource(t *testing.T, name string) string {
	t.Helper()
	var files []string
	for _, pat := range []string{
		filepath.Join(root, name, "*.go"),
		filepath.Join(root, name, "src", "*.ts"),
	} {
		found, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, found...)
	}
	var b strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, ".test.ts") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(src)
	}
	return b.String()
}

// No social adapter may carry observer-shaped monitoring.
func TestSocialAdaptersCarryNoObserverLogic(t *testing.T) {
	for _, name := range socialAdapters {
		src := strings.ToLower(adapterSource(t, name))
		for _, marker := range observerMarkers {
			if strings.Contains(src, marker) {
				t.Errorf("%s mentions %q — broad monitoring belongs to the observer (6/33), "+
					"not on the path that delivers a user's message", name, marker)
			}
		}
	}
}

// A feed poll is the subtler form of the same thing. This pins the exception
// list closed.
func TestFeedPollingIsOnlyWhereItIsRecorded(t *testing.T) {
	// The marker is the shape of "fetch a listing the bot is not addressed in".
	const feedMarker = "/new.json"
	for _, name := range socialAdapters {
		src := adapterSource(t, name)
		polls := strings.Contains(src, feedMarker)
		_, allowed := feedPollExceptions[name]
		switch {
		case polls && !allowed:
			t.Errorf("%s polls a feed (%s) and is not a recorded exception — "+
				"that is observer work under 6/33; add it to feedPollExceptions "+
				"with a reason, or move it to the observer", name, feedMarker)
		case !polls && allowed:
			t.Errorf("%s is listed as a feed-poll exception but no longer polls one; "+
				"delete the entry so the list stays honest", name)
		}
	}
}
