package gateway

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestReplyChainRouting_EmptyReplyTo(t *testing.T) {
	gw, _ := testGateway(t)
	routes := []core.Route{
		{Target: "default-group"},
	}
	msg := core.Message{
		Content:   "hello",
		ReplyToID: "",
	}
	got := gw.resolveTarget(msg, routes, "self")
	if got != "default-group" {
		t.Errorf("resolveTarget = %q, want %q", got, "default-group")
	}
}

func TestReplyChainRouting_WithReplyTo(t *testing.T) {
	gw, s := testGateway(t)

	// Store a bot message that was routed to "routed-group"
	botMsg := core.Message{
		ID:        "bot-msg-1",
		ChatJID:   "tg:123",
		Sender:    "test",
		Name:      "test",
		Content:   "bot response",
		Timestamp: time.Now(),
		BotMsg:    true,
		RoutedTo:  "routed-group",
	}
	if err := s.PutMessage(botMsg); err != nil {
		t.Fatal(err)
	}

	// User replies to that bot message
	routes := []core.Route{
		{Target: "default-group"},
	}
	msg := core.Message{
		Content:   "reply to bot",
		ReplyToID: "bot-msg-1",
	}

	got := gw.resolveTarget(msg, routes, "self")
	if got != "routed-group" {
		t.Errorf("resolveTarget = %q, want %q (should follow reply chain)", got, "routed-group")
	}
}

func TestReplyChainRouting_ReplyToSelfFolder(t *testing.T) {
	gw, s := testGateway(t)

	// Store a bot message routed to "self" (current group)
	botMsg := core.Message{
		ID:        "bot-msg-1",
		ChatJID:   "tg:123",
		Sender:    "test",
		Name:      "test",
		Content:   "bot response",
		Timestamp: time.Now(),
		BotMsg:    true,
		RoutedTo:  "self",
	}
	if err := s.PutMessage(botMsg); err != nil {
		t.Fatal(err)
	}

	// User replies - should return empty (stay in same group)
	routes := []core.Route{
		{Target: "other-group"},
	}
	msg := core.Message{
		Content:   "reply to bot",
		ReplyToID: "bot-msg-1",
	}

	got := gw.resolveTarget(msg, routes, "self")
	if got != "" {
		t.Errorf("resolveTarget = %q, want empty (should stay in self folder)", got)
	}
}

func TestReplyChainRouting_NoRoutedTo(t *testing.T) {
	gw, s := testGateway(t)

	// Store a user message (not a bot message, no routed_to)
	userMsg := core.Message{
		ID:        "user-msg-1",
		ChatJID:   "tg:123",
		Sender:    "user",
		Name:      "user",
		Content:   "user message",
		Timestamp: time.Now(),
		BotMsg:    false,
		RoutedTo:  "",
	}
	if err := s.PutMessage(userMsg); err != nil {
		t.Fatal(err)
	}

	// Reply to user message - should fall back to default routing
	routes := []core.Route{
		{Target: "default-group"},
	}
	msg := core.Message{
		Content:   "reply to user",
		ReplyToID: "user-msg-1",
	}

	got := gw.resolveTarget(msg, routes, "self")
	if got != "default-group" {
		t.Errorf("resolveTarget = %q, want %q (should use default route)", got, "default-group")
	}
}

func TestReplyChainRouting_MessageNotFound(t *testing.T) {
	gw, _ := testGateway(t)

	// Reply to non-existent message
	routes := []core.Route{
		{Target: "default-group"},
	}
	msg := core.Message{
		Content:   "reply to nothing",
		ReplyToID: "non-existent-id",
	}

	got := gw.resolveTarget(msg, routes, "self")
	if got != "default-group" {
		t.Errorf("resolveTarget = %q, want %q (should use default when message not found)", got, "default-group")
	}
}

func TestRoutedToStorage(t *testing.T) {
	_, s := testGateway(t)

	// Store message with routed_to
	msg := core.Message{
		ID:        "msg-1",
		ChatJID:   "tg:123",
		Sender:    "test",
		Name:      "test",
		Content:   "test message",
		Timestamp: time.Now(),
		RoutedTo:  "target-group",
	}
	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}

	// Retrieve routed_to
	routedTo := s.RoutedToByMessageID("msg-1")
	if routedTo != "target-group" {
		t.Errorf("RoutedToByMessageID = %q, want %q", routedTo, "target-group")
	}
}

func TestRoutedToStorage_EmptyString(t *testing.T) {
	_, s := testGateway(t)

	// Store message without routed_to
	msg := core.Message{
		ID:        "msg-1",
		ChatJID:   "tg:123",
		Sender:    "test",
		Name:      "test",
		Content:   "test message",
		Timestamp: time.Now(),
		RoutedTo:  "",
	}
	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}

	// Should return empty
	routedTo := s.RoutedToByMessageID("msg-1")
	if routedTo != "" {
		t.Errorf("RoutedToByMessageID = %q, want empty", routedTo)
	}
}

func TestRoutedToStorage_NotFound(t *testing.T) {
	_, s := testGateway(t)

	// Query non-existent message
	routedTo := s.RoutedToByMessageID("non-existent")
	if routedTo != "" {
		t.Errorf("RoutedToByMessageID = %q, want empty for non-existent", routedTo)
	}
}

func TestTopicStorage(t *testing.T) {
	_, s := testGateway(t)

	// Store message with topic
	msg := core.Message{
		ID:        "msg-1",
		ChatJID:   "tg:123",
		Sender:    "test",
		Name:      "test",
		Content:   "test message",
		Timestamp: time.Now(),
		Topic:     "thread-42",
	}
	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}

	// Retrieve topic
	topic := s.TopicByMessageID("msg-1", "tg:123")
	if topic != "thread-42" {
		t.Errorf("TopicByMessageID = %q, want %q", topic, "thread-42")
	}
}

func TestReplyChainRouting_PrecedenceOverPatternRoute(t *testing.T) {
	gw, s := testGateway(t)

	// Store a bot message routed to "reply-target"
	botMsg := core.Message{
		ID:        "bot-msg-1",
		ChatJID:   "tg:123",
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

	// User replies with content that would match a non-default route
	routes := []core.Route{
		{Match: "sender=urgent:*", Target: "urgent-group"},
		{Target: "default-group"},
	}
	msg := core.Message{
		Content:   "urgent: reply to bot",
		ReplyToID: "bot-msg-1",
	}

	// Reply-chain should take precedence over pattern matching
	got := gw.resolveTarget(msg, routes, "self")
	if got != "reply-target" {
		t.Errorf("resolveTarget = %q, want %q (reply chain should override pattern)", got, "reply-target")
	}
}
