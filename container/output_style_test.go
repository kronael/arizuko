package container

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func never(string) bool  { return false }
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

// TestPickOutputStyle_ShippedStyleExists binds the name the picker hands the
// agent to a file the image actually ships. ant/src/index.ts::readOutputStyle
// reads ~/.claude/output-styles/<name>.md inside a bare try/catch, so a name
// with no file is SILENT: the agent drops to Claude Code's stock formatting and
// nothing logs it (BUGS L27). The channel is the JID scheme
// (routd/dispatch.go::jidScheme), never the daemon name, so a file named after
// the daemon — whapd.md rather than whatsapp.md — would ship and never be read.
//
// shipsFile false is a DECISION, not a gap: that platform renders the agent's
// stock markdown correctly, so a style file would add a surface to maintain and
// change nothing. Shipping one flips the bool.
func TestPickOutputStyle_ShippedStyleExists(t *testing.T) {
	cases := []struct {
		name, channel, jid string
		shipsFile          bool
	}{
		{"whatsapp", "whatsapp", "whatsapp:1@s.whatsapp.net", true},
		{"bluesky", "bluesky", "bluesky:user/did%3Aplc%3Aa", true},
		{"linkedin", "linkedin", "linkedin:post/urn%3Ali%3Ashare%3A1", true},
		{"twitter", "twitter", "twitter:user/alice", true},
		{"slack dm", "slack", "slack:T1/dm/D1", true},
		{"slack channel", "slack", "slack:T1/channel/C1", true},
		{"telegram dm", "telegram", "telegram:user/42", true},
		{"telegram group", "telegram", "telegram:group/42", true},
		{"discord dm", "discord", "discord:dm/D1", true},
		{"discord channel", "discord", "discord:G1/C1", true},
		{"email", "email", "email:someone@example.com", true},
		{"web", "web", "web:solo/inbox", true},
		// reditd and gitd post the agent's text verbatim into a field their
		// platform renders as real markdown, so stock formatting already lands
		// correctly on both.
		{"reddit", "reddit", "reddit:r_golang", false},
		{"github", "github", "github:o/r/issue/1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			style := pickOutputStyle(c.channel, c.jid, "", never)
			if style == "" {
				t.Fatalf("picker named no style for %q", c.jid)
			}
			_, err := os.Stat(filepath.Join("..", "ant", "output-styles", style+".md"))
			switch {
			case c.shipsFile && err != nil:
				t.Fatalf("picker chose %q but ant/output-styles/%s.md does not ship: %v",
					style, style, err)
			case !c.shipsFile && err == nil:
				t.Fatalf("ant/output-styles/%s.md now ships; set shipsFile true for %q",
					style, c.channel)
			}
		})
	}
}
