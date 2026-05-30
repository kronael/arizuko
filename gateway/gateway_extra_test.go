package gateway

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// --- setCurrentTurn / clearCurrentTurn / currentTriggerSender / currentTurnTopic ---

func TestCurrentTurn_SetClearAccessors(t *testing.T) {
	gw, _ := testGateway(t)

	// Before set: both return empty.
	if got := gw.currentTriggerSender("grp"); got != "" {
		t.Errorf("before set: triggerSender = %q, want empty", got)
	}
	if got := gw.currentTurnTopic("grp"); got != "" {
		t.Errorf("before set: topic = %q, want empty", got)
	}

	gw.setCurrentTurn("grp", "telegram:user/42", "#deploy")

	if got := gw.currentTriggerSender("grp"); got != "telegram:user/42" {
		t.Errorf("after set: triggerSender = %q, want telegram:user/42", got)
	}
	if got := gw.currentTurnTopic("grp"); got != "#deploy" {
		t.Errorf("after set: topic = %q, want #deploy", got)
	}

	gw.clearCurrentTurn("grp")

	if got := gw.currentTriggerSender("grp"); got != "" {
		t.Errorf("after clear: triggerSender = %q, want empty", got)
	}
	if got := gw.currentTurnTopic("grp"); got != "" {
		t.Errorf("after clear: topic = %q, want empty", got)
	}
}

// Different folders must not see each other's turn state.
func TestCurrentTurn_IsolatedByFolder(t *testing.T) {
	gw, _ := testGateway(t)

	gw.setCurrentTurn("grp1", "sender-A", "#topic-A")
	gw.setCurrentTurn("grp2", "sender-B", "#topic-B")

	if got := gw.currentTriggerSender("grp1"); got != "sender-A" {
		t.Errorf("grp1 triggerSender = %q, want sender-A", got)
	}
	if got := gw.currentTriggerSender("grp2"); got != "sender-B" {
		t.Errorf("grp2 triggerSender = %q, want sender-B", got)
	}
	if got := gw.currentTurnTopic("grp1"); got != "#topic-A" {
		t.Errorf("grp1 topic = %q, want #topic-A", got)
	}
	if got := gw.currentTurnTopic("grp2"); got != "#topic-B" {
		t.Errorf("grp2 topic = %q, want #topic-B", got)
	}

	// Clearing grp1 must not affect grp2.
	gw.clearCurrentTurn("grp1")
	if got := gw.currentTriggerSender("grp2"); got != "sender-B" {
		t.Errorf("grp2 after clearing grp1: triggerSender = %q, want sender-B", got)
	}
}

// clearCurrentTurn on an absent folder must not panic.
func TestCurrentTurn_ClearAbsent_NoPanic(t *testing.T) {
	gw, _ := testGateway(t)
	// Must not panic.
	gw.clearCurrentTurn("does-not-exist")
}

//--- lookupCommand with Slack backslash prefix ---

func TestLookupCommand_BackslashAlias(t *testing.T) {
	// Slack sometimes delivers /new as \new.
	cmd, _ := lookupCommand(`\new`)
	if cmd == nil {
		t.Fatal("lookupCommand(\\new) returned nil, expected /new")
	}
	if cmd.name != "/new" {
		t.Errorf("cmd.name = %q, want /new", cmd.name)
	}
}

// --- cmdStatus tier-0 gate ---

func TestCmdStatus_TierGate(t *testing.T) {
	// /status is root-only. A tier-2 caller must receive "Permission denied".
	gw, _ := testGateway(t)
	ch := &mockChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "world/a/b"})

	grp, _ := gw.store.GroupByFolder("world/a/b")
	msg := core.Message{ChatJID: "jid1", Content: "/status"}
	if !gw.handleCommand(msg, grp) {
		t.Fatal("handleCommand returned false for /status")
	}
	if got := ch.lastSent(); got != "Permission denied: root only." {
		t.Errorf("sent = %q, want permission denied", got)
	}
}

// --- cmdPing reports session and group info ---

func TestCmdPing_ReportsGroupFolder(t *testing.T) {
	gw, s := testGateway(t)
	ch := &mockChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "world/child"})
	s.SetSession("world/child", "", "ping-session-id")

	grp, _ := gw.store.GroupByFolder("world/child")
	msg := core.Message{ChatJID: "jid1", Content: "/ping"}
	if !gw.handleCommand(msg, grp) {
		t.Fatal("handleCommand returned false for /ping")
	}
	reply := ch.lastSent()
	if reply == "" {
		t.Fatal("no reply from /ping")
	}
	if !containsAll(reply, "pong", "world/child", "ping-ses") {
		t.Errorf("ping reply missing expected fields: %q", reply)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if len(s) < len(p) {
			return false
		}
		found := false
		for i := 0; i+len(p) <= len(s); i++ {
			if s[i:i+len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// --- cmdNew with topic ---

func TestCmdNew_WithTopic_ClearsTopicSession(t *testing.T) {
	gw, s := testGateway(t)
	ch := &mockChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "grp"})

	// Seed a topic session.
	s.SetSession("grp", "#deploy", "topic-sess-1")

	grp, _ := gw.store.GroupByFolder("grp")
	msg := core.Message{ChatJID: "jid1", Content: "/new #deploy"}
	gw.handleCommand(msg, grp)

	if id, _ := s.GetSession("grp", "#deploy"); id != "" {
		t.Errorf("topic session not cleared: %q", id)
	}
}

//--- resolveGroup returns false for own-group JID not in DB ---

func TestResolveGroup_UnregisteredBareFolder(t *testing.T) {
	gw, _ := testGateway(t)
	// "ghost" folder is NOT in the DB.
	msg := core.Message{ChatJID: "ghost", Verb: "message"}
	if _, ok := gw.resolveGroup(msg); ok {
		t.Error("resolveGroup returned true for unregistered bare-folder JID")
	}
}

// --- saveState + loadState round-trip ---

func TestSaveAndLoadState_RoundTrip(t *testing.T) {
	gw, _ := testGateway(t)
	want := time.Date(2026, 1, 15, 9, 30, 0, 123456789, time.UTC)
	gw.lastTimestamp = want
	gw.saveState()

	// Reset and reload.
	gw.lastTimestamp = time.Time{}
	gw.loadState()

	if !gw.lastTimestamp.Equal(want) {
		t.Errorf("loaded = %v, want %v", gw.lastTimestamp, want)
	}
}
