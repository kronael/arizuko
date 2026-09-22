package store

// task_run_logs_test.go proves the two properties the instant-as-identity buys:
// the three task-blind reads come back newest first WITHOUT a sort in the
// reader, and the failed-run window is a range on the key rather than a filter
// over everything.

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// recordRuns appends n runs of one task and returns their positions, newest
// last. It goes through the writer, so the positions are the ones a live run
// would take.
func recordRuns(t *testing.T, s *Store, taskID string, n int) {
	t.Helper()
	for i := range n {
		if err := s.RecordTaskRun(TaskRunLog{
			TaskID: taskID, DurationMS: int64(100 + i), Status: "ok",
		}); err != nil {
			t.Fatalf("record run %d of %s: %v", i, taskID, err)
		}
	}
}

// The feed is newest first, and it is newest first STRUCTURALLY: the position is
// the instant, the read is descending on it, and nothing sorts afterwards.
func TestTheRunFeedIsNewestFirstWithoutASortInTheReader(t *testing.T) {
	s := openEntities(t)
	recordRuns(t, s, "t1", 3)

	all := s.AllRunLogs(10)
	if len(all) != 3 {
		t.Fatalf("AllRunLogs = %d runs, want 3", len(all))
	}
	if all[0].DurationMS != 102 {
		t.Errorf("the newest run has duration %d, want 102: the feed is not newest first",
			all[0].DurationMS)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].ID <= all[i].ID {
			t.Fatalf("positions run %q then %q, which is not descending", all[i-1].ID, all[i].ID)
		}
	}
	// The id IS the instant, so the two can never disagree — which is the whole
	// reason `run_at` is not stored a second time.
	for _, r := range all {
		if got := core.TS(r.RunAt); got != runInstant(r.ID) {
			t.Errorf("run %s renders at %s; the id and the instant have drifted apart",
				r.ID, got)
		}
	}
}

// The per-task history filters on an attribute over the whole partition, which
// is the cost this coordinate pays. It must still be exact and still be bounded.
func TestThePerTaskHistoryNamesOneTaskAndHonoursItsLimit(t *testing.T) {
	s := openEntities(t)
	recordRuns(t, s, "t1", 3)
	recordRuns(t, s, "t2", 2)

	if got := s.TaskRunLogs("t1", 10); len(got) != 3 {
		t.Errorf("t1 has %d runs, want 3", len(got))
	}
	if got := s.TaskRunLogs("t1", 2); len(got) != 2 {
		t.Errorf("with limit 2, t1 answered %d runs", len(got))
	}
	if got := s.TaskRunLogs("t2", 10); len(got) != 2 {
		t.Errorf("t2 has %d runs, want 2: the filter reached another task's runs", len(got))
	}
	if got := s.TaskRunLogs("missing", 10); len(got) != 0 {
		t.Errorf("a task nobody ran answered %d runs", len(got))
	}
}

// The failed-run window is a RANGE on the position, and both halves of the
// portal's dot read it through one method so they cannot disagree about which
// runs they mean.
//
// The old run below is placed through the move that carries a legacy row, which
// is the only writer that can put one in the past — and that is the same builder
// `arizuko migrate-hot` uses, so this drives it too.
func TestTheFailedRunWindowIsARangeAndNotAFilter(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	if err := s.RecordTaskRun(TaskRunLog{TaskID: "t1", Status: "error"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTaskRun(TaskRunLog{TaskID: "t1", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := s.SeedItem(ctx, TaskRunLogs, []string{old.UTC().Format(time.RFC3339), "1"},
		map[string]string{"task_id": "t1", "status": "error"}); err != nil {
		t.Fatalf("seed an older run: %v", err)
	}

	since := time.Now().Add(-24 * time.Hour)
	got, err := s.FailedRunsSince(ctx, since, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the last day holds %d failed runs, want 1: the run 48 hours old is inside "+
			"the window, or the recent one is outside it (%+v)", len(got), got)
	}
	if got[0].Status != "error" {
		t.Errorf("the window answered a %q run; it asks for failures", got[0].Status)
	}
	// Widen the window and the older failure arrives, so the test above is not
	// passing because the read returns nothing.
	wider, err := s.FailedRunsSince(ctx, time.Now().Add(-72*time.Hour), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(wider) != 2 {
		t.Fatalf("a 72-hour window holds %d failed runs, want both", len(wider))
	}
}

// A position of the wrong width is refused at the coordinate rather than sorting
// by width, which is what the ordered declaration is for (BUGS L97).
func TestARunPositionOfTheWrongWidthIsRefused(t *testing.T) {
	if _, err := runLogKey(core.NowTS()); err == nil {
		t.Error("a bare instant was accepted as a run position; it is short by the suffix")
	}
	if _, err := runLogKey(RunStamp(core.NowTS(), "0000000")); err == nil {
		t.Error("a position with a short suffix was accepted")
	}
	if _, err := runLogKey(RunStamp(core.NowTS(), "00000000")); err != nil {
		t.Errorf("a whole position was refused: %v", err)
	}
}
