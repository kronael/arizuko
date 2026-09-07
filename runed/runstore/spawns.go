package runstore

// spawns.go holds every read and write of `spawns`: one item per container
// spawn, the execution-session envelope routd claims a folder's run slot with.
//
// THE PARTITION IS A CONSTANT AND THE SORT KEY IS THE RUN ID, which is not what
// spec 6/37 rule 1 predicted when it modelled this table. That rule counted the
// READS — `WHERE folder=?` and `WHERE folder=? AND state IN (…)` — and told
// this entity to partition by folder. It missed the WRITES. Six operations
// address a spawn by its run id and hold no folder at all:
//
//   - `GET /v1/runs/{run_id}` and `DELETE /v1/runs/{run_id}` are handed one path
//     value, and Manager.Kill is the thing that RESOLVES a run to its folder, so
//     it cannot be given one.
//   - Three of them carry an invariant — Start, End and Kill are guarded
//     transitions — and a guarded write names ONE item. Under a folder partition
//     the guard would have to run after a scan that found the row, which is the
//     read-then-write the condition exists to replace.
//
// WHAT THE CONSTANT COSTS is that the folder reads are whole-partition reads
// with an attribute filter, and that the newest-first order is applied by the
// reader rather than by the sort key.
//
// WHAT MAKES THAT AFFORDABLE IS DRAINAGE, the same excuse chanstore's
// `inbound_pending` has: DropSpawnsBefore deletes everything older than the
// retention runed's hourly sweep passes it (seven days), so the partition is a
// window and not an archive. Measured 2026-09-07 on all three live snapshots:
// 232 items on the busiest (krons) over 53 folders, 144 on sloth, 102 on
// marinade. The unbounded table in this database is `session_log`, and that one
// partitions by folder.
//
// Folder containment could not have ridden on an attribute anyway: a prefix
// predicate runs on the SORT KEY and there is no substring comparison on the
// interface, so RecentSpawns applies groupfolder.Contains itself — which is also
// how it stops being `LIKE`, whose `_`/`%` wildcards and ASCII case folding are
// BUGS L122 and L252. A live `Coach`/`coach` pair is on sloth today.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// spawnPartition is the one partition every spawn lives in. The file header
// says why it is a constant and what that costs.
const spawnPartition = "spawn"

// The attributes an item carries.
const (
	attrFolder       = "folder"
	attrTopic        = "topic"
	attrContainer    = "container_name"
	attrSessionLogID = "session_log_id"
	attrSessionID    = "session_id"
	attrKind         = "kind"
	attrState        = "state"
	attrOutcome      = "outcome"
	attrExitCode     = "exit_code"
	attrSteered      = "steered"
	attrCreatedAt    = "created_at"
	attrStartedAt    = "started_at"
	attrEndedAt      = "ended_at"
)

// KindAgent is the kind a spawn takes when its caller names none. It is the
// default the column carried, so a row written before kinds existed and a
// caller that still does not name one mean the same thing.
const KindAgent = "agent"

// created_at is the only attribute here a query COMPARES by order — the sweep
// asks for everything before a cutoff — so it is the only one declared. A
// declaration is a promise about every value in the entity, and the other two
// timestamps are read back and rendered, never ordered.
func init() { objstore.OrderedAttr(Spawns, attrCreatedAt, tsWidth) }

// Spawn is one container spawn. Kind selects the Manager.Run post-claim
// executor ('agent' | 'hold' | …, spec 5/8 "Filesystem restore claims the
// folder's run slot").
//
// SessionLogID is a string because the session log's identity is a string: it
// is that row's sort key, the instant it started plus a suffix, and there is no
// sequence to name it by (6/37 rule 4).
type Spawn struct {
	RunID         string
	Folder        string
	Topic         string
	ContainerName string
	SessionLogID  string
	SessionID     string
	Kind          string
	State         string
	Outcome       string
	ExitCode      int
	Steered       bool
	CreatedAt     string
	StartedAt     string
	EndedAt       string
}

// ActiveSpawn is a live spawn's identifying info for the steer path.
type ActiveSpawn struct {
	RunID     string
	SessionID string
}

// spawnKey is the coordinate of one spawn.
func spawnKey(runID string) objstore.Key {
	return objstore.Key{Partition: spawnPartition, Sort: runID}
}

// terminalStates are the states a spawn does not come back from. The vocabulary
// is closed and small: dash.go's filter lists queued, running, exited and
// killed, and EndSpawn writes error.
var terminalStates = []string{"exited", "error", "killed"}

