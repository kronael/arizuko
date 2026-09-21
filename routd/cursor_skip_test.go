package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// TestSteerReinjectionOutrunsAPlatformSecond is BUGS L102. An adapter stamps an
// inbound with the PLATFORM's whole second (routd's ingest truncates
// `time.Unix(sec, 0)`), and cmdRoot re-injects the argument stamped from the
// HOST clock. A platform second ahead of the host — three of them, measured on
// sloth — put the re-injection numerically BEHIND the cursor the command itself
// had just set, so `/root <cmd>` acked nothing, ran nothing and leaked its
// pendingElevation entry. Same for `/new <text>` and `#topic <text>`.
func TestSteerReinjectionOutrunsAPlatformSecond(t *testing.T) {
	db, loop, rr := recLoop(t)
	loop.deliver = &recDeliverer{}
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if err := addMembership(db, "u", "role:operator", "test"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	// The platform's clock runs ahead of this host's, and its second is
	// truncated: exactly what an adapter writes.
	platform := time.Now().UTC().Add(5 * time.Second).Truncate(time.Second)
	if err := db.PutMessage(core.Message{ID: "tg-1", ChatJID: "web:demo", Sender: "u",
		Content: "/root mint the token", Timestamp: platform}); err != nil {
		t.Fatalf("put /root: %v", err)
	}

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process /root: %v", err)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("the raw /root reached a turn: %+v", rr.runs)
	}

	// The re-injection has to be READABLE from the cursor the steer just set.
	pending, err := db.NewMessagesForChat("web:demo", db.GetAgentCursor("web:demo"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Content != "mint the token" {
		t.Fatalf("feed after the steer = %+v; the re-injection is buried behind the cursor",
			pending)
	}
	if _, ok := loop.pendingElevation.Load(pending[0].ID); !ok {
		t.Fatal("re-injected row is not registered for elevation")
	}

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process the re-injection: %v", err)
	}
	if len(rr.runs) != 1 || !rr.runs[0].elevated {
		t.Fatalf("runs=%+v; want exactly one ELEVATED turn on the /root argument", rr.runs)
	}
	if _, leaked := loop.pendingElevation.Load(pending[0].ID); leaked {
		t.Error("pendingElevation entry leaked after the elevated turn ran")
	}
}

// TestSameSecondArrivalDuringATurnIsNotSkipped is BUGS L103. Adapters truncate
// platform timestamps to whole seconds, so equal instants inside one chat are
// routine — a Telegram album, a Slack burst, a scheduled fan-out. Two rows of
// one second are then ordered by their IDs, which is arrival order for nothing.
// A row that lands WHILE the turn runs, at the batch tail's second but with a
// lesser id, sorted below the cursor the turn then set, and the strictly-greater
// feed never returned it again: one Telegram upload lost eleven of twelve
// documents. The window is the whole duration of the turn.
func TestSameSecondArrivalDuringATurnIsNotSkipped(t *testing.T) {
	db, loop, rr := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})

	// One whole second, as an adapter writes it.
	sec := time.Now().UTC().Truncate(time.Second)
	if err := db.PutMessage(core.Message{ID: "doc-b", ChatJID: "web:demo", Sender: "u",
		Content: "[Document: one.pdf]", Timestamp: sec}); err != nil {
		t.Fatalf("put the first document: %v", err)
	}
	// The second document of the same upload lands while the first one's turn
	// is in flight. Its id sorts BELOW the tail's — half of every such pair does.
	loop.deliver = &recDeliverer{}
	rr.during = func() {
		if err := db.PutMessage(core.Message{ID: "doc-a", ChatJID: "web:demo", Sender: "u",
			Content: "[Document: two.pdf]", Timestamp: sec}); err != nil {
			t.Errorf("put the second document: %v", err)
		}
	}

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process the first batch: %v", err)
	}
	rr.during = nil

	left, err := db.NewMessagesForChat("web:demo", db.GetAgentCursor("web:demo"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].ID != "doc-a" {
		t.Fatalf("feed after the turn = %+v; the same-second arrival is behind the cursor",
			left)
	}

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process the straggler: %v", err)
	}
	if len(rr.runs) != 2 || !strings.Contains(rr.runs[1].batch, "two.pdf") {
		t.Fatalf("runs=%d, second batch=%q; the second document never reached a turn",
			len(rr.runs), lastBatch(rr.runs))
	}
}

// TestSteerLeavesOlderMessagesTheirTurn is BUGS L104. The steer consumed the
// newest USER row and the cursor advanced past EVERY older row in the batch —
// ordinary messages nothing had dispatched, steered or observed. The user asks
// for something, waits, gets impatient and types a command; both land in one
// batch and the question is skipped for good. An in-flight turn stretches the
// batching window to minutes, so the ordering is common.
func TestSteerLeavesOlderMessagesTheirTurn(t *testing.T) {
	db, loop, rr := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})

	base := time.Now().UTC().Truncate(time.Second)
	if err := db.PutMessage(core.Message{ID: "q1", ChatJID: "web:demo", Sender: "u",
		Content: "please check X", Timestamp: base}); err != nil {
		t.Fatalf("put the question: %v", err)
	}
	if err := db.PutMessage(core.Message{ID: "c1", ChatJID: "web:demo", Sender: "u",
		Content: "/ping", Timestamp: base.Add(time.Second)}); err != nil {
		t.Fatalf("put the command: %v", err)
	}

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}

	// The command still runs and still acks — the steer is not weakened. The
	// ack is looked for among ALL the sends, because the turn this fix restores
	// delivers after it.
	ponged := false
	for _, s := range dl.sends {
		if strings.HasPrefix(s.text, "pong") {
			ponged = true
		}
	}
	if !ponged {
		t.Fatalf("sends=%+v; /ping was not steered", dl.sends)
	}
	// And the question the user asked first reaches a turn.
	if len(rr.runs) != 1 {
		t.Fatalf("runs=%d want 1; the older question was skipped by the steer", len(rr.runs))
	}
	if !strings.Contains(rr.runs[0].batch, "please check X") {
		t.Fatalf("batch=%q; the turn did not carry the older question", rr.runs[0].batch)
	}
	if strings.Contains(rr.runs[0].batch, "/ping") {
		t.Fatalf("batch=%q; the steered command leaked into the turn", rr.runs[0].batch)
	}
	// Both rows are consumed: nothing is re-fed and nothing is left behind.
	left, err := db.NewMessagesForChat("web:demo", db.GetAgentCursor("web:demo"), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range left {
		if m.ID == "q1" || m.ID == "c1" {
			t.Fatalf("%s is still in the feed after its turn closed", m.ID)
		}
	}
}

func lastBatch(runs []turnRec) string {
	if len(runs) == 0 {
		return ""
	}
	return runs[len(runs)-1].batch
}
