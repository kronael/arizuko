package routd

import (
	"strings"
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
		Topic: topic, Timestamp: time.Now().UTC(), Verb: "message"})
	if _, err := loop.processGroupMessages(atChat); err != nil {
		t.Fatal(err)
	}
}

// ranIn returns the runs a folder got.
func ranIn(rr *recRunner, folder string) (out []turnRec) {
	for _, r := range rr.runs {
		if r.folder == folder {
			out = append(out, r)
		}
	}
	return out
}

func atLoopRec(t *testing.T, folders ...string) (*DB, *Loop, *recRunner) {
	t.Helper()
	db, loop, rr := recLoop(t)
	loop.engagementTTL = time.Hour
	for _, f := range folders {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	return db, loop, rr
}

func TestAtRoute_StartsATurnWhereTheChatOnlyObserves(t *testing.T) {
	db, loop, rr := atLoopRec(t, "sloth", "sloth/discord", "elsewhere")
	doSetRoutes(t, db, []core.Route{
		{Match: "at=killer", Target: "elsewhere"},
		{Match: "platform=slack", Target: "sloth/discord?observe"},
	})
	atSend(t, db, loop, "a", "@killer take this", "th1")
	runs := ranIn(rr, "elsewhere")
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one turn in elsewhere", rr.runs)
	}
	if !strings.Contains(runs[0].batch, "take this") || strings.Contains(runs[0].batch, "@killer") {
		t.Fatalf("the turn did not see the stripped text: %q", runs[0].batch)
	}
	if f, ok := db.Engaged(atChat, "th1"); !ok || f != "elsewhere" {
		t.Fatalf("engaged = %q,%v want elsewhere", f, ok)
	}
	r := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Content: "@killer x", Verb: "message"})
	if !r.ok || !r.Addressed || r.Folder != "elsewhere" {
		t.Fatalf("resolution = %+v", r)
	}
}

func TestAtRoute_NoticeModesAddressDestination(t *testing.T) {
	for _, mode := range []string{"announce", "issues"} {
		t.Run(mode, func(t *testing.T) {
			db, loop, rr := atLoopRec(t, "elsewhere", "sloth/discord")
			doSetRoutes(t, db, []core.Route{{Match: "at=killer", Target: "elsewhere?" + mode}, {Match: "platform=slack", Target: "sloth/discord?observe"}})
			atSend(t, db, loop, "a", "@killer take this", "th1")
			if len(ranIn(rr, "elsewhere")) != 1 {
				t.Fatalf("runs = %+v", rr.runs)
			}
			r := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Content: "@killer x", Verb: "message"})
			if !r.ok || !r.Addressed || !r.AtName || r.Folder != "elsewhere" {
				t.Fatalf("resolution = %+v", r)
			}
		})
	}
}

func TestAtName_RelativeStartsATurnWhereTheChatOnlyObserves(t *testing.T) {
	db, loop, rr := atLoopRec(t, "sloth", "sloth/discord", "sloth/killer")
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "sloth/discord?observe"}})
	atSend(t, db, loop, "a", "@killer take this", "th1")
	runs := ranIn(rr, "sloth/killer")
	if len(runs) != 1 || !strings.Contains(runs[0].batch, "take this") || strings.Contains(runs[0].batch, "@killer") {
		t.Fatalf("runs = %+v", rr.runs)
	}
	if f, ok := db.Engaged(atChat, "th1"); !ok || f != "sloth/killer" {
		t.Fatalf("engaged = %q,%v want sloth/killer", f, ok)
	}
	// The thread follows: a plain message is observed under the engaged folder.
	plain := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Topic: "th1", Verb: "message"})
	if !plain.ok || plain.Folder != "sloth/killer" || plain.Addressed {
		t.Fatalf("plain follow-up = %+v", plain)
	}
	other := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Topic: "th2", Verb: "message"})
	if other.ok || other.Observe != "sloth/discord" {
		t.Fatalf("unengaged thread = %+v", other)
	}
}

func TestAtName_UnresolvedStaysObserved(t *testing.T) {
	db, loop, rr := atLoopRec(t, "sloth", "sloth/discord")
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "sloth/discord?observe"}})
	atSend(t, db, loop, "a", "@nobody take this", "")
	if len(rr.runs) != 0 {
		t.Fatalf("an unresolved @name started a turn: %+v", rr.runs)
	}
	r := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Content: "@nobody x", Verb: "message"})
	if r.ok || r.Observe != "sloth/discord" {
		t.Fatalf("resolution = %+v", r)
	}
	if _, ok := db.Engaged(atChat, ""); ok {
		t.Fatal("an unresolved @name opened a window")
	}
}