// live is the predicate for "this run still holds its folder's slot", and it is
// the ONE place that question is expressed — the read that admits a turn, the
// two counts, the orphan sweep and the kill's own guard all take it from here,
// so none of them can drift from the others.
//
// The table said `state IN ('queued','running')` and the interface carries no
// IN and no disjunction, so it is stated as the complement: not any of the three
// terminal states. Three conjuncts where SQL had one list.
//
// `ended_at IS NULL` would have been ONE term and it is not used, which is worth
// writing down because it looks better than it is. It agrees with the state on
// every row of every live database (measured 2026-09-07: 478 rows, zero
// disagreements in either direction) — but only because the three writers that
// set a terminal state also set ended_at, which nothing enforces. It is a second
// fact about liveness beside the column that declares it, and TestExpireOrphans
// is what proved they can come apart: a row born terminal has no ended_at and
// read as live.
func live() []objstore.Cmp {
	out := make([]objstore.Cmp, 0, len(terminalStates))
	for _, st := range terminalStates {
		out = append(out, objstore.Cmp{Attr: attrState, Op: objstore.Ne, Value: st})
	}
	return out
}

// CreateSpawn writes the row that CLAIMS a folder's run slot.
//
// The Put carries ItemAbsent, so a run id that already exists refuses the write
// rather than replacing the row under it — which for a claim means the folder's
// live run would be silently overwritten by a colliding id. A refusal is
// therefore a fault and is reported as one.
func (s *Store) CreateSpawn(ctx context.Context, sp Spawn) error {
	kind := sp.Kind
	if kind == "" {
		kind = KindAgent
	}
	attrs := map[string]string{
		attrFolder:    sp.Folder,
		attrContainer: sp.ContainerName,
		attrKind:      kind,
		attrState:     sp.State,
		attrCreatedAt: core.NowTS(),
	}
	set(attrs, attrTopic, sp.Topic)
	set(attrs, attrSessionLogID, sp.SessionLogID)
	set(attrs, attrSessionID, sp.SessionID)
	committed, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: Spawns, Key: spawnKey(sp.RunID), Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}})
	if err != nil {
		return fmt.Errorf("claim a run slot for %s: %w", sp.Folder, err)
	}
	if !committed {
		return fmt.Errorf("claim a run slot for %s: run %s is already recorded",
			sp.Folder, sp.RunID)
	}
	return nil
}

// SpawnByRunID answers which spawn a run id names. ErrNotFound when there is
// none — the 404 both /v1/runs/{run_id} routes answer.
func (s *Store) SpawnByRunID(ctx context.Context, runID string) (Spawn, error) {
	it, ok, err := s.point(ctx, Spawns, spawnKey(runID))
	if err != nil {
		return Spawn{}, fmt.Errorf("read run %s: %w", runID, err)
	}
	if !ok {
		return Spawn{}, ErrNotFound
	}
	return spawnFrom(it), nil
}

// StartSpawn flips a spawn to running with its resolved session id and reports
// whether the claim was still there to take.
//
// GUARDED ON state='queued', and the guard is the whole point: without it a
// DELETE landing between CreateSpawn and here would be undone — a terminal row
// resurrected and the folder held until RunTTL. started=false means that DELETE
// won and the caller MUST NOT launch, because the folder then reads free while
// a launch nobody can see proceeds, which puts a second container on one
// folder's mount.
func (s *Store) StartSpawn(ctx context.Context, runID, sessionID string) (started bool, err error) {
	attrs := map[string]string{attrState: "running", attrStartedAt: core.NowTS()}
	set(attrs, attrSessionID, sessionID)
	committed, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: Spawns, Key: spawnKey(runID), Attrs: attrs,
		Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: attrState, Op: objstore.Eq, Value: "queued"}}},
	}})
	if err != nil {
		return false, fmt.Errorf("take the claim on run %s: %w", runID, err)
	}
	return committed, nil
}

// EndSpawn records the terminal state, outcome and exit code at teardown.
//
// A 'killed' state is STICKY, and that is the invariant this write carries: the
// spawn goroutine returns after a deliberate DELETE has already killed the
// container, and without the condition its teardown would relabel an operator's
// kill as an error (spec 5/P § DELETE). A refusal is therefore the intended
// outcome rather than a fault — the row is already the one the operator asked
// for — so it is not reported as an error, exactly as the guarded UPDATE it
// replaces affected no rows and said nothing.
func (s *Store) EndSpawn(ctx context.Context, runID, state, outcome string, exitCode int) error {
	attrs := map[string]string{
		attrState:    state,
		attrExitCode: strconv.Itoa(exitCode),
		attrEndedAt:  core.NowTS(),
	}
	set(attrs, attrOutcome, outcome)
	remove := []string{}
	if outcome == "" {
		// The row may already carry an outcome from an earlier write, and an
		// empty one has to be ABSENT rather than an empty string, or a reader
		// testing for its presence finds a value that is not there.
		remove = append(remove, attrOutcome)
	}
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: Spawns, Key: spawnKey(runID),
		Attrs: attrs, Remove: remove,
		Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: attrState, Op: objstore.Ne, Value: "killed"}}},
	}})
	if err != nil {
		return fmt.Errorf("record the end of run %s: %w", runID, err)
	}
	return nil
}

