package main

import (
	"context"
	"fmt"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
	"net/http/httptest"
	"testing"
)

type dashCounter struct {
	objstore.Store
	rows, walks int
	failTasks   bool
}

func (c *dashCounter) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if c.failTasks && q.Entity == store.ScheduledTasks {
		return nil, fmt.Errorf("task store unavailable")
	}
	measured := q
	measured.Where = nil
	measured.Project = nil
	if len(q.Where) > 0 {
		measured.Limit = 0
	}
	rows, err := c.Store.Query(ctx, measured)
	if err != nil {
		return nil, err
	}
	c.rows += len(rows)
	if q.Key.Op == objstore.AnyKey && (q.Entity == store.Routes || q.Entity == store.ScheduledTasks || q.Entity == store.RouteTokens) {
		c.walks++
	}
	return c.Store.Query(ctx, q)
}

// dashd holds no copy of routd's tables: each call reads the route table and
// the task list from the store again.
func TestDashboardReadsTheStoreOnEachCall(t *testing.T) {
	db := routdDB(t)
	for i := range 40 {
		seedScheduledTask(t, db, core.Task{ID: fmt.Sprint(i), Owner: "alpha", Status: core.TaskActive})
		if _, err := store.NewOver(db).AddRoute(core.Route{Seq: 10, Match: fmt.Sprintf("chat_jid=chat:%d", i), Target: "alpha"}); err != nil {
			t.Fatal(err)
		}
	}
	c := &dashCounter{Store: db}
	d := &dash{routd: c, routdURL: stubRoutd(t, c)}
	if tasks, err := d.tasksViaRoutd(t.Context()); err != nil || len(tasks) != 40 {
		t.Fatalf("tasks %d %v", len(tasks), err)
	}
	if got := d.jidFolder("chat:0"); got != "alpha" {
		t.Fatal(got)
	}
	c.rows, c.walks = 0, 0
	if _, err := d.tasksViaRoutd(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := d.jidFolder("chat:0"); got != "alpha" {
		t.Fatal(got)
	}
	if c.walks != 2 {
		t.Fatalf("the second call walked %d tables, want the route table and the task list",
			c.walks)
	}
	seedScheduledTask(t, db, core.Task{ID: "new", Owner: "beta", Status: core.TaskActive})
	if tasks, err := d.tasksViaRoutd(t.Context()); err != nil || len(tasks) != 41 {
		t.Fatalf("changed tasks %d %v", len(tasks), err)
	}
	if _, err := store.NewOver(db).AddRoute(core.Route{Seq: 1, Match: "chat_jid=chat:0", Target: "beta"}); err != nil {
		t.Fatal(err)
	}
	if got := d.jidFolder("chat:0"); got != "beta" {
		t.Fatal("stale route", got)
	}
	sc := store.OwnerScope(store.OwnerRoutd)
	ok, err := db.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Delete, Entity: sc.Entity, Key: sc.Key}})
	if err != nil || !ok {
		t.Fatalf("remove marker: %v %v", ok, err)
	}
	if _, err := d.tasksViaRoutd(t.Context()); err == nil {
		t.Fatal("served unverified tasks")
	}
	if got := d.jidFolder("chat:0"); got != "" {
		t.Fatal("served unverified route", got)
	}
}

func TestTaskReadRecoversAfterReadFailure(t *testing.T) {
	db := routdDB(t)
	seedScheduledTask(t, db, core.Task{ID: "exists", Owner: "alpha", Status: core.TaskActive})
	c := &dashCounter{Store: db, failTasks: true}
	realDB := routd.OpenOver(c)
	t.Cleanup(func() { _ = realDB.Close() })
	server := httptest.NewServer(routd.NewServer(realDB, nil, nil, nil, 0, "").Handler())
	defer server.Close()
	d := &dash{routd: c, routdURL: server.URL}
	if _, err := d.tasksViaRoutd(t.Context()); err == nil {
		t.Error("task read failure became an empty snapshot")
	}
	c.failTasks = false
	if tasks, err := d.tasksViaRoutd(t.Context()); err != nil || len(tasks) != 1 {
		t.Fatalf("recovery without mutation: tasks=%v err=%v", tasks, err)
	}
}
