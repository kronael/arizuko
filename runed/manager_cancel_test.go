package runed

import (
	"context"
	"maps"
	"testing"

	"github.com/kronael/arizuko/objstore"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/runed/runstore"
)

type cancelStore struct {
	spawn, session   map[string]string
	cleanupDeadlines int
}

func (s *cancelStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.Entity == runstore.Spawns {
		return []objstore.Item{{Key: objstore.Key{Partition: q.Partition, Sort: q.Key.Value}, Attrs: maps.Clone(s.spawn)}}, nil
	}
	return nil, nil
}

func (s *cancelStore) GuardedBatch(ctx context.Context, _ []objstore.Check, writes []objstore.Write) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, write := range writes {
		if write.Attrs["state"] == "error" || write.Attrs["ended_at"] != "" && write.Entity == runstore.SessionLog {
			if _, ok := ctx.Deadline(); ok {
				s.cleanupDeadlines++
			}
		}
		switch write.Entity {
		case runstore.Spawns:
			maps.Copy(s.spawn, write.Attrs)
		case runstore.SessionLog:
			maps.Copy(s.session, write.Attrs)
		}
	}
	return true, nil
}

func TestCanceledCallerClosesRunAndSession(t *testing.T) {
	store := &cancelStore{spawn: map[string]string{"state": "queued"}, session: map[string]string{}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg, folders := renderWiring(t)
	manager := NewManager(OpenOver(store), FakeRuntime{Fn: func(context.Context, Turn) RunResult {
		cancel()
		return RunResult{Outcome: runedv1.OutcomeError, Error: "caller canceled", MessageCount: 3}
	}}, ManagerConfig{Cfg: cfg, Folders: folders})
	manager.spawn(ctx, runedv1.RunRequest{Folder: "demo", Packages: []string{"core"}}, "run", "session", "handle")
	if store.cleanupDeadlines != 2 {
		t.Errorf("bounded cleanup writes = %d, want 2", store.cleanupDeadlines)
	}
	if got := store.spawn["state"]; got != "error" {
		t.Errorf("spawn state = %q, want error", got)
	}
	if store.session["ended_at"] == "" || store.session["result"] != "error" || store.session["message_count"] != "3" {
		t.Errorf("session not closed: %v", store.session)
	}
}
