package router

import (
	"os"
	"path/filepath"
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

func TestSenderToUserFileID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"telegram:123456", "tg-123456"},
		{"whatsapp:5551234", "wa-5551234"},
		{"discord:789", "dc-789"},
		{"email:user@example.com", "em-user@example.com"},
		{"unknown:abc", "un-abc"},
		// Typed JIDs (migration 0042): the kind segment never reaches the file
		// id, so the file the agent kept before the migration is still found.
		{"discord:user/789", "dc-789"},
		{"telegram:user/123456", "tg-123456"},
		{"slack:user/U1", "sl-U1"},
		{"email:address/user@example.com", "em-user@example.com"},
		{"whatsapp:5551234@s.whatsapp.net", "wa-5551234@s.whatsapp.net"},
	}
	for _, tc := range cases {
		got := senderToUserFileID(tc.in)
		if got != tc.want {
			t.Fatalf("senderToUserFileID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatMessagesObservedNamesTheChatOnce: an observed row names its chat
// in chat_id like every other row; a second attribute with the same value
// (3508 of 3508 observed lines in two live transcripts) is noise the agent
// has to read past.
func TestFormatMessagesObservedNamesTheChatOnce(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	observed := []core.Message{
		{ID: "o1", ChatJID: "discord:g/c1", Sender: "u1", Content: "bg", Timestamp: ts},
	}
	got := FormatMessages(nil, observed)
	if !strings.Contains(got, `<observed id="o1"`) || !strings.Contains(got, `chat_id="discord:g/c1"`) {
		t.Fatalf("observed row lost its tag or chat_id:\n%s", got)
	}
	if strings.Contains(got, ` source=`) {
		t.Fatalf("observed row repeats chat_id as source:\n%s", got)
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

// users/<id>.md present: the tag gains name (from the file's `name:` line) and
// memory="~/users/<id>". A file with no `name:` line gains memory only. The
// name is XML-escaped. Another sender's file adds nothing to this tag.
func TestUserContextXml_UserFile(t *testing.T) {
	cases := []struct {
		name, file, want string
	}{
		{"name and memory", "name: Alice\nrole: dev\n", `<user id="tg-123" name="Alice" memory="~/users/tg-123" />`},
		{"memory only without a name line", "role: dev\n", `<user id="tg-123" memory="~/users/tg-123" />`},
		{"name is XML-escaped", "name: A & \"B\"\n", `<user id="tg-123" name="A &amp; &quot;B&quot;" memory="~/users/tg-123" />`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "users"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "users", "tg-123.md"), []byte(tc.file), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := UserContextXml("telegram:123", dir); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "users"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "users", "tg-999.md"), []byte("name: Bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := UserContextXml("telegram:123", dir), `<user id="tg-123" />`; got != want {
		t.Fatalf("got %q, want %q", got, want)
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
		{ChatJID: "bluesky:123", Sender: "bluesky:123", Content: "liked", Verb: "like", Timestamp: ts},
		{ChatJID: "reddit:456", Sender: "reddit:alice", Content: "followed", Verb: "follow", Timestamp: ts},
		{ChatJID: "telegram:789", Sender: "telegram:789", Content: "hi", Verb: "message", Timestamp: ts},
	}
	got := FormatMessages(msgs)
	if !strings.Contains(got, `platform="bluesky"`) {
		t.Fatal("should include bluesky platform attr")
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

// The same two rows in the other order answer nothing. An `#observe` route
// carries no verb, so it matches a mention too, and ResolveRoute returns the
// FIRST match in slice order — which store.routeOrder has already sorted to
// `ORDER BY seq, id`. So an observe row at a lower seq makes the mention row
// below it dead. Nothing logs a refusal: the message IS routed, to observe, so
// the channel reads as a silent agent (BUGS L327, eval check 22). This is the
// shape sloth's two Discord channels were in.
func TestResolveRouteTarget_ObserveAboveMentionShadowsIt(t *testing.T) {
	// Written in the order the store hands them over: seq 1 before seq 2.
	routes := []core.Route{
		{Seq: 1, ID: 1, Match: "room=42", Target: "main#observe"},
		{Seq: 2, ID: 2, Match: "room=* verb=mention", Target: "main/main"},
	}
	rt := ResolveRouteTarget(core.Message{ChatJID: "discord:42", Verb: "mention"}, routes)
	if rt.Mode != "observe" {
		t.Fatalf("mode = %q, want observe — this order is the shadowing bug, and the "+
			"fix that makes the mention win belongs in the route rows, not here", rt.Mode)
	}
	if rt.Folder != "main" {
		t.Errorf("folder = %q, want main", rt.Folder)
	}
}

// TestFormatMessagesMarksBotRows: a bot row renders bot="true" and a user row
// does not. The three tools that deliberately return outbound rows
// (inspect_messages, get_thread, fetch_history's cache fallback) read through
// this renderer, and until it emitted the flag the agent read its own past
// words as another user's — BUGS L184.
//
// The user row is asserted CLEAN on purpose: an attribute emitted
// unconditionally would mark every row and answer nothing.
func TestFormatMessagesMarksBotRows(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	got := FormatMessages([]core.Message{
		{ID: "u1", Sender: "alice", Content: "who wrote that?", Timestamp: ts},
		{ID: "b1", Sender: "solo/inbox", Content: "I did", Timestamp: ts.Add(time.Minute), BotMsg: true},
	})

	user, bot := msgLineByID(t, got, "u1"), msgLineByID(t, got, "b1")
	if strings.Contains(user, `bot="true"`) {
		t.Errorf("a user row must not be marked bot: %s", user)
	}
	if !strings.Contains(bot, `bot="true"`) {
		t.Errorf("the agent's own row must be marked bot: %s", bot)
	}
}

// TestFormatMessagesFromMeIsNotOwnAuthorship: FromMe marks the linked
// ACCOUNT's own messages in an imported archive — the operator's, not the
// agent's — so it must never render as the own-authorship marker (BUGS L184).
func TestFormatMessagesFromMeIsNotOwnAuthorship(t *testing.T) {
	got := FormatMessages([]core.Message{
		{ID: "h1", Sender: "owner", Content: "sent from my phone", Timestamp: time.Now(), FromMe: true},
	})
	if strings.Contains(got, `bot="true"`) {
		t.Errorf("FromMe alone must not mark a row as the agent's own: %s", got)
	}
}

// msgLineByID returns the rendered element carrying id="<id>".
func msgLineByID(t *testing.T, rendered, id string) string {
	t.Helper()
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, `id="`+id+`"`) {
			return line
		}
	}
	t.Fatalf("no rendered row with id=%q in:\n%s", id, rendered)
	return ""
}
