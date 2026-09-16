package chanstore_test

// The storage layer's own bodies, run against the endpoint dyntest lends each
// test as its own namespace.
//
// They are about the two guarantees an adapter would be dangerous without: an
// outbound message is delivered exactly once even though routd re-sends the
// same key by design (BUGS D5), and an inbound message routd refused is held
// until routd takes it, oldest first (BUGS L25).

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// newStore hands a test an empty adapter store in a namespace of its own.
func newStore(t *testing.T) *chanstore.Store {
	t.Helper()
	s := dyntest.Store(t)
	for _, entity := range chanstore.Entities {
		if err := s.CreateEntity(t.Context(), entity); err != nil {
			t.Fatalf("create entity %s: %v", entity, err)
		}
	}
	return chanstore.New(s)
}

// ts renders an instant the way every writer here does.
func ts(t time.Time) string { return core.TS(t) }

// The constant the ordered declarations rest on must be core.TS's own width. A
// constant that drifted from the layout would refuse every write rather than
// guard one, so it is checked against the renderer instead of trusted.
func TestOrderedWidthIsCoreTSsOwnWidth(t *testing.T) {
	if got := len(core.TS(time.Now())); got != 30 {
		t.Fatalf("core.TS renders %d bytes and chanstore declares its ordered "+
			"attributes at 30", got)
	}
}

// --- outbound_dedup ---

// The whole of D5: routd re-sends a row that stays `pending`, so the same key
// arrives twice by design. The second claim must answer with the recorded
// platform id, never with permission to post again.
func TestASettledKeyReadsBackItsPlatformID(t *testing.T) {
	st := newStore(t)
	claim, err := st.ClaimDelivery(t.Context(), "out-1", "slack:c/1")
	if err != nil || claim.State != chanstore.ClaimWon {
		t.Fatalf("the first claim on a fresh key is (%v, %v), want ClaimWon", claim.State, err)
	}
	if err := st.RecordDelivered(t.Context(), "out-1", "plat-1"); err != nil {
		t.Fatal(err)
	}
	again, err := st.ClaimDelivery(t.Context(), "out-1", "slack:c/1")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != chanstore.ClaimSent || again.PlatformID != "plat-1" {
		t.Fatalf("the redelivery claim is (%v, %q), want (ClaimSent, plat-1)",
			again.State, again.PlatformID)
	}
}

// A claim whose platform call never settled reads as inflight. The adapter
// cannot tell a lost response from a lost request, so this is the answer that
// makes it report the ambiguity instead of guessing.
func TestAnUnsettledKeyReadsBackAsInflight(t *testing.T) {
	st := newStore(t)
	if _, err := st.ClaimDelivery(t.Context(), "out-2", "slack:c/1"); err != nil {
		t.Fatal(err)
	}
	again, err := st.ClaimDelivery(t.Context(), "out-2", "slack:c/1")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != chanstore.ClaimInflight {
		t.Fatalf("a second claim on an unsettled key is %v, want ClaimInflight", again.State)
	}
}

// EXACTLY-ONCE UNDER CONTENTION. N concurrent claims on one key elect exactly
// one winner, and the losers are not errors — the engine's own uniqueness
// decides it, never a read this package performed. Every extra winner is one
// extra message the user receives.
func TestClaimElectsOneWinnerUnderConcurrency(t *testing.T) {
	st := newStore(t)
	const n = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			claim, err := st.ClaimDelivery(t.Context(), "out-3", "slack:c/1")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("a lost claim must be a state, not an error: %v", err)
				return
			}
			if claim.State == chanstore.ClaimWon {
				wins++
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d of %d concurrent claims won; one key permits one platform call", wins, n)
	}
}

// A failure that PROVES nothing was posted frees the key, so routd's retry gets
// a fresh claim rather than a permanent refusal.
func TestReleaseFreesAnUnsettledClaim(t *testing.T) {
	st := newStore(t)
	if _, err := st.ClaimDelivery(t.Context(), "out-4", "slack:c/1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseUnsentClaim(t.Context(), "out-4"); err != nil {
		t.Fatal(err)
	}
	again, err := st.ClaimDelivery(t.Context(), "out-4", "slack:c/1")
	if err != nil || again.State != chanstore.ClaimWon {
		t.Fatalf("the claim after a release is (%v, %v), want ClaimWon", again.State, err)
	}
}

