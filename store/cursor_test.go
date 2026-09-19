package store

// cursor_test.go proves the one property the two cursors have: they move
// forward and never back. A cursor that walks back re-feeds every message
// between the two values, and a cursor that jumps forward skips them for good
// (BUGS L97, routd migration 0044).
//
// WHY THERE IS NO SLIP DOUBLE HERE, and it is the property rather than an
// omission. conformance.SlipAt forces an interleave by firing between a read and
// the write that follows it, and advanceCursor has no such window on the path
// that commits: the decision IS the write's condition, so two writers are
// serialised by the engine and not by a read either of them performed. The
// interleave is therefore written out — one call commits, the next call is the
// racer that lost — and it is deterministic because each attempt is its own
// transaction. The one body that DOES read before writing is the retry, and
// TestAdvancingToACursorAlreadyHeldIsANoOpAndNotAnError drives that window.

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// at renders an instant at core.TS's width, which is the only width a declared
// ordered attribute accepts.
func at(hour int) string {
	return core.TS(time.Date(2026, 9, 9, hour, 0, 0, 0, time.UTC))
}

// TestAnObservedCursorNeverWalksBackward is the mutation target. Make the
// transition an unconditional write — drop the Lt condition in
// store/cursor.go's first attempt — and the second advance below moves the
// cursor back to 10:00, which is the whole failure.
func TestAnObservedCursorNeverWalksBackward(t *testing.T) {
	s := openEntities(t)
	const folder, topic = "team", "#deploy"

	if err := s.AdvanceObservedCursor(folder, topic, at(12)); err != nil {
		t.Fatalf("open the cursor: %v", err)
	}
	if err := s.AdvanceObservedCursor(folder, topic, at(10)); err != nil {
		t.Fatalf("the losing advance must be a no-op, not an error: %v", err)
	}
	line, ok := s.TopicLineage(folder, topic)
	if !ok {
		t.Fatal("the topic lost its row")
	}
	if line.ObservedCursor != at(12) {
		t.Fatalf("observed cursor = %q, want %q: an advance to an OLDER instant walked the "+
			"cursor back, and every message between the two is fed to the agent again",
			line.ObservedCursor, at(12))
	}

	// Forward still moves, or the guard would pass by refusing everything.
	if err := s.AdvanceObservedCursor(folder, topic, at(14)); err != nil {
		t.Fatalf("advance forward: %v", err)
	}
	if line, _ := s.TopicLineage(folder, topic); line.ObservedCursor != at(14) {
		t.Fatalf("observed cursor = %q, want %q: the guard refuses a forward move too, so "+
			"the test above would pass against a write that never happens",
			line.ObservedCursor, at(14))
	}
}

// TestAnObservedCursorOpensTheRowItNeedsTo pins why the transition is an upsert
// and not an update: buildAgentPrompt advances the cursor BEFORE SetSession
// creates the row, so a write that only updated matched nothing on a topic's
// first turns and re-included the whole observed window every time.
func TestAnObservedCursorOpensTheRowItNeedsTo(t *testing.T) {
	s := openEntities(t)
	if err := s.AdvanceObservedCursor("team", "#new", at(9)); err != nil {
		t.Fatalf("advance onto a topic with no row: %v", err)
	}
	line, ok := s.TopicLineage("team", "#new")
	if !ok {
		t.Fatal("the advance wrote no row, so the next prompt re-reads the whole window")
	}
	if line.ObservedCursor != at(9) {
		t.Fatalf("observed cursor = %q, want %q", line.ObservedCursor, at(9))
	}

	// The other half: a row that exists and has never carried a cursor. That is
	// what SetSession leaves behind, and it is a different attempt.
	if err := s.SetSession("team", "#second", "sess-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceObservedCursor("team", "#second", at(9)); err != nil {
		t.Fatalf("advance onto a row with no cursor: %v", err)
	}
	line, _ = s.TopicLineage("team", "#second")
	if line.ObservedCursor != at(9) {
		t.Fatalf("observed cursor = %q, want %q — the attempt for a row that carries no "+
			"cursor did not fire", line.ObservedCursor, at(9))
	}
	if got, _ := s.GetSession("team", "#second"); got != "sess-1" {
		t.Errorf("session id = %q, want sess-1: the cursor write replaced the item instead "+
			"of merging into it", got)
	}
}

