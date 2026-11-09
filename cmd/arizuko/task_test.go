package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestTaskRunCLI(t *testing.T) {
	if id := os.Getenv("TASK_RUN_TEST_ID"); id != "" {
		os.Args = []string{"arizuko", "task", "run", "-i", "demo", id}
		main()
		return
	}
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	if err := os.MkdirAll(filepath.Join(base, "arizuko_demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := instanceStore(t)
	future := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	for _, status := range []string{core.TaskActive, core.TaskPaused, core.TaskCompleted, core.TaskError} {
		if err := s.CreateTask(core.Task{ID: status, Status: status, Cron: "0 9 * * *", NextRun: &future}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{core.TaskActive, core.TaskPaused, core.TaskCompleted, core.TaskError, "missing"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTaskRunCLI$")
		cmd.Env = append(os.Environ(), "ARIZUKO_PKG_ARGS=task-run", "TASK_RUN_TEST_ID="+id)
		before := time.Now().Add(-time.Second)
		out, err := cmd.CombinedOutput()
		if id != core.TaskActive {
			if err == nil || !strings.Contains(string(out), id) {
				t.Fatalf("%s: error %v, output %q", id, err, out)
			}
			if id != "missing" {
				task, _ := s.GetTask(id)
				if task.Status != id || task.NextRun == nil || !task.NextRun.Equal(future) {
					t.Fatalf("%s changed: %+v", id, task)
				}
			}
			continue
		}
		if err != nil {
			t.Fatalf("run: %v, %s", err, out)
		}
		task, ok := s.GetTask(id)
		if !ok || task.Status != core.TaskActive || task.Cron != "0 9 * * *" || task.NextRun == nil || task.NextRun.Before(before) || task.NextRun.After(time.Now()) {
			t.Fatalf("task after run: %+v", task)
		}
		due, err := s.DueTasks(time.Now())
		if err != nil || len(due) != 1 || due[0].ID != id {
			t.Fatalf("due tasks = %+v, error %v", due, err)
		}
	}
}