// A SETTLED CLAIM SURVIVES A RELEASE. The condition rides on the delete, so a
// release that arrived after the delivery landed cannot take the record away —
// without it the next attempt on the key would post the message a second time.
func TestReleaseLeavesASettledClaim(t *testing.T) {
	st := newStore(t)
	if _, err := st.ClaimDelivery(t.Context(), "out-5", "slack:c/1"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordDelivered(t.Context(), "out-5", "plat-5"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseUnsentClaim(t.Context(), "out-5"); err != nil {
		t.Fatalf("releasing a settled claim is a no-op, not a fault: %v", err)
	}
	again, err := st.ClaimDelivery(t.Context(), "out-5", "slack:c/1")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != chanstore.ClaimSent || again.PlatformID != "plat-5" {
		t.Fatalf("the settled claim reads as (%v, %q) after a release, want (ClaimSent, plat-5)",
			again.State, again.PlatformID)
	}
}

// The read the data move's verify uses answers about a key without CREATING it,
// which is the whole reason it is not ClaimDelivery: a check that wrote its own
// evidence would report every missing row as present.
func TestRecordedClaimWritesNothing(t *testing.T) {
	st := newStore(t)
	if _, held, err := st.RecordedClaim(t.Context(), "out-7"); err != nil || held {
		t.Fatalf("RecordedClaim on a fresh key is (held=%v, %v), want (false, nil)", held, err)
	}
	claim, err := st.ClaimDelivery(t.Context(), "out-7", "slack:c/1")
	if err != nil || claim.State != chanstore.ClaimWon {
		t.Fatalf("the read left a claim behind: the first claim is (%v, %v)", claim.State, err)
	}
	got, held, err := st.RecordedClaim(t.Context(), "out-7")
	if err != nil || !held || got.State != chanstore.ClaimInflight {
		t.Fatalf("RecordedClaim on a claimed key is (%v, held=%v, %v), want ClaimInflight",
			got.State, held, err)
	}
}

// Recording a delivery against a claim that is not there is a fault, not a
// no-op: the next attempt on that key would be free to post the message again.
func TestRecordDeliveredRefusesAMissingClaim(t *testing.T) {
	st := newStore(t)
	if err := st.RecordDelivered(t.Context(), "out-6", "plat-6"); err == nil {
		t.Fatal("recording a delivery for a key nothing claimed must surface")
	}
}

// --- inbound_pending ---

// retain holds one message with a first-seen time the test chooses, so a body
// can lay out an arrival order the ids do not agree with.
func retain(t *testing.T, st *chanstore.Store, id string, seen time.Time) {
	t.Helper()
	if err := st.Retain(t.Context(), id, `{"id":"`+id+`"}`, ts(seen),
		ts(seen.Add(-time.Hour)), "routd said 503"); err != nil {
		t.Fatalf("retain %s: %v", id, err)
	}
}

// The sweep drains OLDEST FIRST and the limit bounds one pass, so a backlog
// bigger than the batch is worked through in arrival order. The ids here sort
// the OPPOSITE way to the arrival times, so a read that ordered by the key
// would answer with the newest three.
func TestDueForRedeliveryAnswersOldestFirstWithinTheLimit(t *testing.T) {
	st := newStore(t)
	base := time.Now().Add(-time.Hour)
	for i, id := range []string{"e", "d", "c", "b", "a"} {
		retain(t, st, id, base.Add(time.Duration(i)*time.Minute))
	}
	due, err := st.DueForRedelivery(t.Context(), ts(time.Now()), 3)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range due {
		got = append(got, d.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"e", "d", "c"}) {
		t.Fatalf("the sweep reads %v, want the three oldest [e d c]", got)
	}
	if due[0].Payload != `{"id":"e"}` || due[0].Attempts != 0 {
		t.Fatalf("the oldest reads back as %+v, want its payload and zero attempts", due[0])
	}
}

// A message whose backoff has not elapsed is not due, which is the whole point
// of the backoff: without the comparison the sweep would re-post every held
// message every 15 seconds.
func TestDueForRedeliveryExcludesWhatIsNotYetDue(t *testing.T) {
	st := newStore(t)
	if err := st.Retain(t.Context(), "later", `{}`, ts(time.Now()),
		ts(time.Now().Add(time.Hour)), "routd said 503"); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueForRedelivery(t.Context(), ts(time.Now()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("%d messages are due while their backoff still runs", len(due))
	}
	if n, err := st.HeldCount(t.Context()); err != nil || n != 1 {
		t.Fatalf("HeldCount = %d, %v; a message not yet due is still held", n, err)
	}
}

// A PLATFORM THAT RE-DELIVERS THE SAME EVENT MUST NOT RESET THE CLOCK. Slack
// retries a non-2xx ack, and a retain that overwrote the row would keep it
// permanently due and turn one undelivered message into a tight retry loop.
func TestRetainLeavesAMessageAlreadyHeld(t *testing.T) {
	st := newStore(t)
	base := time.Now().Add(-time.Hour)
	retain(t, st, "x", base)
	if err := st.Reschedule(t.Context(), "x", 4, ts(time.Now().Add(time.Hour)), "still down"); err != nil {
		t.Fatal(err)
	}
	retain(t, st, "x", time.Now()) // the platform hands the same event over again
	due, err := st.DueForRedelivery(t.Context(), ts(time.Now().Add(2*time.Hour)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Attempts != 4 {
		t.Fatalf("after a platform redelivery the queue is %+v, want one message on its "+
			"fourth attempt", due)
	}
	if now, err := st.DueForRedelivery(t.Context(), ts(time.Now()), 0); err != nil || len(now) != 0 {
		t.Fatalf("%d messages are due now (%v); the redelivery reset the backoff", len(now), err)
	}
}

// A rescheduled message leaves the due set and comes back when its new time
// arrives, which is the backoff the sweep drives.
func TestRescheduleMovesTheMessageOutOfTheDueSet(t *testing.T) {
	st := newStore(t)
	retain(t, st, "x", time.Now().Add(-time.Hour))
	if err := st.Reschedule(t.Context(), "x", 1, ts(time.Now().Add(time.Hour)), "503"); err != nil {
		t.Fatal(err)
	}
	if due, err := st.DueForRedelivery(t.Context(), ts(time.Now()), 0); err != nil || len(due) != 0 {
		t.Fatalf("%d messages due right after a reschedule (%v)", len(due), err)
	}
	due, err := st.DueForRedelivery(t.Context(), ts(time.Now().Add(2*time.Hour)), 0)
	if err != nil || len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("the rescheduled message reads back as %+v (%v), want one on attempt 1", due, err)
	}
}

// Rescheduling a message the sweep just read and nothing else deletes cannot
// silently write nothing: an Update needs its item, and a refusal here means
// something outside the sweep is writing the queue.
func TestRescheduleRefusesAMessageThatIsGone(t *testing.T) {
	st := newStore(t)
	if err := st.Reschedule(t.Context(), "ghost", 1, ts(time.Now()), "503"); err == nil {
		t.Fatal("rescheduling a message nothing holds must surface")
	}
}

// Dropping is what the sweep does for a message routd took and for one routd
// refused, and it runs on a message already gone when a pass overlaps.
func TestDropStopsHoldingAndIsIdempotent(t *testing.T) {
	st := newStore(t)
	retain(t, st, "x", time.Now().Add(-time.Hour))
	if err := st.Drop(t.Context(), "x"); err != nil {
		t.Fatal(err)
	}
	if err := st.Drop(t.Context(), "x"); err != nil {
		t.Fatalf("dropping a message already dropped must be a no-op: %v", err)
	}
	if n, err := st.HeldCount(t.Context()); err != nil || n != 0 {
		t.Fatalf("HeldCount = %d, %v after the drop", n, err)
	}
}

// The /health count and the sweep's own read must agree about what is held. They
// are two different reads of one entity, and a count that drifted from the
// sweep would report a backlog nobody is working through, or hide one.
func TestHeldCountAgreesWithTheSweepsRead(t *testing.T) {
	st := newStore(t)
	base := time.Now().Add(-time.Hour)
	for i, id := range []string{"a", "b", "c"} {
		retain(t, st, id, base.Add(time.Duration(i)*time.Minute))
	}
	if err := st.Drop(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	n, err := st.HeldCount(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	due, err := st.DueForRedelivery(t.Context(), ts(time.Now()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(due) != 2 {
		t.Fatalf("HeldCount = %d and the sweep reads %d; both must be 2", n, len(due))
	}
}

// The sweep compares both timestamps by byte order, so a value of another width
// sorts into the wrong place instead of failing (BUGS L97). The declaration is
// what turns that into a refusal at the write.
func TestATimestampOfTheWrongWidthIsRefused(t *testing.T) {
	st := newStore(t)
	err := st.Retain(t.Context(), "x", `{}`, "2026-09-07T00:00:00Z", ts(time.Now()), "503")
	if err == nil {
		t.Fatal("a first_seen_at that is not core.TS's width must be refused")
	}
}

// --- the data move ---

// The import writes what the store reads back, through the same methods the
// adapter calls. An import that wrote the wrong attribute name would report its
// counts happily and only a read through the real methods catches it.
func TestImportWritesWhatTheStoreReadsBack(t *testing.T) {
	st := newStore(t)
	now := ts(time.Now().Add(-time.Hour))
	snap := chanstore.Snapshot{
		Claims: []chanstore.ClaimRow{
			{IdempotencyKey: "out-1", ChatJID: "slack:c/1", State: "sent",
				PlatformID: "plat-1", CreatedAt: "2026-08-16T18:44:52.33947691Z"},
			{IdempotencyKey: "out-2", ChatJID: "slack:c/1", State: "inflight",
				CreatedAt: "2026-08-16T18:44:52.33947691Z"},
		},
		Held: []chanstore.HeldRow{
			{ID: "in-1", Payload: `{"id":"in-1"}`, Attempts: 3,
				FirstSeenAt: now, NextAttemptAt: now, LastError: "503"},
		},
	}
	counts, err := st.Import(t.Context(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Claims != 2 || counts.Held != 1 {
		t.Fatalf("the import wrote %+v, want 2 claims and 1 held", counts)
	}
	sent, err := st.ClaimDelivery(t.Context(), "out-1", "slack:c/1")
	if err != nil || sent.State != chanstore.ClaimSent || sent.PlatformID != "plat-1" {
		t.Fatalf("the imported settled claim reads as (%v, %q, %v)", sent.State, sent.PlatformID, err)
	}
	inflight, err := st.ClaimDelivery(t.Context(), "out-2", "slack:c/1")
	if err != nil || inflight.State != chanstore.ClaimInflight {
		t.Fatalf("the imported unsettled claim reads as (%v, %v)", inflight.State, err)
	}
	due, err := st.DueForRedelivery(t.Context(), ts(time.Now()), 0)
	if err != nil || len(due) != 1 || due[0].Attempts != 3 || due[0].Payload != `{"id":"in-1"}` {
		t.Fatalf("the imported held message reads back as %+v (%v)", due, err)
	}
}

// An import is idempotent, so a run that failed part way is re-runnable.
func TestImportIsRerunnable(t *testing.T) {
	st := newStore(t)
	snap := chanstore.Snapshot{Claims: []chanstore.ClaimRow{
		{IdempotencyKey: "out-1", ChatJID: "slack:c/1", State: "sent", PlatformID: "plat-1"},
	}}
	for i := range 2 {
		if _, err := st.Import(t.Context(), snap); err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
	}
	got, err := st.ClaimDelivery(t.Context(), "out-1", "slack:c/1")
	if err != nil || got.State != chanstore.ClaimSent || got.PlatformID != "plat-1" {
		t.Fatalf("after two imports the claim reads as (%v, %q, %v)", got.State, got.PlatformID, err)
	}
}

// Every row the entities cannot represent is named at once, before the run that
// would stop half way. A state no reader understands is in that set: it would
// read as "not sent", so the key would refuse every later attempt forever.
func TestCheckSnapshotNamesEveryRowItCannotRepresent(t *testing.T) {
	_, err := chanstore.CheckSnapshot(chanstore.Snapshot{
		Claims: []chanstore.ClaimRow{
			{IdempotencyKey: "", ChatJID: "slack:c/1", State: "sent"},
			{IdempotencyKey: "dup", State: "sent"},
			{IdempotencyKey: "dup", State: "sent"},
			{IdempotencyKey: "odd", State: "queued"},
		},
		Held: []chanstore.HeldRow{{ID: "short", FirstSeenAt: "2026-09-07T00:00:00Z"}},
	})
	if err == nil {
		t.Fatal("a snapshot with four unrepresentable rows must be refused")
	}
	for _, want := range []string{"no idempotency_key", "dup appears twice", `state "queued"`, "short"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%v", want, err)
		}
	}
}
