package gateway

import (
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
)

func TestIsStickyCommand(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"@", true},
		{"@agent", true},
		{"@agent-name", true},
		{"#", true},
		{"#topic", true},
		{"#topic-name", true},
		{"  @agent  ", true}, // trimmed
		{"  #topic  ", true}, // trimmed

		// Not sticky commands
		{"", false},
		{"hello", false},
		{"@agent hello", false},    // has space
		{"hello @agent", false},    // content before
		{"#topic message", false},  // has space
		{"/#topic", false},         // wrong prefix
		{"/command", false},        // slash command
		{"@ agent", false},         // space after @
		{"# topic", false},         // space after #
	}

	for _, tc := range cases {
		got := isStickyCommand(tc.input)
		if got != tc.want {
			t.Errorf("isStickyCommand(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestStickyGroup_SetAndGet(t *testing.T) {
	gw, s := testGateway(t)

	jid := "tg:123"

	// Initially empty
	if got := s.GetStickyGroup(jid); got != "" {
		t.Errorf("initial GetStickyGroup = %q, want empty", got)
	}

	// Set group
	if err := s.SetStickyGroup(jid, "target-group"); err != nil {
		t.Fatal(err)
	}

	got := s.GetStickyGroup(jid)
	if got != "target-group" {
		t.Errorf("GetStickyGroup = %q, want %q", got, "target-group")
	}

	// Clear group
	if err := s.SetStickyGroup(jid, ""); err != nil {
		t.Fatal(err)
	}

	if got := s.GetStickyGroup(jid); got != "" {
		t.Errorf("after clear GetStickyGroup = %q, want empty", got)
	}

	_ = gw // suppress unused
}

func TestStickyTopic_SetAndGet(t *testing.T) {
	gw, s := testGateway(t)

	jid := "tg:123"

	// Initially empty
	if got := s.GetStickyTopic(jid); got != "" {
		t.Errorf("initial GetStickyTopic = %q, want empty", got)
	}

	// Set topic
	if err := s.SetStickyTopic(jid, "support"); err != nil {
		t.Fatal(err)
	}

	got := s.GetStickyTopic(jid)
	if got != "support" {
		t.Errorf("GetStickyTopic = %q, want %q", got, "support")
	}

	// Clear topic
	if err := s.SetStickyTopic(jid, ""); err != nil {
		t.Fatal(err)
	}

	if got := s.GetStickyTopic(jid); got != "" {
		t.Errorf("after clear GetStickyTopic = %q, want empty", got)
	}

	_ = gw // suppress unused
}

func TestResolveTarget_WithStickyGroup(t *testing.T) {
	gw, s := testGateway(t)

	jid := "tg:123"
	s.SetStickyGroup(jid, "sticky-target")

	// Route with sticky set
	routes := []core.Route{
		{Target: "default-group"},
	}
	msg := core.Message{
		Content: "hello",
		ChatJID: jid,
	}

	got := gw.resolveTarget(msg, routes, "self")
	if got != "sticky-target" {
		t.Errorf("resolveTarget = %q, want %q (sticky should override default)", got, "sticky-target")
	}
}

func TestResolveTarget_StickyGroupSameAsSelf(t *testing.T) {
	gw, s := testGateway(t)

	jid := "tg:123"
	s.SetStickyGroup(jid, "self")

	routes := []core.Route{
		{Target: "other-group"},
	}
	msg := core.Message{
		Content: "hello",
		ChatJID: jid,
	}

	// Sticky = self should not route
	got := gw.resolveTarget(msg, routes, "self")
	if got != "" {
		t.Errorf("resolveTarget = %q, want empty (sticky=self should stay)", got)
	}
}

func TestResolveTarget_ReplyChainOverridesStickyGroup(t *testing.T) {
	gw, s := testGateway(t)

	jid := "tg:123"

	// Set sticky group
	s.SetStickyGroup(jid, "sticky-target")

	// Store a bot message routed to different group
	botMsg := core.Message{
		ID:        "bot-msg-1",
		ChatJID:   jid,
		Sender:    "test",
		Name:      "test",
		Content:   "bot response",
		Timestamp: time.Now(),
		BotMsg:    true,
		RoutedTo:  "reply-target",
	}
	if err := s.PutMessage(botMsg); err != nil {
		t.Fatal(err)
	}

	// Reply to bot message - reply chain should take precedence
	routes := []core.Route{}
	msg := core.Message{
		Content:   "reply",
		ChatJID:   jid,
		ReplyToID: "bot-msg-1",
	}

	got := gw.resolveTarget(msg, routes, "self")
	if got != "reply-target" {
		t.Errorf("resolveTarget = %q, want %q (reply chain should override sticky)", got, "reply-target")
	}
}

func TestHandleStickyCommand_SetGroup(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	// Create a group and its default route
	s.PutGroup(core.Group{
		Name:   "Test Group",
		Folder: "test-group",
	})
	s.AddRoute(core.Route{Match: "room=test-group", Target: "test-group"})
	gw.loadState()

	// Create mock channel
	ch := &mockChannel{name: "tg", jids: []string{jid}}
	gw.AddChannel(ch)

	msg := core.Message{
		Content: "@test-group",
		ChatJID: jid,
		Sender:  "user:1",
		BotMsg:  false,
	}

	handled := gw.handleStickyCommand(jid, msg)
	if !handled {
		t.Fatal("handleStickyCommand should have handled @test-group")
	}

	// Check sticky group was set
	got := s.GetStickyGroup(jid)
	if got != "test-group" {
		t.Errorf("GetStickyGroup = %q, want %q", got, "test-group")
	}

	// Check confirmation message
	sent := ch.lastSent()
	if sent != "routing → test-group" {
		t.Errorf("sent message = %q, want confirmation", sent)
	}
}

func TestHandleStickyCommand_SetGroupNotFound(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	ch := &mockChannel{name: "tg", jids: []string{jid}}
	gw.AddChannel(ch)

	msg := core.Message{
		Content: "@nonexistent",
		ChatJID: jid,
		Sender:  "user:1",
		BotMsg:  false,
	}

	handled := gw.handleStickyCommand(jid, msg)
	if !handled {
		t.Fatal("handleStickyCommand should have handled @nonexistent")
	}

	// Sticky group should NOT be set
	got := s.GetStickyGroup(jid)
	if got != "" {
		t.Errorf("GetStickyGroup = %q, want empty (group not found)", got)
	}

	// Check error message
	sent := ch.lastSent()
	if sent != "Failed: group \"nonexistent\" not found" {
		t.Errorf("sent message = %q, want error", sent)
	}
}

func TestHandleStickyCommand_ClearGroup(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	// Set initial sticky
	s.SetStickyGroup(jid, "some-group")

	ch := &mockChannel{name: "tg", jids: []string{jid}}
	gw.AddChannel(ch)

	msg := core.Message{
		Content: "@",
		ChatJID: jid,
		Sender:  "user:1",
		BotMsg:  false,
	}

	handled := gw.handleStickyCommand(jid, msg)
	if !handled {
		t.Fatal("handleStickyCommand should have handled @")
	}

	// Check sticky group was cleared
	got := s.GetStickyGroup(jid)
	if got != "" {
		t.Errorf("GetStickyGroup = %q, want empty (cleared)", got)
	}

	// Check confirmation message
	sent := ch.lastSent()
	if sent != "routing reset to default" {
		t.Errorf("sent message = %q, want confirmation", sent)
	}
}

func TestHandleStickyCommand_SetTopic(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	ch := &mockChannel{name: "tg", jids: []string{jid}}
	gw.AddChannel(ch)

	msg := core.Message{
		Content: "#support",
		ChatJID: jid,
		Sender:  "user:1",
		BotMsg:  false,
	}

	handled := gw.handleStickyCommand(jid, msg)
	if !handled {
		t.Fatal("handleStickyCommand should have handled #support")
	}

	// Check sticky topic was set
	got := s.GetStickyTopic(jid)
	if got != "support" {
		t.Errorf("GetStickyTopic = %q, want %q", got, "support")
	}

	// Check confirmation message
	sent := ch.lastSent()
	if sent != "topic → support" {
		t.Errorf("sent message = %q, want confirmation", sent)
	}
}

func TestHandleStickyCommand_ClearTopic(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	// Set initial sticky
	s.SetStickyTopic(jid, "some-topic")

	ch := &mockChannel{name: "tg", jids: []string{jid}}
	gw.AddChannel(ch)

	msg := core.Message{
		Content: "#",
		ChatJID: jid,
		Sender:  "user:1",
		BotMsg:  false,
	}

	handled := gw.handleStickyCommand(jid, msg)
	if !handled {
		t.Fatal("handleStickyCommand should have handled #")
	}

	// Check sticky topic was cleared
	got := s.GetStickyTopic(jid)
	if got != "" {
		t.Errorf("GetStickyTopic = %q, want empty (cleared)", got)
	}

	// Check confirmation message
	sent := ch.lastSent()
	if sent != "topic reset to default" {
		t.Errorf("sent message = %q, want confirmation", sent)
	}
}

func TestHandleStickyCommand_BotIgnored(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	msg := core.Message{
		Content: "@agent",
		ChatJID: jid,
		Sender:  "bot:1",
		BotMsg:  true, // Bot message
	}

	handled := gw.handleStickyCommand(jid, msg)
	if handled {
		t.Fatal("handleStickyCommand should ignore bot messages")
	}

	// Sticky should not be set
	got := s.GetStickyGroup(jid)
	if got != "" {
		t.Errorf("GetStickyGroup = %q, want empty (bot ignored)", got)
	}
}

func TestHandleStickyCommand_SchedulerIgnored(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	msg := core.Message{
		Content: "@agent",
		ChatJID: jid,
		Sender:  "timed-isolated-1",
		BotMsg:  false,
	}

	handled := gw.handleStickyCommand(jid, msg)
	if handled {
		t.Fatal("handleStickyCommand should ignore scheduler messages")
	}

	// Sticky should not be set
	got := s.GetStickyGroup(jid)
	if got != "" {
		t.Errorf("GetStickyGroup = %q, want empty (scheduler ignored)", got)
	}
}

func TestHandleStickyCommand_NotStickyCommand(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	msg := core.Message{
		Content: "@agent hello",
		ChatJID: jid,
		Sender:  "user:1",
		BotMsg:  false,
	}

	handled := gw.handleStickyCommand(jid, msg)
	if handled {
		t.Fatal("handleStickyCommand should not handle @agent with content")
	}

	// Sticky should not be set
	got := s.GetStickyGroup(jid)
	if got != "" {
		t.Errorf("GetStickyGroup = %q, want empty (not sticky command)", got)
	}
}

func TestEffectiveTopic_PrefersStickyOverMessage(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	// Set sticky topic
	s.SetStickyTopic(jid, "sticky-topic")

	// Message has different topic
	got := gw.effectiveTopic(jid, "message-topic")
	if got != "sticky-topic" {
		t.Errorf("effectiveTopic = %q, want %q (sticky should override)", got, "sticky-topic")
	}
}

func TestEffectiveTopic_FallsBackToMessage(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	// No sticky topic set
	got := gw.effectiveTopic(jid, "message-topic")
	if got != "message-topic" {
		t.Errorf("effectiveTopic = %q, want %q (should use message)", got, "message-topic")
	}

	_ = s // suppress unused
}

func TestEffectiveTopic_BothEmpty(t *testing.T) {
	gw, s := testGateway(t)
	jid := "tg:123"

	// No sticky, no message topic
	got := gw.effectiveTopic(jid, "")
	if got != "" {
		t.Errorf("effectiveTopic = %q, want empty", got)
	}

	_ = s // suppress unused
}