// TestAdvancingToACursorAlreadyHeldIsANoOpAndNotAnError drives the ONE window
// advanceCursor reads in: three refusals, then the read that decides whether the
// cursor is already ahead or an item appeared underneath.
//
// The idempotent re-call is the common case that lands there — the same instant
// advanced twice, which every re-fed batch produces — and it must answer "done"
// rather than exhaust the rounds and report a fault. Take the read's early
// return out of store/cursor.go and this goes red with the retry's own error.
func TestAdvancingToACursorAlreadyHeldIsANoOpAndNotAnError(t *testing.T) {
	s := openEntities(t)
	const folder, topic = "team", "#deploy"

	if err := s.AdvanceObservedCursor(folder, topic, at(12)); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceObservedCursor(folder, topic, at(12)); err != nil {
		t.Fatalf("advancing to the instant already held: %v", err)
	}
	if line, _ := s.TopicLineage(folder, topic); line.ObservedCursor != at(12) {
		t.Fatalf("observed cursor = %q, want %q", line.ObservedCursor, at(12))
	}
	// The same on a cursor that is behind, which is the other state the read
	// finds: a losing advance is a no-op and not a fault either.
	if err := s.AdvanceObservedCursor(folder, topic, at(10)); err != nil {
		t.Fatalf("an advance that is already behind: %v", err)
	}
}

