package store

// task_run_logs.go holds every read and write of `task_run_logs`: one item per
// execution of a scheduled task, appended when timed reports a run and read back
// by an operator and by the agent's inspect_tasks.
//
// THE PARTITION IS OwnerRoutd AND THE SORT KEY IS THE INSTANT. Three of the four
// reads name no task at all — the cross-task run feed and dashd's two
// failed-run reads — and each of the three asks for the newest, so with the
// instant leading they are ordered ranges. The statements they replace were
// three SCANs plus a temp b-tree (EXPLAIN QUERY PLAN, krons' routd.db,
// 2026-09-09), because the table has no index on `run_at` or `status`. The move
// makes them cheaper, not dearer.
//
// WHAT IT COSTS is the fourth: the per-task history reads the partition and
// filters on a `task_id` attribute, where the statement it replaces used
// `idx_task_run_logs_task`. That is one indexed read traded for three ranges,
// and it is the read an operator runs by hand on one task rather than one a loop
// runs. The partition is 1,451 items on krons, 1,447 on sloth and 977 on
// marinade (2026-09-09), growing about ten a day, and it has no drainage.
//
// THE INSTANT IS THE IDENTITY, and that is rule 4. The statements this replaces
// read `ORDER BY id DESC` over an AUTOINCREMENT id, and no document store mints
// one — but the deeper reason is that `id` and `run_at` were two renderings of
// one instant that could disagree, which is what BUGS L257 measured on the run
// log beside this one. So there is ONE: the position is core.TS at its fixed
// width plus the suffix that keeps two runs recorded in the same nanosecond
// apart, and `run_at` is not stored a second time as an attribute.
//
// A RUN LOG OUTLIVES THE TASK IT NAMES. `scheduled_tasks` is an entity and an
// entity has no column a REFERENCES clause can name, so removing a task removes
// the task and nothing else — the decision routd's migration 0009 already
// records, and the same one folder_scope.go makes for a group delete ("history;
// a run names a task id the next tenant never mints"). The three live instances
// already carry 10, 32 and 66 logs whose task is gone.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a run log carries. The instant is the coordinate and is not
// among them.
const (
	attrRunTask       = "task_id"
	attrRunDurationMS = "duration_ms"
	attrRunStatus     = "status"
	attrRunResult     = "result"
	attrRunError      = "error"
)

// runSuffixHex is how many hex digits separate two runs recorded in the same
// nanosecond. Four bytes of randomness, for audit_log's reason.
const runSuffixHex = 8

// RunStampWidth is the width of a run's position: core.TS's thirty bytes, a
// separator, and the suffix. It is exported because the move that carries these
// rows builds one from a legacy row's id and has to render the same width.
const RunStampWidth = tsWidth + 1 + runSuffixHex

// The ordered declaration is what makes the three newest-first reads ordered
// reads: every driver refuses a key of another width, so a short one cannot sort
// into the wrong place (BUGS L97).
func init() { objstore.OrderedSort(TaskRunLogs, RunStampWidth) }

