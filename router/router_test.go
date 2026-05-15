package router

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestFormatMessages(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{Sender: "alice", Name: "Alice", Content: "hello", Timestamp: ts},
		{Sender: "bob", Content: "world", Timestamp: ts.Add(time.Minute)},
	}
	got := FormatMessages(msgs)

	if !strings.Contains(got, "<messages>") {
		t.Fatal("should contain <messages> tag")
	}
	if !strings.Contains(got, `sender="Alice"`) {
		t.Fatal("should use Name when available")
	}
	if !strings.Contains(got, `sender="bob"`) {
		t.Fatal("should fall back to Sender when Name is empty")
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Fatal("should contain message content")
	}
}

func TestFormatMessagesEscape(t *testing.T) {
	msgs := []core.Message{
		{Sender: "a", Name: `"A&B"`, Content: "<script>", Timestamp: time.Now()},
	}
	got := FormatMessages(msgs)
	if strings.Contains(got, "<script>") {
		t.Fatal("should escape angle brackets")
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatal("should have escaped content")
	}
	if !strings.Contains(got, "&quot;A&amp;B&quot;") {
		t.Fatal("should escape name")
	}
}

func TestFormatOutbound(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"strips internal", "before<internal>secret</internal>after", "beforeafter"},
		{"multiline internal", "a<internal>\nfoo\nbar\n</internal>b", "ab"},
		{"trims whitespace", "  hello  ", "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatOutbound(tc.in)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsAuthorizedRoutingTarget(t *testing.T) {
	cases := []struct {
		name           string
		source, target string
		want           bool
	}{
		{"direct child", "main", "main/child", true},
		{"sibling tree", "main", "other/child", false},
		{"same level", "main", "main", false},
		{"grandchild", "main", "main/child/deep", false},
		{"child of nested", "main/child", "main/child/sub", true},
		{"different root", "foo", "bar/child", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAuthorizedRoutingTarget(tc.source, tc.target)
			if got != tc.want {
				t.Fatalf("IsAuthorizedRoutingTarget(%q, %q) = %v, want %v",
					tc.source, tc.target, got, tc.want)
			}
		})
	}
}

