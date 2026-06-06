package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// Tier-2 parity: routing behavior shipped in steer.go / db.go that was
// under-tested against the gated monolith. Each test mirrors the named gated
// assertion so the split proves identical routing semantics.

// --- resolveTarget reply-chain precedence (mirror gateway/thread_routing_test.go) ---

// targetLoop builds a Loop with no queue activity for pure resolveTarget /
// db round-trip assertions.
func targetLoop(t *testing.T) (*DB, *Loop) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	l := NewLoop(db, &recRunner{}, LoopConfig{})
	l.StopQueue()
	return db, l
}

// TestResolveTarget_EmptyReplyTo: no reply → default route target (gated
// TestReplyChainRouting_EmptyReplyTo). resolveTarget reads routes from the DB,
// so the route is seeded there rather than passed in.
func TestResolveTarget_EmptyReplyTo(t *testing.T) {
	db, l := targetLoop(t)
	doSetRoutes(t, db, []core.Route{{Match: "", Target: "default-group"}})
	msg := core.Message{ChatJID: "tg:123", Content: "hello", ReplyToID: ""}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "default-group" {
		t.Errorf("resolveTarget = %q, want default-group", got)
	}
}

// TestResolveTarget_WithReplyTo: a reply to a bot row routed elsewhere follows
// the reply chain over the route table (gated TestReplyChainRouting_WithReplyTo).
func TestResolveTarget_WithReplyTo(t *testing.T) {
	db, l := targetLoop(t)
	doSetRoutes(t, db, []core.Route{{Match: "", Target: "default-group"}})
	_ = db.PutMessage(core.Message{ID: "bot-msg-1", ChatJID: "tg:123", Sender: "test",
		Content: "bot response", Timestamp: time.Now().UTC(), BotMsg: true, RoutedTo: "routed-group"})
	msg := core.Message{ChatJID: "tg:123", Content: "reply to bot", ReplyToID: "bot-msg-1"}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "routed-group" {
		t.Errorf("resolveTarget = %q, want routed-group (follow reply chain)", got)
	}
}

// TestResolveTarget_ReplyToSelfFolder: a reply to a bot row routed to self
// returns "" — stay in the same group (gated TestReplyChainRouting_ReplyToSelfFolder).
func TestResolveTarget_ReplyToSelfFolder(t *testing.T) {
	db, l := targetLoop(t)
	doSetRoutes(t, db, []core.Route{{Match: "", Target: "other-group"}})
	_ = db.PutMessage(core.Message{ID: "bot-msg-1", ChatJID: "tg:123", Sender: "test",
		Content: "bot response", Timestamp: time.Now().UTC(), BotMsg: true, RoutedTo: "self"})
	msg := core.Message{ChatJID: "tg:123", Content: "reply to bot", ReplyToID: "bot-msg-1"}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "" {
		t.Errorf("resolveTarget = %q, want empty (stay in self)", got)
	}
}

// TestResolveTarget_NoRoutedTo: a reply to a row that carries no routed_to
// falls back to default routing (gated TestReplyChainRouting_NoRoutedTo).
func TestResolveTarget_NoRoutedTo(t *testing.T) {
	db, l := targetLoop(t)
	doSetRoutes(t, db, []core.Route{{Match: "", Target: "default-group"}})
	_ = db.PutMessage(core.Message{ID: "user-msg-1", ChatJID: "tg:123", Sender: "user",
		Content: "user message", Timestamp: time.Now().UTC()})
	msg := core.Message{ChatJID: "tg:123", Content: "reply to user", ReplyToID: "user-msg-1"}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "default-group" {
		t.Errorf("resolveTarget = %q, want default-group (no routed_to → default)", got)
	}
}

// TestResolveTarget_MessageNotFound: a reply to an unknown id falls back to
// default routing (gated TestReplyChainRouting_MessageNotFound).
func TestResolveTarget_MessageNotFound(t *testing.T) {
	db, l := targetLoop(t)
	doSetRoutes(t, db, []core.Route{{Match: "", Target: "default-group"}})
	msg := core.Message{ChatJID: "tg:123", Content: "reply to nothing", ReplyToID: "non-existent-id"}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "default-group" {
		t.Errorf("resolveTarget = %q, want default-group (unknown reply → default)", got)
	}
}