// TaskRunLog is one recorded execution, as an operator and the agent read it.
type TaskRunLog struct {
	// ID is the run's position, which is its identity: a fixed-width instant
	// and a suffix. It is what the reads order by.
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	RunAt      time.Time `json:"run_at"`
	DurationMS int64     `json:"duration_ms"`
	Status     string    `json:"status"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// RunStamp assembles a run's position from a rendered instant and a suffix, so
// the live writer and the move that carries an existing row build one shape.
func RunStamp(ts, suffix string) string { return ts + "-" + suffix }

// runNowStamp is the position a run recorded now takes.
func runNowStamp() (string, error) {
	raw := make([]byte, runSuffixHex/2)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("run log position: %w", err)
	}
	return RunStamp(core.NowTS(), hex.EncodeToString(raw)), nil
}

// runLogKey is the coordinate of one recorded run.
func runLogKey(at string) (objstore.Key, error) {
	if len(at) != RunStampWidth {
		return objstore.Key{}, fmt.Errorf("run log position %q is %d bytes and a position is "+
			"%d; the feed is read in position order, so a short one sorts into the wrong "+
			"place instead of failing (BUGS L97)", at, len(at), RunStampWidth)
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: at}, nil
}

// runLogFrom reads one item back into the shape the operator surfaces render.
func runLogFrom(it objstore.Item) TaskRunLog {
	l := TaskRunLog{
		ID:     it.Key.Sort,
		TaskID: it.Attrs[attrRunTask],
		Status: it.Attrs[attrRunStatus],
		Result: it.Attrs[attrRunResult],
		Error:  it.Attrs[attrRunError],
	}
	// The instant lives in the coordinate, so it is read back out of it. A run
	// whose position this cannot parse still renders — the id is the truth and
	// the zero time says so — because dropping the row would hide a failed run
	// from the operator looking for exactly that.
	if t, err := time.Parse(core.TSLayout, runInstant(it.Key.Sort)); err == nil {
		l.RunAt = t
	}
	if ms, err := strconv.ParseInt(it.Attrs[attrRunDurationMS], 10, 64); err == nil {
		l.DurationMS = ms
	}
	return l
}

// runInstant is the instant half of a position.
func runInstant(at string) string {
	if len(at) < tsWidth {
		return ""
	}
	return at[:tsWidth]
}

// RecordTaskRun appends one execution. timed's fire loop is the producer, behind
// POST /v1/tasks/runlog; nothing reads the answer beyond the error.
//
// The position is minted here and the create REFUSES a collision, so two runs
// stamped at one nanosecond with one suffix are loud rather than one lost run.
func (s *Store) RecordTaskRun(l TaskRunLog) error {
	at, err := runNowStamp()
	if err != nil {
		return err
	}
	key, err := runLogKey(at)
	if err != nil {
		return err
	}
	attrs := map[string]string{}
	setAttr(attrs, attrRunTask, l.TaskID)
	setAttr(attrs, attrRunStatus, l.Status)
	setAttr(attrs, attrRunResult, l.Result)
	setAttr(attrs, attrRunError, l.Error)
	if l.DurationMS != 0 {
		attrs[attrRunDurationMS] = strconv.FormatInt(l.DurationMS, 10)
	}
	recorded, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: TaskRunLogs, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("record a run of %s: %w", l.TaskID, err)
	}
	if !recorded {
		return fmt.Errorf("record a run of %s: position %s is taken, so two runs were "+
			"stamped at one instant with one suffix", l.TaskID, at)
	}
	return nil
}

// runLogLimit clamps what a reader may ask for. It is the bound both operator
// reads carried and it is applied here rather than by each caller.
func runLogLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

// TaskRunLogs answers one task's most recent runs, newest first.
//
// It is the read that names a task, so it filters on an attribute over the whole
// partition. The file header says what that costs.
func (s *Store) TaskRunLogs(taskID string, limit int) []TaskRunLog {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: TaskRunLogs, Partition: OwnerRoutd,
		Where:      []objstore.Cmp{{Attr: attrRunTask, Op: objstore.Eq, Value: taskID}},
		Descending: true, Limit: runLogLimit(limit),
	})
	if err != nil {
		return nil
	}
	return runLogsFrom(items)
}

// AllRunLogs answers the most recent runs across every task, newest first — the
// dashboard's cross-task feed. It is an ordered range on the sort key and
// nothing else.
func (s *Store) AllRunLogs(limit int) []TaskRunLog {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: TaskRunLogs, Partition: OwnerRoutd,
		Descending: true, Limit: runLogLimit(limit),
	})
	if err != nil {
		return nil
	}
	return runLogsFrom(items)
}

// FailedRunsSince answers every run that errored at or after `since`, newest
// first, capped at limit.
//
// It is ONE read serving both halves of the portal's failed-run dot — the count
// and the owning folders — because a pair that aggregated differently would
// disagree with itself. The window is a sort-key range now, where the statement
// it replaces compared a 20-byte `2026-09-07T03:00:57Z` against sqlite's
// `datetime('now','-1 day')`, whose 19 bytes put a space where the T is: the T
// sorts after the space, so every run of the cutoff's own DAY was inside the
// window whatever its time. The range answers the day it says.
func (s *Store) FailedRunsSince(ctx context.Context, since time.Time, limit int) ([]TaskRunLog, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TaskRunLogs, Partition: OwnerRoutd,
		Key:        objstore.KeyPredicate{Op: objstore.GeKey, Value: runFloor(since)},
		Where:      []objstore.Cmp{{Attr: attrRunStatus, Op: objstore.Eq, Value: "error"}},
		Descending: true, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read the failed runs since %s: %w", core.TS(since), err)
	}
	return runLogsFrom(items), nil
}

// runFloor is the LOWEST position an instant can carry: the instant itself and
// a suffix of zeros. The bound has to be a whole position, because an ordered
// key is compared at its declared width and a short one is refused.
func runFloor(at time.Time) string {
	return RunStamp(core.TS(at), strings.Repeat("0", runSuffixHex))
}

// CountTaskRunLogs answers how many runs are recorded. `arizuko migrate-hot`
// reads it back to check that every row travelled.
func (s *Store) CountTaskRunLogs(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TaskRunLogs, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the task run logs: %w", err)
	}
	return len(items), nil
}

// runLogsFrom renders a read's items. It is ONE renderer for all four reads,
// because two would drift about which attribute means what.
func runLogsFrom(items []objstore.Item) []TaskRunLog {
	out := make([]TaskRunLog, 0, len(items))
	for _, it := range items {
		out = append(out, runLogFrom(it))
	}
	return out
}
