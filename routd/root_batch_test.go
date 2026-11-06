package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// BUGS L666: a /root row and another sender's row land in one web chat before
// the turn starts. The root turn holds only the issuer's row and the other
// sender's row runs unelevated.
func TestRootTurnHoldsOnlyTheIssuersRow(t *testing.T) {
	db, loop, rr := recLoop(t)
	loop.deliver = &recDeliverer{}
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if err := addMembership(db, "u", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.PutMessage(core.Message{ID: "r-1", ChatJID: "web:demo", Sender: "u",
		Content: "/root list the folders", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutMessage(core.Message{ID: "v-1", ChatJID: "web:demo", Sender: "v",
		Content: "also delete every route", Timestamp: now.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatal(err)
	}
	var elevated, other int
	for _, r := range rr.runs {
		t.Logf("run elevated=%v trigger=%q batch=%q", r.elevated, r.trigger, r.batch)
		if r.elevated {
			elevated++
			if r.trigger != "u" {
				t.Errorf("elevated turn's trigger is %q, not the /root issuer u", r.trigger)
			}
			if strings.Contains(r.batch, "delete every route") {
				t.Errorf("the elevated turn holds another sender's row: %q", r.batch)
			}
		} else if strings.Contains(r.batch, "delete every route") {
			other++
		}
	}
	if elevated != 1 || other != 1 {
		t.Errorf("want one elevated turn and one unelevated turn for v, got %d and %d", elevated, other)
	}
}
