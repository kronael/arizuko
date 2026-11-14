package chanstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/retry"
)

type interruptedInbound struct {
	objstore.Store
	updates int
}

func (s *interruptedInbound) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, write := range writes {
		if write.Entity == chanstore.InboundPending && write.Attrs["retry_state"] != "" {
			s.updates++
			if s.updates == 2 {
				return false, errors.New("interrupted")
			}
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestInboundMigrationResumesWithoutDoubleCharge(t *testing.T) {
	s, c := countedInbound(t)
	now := ts(time.Now())
	for _, id := range []string{"a", "b"} {
		_, err := c.Store.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: chanstore.InboundPending,
			Key: objstore.Key{Partition: "inbound", Sort: id}, Attrs: map[string]string{"payload": id, "first_seen_at": now, "next_attempt_at": now, "attempts": "1"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MigrateInbound(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err := c.Store.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Delete, Entity: objstore.IndexEntity, Key: objstore.Key{Partition: "migrations", Sort: "inbound-retry-v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		_, err = c.Store.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: chanstore.InboundPending, Key: objstore.Key{Partition: "inbound", Sort: id}, Attrs: map[string]string{"attempts": "1"}, Remove: []string{"retry_state"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	interrupted := &interruptedInbound{Store: c}
	if err := chanstore.New(interrupted).MigrateInbound(t.Context()); err == nil {
		t.Fatal("migration did not stop")
	}
	markers, err := c.Store.Query(t.Context(), objstore.Query{Entity: objstore.IndexEntity, Partition: "migrations",
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: "inbound-retry-v1"}})
	if err != nil || len(markers) != 0 {
		t.Fatalf("interrupted migration marked complete: %v %v", markers, err)
	}
	if err := s.MigrateInbound(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DueForRedelivery(t.Context(), now, 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.Attempts != 2 {
			t.Fatalf("attempts=%d", row.Attempts)
		}
	}
	c.rows = 0
	if err := s.MigrateInbound(t.Context()); err != nil || c.rows != 3 {
		t.Fatalf("rerun: %v rows=%d", err, c.rows)
	}
}

func TestStoppedInboundRetainsPayloadAndEpisode(t *testing.T) {
	s, c := countedInbound(t)
	now := time.Now()
	decision := retry.Decision{Failures: 3, Stop: true}
	if err := s.Retain(t.Context(), "x", "original", ts(now), retry.Decision{Failures: 2, NextAt: now}, "retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reschedule(t.Context(), observed(t, s, "x"), decision, "terminal"); err != nil {
		t.Fatal(err)
	}
	if err := s.Retain(t.Context(), "x", "replacement", ts(now), retry.Decision{NextAt: now}, "retry"); err != nil {
		t.Fatal(err)
	}
	rows, err := c.Store.Query(t.Context(), objstore.Query{Entity: chanstore.InboundPending, Partition: "inbound"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	attrs := rows[0].Attrs
	if attrs["payload"] != "original" || attrs["attempts"] != "3" || attrs["retry_state"] != "failed" || attrs["last_error"] != "terminal" {
		t.Fatalf("attrs=%v", attrs)
	}
	if _, ok := attrs["next_attempt_at"]; ok {
		t.Fatalf("failed row has deadline: %v", attrs)
	}
	pointers, err := c.Store.Query(t.Context(), objstore.Query{Entity: objstore.IndexEntity, Partition: "inbound/due"})
	if err != nil || len(pointers) != 0 {
		t.Fatalf("failed row has due pointer: %v %v", pointers, err)
	}
	if due, err := s.DueForRedelivery(t.Context(), ts(now.Add(24*time.Hour)), 0); err != nil || len(due) != 0 {
		t.Fatalf("due=%v err=%v", due, err)
	}
	if count, err := s.HeldCount(t.Context()); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestInboundHydrationRechecksStateAndDeadline(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			s, c := countedInbound(t)
			now := time.Now()
			if err := s.Retain(t.Context(), "x", "payload", ts(now), retry.Decision{Failures: 1, NextAt: now}, "retry"); err != nil {
				t.Fatal(err)
			}
			attrs := map[string]string{"next_attempt_at": ts(now.Add(time.Hour))}
			var remove []string
			if stopped {
				attrs = map[string]string{"retry_state": "failed"}
				remove = []string{"next_attempt_at"}
			}
			ok, err := c.Store.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: chanstore.InboundPending,
				Key: objstore.Key{Partition: "inbound", Sort: "x"}, Attrs: attrs, Remove: remove}})
			if err != nil || !ok {
				t.Fatalf("update primary after pointer snapshot: %v %v", ok, err)
			}
			if due, err := s.DueForRedelivery(t.Context(), ts(now), 50); err != nil || len(due) != 0 {
				t.Fatalf("stale pointer delivered: %v %v", due, err)
			}
		})
	}
}

func TestReadInboundFailureValidatesMetadata(t *testing.T) {
	for _, attrs := range []map[string]string{
		{"attempts": "bad", "retry_state": "failed", "last_error": "stopped"},
		{"attempts": "1", "retry_state": "unknown", "last_error": "stopped"},
		{"attempts": "1", "retry_state": "pending", "last_error": "retry", "next_attempt_at": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
		{"attempts": "1", "retry_state": "failed"},
	} {
		s, c := countedInbound(t)
		_, err := c.Store.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: chanstore.InboundPending, Key: objstore.Key{Partition: "inbound", Sort: "x"}, Attrs: attrs}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadInboundFailure(t.Context(), "x"); err == nil {
			t.Fatalf("accepted %v", attrs)
		}
	}
}

func TestInboundMigrationRetainsExhaustedPayload(t *testing.T) {
	s, c := countedInbound(t)
	now := ts(time.Now())
	_, err := c.Store.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: chanstore.InboundPending,
		Key: objstore.Key{Partition: "inbound", Sort: "x"}, Attrs: map[string]string{"payload": "original", "first_seen_at": now, "next_attempt_at": now, "attempts": "9", "last_error": "unavailable"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateInbound(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := c.Store.Query(t.Context(), objstore.Query{Entity: chanstore.InboundPending, Partition: "inbound"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Attrs["payload"] != "original" || rows[0].Attrs["attempts"] != "10" || rows[0].Attrs["retry_state"] != "failed" {
		t.Fatalf("row=%v", rows[0])
	}
	if _, ok := rows[0].Attrs["next_attempt_at"]; ok {
		t.Fatalf("row=%v", rows[0])
	}
	if due, err := s.DueForRedelivery(t.Context(), ts(time.Now().Add(24*time.Hour)), 0); err != nil || len(due) != 0 {
		t.Fatalf("due=%v err=%v", due, err)
	}
	if count, err := s.HeldCount(t.Context()); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

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
		if err := s.Retain(t.Context(), fmt.Sprint(i), "payload", ts(now), retry.Decision{NextAt: due}, "failure"); err != nil {
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
	if err := s.Reschedule(t.Context(), due[0], retry.Decision{Failures: 1, NextAt: now.Add(time.Hour)}, "failure"); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	if due, err := s.DueForRedelivery(t.Context(), ts(now), 50); err != nil || len(due) != 0 || c.rows != 0 {
		t.Fatalf("rescheduled work: %v %v rows=%d", due, err, c.rows)
	}
	row := observed(t, s, "0")
	for range 2 {
		if err := s.Drop(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.HeldCount(t.Context()); err != nil || n != 39 {
		t.Fatalf("duplicate drop count=%d err=%v", n, err)
	}
	if err := s.Retain(t.Context(), "1", "other", ts(now), retry.Decision{NextAt: now}, "failure"); err != nil {
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
		if err := s.Retain(t.Context(), fmt.Sprint(i), "large", ts(now.Add(time.Duration(i)*time.Second)), retry.Decision{NextAt: now.Add(-time.Duration(i) * time.Second)}, "failure"); err != nil {
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
	now := ts(time.Now().Add(time.Hour))
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
	rows, err := c.Store.Query(t.Context(), objstore.Query{Entity: chanstore.InboundPending, Partition: "inbound"})
	if err != nil || len(rows) != 1 || rows[0].Attrs["next_attempt_at"] != now {
		t.Fatalf("legacy deadline changed: %v %v", rows, err)
	}
	c.rows = 0
	if err := s.MigrateInbound(t.Context()); err != nil || c.rows != 3 {
		t.Fatalf("second migration: %v rows=%d", err, c.rows)
	}
}