// TestResolveTarget_ReplyChainPrecedenceOverPatternRoute: reply-chain wins over
// a pattern route the reply content would otherwise match (gated
// TestReplyChainRouting_PrecedenceOverPatternRoute).
func TestResolveTarget_ReplyChainPrecedenceOverPatternRoute(t *testing.T) {
	db, l := targetLoop(t)
	doSetRoutes(t, db, []core.Route{
		{Seq: 0, Match: "sender=urgent:*", Target: "urgent-group"},
		{Seq: 1, Match: "", Target: "default-group"},
	})
	_ = db.PutMessage(core.Message{ID: "bot-msg-1", ChatJID: "tg:123", Sender: "test",
		Content: "bot response", Timestamp: time.Now().UTC(), BotMsg: true, RoutedTo: "reply-target"})
	msg := core.Message{ChatJID: "tg:123", Content: "urgent: reply to bot", ReplyToID: "bot-msg-1"}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "reply-target" {
		t.Errorf("resolveTarget = %q, want reply-target (reply chain over pattern)", got)
	}
}

// --- sticky-group as delegation target (mirror sticky_routing_test.go) ---

// TestResolveTarget_WithStickyGroup: a set sticky group overrides the default
// route as the delegation target (gated TestResolveTarget_WithStickyGroup).
func TestResolveTarget_WithStickyGroup(t *testing.T) {
	db, l := targetLoop(t)
	_ = db.SetStickyGroup("tg:123", "sticky-target")
	doSetRoutes(t, db, []core.Route{{Match: "", Target: "default-group"}})
	msg := core.Message{ChatJID: "tg:123", Content: "hello"}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "sticky-target" {
		t.Errorf("resolveTarget = %q, want sticky-target (sticky over default)", got)
	}
}

// TestResolveTarget_StickyGroupSameAsSelf: sticky == self → "" (no route)
// (gated TestResolveTarget_StickyGroupSameAsSelf).
func TestResolveTarget_StickyGroupSameAsSelf(t *testing.T) {
	db, l := targetLoop(t)
	_ = db.SetStickyGroup("tg:123", "self")
	doSetRoutes(t, db, []core.Route{{Match: "", Target: "other-group"}})
	msg := core.Message{ChatJID: "tg:123", Content: "hello"}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "" {
		t.Errorf("resolveTarget = %q, want empty (sticky=self stays)", got)
	}
}

// TestResolveTarget_ReplyChainOverridesStickyGroup: reply-chain takes precedence
// over a set sticky group (gated TestResolveTarget_ReplyChainOverridesStickyGroup).
func TestResolveTarget_ReplyChainOverridesStickyGroup(t *testing.T) {
	db, l := targetLoop(t)
	_ = db.SetStickyGroup("tg:123", "sticky-target")
	_ = db.PutMessage(core.Message{ID: "bot-msg-1", ChatJID: "tg:123", Sender: "test",
		Content: "bot response", Timestamp: time.Now().UTC(), BotMsg: true, RoutedTo: "reply-target"})
	msg := core.Message{ChatJID: "tg:123", Content: "reply", ReplyToID: "bot-msg-1"}
	if got := l.resolveTarget("tg:123", msg, "self"); got != "reply-target" {
		t.Errorf("resolveTarget = %q, want reply-target (reply chain over sticky)", got)
	}
}

// --- RoutedTo / Topic storage round-trip (mirror thread_routing_test.go) ---

// TestRoutedToByMessageID_RoundTrip: routed_to written by PutMessage reads back
// via RoutedToByMessageID; absent / unknown → "" (gated TestRoutedToStorage*).
func TestRoutedToByMessageID_RoundTrip(t *testing.T) {
	db, _ := targetLoop(t)
	_ = db.PutMessage(core.Message{ID: "msg-1", ChatJID: "tg:123", Sender: "t",
		Content: "m", Timestamp: time.Now().UTC(), RoutedTo: "target-group"})
	_ = db.PutMessage(core.Message{ID: "msg-2", ChatJID: "tg:123", Sender: "t",
		Content: "m", Timestamp: time.Now().UTC(), RoutedTo: ""})

	if got := db.RoutedToByMessageID("msg-1"); got != "target-group" {
		t.Errorf("RoutedToByMessageID(msg-1) = %q, want target-group", got)
	}
	if got := db.RoutedToByMessageID("msg-2"); got != "" {
		t.Errorf("RoutedToByMessageID(msg-2) = %q, want empty", got)
	}
	if got := db.RoutedToByMessageID("non-existent"); got != "" {
		t.Errorf("RoutedToByMessageID(non-existent) = %q, want empty", got)
	}
}

