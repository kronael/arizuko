package store

// task_run_logs.go holds every read and write of `task_run_logs`: one item per
// execution of a scheduled task, appended when timed reports a run.
//
// The partition is OwnerRoutd and the sort key the instant: three of the four
// reads name no task and ask for the newest, so they are ordered ranges; the
// per-task history filters on a `task_id` attribute over the partition (1,451
// items on krons, about ten a day, no drainage). THE INSTANT IS THE IDENTITY:
// `id` and `run_at` were two renderings of one instant that could disagree
// (BUGS L257), so the position is core.TS plus a suffix and `run_at` is not
// stored a second time.
//
// A run log outlives the task it names: removing a task removes nothing else,
// the same verdict folder_scope.go makes for a group delete.

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

// The attributes a run log carries. The instant is the coordinate.
const (
	attrRunTask       = "task_id"
	attrRunDurationMS = "duration_ms"
	attrRunStatus     = "status"
	attrRunResult     = "result"
	attrRunError      = "error"
)

// runSuffixHex is how many hex digits separate two runs recorded in the same
// nanosecond.
const runSuffixHex = 8

// RunStampWidth is the width of a run's position. Exported because the move
// that carries these rows builds one from a legacy row's id.
const RunStampWidth = tsWidth + 1 + runSuffixHex

// The ordered declaration is what makes the newest-first reads ordered reads:
// every driver refuses a key of another width (BUGS L97).
func init() { objstore.OrderedSort(TaskRunLogs, RunStampWidth) }

// TaskRunLog is one recorded execution, as an operator and the agent read it.
type TaskRunLog struct {
	// ID is the run's position, which is its identity: a fixed-width instant
	// and a suffix.
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	RunAt      time.Time `json:"run_at"`
	DurationMS int64     `json:"duration_ms"`
	Status     string    `json:"status"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// RunStamp assembles a run's position from a rendered instant and a suffix, so
// the live writer and the move build one shape.
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
	// A run whose position this cannot parse still renders, with a zero time:
	// dropping the row would hide a failed run from the operator looking for
	// exactly that.
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

// RecordTaskRun appends one execution; timed's fire loop is the producer,
// behind POST /v1/tasks/runlog. The create REFUSES a collision, so two runs
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

// runLogLimit clamps what a reader may ask for.
func runLogLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

// TaskRunLogs answers one task's most recent runs, newest first: the read that
// names a task, so it filters on an attribute over the whole partition.
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

// AllRunLogs answers the most recent runs across every task, newest first: the
// dashboard's cross-task feed, an ordered range on the sort key.
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
// first, capped at limit: ONE read serving both halves of the portal's
// failed-run dot, so they cannot disagree. The window is a sort-key range; a
// 19-byte `datetime('now')` cutoff, with a space where the T is, put every run
// of the cutoff's own DAY inside the window whatever its time.
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

// runLogsFrom renders a read's items: ONE renderer for all four reads.
func runLogsFrom(items []objstore.Item) []TaskRunLog {
	out := make([]TaskRunLog, 0, len(items))
	for _, it := range items {
		out = append(out, runLogFrom(it))
	}
	return out
}