func TestAtName_NoEngagementWithoutTTL(t *testing.T) {
	db, loop, rr := atLoopRec(t, "sloth", "sloth/discord", "sloth/killer")
	loop.engagementTTL = 0
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "sloth/discord"}})
	atSend(t, db, loop, "a", "@killer x", "")
	if len(ranIn(rr, "sloth/killer")) != 1 {
		t.Fatalf("no turn in sloth/killer: %+v", rr.runs)
	}
	if _, ok := db.Engaged(atChat, ""); ok {
		t.Fatal("zero TTL opened a window")
	}
}

func TestAtRoute_WinsOverRelativeAndMayNameAnyFolder(t *testing.T) {
	db, loop, rr := atLoopRec(t, "sloth", "sloth/discord", "sloth/discord/killer", "sloth/killer", "elsewhere")
	doSetRoutes(t, db, []core.Route{
		{Match: "room=T/C/X at=killer", Target: "sloth/killer"},
		{Match: "at=ext", Target: "elsewhere"},
		{Match: "platform=slack", Target: "sloth/discord"},
	})
	atSend(t, db, loop, "a", "@killer one", "")
	if len(ranIn(rr, "sloth/killer")) != 1 || len(ranIn(rr, "sloth/discord/killer")) != 0 {
		t.Fatalf("at= row did not win over the relative child: %+v", rr.runs)
	}
	atSend(t, db, loop, "b", "@ext two", "")
	if len(ranIn(rr, "elsewhere")) != 1 {
		t.Fatalf("global at= target: %+v", rr.runs)
	}
}

func TestAtRoute_ScopedRowFallsBackToRelative(t *testing.T) {
	db, loop, rr := atLoopRec(t, "sloth", "sloth/discord", "sloth/discord/killer", "sloth/killer")
	doSetRoutes(t, db, []core.Route{
		{Match: "room=OTHER/room at=killer", Target: "sloth/killer"},
		{Match: "platform=slack", Target: "sloth/discord"},
	})
	atSend(t, db, loop, "a", "@killer one", "")
	if len(ranIn(rr, "sloth/discord/killer")) != 1 || len(ranIn(rr, "sloth/killer")) != 0 {
		t.Fatalf("relative default not used: %+v", rr.runs)
	}
}

func TestAtRoute_ReadInSeqOrderWithOtherRows(t *testing.T) {
	db, loop := atLoop(t, "sloth", "sloth/discord", "elsewhere")
	doSetRoutes(t, db, []core.Route{
		{Match: "platform=slack", Target: "sloth/discord?observe"},
		{Match: "at=killer", Target: "elsewhere"},
	})
	got := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Content: "@killer hi", Verb: "message"})
	if got.ok || got.Observe != "sloth/discord" {
		t.Fatalf("a later at= row beat an earlier row: %+v", got)
	}
}

func TestAtRoute_NeverMatchesWithoutALeadingName(t *testing.T) {
	db, loop := atLoop(t, "sloth", "sloth/discord", "sloth/killer")
	doSetRoutes(t, db, []core.Route{
		{Match: "at=killer", Target: "sloth/killer"},
		{Match: "platform=slack", Target: "sloth/discord"},
	})
	for _, c := range []string{"hello", "hi @killer", ""} {
		got := loop.resolve(atChat, core.Message{ChatJID: atChat, Sender: "u", Content: c, Verb: "message"})
		if got.Folder != "sloth/discord" || got.AtName {
			t.Fatalf("%q: an at= row took a message with no leading @name: %+v", c, got)
		}
	}
}

func TestAtName_EnvelopeDoesNotHideThePrefix(t *testing.T) {
	db, loop, rr := atLoopRec(t, "sloth", "sloth/discord", "sloth/killer")
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "sloth/discord?observe"}})
	atSend(t, db, loop, "a", "[github o/r#1 comment] \n\n@killer look", "")
	runs := ranIn(rr, "sloth/killer")
	if len(runs) != 1 || !strings.Contains(runs[0].batch, "[github o/r#1 comment]") || !strings.Contains(runs[0].batch, "look") ||
		strings.Contains(runs[0].batch, "@killer") {
		t.Fatalf("runs = %+v", rr.runs)
	}
}
