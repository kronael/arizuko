package runstore

// spawns.go holds every read and write of `spawns`: one item per container
// spawn, the execution-session envelope routd claims a folder's run slot with.
//
// THE PARTITION IS A CONSTANT AND THE SORT KEY IS THE RUN ID, not the folder
// 6/37 rule 1 predicted: six operations address a spawn by its run id and
// hold no folder at all, and Start, End and Kill are guarded transitions,
// which name ONE item. The cost is that folder reads are whole-partition
// reads with an attribute filter and newest-first order is applied by the
// reader. What makes that affordable is DRAINAGE: DropSpawnsBefore keeps the
// partition a window (seven days), not an archive. Folder containment is
// applied by RecentSpawns through groupfolder.Contains, never LIKE (BUGS
// L122, L252).

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

// spawnPartition is the one partition every spawn lives in.
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

// KindAgent is the kind a spawn takes when its caller names none.
const KindAgent = "agent"

// created_at is the only attribute a query COMPARES by order (the sweep's
// cutoff), so it is the only one declared.
func init() { objstore.OrderedAttr(Spawns, attrCreatedAt, tsWidth) }

// Spawn is one container spawn. Kind selects the Manager.Run post-claim
// executor (spec 5/8). SessionLogID is a string because the session log's
// identity is its sort key (6/37 rule 4).
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

// terminalStates are the states a spawn does not come back from.
var terminalStates = []string{"exited", "error", "killed"}

// live is the predicate for "this run still holds its folder's slot", the ONE
// place that question is expressed, stated as the complement of the terminal
// states because the interface carries no IN. `ended_at IS NULL` is NOT used:
// nothing enforces that a terminal state also sets ended_at, and
// TestExpireOrphans proved they come apart (a row born terminal has no
// ended_at).
func live() []objstore.Cmp {
	out := make([]objstore.Cmp, 0, len(terminalStates))
	for _, st := range terminalStates {
		out = append(out, objstore.Cmp{Attr: attrState, Op: objstore.Ne, Value: st})
	}
	return out
}

// CreateSpawn writes the row that CLAIMS a folder's run slot. The Put carries
// ItemAbsent: a colliding run id would silently overwrite the folder's live
// run, so a refusal is a fault.
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

// SpawnByRunID answers which spawn a run id names; ErrNotFound when none.
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
// whether the claim was still there to take. GUARDED ON state='queued':
// without it a DELETE landing between CreateSpawn and here would be undone, a
// terminal row resurrected and the folder held until RunTTL. started=false
// means that DELETE won and the caller MUST NOT launch.
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

// EndSpawn records the terminal state, outcome and exit code at teardown. A
// 'killed' state is STICKY: the spawn goroutine returns after a deliberate
// DELETE has already killed the container, and without the condition its
// teardown would relabel an operator's kill as an error (spec 5/P § DELETE).
// A refusal is the intended outcome, not reported.
func (s *Store) EndSpawn(ctx context.Context, runID, state, outcome string, exitCode int) error {
	attrs := map[string]string{
		attrState:    state,
		attrExitCode: strconv.Itoa(exitCode),
		attrEndedAt:  core.NowTS(),
	}
	set(attrs, attrOutcome, outcome)
	remove := []string{}
	if outcome == "" {
		// An empty outcome has to be ABSENT, not an empty string a reader
		// testing for presence would find.
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

// KillSpawn records state='killed' for a run that is STILL LIVE. The TOCTOU
// between Kill's read and this write can let a run complete normally in
// between, and the condition stops the kill overwriting an already-terminal
// state (spec 5/P § DELETE). A refusal means the run finished first, not an
// error.
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

// LinkSessionLog records which session-log item a run opened: 6/37 rule 3's
// duplicated key rather than an embed, because the session log is read on its
// OWN, a folder at a time, and nothing reads it through the spawn. The id is
// a sort key inside the spawn's OWN folder partition.
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
// narrowed by state and by folder subtree. An empty state matches every
// state, an empty folder every folder, and a limit outside [1,500] means 50.
// The ORDER is applied here: the sort key is the run id, an identity rather
// than a clock.
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
	// created_at is core.TS, so its byte order is time order; the run id
	// breaks a tie deterministically rather than by arrival.
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
// run; nil means free. This read IS the liveness authority. The folder match
// is EXACT, not a subtree: a parent holding a child's slot would serialize a
// whole tenant onto one run.
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

// LiveSpawnCount answers how many spawns of EVERY kind still hold a slot.
func (s *Store) LiveSpawnCount(ctx context.Context) (int, error) {
	return s.countLive(ctx, nil)
}

// LiveAgentSpawnCount answers how many AGENT spawns hold a slot, the global
// concurrency cap's budget (spec 5/8): a containerless kind must not compete
// with agent turns for the cap.
func (s *Store) LiveAgentSpawnCount(ctx context.Context) (int, error) {
	return s.countLive(ctx, []objstore.Cmp{{Attr: attrKind, Op: objstore.Eq, Value: KindAgent}})
}

// countLive is the one read behind both counts. It projects one small
// attribute: a cap check must not pull every spawn's document to count them.
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

// ExpireOrphans ends every spawn left holding a slot after runed was killed
// with containers live, and answers how many. It runs at STARTUP before any
// executor exists, so a refused write is reported: nothing else writes these
// rows then, and a condition that did not hold means a second process is on
// this database.
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
// many; it is what keeps the partition a window. The CUTOFF is the caller's,
// not a retention this package computes: a test that waits seven days is one
// nobody runs.
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
// attribute becomes its zero value.
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