func TestStripThinkBlocks(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no blocks", "hello world", "hello world"},
		{"simple", "before<think>hidden</think>after", "beforeafter"},
		{"nested", "a<think>x<think>y</think>z</think>b", "ab"},
		{"unclosed hides rest", "before<think>rest", "before"},
		{"multiple", "a<think>1</think>b<think>2</think>c", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripThinkBlocks(tc.in)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractStatusBlocks(t *testing.T) {
	cleaned, statuses := ExtractStatusBlocks("before<status>working on it</status>after")
	if cleaned != "beforeafter" {
		t.Fatalf("cleaned = %q", cleaned)
	}
	if len(statuses) != 1 || statuses[0] != "working on it" {
		t.Fatalf("statuses = %v", statuses)
	}

	cleaned, statuses = ExtractStatusBlocks("no status here")
	if cleaned != "no status here" || len(statuses) != 0 {
		t.Fatal("should pass through")
	}

	cleaned, statuses = ExtractStatusBlocks("<status>  </status>text")
	if len(statuses) != 0 {
		t.Fatal("empty status should be filtered")
	}
	if strings.TrimSpace(cleaned) != "text" {
		t.Fatalf("cleaned = %q", cleaned)
	}
}

func TestSenderToUserFileID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"telegram:123456", "tg-123456"},
		{"whatsapp:5551234", "wa-5551234"},
		{"discord:789", "dc-789"},
		{"email:user@example.com", "em-user@example.com"},
		{"unknown:abc", "un-abc"},
	}
	for _, tc := range cases {
		got := senderToUserFileID(tc.in)
		if got != tc.want {
			t.Fatalf("senderToUserFileID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUserContextXml(t *testing.T) {
	got := UserContextXml("", "/tmp/group")
	if got != "" {
		t.Fatal("empty sender should return empty")
	}
	got = UserContextXml("system", "/tmp/group")
	if got != "" {
		t.Fatal("system sender should return empty")
	}
	got = UserContextXml("telegram:123", "/nonexistent/group")
	if !strings.Contains(got, `id="tg-123"`) {
		t.Fatalf("should contain user id, got %q", got)
	}
	if !strings.Contains(got, "<user ") {
		t.Fatalf("should be user tag, got %q", got)
	}
}

func TestFormatOutboundThinkAndStatus(t *testing.T) {
	got := FormatOutbound("text<think>hidden</think> more<status>s</status> end")
	if strings.Contains(got, "hidden") || strings.Contains(got, "<think>") {
		t.Fatal("should strip think blocks")
	}
	if strings.Contains(got, "<status>") {
		t.Fatal("should strip status blocks")
	}
	if !strings.Contains(got, "text") || !strings.Contains(got, "more") {
		t.Fatal("should keep surrounding text")
	}
}

func TestExpandTarget(t *testing.T) {
	msg := core.Message{Sender: "telegram:123456"}
	cases := []struct {
		target, want string
	}{
		{"atlas/support", "atlas/support"},
		{"atlas/{sender}", "atlas/tg-123456"},
		{"{sender}", "tg-123456"},
	}
	for _, tc := range cases {
		got := expandTarget(tc.target, msg)
		if got != tc.want {
			t.Fatalf("expandTarget(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}

	// No sender → returns ""
	got := expandTarget("atlas/{sender}", core.Message{})
	if got != "" {
		t.Fatalf("expected empty for no sender, got %q", got)
	}
}

func TestResolveRouteTemplate(t *testing.T) {
	msg := core.Message{
		Sender:  "telegram:123456",
		Content: "hello",
	}
	routes := []core.Route{
		{Seq: 0, Match: "sender=telegram:*", Target: "atlas/{sender}"},
		{Seq: 1, Match: "", Target: "atlas/support"},
	}
	got := ResolveRoute(msg, routes)
	if got != "atlas/tg-123456" {
		t.Fatalf("expected atlas/tg-123456, got %q", got)
	}

	// Empty sender: first route (sender glob) cannot match empty sender,
	// falls through to catch-all.
	msg2 := core.Message{Content: "hello"}
	got = ResolveRoute(msg2, routes)
	if got != "atlas/support" {
		t.Fatalf("expected atlas/support fallthrough, got %q", got)
	}
}

func TestRouteMatchVerb(t *testing.T) {
	routes := []core.Route{
		{Seq: 0, Match: "verb=like", Target: "reactions"},
		{Seq: 1, Match: "verb=follow", Target: "followers"},
		{Seq: 2, Match: "", Target: "inbox"},
	}
	cases := []struct {
		msg  core.Message
		want string
	}{
		{core.Message{Verb: "like", Content: "❤️"}, "reactions"},
		{core.Message{Verb: "follow", Content: "x followed you"}, "followers"},
		{core.Message{Verb: "message", Content: "hi"}, "inbox"},
	}
	for _, tc := range cases {
		got := ResolveRoute(tc.msg, routes)
		if got != tc.want {
			t.Fatalf("verb=%q: got %q, want %q", tc.msg.Verb, got, tc.want)
		}
	}
}

// RouteMatches uses path.Match per "key=glob" predicate, AND'd across
// fields. Operators rely on these semantics when scoping routes.
func TestRouteMatch_GlobFields(t *testing.T) {
	cases := []struct {
		name  string
		match string
		msg   core.Message
		want  bool
	}{
		{
			name:  "room=* matches non-empty room",
			match: "room=*",
			msg:   core.Message{ChatJID: "discord:123"},
			want:  true,
		},
		{
			name:  "chat_jid=discord:* matches discord",
			match: "chat_jid=discord:*",
			msg:   core.Message{ChatJID: "discord:42"},
			want:  true,
		},
		{
			name:  "chat_jid=discord:* rejects telegram",
			match: "chat_jid=discord:*",
			msg:   core.Message{ChatJID: "telegram:42"},
			want:  false,
		},
		{
			name:  "platform AND room match",
			match: "platform=discord room=12345",
			msg:   core.Message{ChatJID: "discord:12345"},
			want:  true,
		},
		{
			name:  "multi-field rejects wrong platform",
			match: "platform=discord room=12345",
			msg:   core.Message{ChatJID: "telegram:12345"},
			want:  false,
		},
		{
			name:  "multi-field rejects wrong room",
			match: "platform=discord room=12345",
			msg:   core.Message{ChatJID: "discord:99999"},
			want:  false,
		},
		{
			name:  "bracket glob matches",
			match: "sender=telegram:user[0-9]*",
			msg:   core.Message{Sender: "telegram:user42"},
			want:  true,
		},
		{
			name:  "bracket glob rejects non-digit",
			match: "sender=telegram:user[0-9]*",
			msg:   core.Message{Sender: "telegram:bot"},
			want:  false,
		},
		{
			name:  "empty match matches all",
			match: "",
			msg:   core.Message{ChatJID: "anything:any"},
			want:  true,
		},
		// Typed-JID schema: kind discriminator in first segment, * does
		// not cross /. These are the canonical post-migration matchers.
		{
			name:  "telegram:group/* matches group jid",
			match: "chat_jid=telegram:group/*",
			msg:   core.Message{ChatJID: "telegram:group/123"},
			want:  true,
		},
		{
			name:  "telegram:group/* rejects user jid (different kind)",
			match: "chat_jid=telegram:group/*",
			msg:   core.Message{ChatJID: "telegram:user/123"},
			want:  false,
		},
		{
			name:  "telegram:group/* rejects deeper path (* doesn't cross /)",
			match: "chat_jid=telegram:group/*",
			msg:   core.Message{ChatJID: "telegram:group/123/sub"},
			want:  false,
		},
		{
			name:  "discord:dm/* matches dm channel",
			match: "chat_jid=discord:dm/*",
			msg:   core.Message{ChatJID: "discord:dm/4242"},
			want:  true,
		},
		{
			name:  "discord:<guild>/* matches guild channel",
			match: "chat_jid=discord:67890/*",
			msg:   core.Message{ChatJID: "discord:67890/12345"},
			want:  true,
		},
		{
			name:  "whatsapp:*@g.us matches group server",
			match: "chat_jid=whatsapp:*@g.us",
			msg:   core.Message{ChatJID: "whatsapp:1234@g.us"},
			want:  true,
		},
		{
			name:  "whatsapp:*@g.us rejects DM server",
			match: "chat_jid=whatsapp:*@g.us",
			msg:   core.Message{ChatJID: "whatsapp:1234@s.whatsapp.net"},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RouteMatches(core.Route{Match: tc.match}, tc.msg)
			if got != tc.want {
				t.Errorf("RouteMatches(%q, %+v) = %v, want %v",
					tc.match, tc.msg, got, tc.want)
			}
		})
	}
}

func TestFormatMessagesPlatformVerb(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{ChatJID: "mastodon:123", Sender: "mastodon:123", Content: "liked", Verb: "like", Timestamp: ts},
		{ChatJID: "reddit:456", Sender: "reddit:alice", Content: "followed", Verb: "follow", Timestamp: ts},
		{ChatJID: "telegram:789", Sender: "telegram:789", Content: "hi", Verb: "message", Timestamp: ts},
	}
	got := FormatMessages(msgs)
	if !strings.Contains(got, `platform="mastodon"`) {
		t.Fatal("should include mastodon platform attr")
	}
	if !strings.Contains(got, `platform="reddit"`) {
		t.Fatal("should include reddit platform attr")
	}
	if !strings.Contains(got, `verb="like"`) {
		t.Fatal("should include like verb attr")
	}
	if !strings.Contains(got, `verb="follow"`) {
		t.Fatal("should include follow verb attr")
	}
	// message verb is default; should NOT appear
	if strings.Contains(got, `verb="message"`) {
		t.Fatal("default message verb should be omitted")
	}
}

func TestFormatMessagesThread(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{ChatJID: "discord:ch1", Sender: "discord:u1", Content: "in thread", Topic: "thread-123", Timestamp: ts},
	}
	got := FormatMessages(msgs)
	if !strings.Contains(got, `thread="thread-123"`) {
		t.Fatalf("should include thread attr, got: %s", got)
	}
}

func TestFormatMessagesChatID(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{ChatJID: "telegram:-100123", Sender: "telegram:111", Content: "hey", Timestamp: ts},
	}
	got := FormatMessages(msgs)
	if !strings.Contains(got, `chat_id="telegram:-100123"`) {
		t.Fatalf("should include chat_id attr, got: %s", got)
	}
}

func TestFormatMessagesReplyPointerSelfClosing(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{
			ID: "m1", Sender: "u1", Content: "answer", Timestamp: ts,
			ReplyToID: "p9", ReplyToSender: "Bot",
			// ReplyToText empty → self-closing pointer (parent assumed in session)
		},
	}
	got := FormatMessages(msgs)
	if !strings.Contains(got, `<reply-to id="p9" sender="Bot"/>`) {
		t.Fatalf("expected self-closing reply-to, got:\n%s", got)
	}
	if strings.Contains(got, `reply_to="p9"`) {
		t.Fatal("legacy reply_to attribute on <message> should be gone")
	}
	if strings.Contains(got, `<reply_to `) {
		t.Fatal("legacy <reply_to> inline element should be gone")
	}
	if !strings.Contains(got, `<message id="m1"`) {
		t.Fatalf("expected id on <message>, got:\n%s", got)
	}
}