// KillSpawn records state='killed' for a run that is STILL LIVE.
//
// The TOCTOU between Kill's read and this write can let a run complete normally
// in between, and the condition is what stops the kill overwriting an
// already-terminal state (spec 5/P § DELETE). It is `live()` rather than the
// table's `state IN ('queued','running')` for the reason live() states.
//
// A refusal means the run finished first, which is the case the guard is for
// and not an error.
func (s *Store) KillSpawn(ctx context.Context, runID string) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: Spawns, Key: spawnKey(runID),
		Attrs: map[string]string{
			attrState:    "killed",
			attrExitCode: "-1",
			attrEndedAt:  core.NowTS(),
		},
		// A kill is operator intent, not a run failure, so it deliberately
		// leaves no outcome behind for the breaker to read.
		Remove: []string{attrOutcome},
		Cond:   objstore.Cond{Where: live()},
	}})
	if err != nil {
		return fmt.Errorf("kill run %s: %w", runID, err)
	}
	return nil
}

// MarkSteered records that a steer-into-running write happened on a spawn.
func (s *Store) MarkSteered(ctx context.Context, runID string) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: Spawns, Key: spawnKey(runID),
		Attrs: map[string]string{attrSteered: "1"},
	}})
	if err != nil {
		return fmt.Errorf("mark run %s steered: %w", runID, err)
	}
	return nil
}

// LinkSessionLog records which session-log item a run opened.
//
// This is 6/37 rule 3's duplicated key rather than an embed: the session log is
// read on its OWN, a folder at a time, by the continuity hint and the
// inspect_session tool, and nothing ever reads it through the spawn. What the
// copy buys is the other direction — a run that opened no session (an isolated
// turn, or any non-agent kind) is the one carrying no link at all.
//
// The id is a sort key inside the spawn's OWN folder partition, because
// OpenSession is called with the folder the spawn claimed. Measured across all
// three live databases 2026-09-07: 211 spawns carry a session_log_id, every one
// resolves, and not one names a session in another folder.
func (s *Store) LinkSessionLog(ctx context.Context, runID, logID string) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: Spawns, Key: spawnKey(runID),
		Attrs: map[string]string{attrSessionLogID: logID},
	}})
	if err != nil {
		return fmt.Errorf("link run %s to its session log: %w", runID, err)
	}
	return nil
}

// RecentSpawns answers the operator dashboard's question: the newest spawns,
// narrowed by state and by folder subtree.
//
// It is named for the window rather than for the table because the window IS
// the answer — an empty state matches every state, an empty folder every
// folder, and the limit is clamped to a page (a limit outside [1,500] means the
// default 50, which is what the old reader did with an unhelpfully silent
// `limit <= 0 || limit > 500`).
//
// The ORDER is applied here, not by the driver, because the sort key is the run
// id and a run id is an identity rather than a clock. The file header says what
// bounds the read that costs.
func (s *Store) RecentSpawns(ctx context.Context, state, folder string, limit int) ([]Spawn, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var where []objstore.Cmp
	if state != "" {
		where = append(where, objstore.Cmp{Attr: attrState, Op: objstore.Eq, Value: state})
	}
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: Spawns, Partition: spawnPartition, Where: where,
	})
	if err != nil {
		return nil, fmt.Errorf("read the recent runs: %w", err)
	}
	out := make([]Spawn, 0, len(items))
	for _, it := range items {
		sp := spawnFrom(it)
		if folder != "" && !groupfolder.Contains(folder, sp.Folder) {
			continue
		}
		out = append(out, sp)
	}
	// created_at is core.TS, so its byte order is time order; the run id breaks
	// a tie the way the rowid did, deterministically rather than by arrival.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].RunID > out[j].RunID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// LiveSpawnInFolder answers whether a folder's run slot is taken, and by which
// run. nil means the folder is free.
//
// Manager holds no in-memory spawn state; this read IS the liveness authority,
// and admit() looks the steer callback up by the run id it returns.
//
// The folder match is EXACT, not a subtree: the slot belongs to one folder, and
// a parent holding a child's slot would serialize a whole tenant onto one run.
func (s *Store) LiveSpawnInFolder(ctx context.Context, folder string) (*ActiveSpawn, error) {
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: Spawns, Partition: spawnPartition,
		Where:   append([]objstore.Cmp{{Attr: attrFolder, Op: objstore.Eq, Value: folder}}, live()...),
		Project: []string{attrSessionID, attrCreatedAt},
	})
	if err != nil {
		return nil, fmt.Errorf("read %s's live run: %w", folder, err)
	}
	var newest *objstore.Item
	for i := range items {
		if newest == nil || items[i].Attrs[attrCreatedAt] > newest.Attrs[attrCreatedAt] {
			newest = &items[i]
		}
	}
	if newest == nil {
		return nil, nil
	}
	return &ActiveSpawn{RunID: newest.Key.Sort, SessionID: newest.Attrs[attrSessionID]}, nil
}

