package main

// helpers_test.go — targeted tests for slakd helpers not covered elsewhere:
//   chatNameFrom, paneTitle, attachmentsFor.
//
// These use the existing setupBot / newSlackMock helpers from
// integration_test.go (same package).

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// ---------------------------------------------------------------------------
// chatNameFrom
// ---------------------------------------------------------------------------

func TestChatNameFrom_NormalChannel(t *testing.T) {
	c := &slackConvInfo{Name: "general"}
	got := chatNameFrom(c)
	if got != "#general" {
		t.Errorf("chatNameFrom({Name:general}) = %q, want #general", got)
	}
}

func TestChatNameFrom_IM(t *testing.T) {
	// DMs have no meaningful channel name; return empty.
	c := &slackConvInfo{Name: "D0XY", IsIM: true}
	got := chatNameFrom(c)
	if got != "" {
		t.Errorf("chatNameFrom IM = %q, want empty", got)
	}
}

func TestChatNameFrom_EmptyName(t *testing.T) {
	c := &slackConvInfo{}
	got := chatNameFrom(c)
	if got != "" {
		t.Errorf("chatNameFrom empty name = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// paneTitle
// ---------------------------------------------------------------------------

func TestPaneTitle_WithAssistantName(t *testing.T) {
	b := &bot{cfg: config{AssistantName: "atlas"}}
	if got := b.paneTitle(); got != "atlas — chat" {
		t.Errorf("paneTitle = %q, want 'atlas — chat'", got)
	}
}

func TestPaneTitle_NoAssistantName(t *testing.T) {
	b := &bot{cfg: config{AssistantName: ""}}
	if got := b.paneTitle(); got != "chat" {
		t.Errorf("paneTitle = %q, want 'chat'", got)
	}
}

// ---------------------------------------------------------------------------
// attachmentsFor
// ---------------------------------------------------------------------------

func TestAttachmentsFor_Empty(t *testing.T) {
	b := &bot{}
	content, atts := b.attachmentsFor("text", nil)
	if content != "text" {
		t.Errorf("content = %q", content)
	}
	if len(atts) != 0 {
		t.Errorf("atts = %d", len(atts))
	}
}

func TestAttachmentsFor_SkipsFilesWithoutURL(t *testing.T) {
	b := &bot{}
	files := []slackFile{{Name: "x.pdf", Mimetype: "application/pdf", URLPriv: ""}}
	content, atts := b.attachmentsFor("base", files)
	if content != "base" {
		t.Errorf("content = %q, want base (no change)", content)
	}
	if len(atts) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(atts))
	}
}

func TestAttachmentsFor_ProxiesURLWhenListenURLSet(t *testing.T) {
	b := &bot{
		cfg:   config{ListenURL: "http://slakd:8080"},
		files: chanlib.NewURLCache(10),
	}
	files := []slackFile{
		{
			Name:     "doc.pdf",
			Mimetype: "application/pdf",
			URLPriv:  "https://files.slack.com/F1/doc.pdf",
			Size:     1024,
		},
	}
	content, atts := b.attachmentsFor("see file", files)
	if !strings.Contains(content, "[Attachment: doc.pdf]") {
		t.Errorf("content missing attachment label: %q", content)
	}
	if len(atts) != 1 {
		t.Fatalf("atts = %d, want 1", len(atts))
	}
	if !strings.HasPrefix(atts[0].URL, "http://slakd:8080/files/") {
		t.Errorf("URL should be proxied: %q", atts[0].URL)
	}
	if atts[0].Mime != "application/pdf" {
		t.Errorf("mime = %q", atts[0].Mime)
	}
	if atts[0].Filename != "doc.pdf" {
		t.Errorf("filename = %q", atts[0].Filename)
	}
	if atts[0].Size != 1024 {
		t.Errorf("size = %d", atts[0].Size)
	}
}

func TestAttachmentsFor_UsesRawURLWhenNoListenURL(t *testing.T) {
	b := &bot{cfg: config{ListenURL: ""}}
	files := []slackFile{
		{
			Name:    "img.png",
			URLPriv: "https://files.slack.com/F2/img.png",
		},
	}
	_, atts := b.attachmentsFor("", files)
	if len(atts) != 1 {
		t.Fatalf("atts = %d", len(atts))
	}
	if atts[0].URL != "https://files.slack.com/F2/img.png" {
		t.Errorf("URL should be raw when ListenURL unset: %q", atts[0].URL)
	}
}

func TestAttachmentsFor_FallsBackFilenameWhenEmpty(t *testing.T) {
	b := &bot{}
	files := []slackFile{{Name: "", URLPriv: "https://files.slack.com/F3/x"}}
	content, atts := b.attachmentsFor("", files)
	if !strings.Contains(content, "[Attachment: attachment]") {
		t.Errorf("content = %q, want 'attachment' fallback name", content)
	}
	if len(atts) != 1 || atts[0].Filename != "attachment" {
		t.Errorf("filename fallback wrong: %+v", atts)
	}
}

// ---------------------------------------------------------------------------
// demangleMentions
// ---------------------------------------------------------------------------

// In-content mention tokens are rewritten to readable names before inbound
// delivery so the agent never echoes raw ids ("@U0B70FBE7CG here's...").
func TestDemangleMentions(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock) // users.info mock resolves every id to "alice"

	cases := []struct{ name, in, want string }{
		{"bare user", "<@U0B70FBE7CG> here's the data", "@alice here's the data"},
		{"labeled user", "hi <@U123|bob>", "hi @bob"},
		{"labeled channel", "see <#C123|general> please", "see #general please"},
		{"bare channel stays", "bare <#C123> stays", "bare <#C123> stays"},
		{"two users", "<@U1> and <@U2>", "@alice and @alice"},
		{"no tokens", "no mentions here", "no mentions here"},
	}
	for _, c := range cases {
		if got := b.demangleMentions(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The bot's own mention resolves to @AssistantName without users.info (which
// needs the users:read scope the token may lack), matching discd's self-mention
// rewrite. Other ids still go through userName.
func TestDemangleMentions_BotSelf(t *testing.T) {
	b := &bot{cfg: config{AssistantName: "atlas"}}
	b.botUserID.Store("UBOT")
	if got := b.demangleMentions("<@UBOT> ping"); got != "@atlas ping" {
		t.Errorf("self mention: got %q, want %q", got, "@atlas ping")
	}
}

// The inbound path delivers DEMANGLED content to routd.
func TestInbound_MentionDemangled(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "channel_type": "channel",
	    "channel": "C0HJK",
	    "user": "U99",
	    "text": "<@U0B70FBE7CG> here's what the data shows",
	    "ts": "1700000444.000100"
	  }
	}`)
	w := httptest.NewRecorder()
	b.handleEvent(body, w)
	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs", len(msgs))
	}
	if msgs[0].Content != "@alice here's what the data shows" {
		t.Errorf("content = %q", msgs[0].Content)
	}
}
