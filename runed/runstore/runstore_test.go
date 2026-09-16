package runstore_test

// The storage layer's own bodies, run against the endpoint dyntest lends each
// test as its own namespace.
//
// They are about the guarantees runed would be dangerous without: a run slot is
// claimed once and taken once, a kill never relabels a run that finished first,
// and the breaker's count is the one the caller compares against the threshold.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/runed/runstore"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// newStore hands a test an empty runed store in a namespace of its own.
func newStore(t *testing.T) *runstore.Store {
	t.Helper()
	return runstore.New(newDriver(t))
}

// newDriver hands back the driver itself, for the tests that wrap it.
func newDriver(t *testing.T) *dynstore.Store {
	t.Helper()
	s := dyntest.Store(t)
	if err := runstore.EnsureEntities(t.Context(), s); err != nil {
		t.Fatalf("create entities: %v", err)
	}
	return s
}

// The constant the ordered declarations rest on must be core.TS's own width. A
// constant that drifted from the layout would refuse every write rather than
// guard one, so it is checked against the renderer instead of trusted.
func TestOrderedWidthIsCoreTSsOwnWidth(t *testing.T) {
	if got := len(core.TS(time.Now())); got != 30 {
		t.Fatalf("core.TS renders %d bytes and runstore declares created_at "+
			"and the session key against 30", got)
	}
	if runstore.SessionIDWidth != 30+1+8 {
		t.Fatalf("SessionIDWidth is %d, want core.TS plus a separator plus eight "+
			"hex digits", runstore.SessionIDWidth)
	}
}

// --- spawns ---

