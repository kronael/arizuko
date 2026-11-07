package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestSeedDefaultTasks(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.SeedDefaultTasks("alice", "alice"); err != nil {
		t.Fatal(err)
	}

	seeded := s.ListTasks("alice", false)
	if len(seeded) != len(defaultTasks) {
		t.Fatalf("want %d tasks, got %d", len(defaultTasks), len(seeded))
	}

	seen := map[string]bool{}
	for _, task := range seeded {
		seen[task.ID] = true
		if task.ChatJID != "alice" {
			t.Errorf("want chat_jid=alice, got %q", task.ChatJID)
		}
		if task.Status != "active" {
			t.Errorf("want status=active, got %q", task.Status)
		}
		if task.ContextMode != "isolated" {
			t.Errorf("want context_mode=isolated, got %q", task.ContextMode)
		}
	}
	for i, dt := range defaultTasks {
		id := "alice-mem-" + strconv.Itoa(i)
		if !seen[id] {
			t.Errorf("missing task %s (%s)", id, dt.prompt)
		}
	}

	// Idempotent: second seed does not duplicate.
	if err := s.SeedDefaultTasks("alice", "alice"); err != nil {
		t.Fatal(err)
	}
	if again := s.ListTasks("alice", false); len(again) != len(defaultTasks) {
		t.Fatalf("after re-seed want %d, got %d", len(defaultTasks), len(again))
	}

	first, ok := s.GetTask("alice-mem-0")
	if !ok {
		t.Fatal("alice-mem-0 missing after the re-seed")
	}
	if first.Prompt != defaultTasks[0].prompt || first.Cron != defaultTasks[0].cron {
		t.Errorf("alice-mem-0 got (%q,%q), want (%q,%q)",
			first.Prompt, first.Cron, defaultTasks[0].prompt, defaultTasks[0].cron)
	}
}

// A seeded task carries no next_run; the first poll fills it with the first
// instant its OWN cron names, so a new group is not already due the moment it
// is created. Seeding `now` made the next scheduler sweep fire all five compaction
// tasks at once, on the group's root topic, and the root session each of those
// turns persisted then aborted the next capability eval (BUGS L312).
//
// The expectation is read off the expression by hand rather than from
// core.NextRun, which is the code under test.
func TestSeedDefaultTasks_FirstRunFollowsItsCron(t *testing.T) {
	// firstRun asserts the wall-clock fields each seeded expression names.
	firstRun := map[string]func(time.Time) bool{
		"0 2 * * *": func(r time.Time) bool { return r.Hour() == 2 && r.Minute() == 0 },
		"0 3 * * 1": func(r time.Time) bool {
			return r.Hour() == 3 && r.Minute() == 0 && r.Weekday() == time.Monday
		},
		"0 4 1 * *": func(r time.Time) bool { return r.Hour() == 4 && r.Minute() == 0 && r.Day() == 1 },
	}

	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	loc, err := time.LoadLocation(core.Timezone())
	if err != nil {
		t.Fatal(err)
	}
	seededAt := time.Now()
	if err := s.SeedDefaultTasks("alice", "alice"); err != nil {
		t.Fatal(err)
	}
	for i := range defaultTasks {
		id := "alice-mem-" + strconv.Itoa(i)
		if task, _ := s.GetTask(id); task.NextRun != nil {
			t.Fatalf("%s: seeded with next_run %v, want it left to the scheduler", id, task.NextRun)
		}
	}

	// The behaviour that cost the five turns: the fire loop's own reader must
	// claim nothing from a group that was just created, and fills each run.
	due, err := s.DueTasks(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("a freshly seeded group is already due: %d tasks claimed", len(due))
	}
	for i, dt := range defaultTasks {
		id := "alice-mem-" + strconv.Itoa(i)
		task, ok := s.GetTask(id)
		if !ok {
			t.Fatalf("%s missing after seeding", id)
		}
		if task.NextRun == nil {
			t.Fatalf("%s: the poll left no next_run", id)
		}
		run := task.NextRun.In(loc)
		if !run.After(seededAt) {
			t.Errorf("%s (%s): next_run %s is not after the seed instant %s",
				id, dt.cron, run, seededAt)
		}
		want, known := firstRun[dt.cron]
		if !known {
			t.Fatalf("%s: no expectation written for cron %q", id, dt.cron)
		}
		if !want(run) {
			t.Errorf("%s: next_run %s does not match cron %q", id, run, dt.cron)
		}
	}
}