// LiveSpawnCount answers how many spawns of EVERY kind still hold a slot — the
// true in-flight count, which shutdown logging reports.
func (s *Store) LiveSpawnCount(ctx context.Context) (int, error) {
	return s.countLive(ctx, nil)
}

// LiveAgentSpawnCount answers how many AGENT spawns hold a slot, which is the
// global concurrency cap's actual budget (spec 5/8). A containerless kind
// consumes no docker resource comparable to a container and must not compete
// with agent turns for the cap, in either direction.
func (s *Store) LiveAgentSpawnCount(ctx context.Context) (int, error) {
	return s.countLive(ctx, []objstore.Cmp{{Attr: attrKind, Op: objstore.Eq, Value: KindAgent}})
}

// countLive is the one read behind both counts, so a change to what "live"
// means cannot land on one of them and miss the other. It projects one small
// attribute: the answer is the number of items, and a cap check must not pull
// every spawn's document through the driver to count them.
func (s *Store) countLive(ctx context.Context, extra []objstore.Cmp) (int, error) {
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: Spawns, Partition: spawnPartition,
		Where:   append(live(), extra...),
		Project: []string{attrKind},
	})
	if err != nil {
		return 0, fmt.Errorf("count the live runs: %w", err)
	}
	return len(items), nil
}

// ExpireOrphans ends every spawn left holding a slot and answers how many.
//
// runed was killed while containers were live, so those rows count toward the
// concurrency cap and block the folder forever. It runs at STARTUP, before the
// mux is served and before any executor exists, which is why a refused write is
// reported rather than skipped: at that moment nothing else writes these rows,
// so a condition that did not hold means a second process is on this database.
func (s *Store) ExpireOrphans(ctx context.Context) (int, error) {
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: Spawns, Partition: spawnPartition, Where: live(), Project: []string{attrState},
	})
	if err != nil {
		return 0, fmt.Errorf("read the orphaned runs: %w", err)
	}
	now := core.NowTS()
	writes := make([]objstore.Write, 0, len(items))
	for _, it := range items {
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: Spawns, Key: it.Key,
			Attrs: map[string]string{
				attrState: "exited", attrOutcome: "error", attrExitCode: "-1", attrEndedAt: now,
			},
			Cond: objstore.Cond{Where: live()},
		})
	}
	return s.applyInChunks(ctx, "expire the orphaned runs", writes)
}

// DropSpawnsBefore deletes every spawn created before cutoff and answers how
// many. It is what bounds this entity's one partition, so the header's "the
// partition is a window and not an archive" rests on it running.
//
// The CUTOFF is the caller's, not a retention this package computes: the clock
// belongs to the sweep that owns the policy, and a test that has to wait seven
// days to observe the behaviour is one nobody runs.
func (s *Store) DropSpawnsBefore(ctx context.Context, cutoff time.Time) (int, error) {
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: Spawns, Partition: spawnPartition,
		Where:   []objstore.Cmp{{Attr: attrCreatedAt, Op: objstore.Lt, Value: core.TS(cutoff)}},
		Project: []string{attrCreatedAt},
	})
	if err != nil {
		return 0, fmt.Errorf("read the aged runs: %w", err)
	}
	writes := make([]objstore.Write, 0, len(items))
	for _, it := range items {
		writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: Spawns, Key: it.Key})
	}
	return s.applyInChunks(ctx, "drop the aged runs", writes)
}

// spawnFrom renders one stored item as the spawn a caller reads. Every absent
// attribute becomes its zero value: the reader's job is to render, and a
// three-valued field crossing an HTTP boundary buys nothing but null-checks in
// every consumer.
func spawnFrom(it objstore.Item) Spawn {
	a := it.Attrs
	exit, _ := strconv.Atoi(a[attrExitCode])
	return Spawn{
		RunID:         it.Key.Sort,
		Folder:        a[attrFolder],
		Topic:         a[attrTopic],
		ContainerName: a[attrContainer],
		SessionLogID:  a[attrSessionLogID],
		SessionID:     a[attrSessionID],
		Kind:          a[attrKind],
		State:         a[attrState],
		Outcome:       a[attrOutcome],
		ExitCode:      exit,
		Steered:       a[attrSteered] == "1",
		CreatedAt:     a[attrCreatedAt],
		StartedAt:     a[attrStartedAt],
		EndedAt:       a[attrEndedAt],
	}
}
