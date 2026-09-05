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
// tests/shutdown reads signal ordering. It was written after the spec's own
// migration section was found to be wrong: it claimed every social adapter was
// already thin, and reditd was not.
package socialboundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../.."

// socialAdapters are the Go social adapters 6/33 names. mastd and twitd are
// absent from this list because neither ships Go source in this tree.
var socialAdapters = []string{"bskyd", "reditd", "linkd"}

// observerMarkers are the shapes that mean broad monitoring rather than
// bot-directed notification. Deliberately blunt: a false positive costs a
// comment explaining why a name is innocent, while a false negative is
// unbounded polling nobody noticed.
var observerMarkers = []string{"hashtag", "trending", "competitor"}

// feedPollExceptions records the adapters that ALREADY cross the boundary,
// with the reason. reditd polls each configured subreddit's /new feed — a feed
// the bot is not addressed in — and it predates this spec. Removing it would
// take a working capability off a deployed adapter with no replacement, and
// the observer that should own it is unspecced, so it is recorded rather than
// deleted (6/33 §"The one exception: reditd's subreddit poll").
//
// The point of the map is that it is a CLOSED list: a second adapter cannot
// start polling a feed without editing this line and reading why.
var feedPollExceptions = map[string]string{
	"reditd": "/r/<sr>/new.json per SUBREDDITS — pending the observer",
}

func adapterSource(t *testing.T, name string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, name, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
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

// A feed poll is the subtler form of the same thing, and one adapter already
// does it. This pins the exception list closed.
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

// The bot-directed poll is what an adapter IS for, so it must not be lost while
// tightening the rule above.
func TestReditdStillPollsItsOwnInbox(t *testing.T) {
	if !strings.Contains(adapterSource(t, "reditd"), "/message/inbox.json") {
		t.Error("reditd no longer polls its own inbox — that is the notification " +
			"stream 6/33 requires an adapter to cover")
	}
}