// The scheduler alone fills next_run: an active task with a cron and none gets
// the cron's first instant, is not fired by the poll that filled it, and keeps
// that instant on the next poll.
func TestDueTasks_FillsAMissingNextRun(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateTask(core.Task{
		ID: "fill", Owner: "main", ChatJID: "tg:1", Prompt: "p",
		Cron: "0 9 * * *", Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	due, err := s.DueTasks(time.Now().Add(72 * time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatalf("first poll = %d tasks, err %v; it fills next_run and fires nothing", len(due), err)
	}
	got, _ := s.GetTask("fill")
	if got.NextRun == nil || !got.NextRun.After(time.Now()) ||
		got.NextRun.Hour() != 9 || got.NextRun.Minute() != 0 {
		t.Fatalf("next_run = %v, want the next 09:00", got.NextRun)
	}
	if got.Status != core.TaskActive {
		t.Fatalf("status = %q, want active (filled, not fired)", got.Status)
	}
	filled := *got.NextRun

	due, err = s.DueTasks(time.Now().Add(72 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || !due[0].NextRun.Equal(filled) {
		t.Fatalf("second poll = %+v, want the task due at the filled %v", due, filled)
	}
}

// A task whose next_run an operator or the job itself set keeps it: the fill
// never overwrites one.
func TestDueTasks_FillKeepsAnExplicitNextRun(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	explicit := time.Now().Add(240 * time.Hour).Truncate(time.Second)
	if err := s.CreateTask(core.Task{
		ID: "kept", Owner: "main", ChatJID: "tg:1", Prompt: "p",
		Cron: "0 9 * * *", NextRun: &explicit, Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DueTasks(time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask("kept"); got.NextRun == nil || !got.NextRun.Equal(explicit) {
		t.Fatalf("next_run = %v, want the explicit %v", got.NextRun, explicit)
	}
}

// A recurring task whose cron will not parse stops with status 'error' rather
// than staying active with no next_run, the state nothing recovers from
// (BUGS L74). An active task with no cron and no next_run can never fire, so it
// stops with 'error' too.
func TestDueTasks_BadCronIsAnError(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, cron := range map[string]string{"bad": "not a cron", "bare": ""} {
		if err := s.CreateTask(core.Task{
			ID: id, Owner: "main", ChatJID: "tg:1", Prompt: "p",
			Cron: cron, Status: core.TaskActive, Created: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	due, err := s.DueTasks(time.Now().Add(72 * time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatalf("DueTasks = %d tasks, err %v; want none", len(due), err)
	}
	if got, _ := s.GetTask("bad"); got.Status != core.TaskError || got.NextRun != nil {
		t.Fatalf("bad cron: status %q next_run %v, want error with no next_run", got.Status, got.NextRun)
	}
	if got, _ := s.GetTask("bare"); got.Status != core.TaskError || got.NextRun != nil {
		t.Fatalf("no cron: status %q next_run %v, want error with no next_run", got.Status, got.NextRun)
	}
}

// RescheduleTask with no next_run reads the task's own cron: the next instant
// for a recurring task, 'completed' for a one-shot, 'error' for a cron that will
// not parse. An explicit next_run is stored as given, and a task deleted since
// the claim stays deleted.
func TestRescheduleTask_ReadsTheCron(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for id, cron := range map[string]string{"rec": "0 9 * * *", "once": "", "bad": "not a cron"} {
		if err := s.CreateTask(core.Task{
			ID: id, Owner: "main", ChatJID: "tg:1", Prompt: "p", Cron: cron,
			NextRun: &now, Status: TaskStatusFiring, Created: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.RescheduleTask(id, ""); err != nil {
			t.Fatal(err)
		}
	}
	rec, _ := s.GetTask("rec")
	if rec.Status != core.TaskActive || rec.NextRun == nil || !rec.NextRun.After(now) ||
		rec.NextRun.Hour() != 9 || rec.NextRun.Minute() != 0 {
		t.Errorf("recurring: status %q next_run %v, want active at the next 09:00", rec.Status, rec.NextRun)
	}
	once, _ := s.GetTask("once")
	if once.Status != core.TaskCompleted || once.NextRun != nil {
		t.Errorf("one-shot: status %q next_run %v, want completed with none", once.Status, once.NextRun)
	}
	bad, _ := s.GetTask("bad")
	if bad.Status != core.TaskError || bad.NextRun != nil {
		t.Errorf("bad cron: status %q next_run %v, want error with none", bad.Status, bad.NextRun)
	}

	explicit := now.Add(time.Hour).Truncate(time.Second)
	if err := s.SetTaskStatus("rec", TaskStatusFiring); err != nil {
		t.Fatal(err)
	}
	if err := s.RescheduleTask("rec", explicit.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask("rec"); got.NextRun == nil || !got.NextRun.Equal(explicit) {
		t.Errorf("explicit: next_run %v, want %v", got.NextRun, explicit)
	}
	if err := s.RescheduleTask("gone", ""); err != nil {
		t.Errorf("a deleted task: %v, want nothing written and no error", err)
	}
	if _, ok := s.GetTask("gone"); ok {
		t.Error("RescheduleTask created the deleted task")
	}
}

// DueTasks must claim each due task exactly once: the UPDATE...RETURNING
// flips status='active'→'firing' atomically, so a second poll over the same
// due set returns nothing (no two pollers fire the same task).
func TestDueTasks_ClaimsOnce(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	past := time.Now().Add(-time.Hour)
	if err := s.CreateTask(core.Task{
		ID: "t1", Owner: "alice", ChatJID: "alice", Prompt: "/ping",
		NextRun: &past, Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	first, err := s.DueTasks(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != "t1" {
		t.Fatalf("first claim: want [t1], got %v", first)
	}
	if first[0].Status != "firing" {
		t.Errorf("claimed task status: want firing, got %q", first[0].Status)
	}

	// Second poll of the same due set claims nothing — t1 is already 'firing'.
	second, err := s.DueTasks(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second claim: want none, got %v (double-fire)", second)
	}
}

// A task stranded in 'firing' by a mid-fire crash is re-armed by startup
// recovery; an 'active' task is left untouched.
func TestRecoverFiringTasks(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	past := time.Now().Add(-time.Hour)
	if err := s.CreateTask(core.Task{
		ID: "stuck", Owner: "a", ChatJID: "a", Prompt: "/ping",
		NextRun: &past, Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// Claim it → 'firing', then simulate a crash before reschedule.
	if _, err := s.DueTasks(time.Now()); err != nil {
		t.Fatal(err)
	}
	// Stuck: the next poll claims nothing (it's 'firing').
	if due, _ := s.DueTasks(time.Now()); len(due) != 0 {
		t.Fatalf("expected stuck task, got %v", due)
	}
	// Startup recovery re-arms it.
	n, err := s.RecoverFiringTasks()
	if err != nil || n != 1 {
		t.Fatalf("RecoverFiringTasks = %d, %v; want 1, nil", n, err)
	}
	// Now it fires again.
	due, err := s.DueTasks(time.Now())
	if err != nil || len(due) != 1 || due[0].ID != "stuck" {
		t.Fatalf("after recovery: want [stuck], got %v (err %v)", due, err)
	}
}

// A next_run written between the poll's read and its fill stands: the fill is
// conditional on the attribute still being absent.
func TestFillNextRun_LosesToAWriteAfterTheRead(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateTask(core.Task{
		ID: "race", Owner: "main", ChatJID: "tg:1", Prompt: "p",
		Cron: "0 9 * * *", Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	items, err := s.allTasks(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("read tasks: %d items, err %v", len(items), err)
	}
	stale := items[0]

	explicit := time.Now().Add(100 * time.Hour).Truncate(time.Second)
	ws := &WriteSet{}
	if err := s.PatchTaskInto(ws, "race", "", explicit.Format(time.RFC3339), errors.New("gone")); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), ws); err != nil {
		t.Fatal(err)
	}

	if err := s.fillNextRun(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask("race"); got.NextRun == nil || !got.NextRun.Equal(explicit) {
		t.Fatalf("next_run = %v, the fill overwrote the write that came after its read (want %v)",
			got.NextRun, explicit)
	}
}

// RescheduleTask is the re-arm of a CLAIMED task: a task that is no longer
// firing (paused or moved by the job in the window) is not touched.
func TestRescheduleTask_OnlyReArmsAFiringTask(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	moved := now.Add(72 * time.Hour).Truncate(time.Second)
	for id, st := range map[string]string{"paused": core.TaskPaused, "moved": core.TaskActive} {
		if err := s.CreateTask(core.Task{
			ID: id, Owner: "main", ChatJID: "tg:1", Prompt: "p", Cron: "0 9 * * *",
			NextRun: &moved, Status: st, Created: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.RescheduleTask(id, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.GetTask("paused"); got.Status != core.TaskPaused || !got.NextRun.Equal(moved) {
		t.Errorf("paused: status %q next_run %v, want untouched", got.Status, got.NextRun)
	}
	if got, _ := s.GetTask("moved"); !got.NextRun.Equal(moved) {
		t.Errorf("job-moved: next_run %v, want the job's %v", got.NextRun, moved)
	}
}

// Resume drops a cron task's stale next_run so the scheduler fills the next
// instant, keeps a one-shot's (it has no cron to fill from), and leaves a task
// that is not paused alone.
func TestResumeTaskInto(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stale := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	seeds := map[string]core.Task{
		"cron":   {Cron: "0 9 * * *", Status: core.TaskPaused},
		"shot":   {Status: core.TaskPaused},
		"active": {Cron: "0 9 * * *", Status: core.TaskActive},
	}
	for id, task := range seeds {
		task.ID, task.Owner, task.ChatJID, task.Prompt = id, "main", "tg:1", "p"
		task.NextRun, task.Created = &stale, time.Now()
		if err := s.CreateTask(task); err != nil {
			t.Fatal(err)
		}
		cur, _ := s.GetTask(id)
		ws := &WriteSet{}
		if err := s.ResumeTaskInto(ws, cur, errors.New("refused")); err != nil {
			t.Fatal(err)
		}
		if err := s.Apply(context.Background(), ws); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.GetTask("cron"); got.Status != core.TaskActive || got.NextRun != nil {
		t.Errorf("cron: status %q next_run %v, want active with none", got.Status, got.NextRun)
	}
	if got, _ := s.GetTask("shot"); got.Status != core.TaskActive || got.NextRun == nil || !got.NextRun.Equal(stale) {
		t.Errorf("one-shot: status %q next_run %v, want active keeping %v", got.Status, got.NextRun, stale)
	}
	if got, _ := s.GetTask("active"); got.NextRun == nil || !got.NextRun.Equal(stale) {
		t.Errorf("active: next_run %v, want untouched", got.NextRun)
	}
}