// A claim is taken ONCE. StartSpawn is guarded on state='queued', so the second
// caller is told the claim was already taken rather than launching a second
// container onto one folder's mount.
func TestTheRunSlotIsTakenOnce(t *testing.T) {
	st := newStore(t)
	if err := st.CreateSpawn(t.Context(), runstore.Spawn{
		RunID: "run_1", Folder: "acme", ContainerName: "c1", State: "queued",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	started, err := st.StartSpawn(t.Context(), "run_1", "sess")
	if err != nil || !started {
		t.Fatalf("first StartSpawn = (%v, %v), want (true, nil)", started, err)
	}
	started, err = st.StartSpawn(t.Context(), "run_1", "sess")
	if err != nil || started {
		t.Fatalf("second StartSpawn = (%v, %v), want (false, nil)", started, err)
	}
}

// A run id is claimed once too: CreateSpawn refuses to overwrite a live run's
// row, so a colliding id is loud rather than a folder silently losing its run.
func TestASecondClaimOnOneRunIDIsRefused(t *testing.T) {
	st := newStore(t)
	sp := runstore.Spawn{RunID: "run_1", Folder: "acme", ContainerName: "c1", State: "queued"}
	if err := st.CreateSpawn(t.Context(), sp); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := st.CreateSpawn(t.Context(), sp)
	if err == nil {
		t.Fatal("a second claim on run_1 was accepted")
	}
	if !strings.Contains(err.Error(), "already recorded") {
		t.Errorf("the refusal %q does not say the run is already there", err)
	}
}

// The kill's whole invariant: a run that completed between the caller's read and
// this write keeps its terminal state. Killing it must not relabel it.
func TestAKillDoesNotRelabelARunThatFinishedFirst(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	_ = st.CreateSpawn(ctx, runstore.Spawn{RunID: "done", Folder: "a", ContainerName: "c", State: "queued"})
	if _, err := st.StartSpawn(ctx, "done", "s"); err != nil {
		t.Fatal(err)
	}
	if err := st.EndSpawn(ctx, "done", "exited", "ok", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.KillSpawn(ctx, "done"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	sp, err := st.SpawnByRunID(ctx, "done")
	if err != nil {
		t.Fatal(err)
	}
	if sp.State != "exited" || sp.Outcome != "ok" {
		t.Fatalf("a finished run reads back %s/%s, want exited/ok", sp.State, sp.Outcome)
	}

	// A live one IS killed, and the kill leaves no outcome behind: it is
	// operator intent, not a run failure, so nothing must read it as one.
	_ = st.CreateSpawn(ctx, runstore.Spawn{RunID: "live", Folder: "b", ContainerName: "c", State: "queued"})
	if _, err := st.StartSpawn(ctx, "live", "s"); err != nil {
		t.Fatal(err)
	}
	if err := st.KillSpawn(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	sp, _ = st.SpawnByRunID(ctx, "live")
	if sp.State != "killed" || sp.Outcome != "" || sp.ExitCode != -1 {
		t.Fatalf("the killed run reads back %+v, want state=killed outcome=\"\" exit=-1", sp)
	}
}

// A teardown arriving after a deliberate kill must not overwrite it (spec 5/P §
// DELETE). It is the guard the other way round from the test above.
func TestATeardownDoesNotOverwriteAKill(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	_ = st.CreateSpawn(ctx, runstore.Spawn{RunID: "r", Folder: "a", ContainerName: "c", State: "queued"})
	if _, err := st.StartSpawn(ctx, "r", "s"); err != nil {
		t.Fatal(err)
	}
	if err := st.KillSpawn(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	if err := st.EndSpawn(ctx, "r", "error", "error", 1); err != nil {
		t.Fatalf("end: %v", err)
	}
	sp, _ := st.SpawnByRunID(ctx, "r")
	if sp.State != "killed" {
		t.Fatalf("the killed run reads back state=%q; a teardown relabelled an "+
			"operator's kill as a failure", sp.State)
	}
}

// Liveness is the folder's slot: exactly the queued and running runs, whatever
// their kind, and the counts and the per-folder read agree because they take one
// predicate.
func TestLivenessIsTheQueuedAndRunningRuns(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	for _, sp := range []runstore.Spawn{
		{RunID: "q", Folder: "a", ContainerName: "c", State: "queued"},
		{RunID: "r", Folder: "b", ContainerName: "c", State: "running"},
		{RunID: "e", Folder: "c", ContainerName: "c", State: "exited"},
		{RunID: "x", Folder: "d", ContainerName: "c", State: "error"},
		{RunID: "k", Folder: "e", ContainerName: "c", State: "killed"},
		{RunID: "h", Folder: "f", ContainerName: "c", State: "running", Kind: "hold"},
	} {
		if err := st.CreateSpawn(ctx, sp); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.LiveSpawnCount(ctx); err != nil || n != 3 {
		t.Fatalf("LiveSpawnCount = (%d, %v), want 3 (queued, running, the hold)", n, err)
	}
	// The cap's budget is agent runs only: a containerless kind consumes no
	// docker resource and must not compete for it.
	if n, err := st.LiveAgentSpawnCount(ctx); err != nil || n != 2 {
		t.Fatalf("LiveAgentSpawnCount = (%d, %v), want 2 (the hold does not count)", n, err)
	}
	for _, folder := range []string{"c", "d", "e"} {
		got, err := st.LiveSpawnInFolder(ctx, folder)
		if err != nil || got != nil {
			t.Errorf("folder %s reads live as %+v (err=%v); its run is terminal", folder, got, err)
		}
	}
	got, err := st.LiveSpawnInFolder(ctx, "b")
	if err != nil || got == nil || got.RunID != "r" {
		t.Fatalf("folder b's live run = %+v (err=%v), want r", got, err)
	}
}

// The slot belongs to ONE folder. A parent holding a child's slot would
// serialize a whole tenant onto one run, so the folder match is exact.
func TestALiveChildDoesNotHoldItsParentsSlot(t *testing.T) {
	st := newStore(t)
	if err := st.CreateSpawn(t.Context(), runstore.Spawn{
		RunID: "r", Folder: "acme/eng", ContainerName: "c", State: "running",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LiveSpawnInFolder(t.Context(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("acme reads busy because acme/eng is running: %+v", got)
	}
}

// The dashboard's window: newest first, narrowed by state and by folder SUBTREE.
// Containment is groupfolder's, so `_` is a literal underscore rather than
// LIKE's wildcard and `Coach` is not `coach` (BUGS L122, L252 — a live instance
// carries that pair).
func TestRecentSpawnsIsNewestFirstInsideTheSubtree(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	for _, sp := range []runstore.Spawn{
		{RunID: "r1", Folder: "acme", ContainerName: "c", State: "exited"},
		{RunID: "r2", Folder: "acme/eng", ContainerName: "c", State: "running"},
		{RunID: "r3", Folder: "a_me", ContainerName: "c", State: "exited"},
		{RunID: "r4", Folder: "Acme", ContainerName: "c", State: "exited"},
	} {
		if err := st.CreateSpawn(ctx, sp); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct created_at, so the order is decidable
	}
	rows, err := st.RecentSpawns(ctx, "", "acme", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RunID != "r2" || rows[1].RunID != "r1" {
		t.Fatalf("acme's subtree reads back %v, want [r2 r1] newest first", ids(rows))
	}
	if rows, _ := st.RecentSpawns(ctx, "running", "", 0); len(rows) != 1 || rows[0].RunID != "r2" {
		t.Fatalf("state=running reads back %v, want [r2]", ids(rows))
	}
	if rows, _ := st.RecentSpawns(ctx, "", "", 1); len(rows) != 1 {
		t.Fatalf("limit 1 read back %d rows", len(rows))
	}
}

// ids renders a row list for a failure message.
func ids(rows []runstore.Spawn) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RunID)
	}
	return out
}

// A restart ends every run that still held a slot, and leaves the terminal ones
// exactly as they are — including the outcome a kill deliberately left off.
func TestExpireOrphansEndsOnlyTheLiveOnes(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	_ = st.CreateSpawn(ctx, runstore.Spawn{RunID: "q", Folder: "a", ContainerName: "c", State: "queued"})
	_ = st.CreateSpawn(ctx, runstore.Spawn{RunID: "r", Folder: "b", ContainerName: "c", State: "running"})
	_ = st.CreateSpawn(ctx, runstore.Spawn{RunID: "e", Folder: "c", ContainerName: "c", State: "exited"})

	n, err := st.ExpireOrphans(ctx)
	if err != nil || n != 2 {
		t.Fatalf("ExpireOrphans = (%d, %v), want 2", n, err)
	}
	if n, _ := st.LiveSpawnCount(ctx); n != 0 {
		t.Errorf("%d runs still hold a slot after the sweep", n)
	}
	sp, _ := st.SpawnByRunID(ctx, "e")
	if sp.State != "exited" || sp.EndedAt != "" {
		t.Errorf("an already-terminal run was rewritten: %+v", sp)
	}
	sp, _ = st.SpawnByRunID(ctx, "q")
	if sp.State != "exited" || sp.Outcome != "error" || sp.EndedAt == "" {
		t.Errorf("the orphan reads back %+v, want exited/error with an end time", sp)
	}
}

// --- session_log ---

// The history a folder reads back: newest first, opened by one write and closed
// by a second one on the SAME key.
func TestASessionIsOpenedAndClosedOnOneKey(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	id, err := st.OpenSession(ctx, "acme", "sess-a")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(id) != runstore.SessionIDWidth {
		t.Fatalf("the session id %q is %d bytes, want %d", id, len(id), runstore.SessionIDWidth)
	}
	rows, _ := st.RecentSessions(ctx, "acme", 10)
	if len(rows) != 1 || rows[0].EndedAt != "" || rows[0].SessionID != "sess-a" {
		t.Fatalf("the open session reads back %+v", rows)
	}
	if err := st.CloseSession(ctx, "acme", id, "sess-b", "ok", "boom", 7); err != nil {
		t.Fatalf("close: %v", err)
	}
	rows, _ = st.RecentSessions(ctx, "acme", 10)
	if len(rows) != 1 {
		t.Fatalf("closing opened a SECOND item: %+v", rows)
	}
	got := rows[0]
	if got.ID != id || got.SessionID != "sess-b" || got.Result != "ok" ||
		got.Error != "boom" || got.MessageCount != 7 || got.EndedAt == "" {
		t.Fatalf("the closed session reads back %+v", got)
	}
	if got.StartedAt != id[:30] {
		t.Errorf("started_at is %q and the id carries %q; the instant is stored twice",
			got.StartedAt, id[:30])
	}
}

// An empty new session id leaves the one the run started with, which is the
// COALESCE the statement carried.
func TestClosingWithNoNewSessionIDKeepsTheOpenedOne(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	id, _ := st.OpenSession(ctx, "acme", "sess-a")
	if err := st.CloseSession(ctx, "acme", id, "", "ok", "", 1); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.RecentSessions(ctx, "acme", 10)
	if len(rows) != 1 || rows[0].SessionID != "sess-a" {
		t.Fatalf("the closed session reads back %+v, want session_id sess-a", rows)
	}
}

// Closing a session nobody opened is a caller fault and says so. The old
// statement affected no rows and reported success, which is how a run's history
// could go missing with nothing to read.
func TestClosingASessionThatIsNotThereIsLoud(t *testing.T) {
	st := newStore(t)
	err := st.CloseSession(t.Context(), "acme", core.TS(time.Now())+"-00000001", "", "ok", "", 1)
	if err == nil {
		t.Fatal("closing a session nobody opened reported success")
	}
	if !strings.Contains(err.Error(), "no such session") {
		t.Errorf("the refusal %q does not name what is wrong", err)
	}
}

// A folder reads its OWN history and nobody else's: the folder is the partition,
// so this is what containment costs and buys here.
func TestASessionReadIsBoundToItsFolder(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	if _, err := st.OpenSession(ctx, "alice", "sa"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenSession(ctx, "bob", "sb"); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.RecentSessions(ctx, "alice", 10)
	if len(rows) != 1 || rows[0].SessionID != "sa" {
		t.Fatalf("alice reads back %+v, want only her own session", rows)
	}
}

// The page is newest first and the limit bounds it, which is the read routd
// makes per turn against a folder holding twelve thousand items.
func TestRecentSessionsIsNewestFirstAndBounded(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	var ids []string
	for i := range 5 {
		id, err := st.OpenSession(ctx, "acme", fmt.Sprintf("s%d", i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows, err := st.RecentSessions(ctx, "acme", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("read back %d sessions, want 2", len(rows))
	}
	if rows[0].ID != ids[4] || rows[1].ID != ids[3] {
		t.Fatalf("the page is %s,%s; want the two newest %s,%s",
			rows[0].ID, rows[1].ID, ids[4], ids[3])
	}
}

// --- circuit_breaker ---

// The streak a folder has never had is zero, and it counts up one run at a time.
func TestTheStreakCountsUpFromNothing(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	if n, err := st.FailureStreak(ctx, "acme"); err != nil || n != 0 {
		t.Fatalf("a folder with no history reads (%d, %v), want (0, nil)", n, err)
	}
	for want := 1; want <= 3; want++ {
		got, err := st.RecordFailure(ctx, "acme")
		if err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		if got != want {
			t.Fatalf("RecordFailure answered %d, want %d — the caller compares this "+
				"against the threshold with ==, so a wrong value misses the trip", got, want)
		}
	}
	if n, _ := st.FailureStreak(ctx, "acme"); n != 3 {
		t.Errorf("the stored streak is %d, want 3", n)
	}
	if err := st.ClearFailures(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.FailureStreak(ctx, "acme"); n != 0 {
		t.Errorf("the cleared streak is %d, want 0", n)
	}
	// It counts up again from the zero the clear wrote, rather than refusing a
	// counter that is not there.
	if n, err := st.RecordFailure(ctx, "acme"); err != nil || n != 1 {
		t.Fatalf("the first failure after a clear = (%d, %v), want (1, nil)", n, err)
	}
}

// One folder's streak is its own: the folder is the whole coordinate, so a
// failure in one never counts against another.
func TestOneFoldersStreakIsItsOwn(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	if _, err := st.RecordFailure(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.FailureStreak(ctx, "other"); n != 0 {
		t.Fatalf("another folder's streak reads %d", n)
	}
}

// THE DEFECT THIS MOVE FIXES. The old shape committed the increment and then
// read the counter back with a second statement, so anything that reset the
// folder in between answered the caller a count that had already been thrown
// away — and Manager.endRun compares that answer against the threshold with
// `==`, so the breaker never tripped at all.
//
// This wraps the driver so a clear lands the instant a write commits, which is
// the race made deterministic. RecordFailure must still answer the value it
// wrote, because it computed it: the value is the CAS's, not a re-read's.
func TestTheStreakIsTheValueTheWriteCommitted(t *testing.T) {
	driver := newDriver(t)
	st := runstore.New(&clearAfterWrite{Store: driver, folder: "acme"})
	got, err := st.RecordFailure(t.Context(), "acme")
	if err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if got != 1 {
		t.Fatalf("RecordFailure answered %d after a concurrent clear, want 1 — it must "+
			"answer what it committed, not what a second read finds", got)
	}
}

// clearAfterWrite deletes one folder's breaker item the moment any write
// commits, standing in for a ClearFailures that lands between a commit and a
// read the caller would otherwise have made.
type clearAfterWrite struct {
	*dynstore.Store
	folder string
	fired  bool
}

func (c *clearAfterWrite) GuardedBatch(ctx context.Context, checks []objstore.Check,
	writes []objstore.Write) (bool, error) {
	committed, err := c.Store.GuardedBatch(ctx, checks, writes)
	if err != nil || !committed || c.fired {
		return committed, err
	}
	c.fired = true
	_, derr := c.Store.GuardedBatch(ctx, nil, []objstore.Write{{
		Op:     objstore.Delete,
		Entity: runstore.CircuitBreaker,
		Key:    objstore.Key{Partition: c.folder, Sort: c.folder},
	}})
	return committed, derr
}

// THE COMPARE-AND-SWAP ITSELF, made deterministic. A counter that another
// caller moved between this call's read and its write must be RE-READ, not
// overwritten — the lost update.
//
// The concurrent body below is the same property and it does not prove it on
// its own: with the swap replaced by a plain read-then-write, `go test -short`
// stays GREEN because the goroutines never interleave on an in-memory SQLite,
// and only `-race` widens the window enough to catch it (`make test` passes no
// -race). This body forces the interleave instead of hoping for it.
func TestAMovedCounterIsRereadRatherThanOverwritten(t *testing.T) {
	driver := newDriver(t)
	bump := &bumpBeforeWrite{Store: driver, folder: "acme"}
	st := runstore.New(bump)
	seed := runstore.New(driver)
	if n, err := seed.RecordFailure(t.Context(), "acme"); err != nil || n != 1 {
		t.Fatalf("seeding the streak = (%d, %v)", n, err)
	}

	// The wrapper bumps the counter to 2 between this call's read and its
	// write, so the write must be refused and the call must come back with 3.
	got, err := st.RecordFailure(t.Context(), "acme")
	if err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if !bump.fired {
		t.Fatal("the wrapper never got a write to race, so this body proved nothing")
	}
	if got != 3 {
		t.Fatalf("RecordFailure answered %d, want 3 — a counter someone else moved was "+
			"overwritten instead of re-read, so one folder's failure went uncounted", got)
	}
	if n, _ := seed.FailureStreak(t.Context(), "acme"); n != 3 {
		t.Fatalf("the stored streak is %d, want 3: the racing increment was lost", n)
	}
}

// bumpBeforeWrite increments one folder's counter out of band the first time an
// UPDATE reaches it, standing in for a second caller that got there first.
//
// It waits for the Update rather than firing on the first write of any kind,
// because the first write RecordFailure makes is the create-if-absent probe.
// Firing on that one bumps the counter BEFORE the increment reads it, which is
// not a race at all — and a first draft that did exactly that stayed green
// against a deliberately broken implementation.
type bumpBeforeWrite struct {
	*dynstore.Store
	folder string
	fired  bool
}

func (b *bumpBeforeWrite) GuardedBatch(ctx context.Context, checks []objstore.Check,
	writes []objstore.Write) (bool, error) {
	if !b.fired && len(writes) == 1 && writes[0].Op == objstore.Update {
		b.fired = true
		if _, _, err := objstore.Increment(ctx, b.Store, runstore.CircuitBreaker,
			objstore.Key{Partition: b.folder, Sort: b.folder}, "failures", 1, nil, nil); err != nil {
			return false, err
		}
	}
	return b.Store.GuardedBatch(ctx, checks, writes)
}

// A RESET LANDING MID-INCREMENT counts from the zero it wrote instead of failing
// on a counter that is not there.
//
// That is why the reset writes a zero rather than removing the item:
// objstore.Increment refuses a counter with no value — "a counter with no zero
// is a modelling gap rather than a value to invent" — and a delete is the one
// thing that could take it away under a concurrent failure. Manager.Run clears
// an open breaker while the previous run's endRun is still counting, so the two
// really do overlap.
func TestAResetMidIncrementCountsFromItsZero(t *testing.T) {
	driver := newDriver(t)
	raw := runstore.New(driver)
	if _, err := raw.RecordFailure(t.Context(), "acme"); err != nil {
		t.Fatal(err)
	}
	clear := &clearAfterRefusedCreate{Store: driver, st: raw, folder: "acme"}
	got, err := runstore.New(clear).RecordFailure(t.Context(), "acme")
	if err != nil {
		t.Fatalf("a reset landing mid-increment failed the count: %v", err)
	}
	if !clear.fired {
		t.Fatal("the wrapper never got a refused create to race, so this proved nothing")
	}
	if got != 1 {
		t.Fatalf("RecordFailure answered %d after a reset landed under it, want 1", got)
	}
}

// clearAfterRefusedCreate clears the folder's streak the moment RecordFailure's
// create-if-absent probe is refused, which is the gap between that probe and the
// increment's own read. It calls ClearFailures rather than deleting, so it
// follows whichever reset is compiled.
type clearAfterRefusedCreate struct {
	*dynstore.Store
	st     *runstore.Store
	folder string
	fired  bool
}

func (c *clearAfterRefusedCreate) GuardedBatch(ctx context.Context, checks []objstore.Check,
	writes []objstore.Write) (bool, error) {
	committed, err := c.Store.GuardedBatch(ctx, checks, writes)
	if err != nil || committed || c.fired ||
		len(writes) != 1 || writes[0].Op != objstore.Put {
		return committed, err
	}
	c.fired = true
	return committed, c.st.ClearFailures(ctx, c.folder)
}

// Concurrent failures each count once. The compare-and-swap is what makes the
// answers a permutation of 1..N rather than N callers reading the same number.
//
// It is the live shape of the body above and it is only PROVEN under -race: a
// deliberate lost update leaves this green on an in-memory SQLite and turns it
// red under the flag. Read them as a pair.
func TestConcurrentFailuresEachCountOnce(t *testing.T) {
	st := newStore(t)
	const n = 8
	var (
		mu   sync.Mutex
		seen = map[int]int{}
		wg   sync.WaitGroup
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := st.RecordFailure(context.Background(), "acme")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("RecordFailure: %v", err)
				return
			}
			seen[got]++
		}()
	}
	wg.Wait()
	for i := 1; i <= n; i++ {
		if seen[i] != 1 {
			t.Errorf("%d callers were answered %d; %d concurrent failures must answer "+
				"1..%d exactly once each, or two runs read one streak", seen[i], i, n, n)
		}
	}
	if got, _ := st.FailureStreak(t.Context(), "acme"); got != n {
		t.Errorf("the stored streak is %d after %d failures", got, n)
	}
}

// --- import ---

// The move carries the ORDER the sequence gave, which is the thing most likely
// to be wrong: the stored started_at is second-resolution and sessions share a
// second by the dozen, so the old row id is what breaks the tie.
func TestTheImportCarriesTheSequenceOrder(t *testing.T) {
	st := newStore(t)
	second := "2026-09-05T20:19:00Z"
	snap := runstore.Snapshot{Sessions: []runstore.SessionRow{
		{ID: 1, GroupFolder: "acme", SessionID: "first", StartedAt: second},
		{ID: 2, GroupFolder: "acme", SessionID: "second", StartedAt: second},
		{ID: 3, GroupFolder: "acme", SessionID: "third", StartedAt: second},
	}}
	counts, err := st.Import(t.Context(), snap)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if counts.Sessions != 3 {
		t.Fatalf("imported %d sessions, want 3", counts.Sessions)
	}
	rows, err := st.RecentSessions(t.Context(), "acme", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"third", "second", "first"}
	for i, w := range want {
		if rows[i].SessionID != w {
			t.Fatalf("the page reads %s at %d, want %s — three sessions in one second "+
				"came back in the wrong order", rows[i].SessionID, i, w)
		}
	}
}

// Every timestamp is re-rendered at core.TS's width, because the sweep compares
// created_at by byte order and a twenty-byte value sorts into the wrong place
// (BUGS L97). The tables stored twenty bytes on every live row.
func TestTheImportRewritesTimestampsToOneWidth(t *testing.T) {
	st := newStore(t)
	snap := runstore.Snapshot{Spawns: []runstore.SpawnRow{{
		RunID: "run_1", Folder: "acme", ContainerName: "c", State: "exited",
		CreatedAt: "2026-09-07T03:11:21Z", StartedAt: "2026-09-07T03:11:22Z",
		EndedAt: "2026-09-07T03:11:30Z",
	}}}
	if _, err := st.Import(t.Context(), snap); err != nil {
		t.Fatalf("import: %v", err)
	}
	sp, err := st.SpawnByRunID(t.Context(), "run_1")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"created_at": sp.CreatedAt, "started_at": sp.StartedAt, "ended_at": sp.EndedAt,
	} {
		if len(got) != 30 {
			t.Errorf("%s came over as %q, %d bytes; the sweep compares it by byte order",
				name, got, len(got))
		}
	}
	// And the sweep reaches it, which is the whole point of the rewrite.
	n, err := st.DropSpawnsBefore(t.Context(), time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 1 {
		t.Fatalf("the sweep took %d migrated runs (err=%v), want 1", n, err)
	}
}

// A spawn's link to its session is a sort key inside the spawn's OWN folder, so
// the import resolves the old sequence number through the sessions it carries.
func TestTheImportResolvesASpawnsSessionLink(t *testing.T) {
	st := newStore(t)
	snap := runstore.Snapshot{
		Sessions: []runstore.SessionRow{
			{ID: 42, GroupFolder: "acme", SessionID: "s", StartedAt: "2026-09-05T20:19:00Z"},
		},
		Spawns: []runstore.SpawnRow{{
			RunID: "run_1", Folder: "acme", ContainerName: "c", State: "exited",
			SessionLogID: 42, CreatedAt: "2026-09-05T20:19:00Z",
		}},
	}
	if _, err := st.Import(t.Context(), snap); err != nil {
		t.Fatalf("import: %v", err)
	}
	sp, _ := st.SpawnByRunID(t.Context(), "run_1")
	rows, _ := st.RecentSessions(t.Context(), "acme", 1)
	if len(rows) != 1 || sp.SessionLogID != rows[0].ID {
		t.Fatalf("the spawn links to %q and the session is %q", sp.SessionLogID, rows[0].ID)
	}
}

// A row the entities cannot address stops the run BY NAME rather than half way
// through it, and every refusal is reported at once.
func TestTheImportNamesEveryRowItCannotRepresent(t *testing.T) {
	snap := runstore.Snapshot{
		Sessions: []runstore.SessionRow{
			{ID: 1, GroupFolder: "", StartedAt: "2026-09-05T20:19:00Z"},
			{ID: 2, GroupFolder: "acme", StartedAt: "not a time"},
		},
		Spawns: []runstore.SpawnRow{
			{RunID: "", Folder: "acme", CreatedAt: "2026-09-05T20:19:00Z"},
			{RunID: "run_x", Folder: "acme", SessionLogID: 99, CreatedAt: "2026-09-05T20:19:00Z"},
		},
		Breakers: []runstore.BreakerRow{{Folder: "", Failures: 1}},
	}
	_, err := runstore.CheckSnapshot(snap)
	if err == nil {
		t.Fatal("a snapshot with five unrepresentable rows was accepted")
	}
	for _, want := range []string{
		"names no group_folder", "not a time", "has no run_id",
		"session 99", "names no folder",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%v", want, err)
		}
	}
}