// TestTopicByID_RoundTrip: topic written by PutMessage reads back via TopicByID
// (routd's TopicByMessageID twin — reaction/reply thread-parent lookup). Mirror
// gated TestTopicStorage + store TestTopicByMessageID.
func TestTopicByID_RoundTrip(t *testing.T) {
	db, _ := targetLoop(t)
	_ = db.PutMessage(core.Message{ID: "msg-1", ChatJID: "tg:123", Sender: "t",
		Content: "m", Timestamp: time.Now().UTC(), Topic: "thread-42"})
	_ = db.PutMessage(core.Message{ID: "msg-2", ChatJID: "tg:123", Sender: "t",
		Content: "m", Timestamp: time.Now().UTC(), Topic: ""})

	if got := db.TopicByID("msg-1"); got != "thread-42" {
		t.Errorf("TopicByID(msg-1) = %q, want thread-42", got)
	}
	if got := db.TopicByID("msg-2"); got != "" {
		t.Errorf("TopicByID(msg-2) = %q, want empty", got)
	}
	if got := db.TopicByID("non-existent"); got != "" {
		t.Errorf("TopicByID(non-existent) = %q, want empty", got)
	}
}

// --- web: strict 1:1 resolution (mirror gateway_test.go) ---

// TestResolveGroup_WebStrict1to1: a web: JID whose folder is not a registered
// group returns ok=false — no fuzzy parent-folder fallback (gated
// TestResolveGroup_WebStrict1to1).
func TestResolveGroup_WebStrict1to1(t *testing.T) {
	db, l := targetLoop(t)
	_ = db.PutGroup(core.Group{Folder: "atlas/strengths"})
	last := core.Message{ChatJID: "web:atlas/strengths/submissions", Verb: "message"}
	if _, ok := l.resolveGroup("web:atlas/strengths/submissions", last); ok {
		t.Fatal("resolveGroup returned true for web JID with no matching group")
	}
}

// TestFolderForJid_WebStrict1to1: the queue's folder-mutex lookup for a web: JID
// with no matching group returns "" (gated TestFolderForJid_WebStrict1to1). The
// chat has a backing message row so folderForJid reaches resolveGroup.
func TestFolderForJid_WebStrict1to1(t *testing.T) {
	db, l := targetLoop(t)
	_ = db.PutGroup(core.Group{Folder: "atlas/strengths"})
	jid := "web:atlas/strengths/submissions"
	_ = db.PutMessage(core.Message{ID: "m1", ChatJID: jid, Sender: "u",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})
	// folderForJid falls back to the jid itself only when resolveGroup is ok;
	// a web JID with no group resolves !ok, so the mutex key stays the raw jid.
	if got := l.folderForJid(jid); got == "atlas/strengths" {
		t.Errorf("folderForJid = %q, want no fuzzy match to atlas/strengths", got)
	}
}

// --- /new #topic clears the topic session (mirror gateway_extra) ---

// TestCmdNew_WithTopic_ClearsTopicSession: `/new #deploy` clears the (folder,
// #deploy) session, not the root session (gated TestCmdNew_WithTopic_ClearsTopicSession).
func TestCmdNew_WithTopic_ClearsTopicSession(t *testing.T) {
	db, l := targetLoop(t)
	l.deliver = &recDeliverer{}
	_ = db.PutGroup(core.Group{Folder: "grp"})
	_ = db.PutSession("grp", "#deploy", "topic-sess-1")
	_ = db.PutSession("grp", "", "root-sess-1")

	l.cmdNew("web:grp", "grp", "#deploy")

	if id := db.SessionID("grp", "#deploy"); id != "" {
		t.Errorf("topic session not cleared: %q", id)
	}
	if id := db.SessionID("grp", ""); id != "root-sess-1" {
		t.Errorf("root session wrongly cleared: %q want root-sess-1", id)
	}
}

