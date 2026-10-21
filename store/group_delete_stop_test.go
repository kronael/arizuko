package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// Deleting `eval` with a child `eval/child` -> stop is called once for each of
// the two folders, and neither folder resolves any more at that moment, so
// routd cannot dispatch into a folder whose run is being stopped.
func TestDeleteGroupStoppingStopsEachFolderAfterThePurge(t *testing.T) {
	s := openMem(t)
	for _, folder := range []string{"eval", "eval/child", "keeper"} {
		if err := s.PutGroupRow(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
			t.Fatalf("seed %s: %v", folder, err)
		}
	}
	var stopped []string
	stillResolved := map[string]bool{}
	stop := func(_ context.Context, folder string) error {
		stopped = append(stopped, folder)
		_, stillResolved[folder] = s.GroupByFolder(folder)
		return nil
	}

	removed, _, err := s.DeleteGroupStopping(context.Background(), "eval", stop)
	if err != nil {
		t.Fatalf("DeleteGroupStopping: %v", err)
	}

	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	slices.Sort(stopped)
	if !slices.Equal(stopped, []string{"eval", "eval/child"}) {
		t.Fatalf("stopped = %v, want [eval eval/child]", stopped)
	}
	for _, folder := range stopped {
		if stillResolved[folder] {
			t.Errorf("%s still had a groups row when its run was stopped", folder)
		}
	}
	if _, ok := s.GroupByFolder("keeper"); !ok {
		t.Error("keeper was deleted")
	}
}

// One stop fails -> the purge stands, the other folder is still stopped, and
// the error names the folder whose stop failed.
func TestDeleteGroupStoppingReturnsStopErrorsAfterThePurge(t *testing.T) {
	s := openMem(t)
	for _, folder := range []string{"eval", "eval/child"} {
		if err := s.PutGroupRow(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
			t.Fatalf("seed %s: %v", folder, err)
		}
	}
	var stopped []string
	stop := func(_ context.Context, folder string) error {
		stopped = append(stopped, folder)
		if folder == "eval" {
			return errors.New("runed unreachable")
		}
		return nil
	}

	removed, _, err := s.DeleteGroupStopping(context.Background(), "eval", stop)

	if err == nil || !strings.Contains(err.Error(), "stop eval: runed unreachable") {
		t.Fatalf("err = %v, want it to name the failed stop of eval", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2: the purge must stand", removed)
	}
	if len(stopped) != 2 {
		t.Errorf("stopped = %v, want both folders tried", stopped)
	}
	if _, ok := s.GroupByFolder("eval"); ok {
		t.Error("the stop error undid the purge")
	}
}
