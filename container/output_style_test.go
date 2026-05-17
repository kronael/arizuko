package container

import (
	"os"
	"path/filepath"
	"testing"
)

func mkStyles(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		fp := filepath.Join(dir, n+".md")
		if err := os.WriteFile(fp, []byte("# "+n+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", fp, err)
		}
	}
	return dir
}

func never(string) bool { return false }
func always(string) bool { return true }

func TestPickOutputStyle_SurfaceMatrix(t *testing.T) {
	dir := mkStyles(t,
		"slack", "slack-dm", "slack-channel", "slack-thread", "slack-pane",
		"telegram", "telegram-dm", "telegram-group",
		"discord", "discord-dm", "discord-channel",
		"email", "web",
	)
	cases := []struct {
		name    string
		channel string
		jid     string
		topic   string
		pane    func(string) bool
		want    string
	}{
		{"slack dm", "slack", "slack:T1/dm/D1", "", never, "slack-dm"},
		{"slack channel top-level", "slack", "slack:T1/channel/C1", "", never, "slack-channel"},
		{"slack channel thread", "slack", "slack:T1/channel/C1", "T123", never, "slack-thread"},
		{"slack channel pane", "slack", "slack:T1/channel/C1", "", always, "slack-pane"},
		{"slack mpim group", "slack", "slack:T1/group/G1", "", never, "slack-channel"},
		{"telegram dm", "telegram", "telegram:user/42", "", never, "telegram-dm"},
		{"telegram group", "telegram", "telegram:group/42", "", never, "telegram-group"},
		{"discord dm", "discord", "discord:dm/D1", "", never, "discord-dm"},
		{"discord channel", "discord", "discord:G1/C1", "", never, "discord-channel"},
		{"email any", "email", "", "", never, "email"},
		{"web any", "web", "", "", never, "web"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickOutputStyle(c.channel, c.jid, c.topic, dir, c.pane)
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestPickOutputStyle_FallbackToPlatform(t *testing.T) {
	// Only slack.md exists; slack-channel.md does not.
	dir := mkStyles(t, "slack")
	got := pickOutputStyle("slack", "slack:T1/channel/C1", "", dir, never)
	if got != "slack" {
		t.Fatalf("got %q want %q", got, "slack")
	}
}

func TestPickOutputStyle_FallbackToEmpty(t *testing.T) {
	dir := mkStyles(t) // no files at all
	got := pickOutputStyle("slack", "slack:T1/channel/C1", "", dir, never)
	if got != "" {
		t.Fatalf("got %q want \"\"", got)
	}
}

func TestPickOutputStyle_EmptyChannel(t *testing.T) {
	dir := mkStyles(t, "slack")
	got := pickOutputStyle("", "slack:T1/dm/D1", "", dir, never)
	if got != "" {
		t.Fatalf("got %q want \"\"", got)
	}
}

func TestPickOutputStyle_NilPaneLookup(t *testing.T) {
	dir := mkStyles(t, "slack-channel", "slack")
	// Nil pane lookup must not panic; should fall through to channel.
	got := pickOutputStyle("slack", "slack:T1/channel/C1", "", dir, nil)
	if got != "slack-channel" {
		t.Fatalf("got %q want %q", got, "slack-channel")
	}
}

func TestPickOutputStyle_UnknownPlatformReturnsPlatform(t *testing.T) {
	// Platforms without per-surface split (e.g. whatsapp, reddit) just
	// resolve to their platform name when the file exists.
	dir := mkStyles(t, "whapd")
	got := pickOutputStyle("whapd", "whatsapp:foo", "", dir, never)
	if got != "whapd" {
		t.Fatalf("got %q want %q", got, "whapd")
	}
}

func TestPickOutputStyle_EmptyStylesDirNonFatal(t *testing.T) {
	// Misconfigured HostAppDir → empty stylesDir. Picker must still
	// return SOMETHING reasonable (the candidate name) and not panic.
	got := pickOutputStyle("slack", "slack:T1/channel/C1", "T123", "", never)
	if got != "slack-thread" {
		t.Fatalf("got %q want %q", got, "slack-thread")
	}
	// And without a surface match, falls through to the channel name.
	got = pickOutputStyle("whapd", "whatsapp:foo", "", "", never)
	if got != "whapd" {
		t.Fatalf("got %q want %q", got, "whapd")
	}
}

func TestParseSlackJID(t *testing.T) {
	cases := []struct {
		jid                       string
		wantWS, wantKind, wantID  string
		wantOK                    bool
	}{
		{"slack:T1/channel/C1", "T1", "channel", "C1", true},
		{"slack:T1/dm/D1", "T1", "dm", "D1", true},
		{"slack:T1/group/G1", "T1", "group", "G1", true},
		{"telegram:user/42", "", "", "", false},
		{"slack:", "", "", "", false},
		{"slack:T1", "", "", "", false},
		{"slack:T1/channel", "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.jid, func(t *testing.T) {
			ws, kind, id, ok := parseSlackJID(c.jid)
			if ok != c.wantOK || ws != c.wantWS || kind != c.wantKind || id != c.wantID {
				t.Fatalf("got (%q,%q,%q,%v) want (%q,%q,%q,%v)",
					ws, kind, id, ok, c.wantWS, c.wantKind, c.wantID, c.wantOK)
			}
		})
	}
}
