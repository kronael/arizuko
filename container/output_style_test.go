package container

import (
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func never(string) bool { return false }
func always(string) bool { return true }

func TestPickOutputStyle_SurfaceMatrix(t *testing.T) {
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
		{"slack group", "slack", "slack:T1/group/G1", "", never, "slack-channel"},
		{"telegram dm", "telegram", "telegram:user/42", "", never, "telegram-dm"},
		{"telegram group", "telegram", "telegram:group/42", "", never, "telegram-group"},
		{"discord dm", "discord", "discord:dm/D1", "", never, "discord-dm"},
		{"discord channel", "discord", "discord:G1/C1", "", never, "discord-channel"},
		{"email any", "email", "", "", never, "email"},
		{"web any", "web", "", "", never, "web"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickOutputStyle(c.channel, c.jid, c.topic, c.pane); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestPickOutputStyle_EmptyChannel(t *testing.T) {
	if got := pickOutputStyle("", "slack:T1/dm/D1", "", never); got != "" {
		t.Fatalf("got %q want \"\"", got)
	}
}

func TestPickOutputStyle_NilPaneLookup(t *testing.T) {
	// Nil pane lookup must not panic; channel falls back to "channel" surface.
	if got := pickOutputStyle("slack", "slack:T1/channel/C1", "", nil); got != "slack-channel" {
		t.Fatalf("got %q want %q", got, "slack-channel")
	}
}

func TestPickOutputStyle_UnknownPlatformFallsBack(t *testing.T) {
	// Platforms without a per-surface split resolve to the platform name.
	if got := pickOutputStyle("whapd", "whatsapp:foo", "", never); got != "whapd" {
		t.Fatalf("got %q want %q", got, "whapd")
	}
}

func TestPickOutputStyle_MalformedSlackJID(t *testing.T) {
	// Bad JID → deriveSurface returns "" → fall back to platform.
	if got := pickOutputStyle("slack", "slack:malformed", "", never); got != "slack" {
		t.Fatalf("got %q want %q", got, "slack")
	}
}

func TestParseSlackJID(t *testing.T) {
	cases := []struct {
		jid                      string
		wantWS, wantKind, wantID string
		wantOK                   bool
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
			p, ok := chanlib.ParseSlackJID(c.jid)
			if ok != c.wantOK || p.Workspace != c.wantWS || p.Kind != c.wantKind || p.ID != c.wantID {
				t.Fatalf("got (%q,%q,%q,%v) want (%q,%q,%q,%v)",
					p.Workspace, p.Kind, p.ID, ok, c.wantWS, c.wantKind, c.wantID, c.wantOK)
			}
		})
	}
}
