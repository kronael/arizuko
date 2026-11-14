package chanstore_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

type inboundCounter struct {
	objstore.Store
	rows, payloads int
}

func (c *inboundCounter) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	measured := q
	measured.Where, measured.Project = nil, nil
	rows, err := c.Store.Query(ctx, measured)
	if err != nil {
		return nil, err
	}
	c.rows += len(rows)
	if q.Entity == chanstore.InboundPending {
		c.payloads += len(rows)
	}
	return c.Store.Query(ctx, q)
}
func countedInbound(t *testing.T) (*chanstore.Store, *inboundCounter) {
	t.Helper()
	raw := dyntest.Store(t)
	for _, e := range chanstore.Entities {
		if err := raw.CreateEntity(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	c := &inboundCounter{Store: raw}
	return chanstore.New(c), c
}
func TestInboundReadsOnlyDueRowsAndOneCount(t *testing.T) {
	s, c := countedInbound(t)
	now := time.Now()
	for i := range 40 {
		due := now.Add(time.Hour)
		if i == 0 {
			due = now.Add(-time.Second)
		}
		if err := s.Retain(t.Context(), fmt.Sprint(i), "payload", ts(now), ts(due), ""); err != nil {
			t.Fatal(err)
		}
	}
	c.rows, c.payloads = 0, 0
	due, err := s.DueForRedelivery(t.Context(), ts(now), 50)
	if err != nil || len(due) != 1 || due[0].ID != "0" || c.rows != 2 || c.payloads != 1 {
		t.Fatalf("due=%v err=%v rows=%d payloads=%d", due, err, c.rows, c.payloads)
	}
	c.rows = 0
	if n, err := s.HeldCount(t.Context()); err != nil || n != 40 || c.rows != 1 {
		t.Fatalf("count=%d err=%v rows=%d", n, err, c.rows)
	}
	if err := s.Reschedule(t.Context(), "0", 1, ts(now.Add(time.Hour)), ""); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	if due, err := s.DueForRedelivery(t.Context(), ts(now), 50); err != nil || len(due) != 0 || c.rows != 0 {
		t.Fatalf("rescheduled work: %v %v rows=%d", due, err, c.rows)
	}
	for range 2 {
		if err := s.Drop(t.Context(), "0"); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.HeldCount(t.Context()); err != nil || n != 39 {
		t.Fatalf("duplicate drop count=%d err=%v", n, err)
	}
	if err := s.Retain(t.Context(), "1", "other", ts(now), ts(now), ""); err != nil {
		t.Fatal(err)
	}
	if n, err := s.HeldCount(t.Context()); err != nil || n != 39 {
		t.Fatalf("duplicate retain count=%d err=%v", n, err)
	}
}
func TestInboundLimitReadsOnlySelectedPayloads(t *testing.T) {
	s, c := countedInbound(t)
	now := time.Now()
	for i := range 60 {
		if err := s.Retain(t.Context(), fmt.Sprint(i), "large", ts(now.Add(time.Duration(i)*time.Second)), ts(now), ""); err != nil {
			t.Fatal(err)
		}
	}
	c.rows, c.payloads = 0, 0
	due, err := s.DueForRedelivery(t.Context(), ts(now), 50)
	if err != nil || len(due) != 50 || due[0].ID != "0" || due[49].ID != "49" || c.rows != 110 || c.payloads != 50 {
		t.Fatalf("due=%v err=%v rows=%d payloads=%d", due, err, c.rows, c.payloads)
	}
}
func TestInboundMigrationRunsOnce(t *testing.T) {
	s, c := countedInbound(t)
	now := ts(time.Now())
	if ok, err := c.Store.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: chanstore.InboundPending,
		Key: objstore.Key{Partition: "inbound", Sort: "legacy"}, Attrs: map[string]string{"payload": "data", "first_seen_at": now, "next_attempt_at": now, "attempts": "0"}}}); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if err := s.MigrateInbound(t.Context()); err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueForRedelivery(t.Context(), now, 50); err != nil || len(due) != 1 {
		t.Fatalf("legacy due=%v err=%v", due, err)
	}
	if n, err := s.HeldCount(t.Context()); err != nil || n != 1 {
		t.Fatalf("legacy count=%d err=%v", n, err)
	}
	c.rows = 0
	if err := s.MigrateInbound(t.Context()); err != nil || c.rows != 2 {
		t.Fatalf("second migration: %v rows=%d", err, c.rows)
	}
}