// --- slash command parsing: backslash alias + pure fns ---

// TestLookupCommand_BackslashAlias: the Slack-pasted "\new" maps to /new, and
// the Telegram "@botname" suffix is stripped (gated commands parsing). The
// handleCommand switch then dispatches it identically.
func TestLookupCommand_BackslashAlias(t *testing.T) {
	cases := []struct {
		in, head, arg string
	}{
		{`\new`, "/new", ""},
		{`\new look into X`, "/new", "look into X"},
		{`/new`, "/new", ""},
		{`/ping@mybot`, "/ping", ""},
		{`/root@mybot do thing`, "/root", "do thing"},
		{`not a command`, "", ""},
		{``, "", ""},
	}
	for _, c := range cases {
		head, arg := lookupCommand(c.in)
		if head != c.head || arg != c.arg {
			t.Errorf("lookupCommand(%q) = (%q,%q), want (%q,%q)", c.in, head, arg, c.head, c.arg)
		}
	}
}

// TestBackslashNewDispatches: a \new arriving as a real inbound is consumed by
// the steer layer exactly as /new (clears session, advances cursor, no turn).
func TestBackslashNewDispatches(t *testing.T) {
	db, l, rr := recLoop(t)
	l.deliver = &recDeliverer{}
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.PutSession("demo", "", "sess-X")
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: `\new`, Timestamp: time.Now().UTC()})

	l.pollOnce()
	if db.SessionID("demo", "") != "" {
		t.Fatal(`\new did not clear session`)
	}
	if db.GetAgentCursor("web:demo") == "" {
		t.Fatal(`\new did not advance cursor`)
	}
	if len(rr.runs) != 0 {
		t.Fatalf(`\new dispatched a turn: %+v`, rr.runs)
	}
}

// TestIsStickyCommand: the bare-@/# sticky-nav recognizer (gated TestIsStickyCommand).
func TestIsStickyCommand(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"@", true}, {"@agent", true}, {"@agent-name", true},
		{"#", true}, {"#topic", true}, {"#topic-name", true},
		{"  @agent  ", true}, {"  #topic  ", true},
		{"", false}, {"hello", false}, {"@agent hello", false},
		{"hello @agent", false}, {"#topic message", false}, {"/#topic", false},
		{"/command", false}, {"@ agent", false}, {"# topic", false},
	}
	for _, c := range cases {
		if got := isStickyCommand(c.in); got != c.want {
			t.Errorf("isStickyCommand(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestCmdText: strips a leading [bracket] prefix and an @sender prefix to reach
// the bare command text (gated cmdText).
func TestCmdText(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/new", "/new"},
		{"[alice] /new", "/new"},
		{"@alice /new", "/new"},
		{"[alice] @bob /new look", "/new look"},
		{"@alice", ""},
		{"plain text", "plain text"},
		{"  /ping  ", "/ping"},
	}
	for _, c := range cases {
		if got := cmdText(c.in); got != c.want {
			t.Errorf("cmdText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestParsePrefix: @child / #topic at message start parse; mid-content @/# are
// references, not nav (gated TestParsePrefix — incl. the forwarded-tweet regression).
func TestParsePrefix(t *testing.T) {
	cases := []struct {
		name, in, wantName, wantRest string
		wantOK                       bool
	}{
		{"@ at start", "@alice hello world", "alice", "hello world", true},
		{"@ mid-content ignored", "hello @alice world", "", "", false},
		{"twitter content ignored", "Solana's Napster Era Is Over\nbuffalu\n@buffalu__\n·\n7h", "", "", false},
		{"leading whitespace", "  @alice hello", "alice", "hello", true},
		{"# at start", "#topic rest of message", "topic", "rest of message", true},
		{"# mid-sentence ignored", "ask #general for help", "", "", false},
		{"none", "no prefix here", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, c := range cases {
		name, rest, ok := parsePrefix(c.in)
		if ok != c.wantOK || name != c.wantName || rest != c.wantRest {
			t.Errorf("parsePrefix(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, name, rest, ok, c.wantName, c.wantRest, c.wantOK)
		}
	}
}