// TestAResetTopicKeepsTheResetRatherThanTheCursorItHeld pins what a `/new` reset
// racing a prompt build leaves behind: the topic may come back, but never
// carrying the cursor the reset removed.
func TestAResetTopicKeepsTheResetRatherThanTheCursorItHeld(t *testing.T) {
	s := openEntities(t)
	const folder, topic = "team", "#deploy"

	if err := s.AdvanceObservedCursor(folder, topic, at(12)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(folder, topic); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceObservedCursor(folder, topic, at(10)); err != nil {
		t.Fatalf("the advance that lost the race to the reset: %v", err)
	}
	line, ok := s.TopicLineage(folder, topic)
	if ok && line.ObservedCursor == at(12) {
		t.Fatal("the reset topic came back carrying the cursor the reset removed")
	}
}

// TestACursorRefusesAValueOfAnotherWidth is the ordered declaration's mutation
// target, and it pins TWO refusals now.
//
// Delete the objstore.OrderedLead call in store/chats.go's init and the
// twenty-byte cursor lands, sorting before every padded row of its own second —
// BUGS L97 read off live data. Delete requireMessageCursor and the BARE INSTANT
// lands, which is the newer half: a dispatch cursor holds a whole message
// position, and a bare instant sorts below every message at that instant, so
// `> cursor` hands the whole second back on every poll for ever.
func TestACursorRefusesAValueOfAnotherWidth(t *testing.T) {
	s := openEntities(t)
	const jid = "slack:T/C/U"

	err := s.SetAgentCursor(jid, "2026-09-09T12:00:00Z")
	if err == nil {
		t.Fatal("a twenty-byte cursor was accepted; it sorts before every padded row of its " +
			"own second, so the poll loop skips them for good (BUGS L97)")
	}
	if got, _ := s.AgentCursor(jid); got != "" {
		t.Errorf("the refused write still landed: cursor = %q", got)
	}
	// A BARE INSTANT of the declared width is refused too, and that is the half
	// the composite key adds: it names the start of a second rather than a
	// message in it.
	if err := s.SetAgentCursor(jid, at(12)); err == nil {
		t.Fatal("a bare instant was accepted as a dispatch cursor; it sorts below every " +
			"message at its own instant, so the poll loop re-feeds that instant for ever")
	}
	// A WHOLE POSITION is accepted, or the two refusals above would prove nothing.
	want := MsgStamp(at(12), "m1")
	if err := s.SetAgentCursor(jid, want); err != nil {
		t.Fatalf("a whole message position was refused: %v", err)
	}
	if got, _ := s.AgentCursor(jid); got != want {
		t.Errorf("cursor = %q, want %q", got, want)
	}

	// The same rule on the other cursor, and on the two instants beside them.
	if err := s.AdvanceObservedCursor("team", "#x", "2026-09-09T12:00:00Z"); err == nil {
		t.Error("sessions.observed_cursor accepted a twenty-byte value")
	}
}

// TestBumpEngagementWithNoFolderUnclaimsTheWindow pins what an empty folder
// means. The column it replaces was NOT NULL DEFAULT ” and this write
// overwrote it unconditionally, so an empty folder meant UNCLAIMED — and absent
// is how this model spells that.
//
// Take the Remove out of store/chat_reply_state.go's bumpEngagement and this
// goes red with the previous folder still holding a window an operator released.
// POST /v1/engagement reaches it: ownsFolder reports true for an empty target.
func TestBumpEngagementWithNoFolderUnclaimsTheWindow(t *testing.T) {
	s := openEntities(t)
	const jid = "slack:c/1"
	until := time.Now().Add(time.Hour)

	if err := s.BumpEngagement(jid, "", "corp/eng", until); err != nil {
		t.Fatal(err)
	}
	if e, live := s.Engagement(jid, ""); !live || e.Folder != "corp/eng" {
		t.Fatalf("seed: %+v live=%v", e, live)
	}
	if err := s.BumpEngagement(jid, "", "", until); err != nil {
		t.Fatal(err)
	}
	e, live := s.Engagement(jid, "")
	if !live {
		t.Fatal("the window ended; only a zero deadline ends one")
	}
	if e.Folder != "" {
		t.Fatalf("the window is still claimed by %q after a bump that named no folder", e.Folder)
	}
}

// TestRecordingADirectChatOpensItsRow pins that recording a DIRECT chat is a
// write and not a no-op: the flag is the attribute's absence, so the only thing
// left to say is that the chat exists, and a caller that names one this store
// has never seen must still end up with a row.
func TestRecordingADirectChatOpensItsRow(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	if err := s.SetChatIsGroup("slack:NEW", false); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountChats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the partition holds %d chats, want 1: recording a direct chat left no row, "+
			"so the instance has no record it saw the chat", n)
	}
	if is, _ := s.ChatIsGroup("slack:NEW"); is {
		t.Error("a direct chat reads as multi-party")
	}
}

// TestTheProactiveCooldownMergesRatherThanReplaces pins the one write among the
// five that was a bare whole-item Put. It costs nothing while the item carries
// one attribute; it drops the second one the day this entity grows a field.
func TestTheProactiveCooldownMergesRatherThanReplaces(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	const jid = "slack:T/C/P"
	key, err := proactiveKey(jid)
	if err != nil {
		t.Fatal(err)
	}
	// A second attribute, standing in for the field this entity has not grown yet.
	if err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: ChatProactive, Key: key,
		Attrs: map[string]string{"cooldown_hours": "48"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProactiveFire(ctx, jid, time.Now()); err != nil {
		t.Fatal(err)
	}
	it, ok, err := s.itemAt(ctx, ChatProactive, key)
	if err != nil || !ok {
		t.Fatalf("the item went: ok=%v err=%v", ok, err)
	}
	if it.Attrs["cooldown_hours"] != "48" {
		t.Errorf("the stamp replaced the whole item: attrs = %v", it.Attrs)
	}
	if it.Attrs[AttrProactiveFiredAt] == "" {
		t.Error("the stamp did not land")
	}
}
