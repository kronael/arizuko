package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// A new onboarding request queues one notice to the `#issues` chats; a later
// message of the same request queues none; no `#issues` route is no error.
func TestRouteMissOnboardingNotice(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, nopRunner{}, LoopConfig{OnboardingEnabled: true})
	loop.StopQueue()
	loop.SetOnbodClient(&fakeOnbod{})
	const chat = "telegram:user/7"
	miss := func(id string) {
		loop.routeMiss(chat, []core.Message{{ID: id, ChatJID: chat, Sender: "telegram:user/7", Timestamp: time.Now()}}, resolution{})
	}
	pending := func() []store.PendingMessage {
		rows, err := db.PendingOutbound(time.Now().Add(time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	miss("m0") // no #issues route: no error, no notice
	if got := pending(); len(got) != 0 {
		t.Fatalf("notice without a #issues route: %v", got)
	}

	loop.SetOnbodClient(&fakeOnbod{})
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=telegram:me", Target: "demo?issues"}})
	miss("m1")
	miss("m2")
	miss("m3")
	rows := pending()
	if len(rows) != 1 {
		t.Fatalf("want one notice for one request, got %d: %v", len(rows), rows)
	}
	if r := rows[0]; r.ChatJID != "telegram:me" || !strings.Contains(r.Content, chat) {
		t.Fatalf("notice = %+v", r)
	}
}
