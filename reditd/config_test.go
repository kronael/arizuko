package main

import (
	"testing"
	"time"
)

// The poll interval is what stands between this adapter and Reddit's rate
// limiter. A zero or negative value turns the poll loop into a busy loop
// hammering the API, which gets the account rate-limited and then banned —
// so an unusable value must land on the default, never on zero.
func TestParseDurationFloorsAtOneSecond(t *testing.T) {
	const fallback = 5 * time.Minute
	cases := map[string]time.Duration{
		// Honoured.
		"1s":    time.Second,
		"30s":   30 * time.Second,
		"5m":    5 * time.Minute,
		"1h30m": 90 * time.Minute,

		// Below the floor, or not a duration at all: the default.
		"0":         fallback,
		"0s":        fallback,
		"999ms":     fallback,
		"-1m":       fallback,
		"":          fallback,
		"5":         fallback, // no unit — time.ParseDuration rejects it
		"5 minutes": fallback,
		"forever":   fallback,
	}
	for in, want := range cases {
		if got := parseDuration(in); got != want {
			t.Errorf("parseDuration(%q) = %s, want %s", in, got, want)
		}
	}
	for in := range cases {
		if got := parseDuration(in); got <= 0 {
			t.Errorf("parseDuration(%q) = %s; a non-positive interval is a busy loop", in, got)
		}
	}
}

// loadConfig is the adapter's whole configuration surface. The defaults are
// what a deployment gets when it sets only the four credentials, so they have
// to be the working ones — CLAUDE.md pins every daemon's in-container port at
// :8080, and REDDIT_SUBREDDITS empty must mean "none", not a panic.
func TestLoadConfigDefaultsAndParsing(t *testing.T) {
	for _, k := range []string{
		"CHANNEL_NAME", "REDDIT_SUBREDDITS", "REDDIT_USER_AGENT", "LISTEN_ADDR",
		"LISTEN_URL", "DATA_DIR", "MEDIA_MAX_FILE_BYTES", "REDDIT_POLL_INTERVAL",
	} {
		t.Setenv(k, "")
	}
	for _, k := range []string{
		"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "REDDIT_USERNAME",
		"REDDIT_PASSWORD", "ROUTER_URL",
	} {
		t.Setenv(k, "set-"+k)
	}

	c := loadConfig()
	if c.Name != "reddit" {
		t.Errorf("Name = %q, want the reddit default", c.Name)
	}
	if c.ClientID != "set-REDDIT_CLIENT_ID" || c.RouterURL != "set-ROUTER_URL" {
		t.Errorf("required vars not carried through: %+v", c)
	}
	if len(c.Subreddits) != 0 {
		t.Errorf("Subreddits = %v, want empty for an unset list", c.Subreddits)
	}
	if c.PollInterval != 5*time.Minute {
		t.Errorf("PollInterval = %s, want the 5m default", c.PollInterval)
	}
	if c.MediaMaxBytes != 20*1024*1024 {
		t.Errorf("MediaMaxBytes = %d, want 20MiB", c.MediaMaxBytes)
	}

	// The subreddit list is comma-separated with whitespace trimmed, and an
	// empty entry is dropped rather than polled as "".
	t.Setenv("REDDIT_SUBREDDITS", " golang , , rust,  ")
	t.Setenv("REDDIT_POLL_INTERVAL", "45s")
	t.Setenv("MEDIA_MAX_FILE_BYTES", "1024")
	c = loadConfig()
	if len(c.Subreddits) != 2 || c.Subreddits[0] != "golang" || c.Subreddits[1] != "rust" {
		t.Errorf("Subreddits = %#v, want [golang rust]", c.Subreddits)
	}
	if c.PollInterval != 45*time.Second {
		t.Errorf("PollInterval = %s, want 45s", c.PollInterval)
	}
	if c.MediaMaxBytes != 1024 {
		t.Errorf("MediaMaxBytes = %d, want the override", c.MediaMaxBytes)
	}

	// A bad interval must not silently become a busy loop.
	t.Setenv("REDDIT_POLL_INTERVAL", "nonsense")
	if got := loadConfig().PollInterval; got != 5*time.Minute {
		t.Errorf("an unparseable interval gave %s, want the 5m default", got)
	}
}
