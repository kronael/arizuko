package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func atLoop(t *testing.T, folders ...string) (*DB, *Loop) {
	t.Helper()
	db, loop, _ := recLoop(t)
	loop.engagementTTL = time.Hour
	for _, f := range folders {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	return db, loop
}

func TestFindNamedGroup(t *testing.T) {
	_, loop := atLoop(t, "sloth", "sloth/discord", "sloth/discord/killer", "sloth/killer",
		"sloth/ops", "other", "other/spy", "spy", "sloth/team", "sloth/team/a", "top")
	for _, c := range []struct{ from, name, want string }{
		{"sloth/discord", "killer", "sloth/discord/killer"}, // child first
		{"sloth/team", "killer", "sloth/killer"},            // parent's child
		{"sloth/discord", "ops", "sloth/ops"},               // sibling
		{"sloth/team/a", "ops", "sloth/ops"},                // grandparent's child
		{"sloth/discord", "spy", ""},                        // another world's child
		{"sloth/discord", "top", ""},                        // never above the world
		{"sloth/discord", "nope", ""},
	} {
		if got := loop.findNamedGroup(c.from, c.name); got != c.want {
			t.Errorf("findNamedGroup(%q,%q) = %q want %q", c.from, c.name, got, c.want)
		}
	}
}

const atChat = "slack:T/C/X"

func atSend(t *testing.T, db *DB, loop *Loop, id, content, topic string) {
	t.Helper()
	_ = db.PutMessage(core.Message{ID: id, ChatJID: atChat, Sender: "u", Content: content,
		Topic: topic, Timestamp: time.Now().UTC(), Verb: "mention"})
	if _, err := loop.processGroupMessages(atChat); err != nil {
		t.Fatal(err)
	}
}

func TestAtName_DelegatesToAncestorsChildAndEngages(t *testing.T) {
	db, loop := atLoop(t, "sloth", "sloth/discord", "sloth/killer")
	doSetRoutes(t, db, []core.Route{
		{Match: "platform=slack verb=mention", Target: "sloth/discord"},
		{Match: "platform=slack", Target: "sloth/discord#observe"},
	})
	atSend(t, db, loop, "a", "@killer take this", "th1")
	rows, _ := db.MessagesSince("sloth/killer", "")
	if len(rows) != 1 || rows[0].Content != "take this" || rows[0].ForwardedFrom != atChat {
		t.Fatalf("delegation rows = %+v", rows)
	}
	if f, ok := db.Engaged(atChat, "th1"); !ok || f != "sloth/killer" {
		t.Fatalf("engaged = %q,%v want sloth/killer", f, ok)
	}
	// A plain message in the thread is observed under the engaged folder, not
	// addressed; a mention in it is addressed. Another thread without a window
	// stays with the route.
	plain := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Topic: "th1", Verb: "message"})
	if !plain.ok || plain.Folder != "sloth/killer" || plain.Addressed {
		t.Fatalf("plain follow-up = %+v", plain)
	}
	ment := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Topic: "th1", Verb: "mention"})
	if !ment.ok || ment.Folder != "sloth/killer" || !ment.Addressed {
		t.Fatalf("mention follow-up = %+v", ment)
	}
	other := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Topic: "th2", Verb: "message"})
	if other.ok || other.Observe != "sloth/discord" {
		t.Fatalf("unengaged thread = %+v", other)
	}
}

func TestAtName_RootWindowCoversANewThread(t *testing.T) {
	db, loop := atLoop(t, "sloth", "sloth/discord", "sloth/killer")
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "sloth/discord"}})
	atSend(t, db, loop, "a", "@killer take this", "")
	got := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Topic: "newthread", Verb: "message"})
	if !got.ok || got.Folder != "sloth/killer" {
		t.Fatalf("a thread started after the delegation = %+v", got)
	}
}

func TestAtName_NoEngagementWithoutTTL(t *testing.T) {
	db, loop := atLoop(t, "sloth", "sloth/discord", "sloth/killer")
	loop.engagementTTL = 0
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "sloth/discord"}})
	atSend(t, db, loop, "a", "@killer x", "")
	if _, ok := db.Engaged(atChat, ""); ok {
		t.Fatal("zero TTL opened a window")
	}
}

func TestAtRoute_WinsOverRelativeAndMayNameAnyFolder(t *testing.T) {
	db, loop := atLoop(t, "sloth", "sloth/discord", "sloth/discord/killer", "sloth/killer", "elsewhere")
	doSetRoutes(t, db, []core.Route{
		{Match: "room=T/C/X at=killer", Target: "sloth/killer"},
		{Match: "at=ext", Target: "elsewhere"},
		{Match: "platform=slack", Target: "sloth/discord"},
	})
	atSend(t, db, loop, "a", "@killer one", "")
	if rows, _ := db.MessagesSince("sloth/killer", ""); len(rows) != 1 {
		t.Fatalf("at= row did not win: %d rows on sloth/killer", len(rows))
	}
	if rows, _ := db.MessagesSince("sloth/discord/killer", ""); len(rows) != 0 {
		t.Fatalf("relative child took the message: %d", len(rows))
	}
	atSend(t, db, loop, "b", "@ext two", "")
	if rows, _ := db.MessagesSince("elsewhere", ""); len(rows) != 1 {
		t.Fatalf("global at= target: %d rows", len(rows))
	}
}

func TestAtRoute_CombinesWithRoomAndFallsBackToRelative(t *testing.T) {
	db, loop := atLoop(t, "sloth", "sloth/discord", "sloth/discord/killer", "sloth/killer")
	doSetRoutes(t, db, []core.Route{
		{Match: "room=OTHER/room at=killer", Target: "sloth/killer"},
		{Match: "platform=slack", Target: "sloth/discord"},
	})
	atSend(t, db, loop, "a", "@killer one", "")
	if rows, _ := db.MessagesSince("sloth/discord/killer", ""); len(rows) != 1 {
		t.Fatalf("relative default not used: %d rows", len(rows))
	}
	if rows, _ := db.MessagesSince("sloth/killer", ""); len(rows) != 0 {
		t.Fatalf("room-scoped row matched another room: %d", len(rows))
	}
}

func TestAtRoute_NeverRoutesAnOrdinaryMessage(t *testing.T) {
	db, loop := atLoop(t, "sloth", "sloth/discord", "sloth/killer")
	doSetRoutes(t, db, []core.Route{
		{Match: "at=killer", Target: "sloth/killer"},
		{Match: "platform=slack", Target: "sloth/discord"},
	})
	got := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Content: "@killer hi", Verb: "message"})
	if got.Folder != "sloth/discord" {
		t.Fatalf("an at= row routed the chat: %+v", got)
	}
}
