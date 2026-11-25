package main

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/googd/googstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type pollCounter struct {
	objstore.Store
	rows, walks int
	hook        func(objstore.Query)
}

func (c *pollCounter) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if c.hook != nil {
		c.hook(q)
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
	if q.Key.Op == objstore.AnyKey && (q.Entity == googstore.PollEntity || q.Entity == store.GDriveFiles) {
		c.walks++
	}
	return c.Store.Query(ctx, q)
}
func watchPoll(t *testing.T, p *poller) *pollCounter {
	t.Helper()
	c := &pollCounter{Store: p.db.(*objstore.Indexed).Store}
	indexed, err := googstore.Open(t.Context(), c, p.name)
	if err != nil {
		t.Fatal(err)
	}
	p.db = indexed
	p.bindings = store.NewOver(c)
	return c
}
func updatePoll(t *testing.T, p *poller, id string, attrs map[string]string) {
	t.Helper()
	ok, err := p.db.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: googstore.PollEntity, Key: objstore.Key{Partition: p.name, Sort: id}, Attrs: attrs}})
	if err != nil || !ok {
		t.Fatalf("update poll: %v %v", ok, err)
	}
}

// A sweep holds no copy of the bindings: it reads the bindings and the poll
// rows once each, and it polls only the files that are due.
func TestSweepReadsTheBindingsAndPollsOnlyDueFiles(t *testing.T) {
	calls := 0
	ids := make([]string, 41)
	for i := range ids {
		ids[i] = fmt.Sprintf("file%d", i)
	}
	p, _, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{}`) }, ids...)
	c := watchPoll(t, p)
	if err := p.reconcileBindings(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range ids[1:] {
		updatePoll(t, p, id, map[string]string{"next_poll_at": core.TS(now.Add(time.Hour))})
	}
	c.rows, c.walks = 0, 0
	p.sweep(t.Context(), now)
	if calls != 2 || c.walks != 2 {
		t.Fatalf("calls=%d walks=%d", calls, c.walks)
	}
	c.walks = 0
	p.sweep(t.Context(), now)
	if calls != 2 || c.walks != 2 {
		t.Fatalf("idle calls=%d walks=%d", calls, c.walks)
	}
}
func TestPollRestartKeepsBackoffAndCursor(t *testing.T) {
	calls := 0
	p, db, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(429) }, "file")
	db.put("file", map[string]string{"cursor": "2026-01-02T00:00:00Z"})
	now := time.Now()
	p.sweep(t.Context(), now)
	before := db.state("file")
	restarted := &poller{b: p.b, bindings: p.bindings, db: p.db, router: p.router, name: p.name, interval: p.interval}
	restarted.sweep(t.Context(), now.Add(time.Second))
	if calls != 2 || db.state("file").next != before.next {
		t.Fatalf("restart lost deadline: calls=%d", calls)
	}
	restarted.sweep(t.Context(), before.next)
	got := db.state("file")
	if calls != 4 || got.delay != time.Minute || got.cursor != before.cursor {
		t.Fatalf("restart lost state: calls=%d state=%+v", calls, got)
	}
}
func TestPollUnbindPreservesCursorAndRebindWakes(t *testing.T) {
	p, db, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }, "file")
	db.put("file", map[string]string{"cursor": "2026-01-02T00:00:00Z"})
	now := time.Now()
	p.sweep(t.Context(), now)
	if ok, err := p.bindings.RemoveGDriveFile("file", "world"); err != nil || !ok {
		t.Fatalf("unbind %v %v", ok, err)
	}
	p.sweep(t.Context(), now)
	if got := db.row("file"); got["active"] != "0" || got["cursor"] != "2026-01-02T00:00:00Z" {
		t.Fatal(got)
	}
	if work, err := googstore.Due(t.Context(), p.db, p.name, now.Add(time.Hour)); err != nil || len(work) != 0 {
		t.Fatalf("unbound work %v %v", work, err)
	}
	if err := p.bindings.AddGDriveFile(store.GDriveFile{FileID: "file", OwnerFolder: "world", TargetFolder: "world"}); err != nil {
		t.Fatal(err)
	}
	p.sweep(t.Context(), now)
	if got := db.row("file"); got["active"] != "1" || got["cursor"] != "2026-01-02T00:00:00Z" {
		t.Fatal(got)
	}
}
func TestPollKeepsConcurrentState(t *testing.T) {
	p, db, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }, "file")
	if err := p.reconcileBindings(t.Context()); err != nil {
		t.Fatal(err)
	}
	item := db.item("file")
	updatePoll(t, p, "file", map[string]string{"cursor": "2026-11-01T00:00:00Z", "next_poll_at": core.TS(time.Now().Add(time.Hour))})
	if err := p.save(t.Context(), item, &filePoll{cursor: "2026-01-01T00:00:00Z"}); err == nil {
		t.Fatal("stale completion accepted")
	}
	if db.row("file")["cursor"] != "2026-11-01T00:00:00Z" {
		t.Fatal("lost new cursor")
	}
}
func TestPollChecksHydratedStateAndBinding(t *testing.T) {
	for _, change := range []string{"deadline", "inactive", "removed", "replaced"} {
		t.Run(change, func(t *testing.T) {
			calls := 0
			p, _, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{}`) }, "file")
			if err := p.reconcileBindings(t.Context()); err != nil {
				t.Fatal(err)
			}
			c := watchPoll(t, p)
			c.hook = func(q objstore.Query) {
				if q.Entity != googstore.PollEntity || q.Key.Op != objstore.ExactKey {
					return
				}
				c.hook = nil
				switch change {
				case "deadline":
					updatePoll(t, p, "file", map[string]string{"next_poll_at": core.TS(time.Now().Add(time.Hour))})
				case "inactive":
					updatePoll(t, p, "file", map[string]string{"active": "0"})
				default:
					if ok, err := p.bindings.RemoveGDriveFile("file", "world"); err != nil || !ok {
						t.Fatalf("remove %v %v", ok, err)
					}
					if change == "replaced" {
						if err := p.bindings.AddGDriveFile(store.GDriveFile{FileID: "file", OwnerFolder: "world", TargetFolder: "world"}); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			p.sweep(t.Context(), time.Now())
			if calls != 0 {
				t.Fatalf("polled %s work: %d", change, calls)
			}
		})
	}
}
func TestPollMigrationRunsOnce(t *testing.T) {
	p, _, _ := newPollTest(t, func(http.ResponseWriter, *http.Request) {})
	raw := p.db.(*objstore.Indexed).Store
	attrs := map[string]string{"active": "1", "cursor": "2026-01-02T00:00:00Z"}
	ok, err := raw.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: googstore.PollEntity, Key: objstore.Key{Partition: "other", Sort: "file"}, Attrs: attrs}})
	if err != nil || !ok {
		t.Fatalf("legacy %v %v", ok, err)
	}
	c := &pollCounter{Store: raw}
	indexed, err := googstore.Open(t.Context(), c, "other")
	if err != nil {
		t.Fatal(err)
	}
	due, err := googstore.Due(t.Context(), indexed, "other", time.Now())
	if err != nil || len(due) != 1 || due[0].Attrs["cursor"] != attrs["cursor"] {
		t.Fatalf("due %v %v", due, err)
	}
	before := maps.Clone(due[0].Attrs)
	c.rows, c.walks = 0, 0
	if _, err = googstore.Open(t.Context(), c, "other"); err != nil {
		t.Fatal(err)
	}
	if c.rows != 1 || c.walks != 0 {
		t.Fatalf("repeated migration rows=%d walks=%d", c.rows, c.walks)
	}
	due, err = googstore.Due(t.Context(), indexed, "other", time.Now())
	if err != nil || !maps.Equal(before, due[0].Attrs) {
		t.Fatal("migration changed existing data")
	}
}

func TestPollDeadlineStartsAfterEarlierFiles(t *testing.T) {
	p, db, _ := newPollTest(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(20 * time.Millisecond); fmt.Fprint(w, `{}`) }, "a", "b")
	p.interval = time.Second
	if err := p.reconcileBindings(t.Context()); err != nil {
		t.Fatal(err)
	}
	due, err := googstore.Due(t.Context(), p.db, p.name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	last := due[len(due)-1].Key.Sort
	start := time.Now()
	p.sweep(t.Context(), start)
	if next, floor := db.state(last).next, start.Add(time.Second+20*time.Millisecond); next.Before(floor) {
		t.Fatalf("next deadline %v is before %v: omits the earlier file's 20ms poll", next, floor)
	}
}