func TestFormatMessagesReplyPointerWithExcerpt(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{
			ID: "m1", Sender: "u1", Content: "fine.", Timestamp: ts,
			ReplyToID: "p9", ReplyToSender: "Bot", ReplyToText: "how are you?",
		},
	}
	got := FormatMessages(msgs)
	want := `<reply-to id="p9" sender="Bot">how are you?</reply-to>`
	if !strings.Contains(got, want) {
		t.Fatalf("expected reply-to with excerpt, got:\n%s", got)
	}
	// Must precede the <message> tag — pointer is structural header.
	pi := strings.Index(got, want)
	mi := strings.Index(got, `<message id="m1"`)
	if pi < 0 || mi < 0 || pi > mi {
		t.Fatalf("reply-to should appear before <message>, got:\n%s", got)
	}
}

func TestFormatMessagesReplyPointerUnknownSender(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{ID: "m1", Sender: "u1", Content: "x", Timestamp: ts, ReplyToID: "p9"},
	}
	got := FormatMessages(msgs)
	if !strings.Contains(got, `<reply-to id="p9" sender="unknown"/>`) {
		t.Fatalf("expected sender=\"unknown\" fallback, got:\n%s", got)
	}
}

func TestEscapeXml(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"<>&\"", "&lt;&gt;&amp;&quot;"},
		{"a&b<c>d", "a&amp;b&lt;c&gt;d"},
	}
	for _, tc := range cases {
		got := escapeXml(tc.in)
		if got != tc.want {
			t.Fatalf("escapeXml(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveRouteTarget_BareTrigger(t *testing.T) {
	routes := []core.Route{
		{Seq: 0, Match: "room=42", Target: "rhias/nemo"},
	}
	msg := core.Message{ChatJID: "telegram:42", Verb: "message"}
	rt := ResolveRouteTarget(msg, routes)
	if rt.Folder != "rhias/nemo" || rt.Mode != "" {
		t.Fatalf("bare target should resolve to trigger, got %+v", rt)
	}
}

func TestResolveRouteTarget_ObserveFragment(t *testing.T) {
	routes := []core.Route{
		{Seq: 0, Match: "room=42", Target: "rhias/nemo#observe"},
	}
	msg := core.Message{ChatJID: "telegram:42", Verb: "message"}
	rt := ResolveRouteTarget(msg, routes)
	if rt.Folder != "rhias/nemo" || rt.Mode != "observe" {
		t.Fatalf("expected folder=rhias/nemo mode=observe, got %+v", rt)
	}
}

// TestResolveRouteTarget_SlothPattern verifies the canonical mention-only
// channel setup: a higher-priority verb=mention trigger row + a lower-
// priority catch-all `#observe` row. Mentions fire; everything else
// stores under the folder as observed context.
func TestResolveRouteTarget_SlothPattern(t *testing.T) {
	routes := []core.Route{
		{Seq: -1, Match: "room=42 verb=mention", Target: "main"},
		{Seq: 0, Match: "room=42", Target: "main#observe"},
	}
	mention := core.Message{ChatJID: "discord:42", Verb: "mention"}
	if rt := ResolveRouteTarget(mention, routes); rt.Folder != "main" || rt.Mode != "" {
		t.Fatalf("mention should fire trigger, got %+v", rt)
	}
	regular := core.Message{ChatJID: "discord:42", Verb: "message"}
	if rt := ResolveRouteTarget(regular, routes); rt.Folder != "main" || rt.Mode != "observe" {
		t.Fatalf("non-mention should observe, got %+v", rt)
	}
}
