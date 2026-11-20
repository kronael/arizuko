package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/retry"
)

func pendingForTest(t *testing.T, s *Store, m core.Message) PendingMessage {
	t.Helper()
	pending, ok, err := s.PendingDelivery(t.Context(), m)
	if err != nil || !ok {
		t.Fatalf("pending delivery: %v %v", ok, err)
	}
	return pending
}

func TestOutboundDueRowsCannotHideBehindBackoff(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	for i := range 201 {
		m := core.Message{ID: fmt.Sprint(i), ChatJID: "chat", Sender: "bot", Content: "owed", BotMsg: true, Timestamp: now.Add(-2*time.Hour + time.Duration(i)*time.Nanosecond)}
		if i == 200 {
			m.Timestamp = now.Add(-time.Minute)
		}
		if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
			t.Fatal(err)
		}
		if i < 200 {
			if _, _, err := s.RecordDeliveryFailure(t.Context(), pendingForTest(t, s, m), retry.Failure{Kind: "transient"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := &stateReadCounter{Store: s.obj}
	reader := NewOver(c)
	due, err := reader.PendingOutbound(t.Context(), now, 200)
	if err != nil || len(due) != 1 || due[0].ID != "200" || c.rows != 3 {
		t.Fatalf("due count=%d err=%v rows=%d", len(due), err, c.rows)
	}
	if err := s.MarkDelivered(t.Context(), due[0].Message, "pid"); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	if due, err := reader.PendingOutbound(t.Context(), now, 200); err != nil || len(due) != 0 || c.rows != 0 {
		t.Fatalf("acknowledged work: %v %v rows=%d", due, err, c.rows)
	}
}
func TestOutboundFailureSchedulesFromAttempt(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	m := core.Message{ID: "old", ChatJID: "chat", Sender: "bot", Content: "owed", BotMsg: true, Timestamp: now.Add(-24 * time.Hour)}
	if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	if n, dead, err := s.RecordDeliveryFailure(t.Context(), pendingForTest(t, s, m), retry.Failure{Kind: "transient"}); err != nil || n != 1 || dead {
		t.Fatalf("failure=%d %v %v", n, dead, err)
	}
	if due, err := s.PendingOutbound(t.Context(), now.Add(29*time.Second), 10); err != nil || len(due) != 0 {
		t.Fatalf("early retry: %v %v", due, err)
	}
	due, err := s.PendingOutbound(t.Context(), now.Add(31*time.Second), 10)
	if err != nil || len(due) != 1 || due[0].NextAttemptAt.Before(now.Add(30*time.Second)) {
		t.Fatalf("deadline lost: %v %v", due, err)
	}
}

func TestOutboundTenthFailureStops(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	m := core.Message{ID: "ten", ChatJID: "chat", Sender: "bot", BotMsg: true, Timestamp: time.Now()}
	if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		n, dead, err := s.RecordDeliveryFailure(t.Context(), pendingForTest(t, s, m), retry.Failure{Kind: "transient"})
		if err != nil || n != i || dead != (i == 10) {
			t.Fatalf("failure %d: n=%d dead=%v err=%v", i, n, dead, err)
		}
	}
	failed, err := s.DeadLetters(t.Context(), "", 10)
	if err != nil || len(failed) != 1 || failed[0].Attempts != 10 || !failed[0].NextAttemptAt.IsZero() {
		t.Fatalf("terminal delivery: %+v %v", failed, err)
	}
	if rows, err := s.PendingOutbound(t.Context(), time.Now().Add(24*time.Hour), 10); err != nil || len(rows) != 0 {
		t.Fatalf("terminal delivery remains due: %+v %v", rows, err)
	}
}

func TestOutboundFailureRespectsRetryAtAndExpiry(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now().UTC()
	retryAt := now.Add(2 * time.Hour).Truncate(time.Millisecond)
	for _, expiry := range []time.Time{retryAt.Add(time.Hour), retryAt} {
		m := core.Message{ID: expiry.String(), ChatJID: "chat", Sender: "bot", BotMsg: true, Timestamp: now}
		if _, err := s.putMessage(t.Context(), m, core.MessageStatusPending, messageOptions{exact: true, delivery: map[string]string{"expires_at": core.TS(expiry)}}); err != nil {
			t.Fatal(err)
		}
		n, dead, err := s.RecordDeliveryFailure(t.Context(), pendingForTest(t, s, m), retry.Failure{Kind: "transient", RetryAt: retryAt})
		if err != nil || n != 1 || dead != expiry.Equal(retryAt) {
			t.Fatalf("expiry %v: n=%d dead=%v err=%v", expiry, n, dead, err)
		}
		if !dead {
			row, ok, err := s.PendingDelivery(t.Context(), m)
			if err != nil || !ok || !row.NextAttemptAt.Equal(retryAt) {
				t.Fatalf("persisted deadline: %+v %v %v", row, ok, err)
			}
		}
	}
}

type deliveryRace struct {
	objstore.Store
	other      *Store
	m          core.Message
	ack, fired bool
}

func (r *deliveryRace) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if !r.fired {
		r.fired = true
		if r.ack {
			if err := r.other.MarkDelivered(ctx, r.m, "accepted"); err != nil {
				return false, err
			}
		} else {
			pending, ok, err := r.other.PendingDelivery(ctx, r.m)
			if err != nil || !ok {
				return false, fmt.Errorf("pending: %v %v", ok, err)
			}
			if _, _, err := r.other.RecordDeliveryFailure(ctx, pending, retry.Failure{Kind: "transient"}); err != nil {
				return false, err
			}
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}
func TestOutboundFailureKeepsConcurrentTransitions(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			s := openMem(t)
			defer s.Close()
			m := core.Message{ID: "race", ChatJID: "chat", Sender: "bot", Content: "owed", BotMsg: true, Timestamp: time.Now().Add(-time.Hour)}
			if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
				t.Fatal(err)
			}
			pending := pendingForTest(t, s, m)
			race := NewOver(&deliveryRace{Store: s.obj, other: s, m: m, ack: ack})
			n, dead, err := race.RecordDeliveryFailure(t.Context(), pending, retry.Failure{Kind: "transient"})
			if err != nil || dead {
				t.Fatalf("concurrent transition lost: n=%d dead=%v err=%v", n, dead, err)
			}
			if n != 0 {
				t.Fatalf("stale failure count=%d, want 0", n)
			}
			if !ack {
				if got := pendingForTest(t, s, m); got.Attempts != 1 {
					t.Fatalf("concurrent failure count=%d", got.Attempts)
				}
			}
			if ack {
				if due, err := s.PendingOutbound(t.Context(), time.Now().Add(time.Hour), 10); err != nil || len(due) != 0 {
					t.Fatalf("delivery resurrected: %v %v", due, err)
				}
				if failed, err := s.DeadLetters(t.Context(), "", 10); err != nil || len(failed) != 0 {
					t.Fatalf("acknowledged delivery failed: %v %v", failed, err)
				}
			}
		})
	}
}

func TestOutboundStaleSnapshotDoesNotReplaceDelivery(t *testing.T) {
	s := openMem(t)
	m := core.Message{ID: "stale", ChatJID: "chat", Sender: "bot", BotMsg: true, Timestamp: time.Now().Add(-time.Hour)}
	if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	stale := pendingForTest(t, s, m)
	due := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Millisecond)
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: MessageDelivery, Key: stale.observed.Key, Attrs: map[string]string{"next_attempt_at": core.TS(due)}}); err != nil {
		t.Fatal(err)
	}
	if n, dead, err := s.RecordDeliveryFailure(t.Context(), stale, retry.Failure{Kind: "permanent"}); err != nil || n != 0 || dead {
		t.Fatalf("stale failure: %d %v %v", n, dead, err)
	}
	if err := s.DeadLetter(t.Context(), stale, stale.Attempts); err != nil {
		t.Fatal(err)
	}
	got := pendingForTest(t, s, m)
	if got.Attempts != 0 || !got.NextAttemptAt.Equal(due) {
		t.Fatalf("newer delivery replaced: %+v", got)
	}
	failed, err := s.DeadLetters(t.Context(), "", 10)
	if err != nil || len(failed) != 0 {
		t.Fatalf("stale dead letter: %+v %v", failed, err)
	}
}

func TestStatusAcknowledgementClearsConcurrentFailure(t *testing.T) {
	s := openTurnEntities(t)
	m := core.Message{ID: "status", ChatJID: "chat", Verb: "status", BotMsg: true, Timestamp: time.Now()}
	if err := s.QueueStatus(t.Context(), m, "waiting", "waiting"); err != nil {
		t.Fatal(err)
	}
	m, _ = s.MessageByID(m.ChatJID, m.ID)
	p, ok, err := s.PendingDelivery(t.Context(), m)
	if err != nil || !ok {
		t.Fatalf("pending: %v %v", ok, err)
	}
	if err := s.FailStatus(t.Context(), p, retry.Failure{Kind: "permanent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivered(t.Context(), m, "accepted"); err != nil {
		t.Fatal(err)
	}
	if failed, err := s.DeadLetters(t.Context(), "", 10); err != nil || len(failed) != 0 {
		t.Fatalf("acknowledged status failed: %v %v", failed, err)
	}
}

func TestOutboundRetryMigrationPreservesLegacyDeadline(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := t.Context()
	if err := s.MigrateStateIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := core.Message{ID: "legacy-retry", ChatJID: "chat", Sender: "bot", BotMsg: true, Timestamp: now.Add(-time.Hour)}
	key, err := messageKeyOf(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutOutbound(ctx, m, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	source := deliveryKey(core.MessageStatusPending, key)
	due := m.Timestamp.Add(2 * time.Minute)
	pointer := objstore.DueIndexKey("outbound/due", core.TS(due), MessageDelivery, source)
	writes := []objstore.Write{
		{Op: objstore.Delete, Entity: objstore.IndexEntity, Key: objstore.DueIndexKey("outbound/due", core.TS(m.Timestamp.Add(30*time.Second)), MessageDelivery, source)},
		{Op: objstore.Put, Entity: MessageDelivery, Key: source, Attrs: map[string]string{attrDelChatJID: m.ChatJID, attrDelAttempts: "2"}},
		{Op: objstore.Put, Entity: objstore.IndexEntity, Key: pointer, Attrs: map[string]string{"entity": MessageDelivery, "source_partition": source.Partition, "source_sort": source.Sort}},
		{Op: objstore.Delete, Entity: objstore.IndexEntity, Key: objstore.Key{Partition: "migrations", Sort: "outbound-retry-v1"}},
	}
	if ok, err := s.obj.GuardedBatch(ctx, nil, writes); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	high := core.Message{ID: "legacy-high", ChatJID: "chat", Sender: "bot", BotMsg: true, Timestamp: now}
	explicit := core.Message{ID: "explicit-retry", ChatJID: "chat", Sender: "bot", BotMsg: true, Timestamp: now.Add(time.Second)}
	explicitDue := now.Add(2 * time.Hour)
	for _, extra := range []core.Message{high, explicit} {
		if err := s.PutOutbound(ctx, extra, core.MessageStatusPending); err != nil {
			t.Fatal(err)
		}
		extraKey, err := messageKeyOf(extra)
		if err != nil {
			t.Fatal(err)
		}
		extraSource := deliveryKey(core.MessageStatusPending, extraKey)
		attrs := map[string]string{attrDelChatJID: extra.ChatJID, attrDelAttempts: "12"}
		expected := now.Add(time.Hour)
		if extra.ID == explicit.ID {
			attrs["next_attempt_at"] = core.TS(explicitDue)
			expected = explicitDue
		}
		pointer := objstore.DueIndexKey("outbound/due", core.TS(expected), MessageDelivery, extraSource)
		if ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{
			{Op: objstore.Put, Entity: MessageDelivery, Key: extraSource, Attrs: attrs},
			{Op: objstore.Delete, Entity: objstore.IndexEntity, Key: objstore.DueIndexKey("outbound/due", core.TS(extra.Timestamp.Add(30*time.Second)), MessageDelivery, extraSource)},
			{Op: objstore.Put, Entity: objstore.IndexEntity, Key: pointer, Attrs: map[string]string{"entity": MessageDelivery, "source_partition": extraSource.Partition, "source_sort": extraSource.Sort}},
		}); err != nil || !ok {
			t.Fatalf("seed extra: %v %v", ok, err)
		}
	}
	if err := s.MigrateStateIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []core.Message{high, explicit} {
		row, ok, err := s.PendingDelivery(ctx, extra)
		expected := now.Add(time.Hour)
		if extra.ID == explicit.ID {
			expected = explicitDue
		}
		if err != nil || !ok || row.Attempts != 12 || !row.NextAttemptAt.Equal(expected) {
			t.Fatalf("extra legacy row: %+v %v %v", row, ok, err)
		}
	}
	counter := &stateReadCounter{Store: s.obj}
	if err := NewOver(counter).MigrateStateIndexes(ctx); err != nil || counter.rows != 12 {
		t.Fatalf("repeat migration: %v rows=%d", err, counter.rows)
	}
	row, ok, err := s.PendingDelivery(ctx, m)
	if err != nil || !ok || row.Attempts != 2 || !row.NextAttemptAt.Equal(due) {
		t.Fatalf("legacy row: %+v %v %v", row, ok, err)
	}
	rows, err := s.PendingOutbound(ctx, due.Add(-time.Nanosecond), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("early due: %+v %v", rows, err)
	}
	rows, err = s.PendingOutbound(ctx, due, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != m.ID {
		t.Fatalf("due: %+v %v", rows, err)
	}
}

type outboundMigrationRace struct {
	objstore.Store
	other *Store
	key   objstore.Key
	due   time.Time
	fired bool
}

func (r *outboundMigrationRace) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, write := range writes {
		if !r.fired && write.Entity == MessageDelivery && write.Key == r.key {
			r.fired = true
			err := r.other.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: MessageDelivery, Key: r.key, Attrs: map[string]string{attrDelAttempts: "4", "next_attempt_at": core.TS(r.due)}})
			if err != nil {
				return false, err
			}
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}

func TestOutboundRetryMigrationPreservesConcurrentFailure(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := t.Context()
	if err := s.MigrateStateIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := core.Message{ID: "race-retry", ChatJID: "chat", Sender: "bot", BotMsg: true, Timestamp: now}
	if err := s.PutOutbound(ctx, m, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	key, err := messageKeyOf(m)
	if err != nil {
		t.Fatal(err)
	}
	source := deliveryKey(core.MessageStatusPending, key)
	if ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{
		{Op: objstore.Update, Entity: MessageDelivery, Key: source, Remove: []string{"next_attempt_at"}},
		{Op: objstore.Delete, Entity: objstore.IndexEntity, Key: objstore.Key{Partition: "migrations", Sort: "outbound-retry-v1"}},
	}); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	future := now.Add(3 * time.Hour)
	race := &outboundMigrationRace{Store: s.obj, other: s, key: source, due: future}
	if err := NewOver(race).MigrateStateIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	row, ok, err := s.PendingDelivery(ctx, m)
	if !race.fired || err != nil || !ok || row.Attempts != 4 || !row.NextAttemptAt.Equal(future) {
		t.Fatalf("concurrent failure: %+v fired=%v ok=%v err=%v", row, race.fired, ok, err)
	}
}

func TestFirstOutboundFailure(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now().UTC()
	due := now.Add(time.Hour).Truncate(time.Millisecond)
	m := core.Message{ID: "first", ChatJID: "chat", Content: "original", BotMsg: true, Timestamp: now}
	d, err := s.PutOutboundFailure(t.Context(), m, retry.Failure{Kind: "transient", RetryAt: due})
	if err != nil || d.Failures != 1 || d.Stop || !d.NextAt.Equal(due) {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
	pending := pendingForTest(t, s, m)
	if pending.Attempts != 1 || !pending.NextAttemptAt.Equal(due) {
		t.Fatalf("pending=%+v", pending)
	}
	m.Content = "replacement"
	again, err := s.PutOutboundFailure(t.Context(), m, retry.Failure{Kind: "permanent"})
	if err != nil || again != d {
		t.Fatalf("duplicate=%+v err=%v", again, err)
	}
	pending = pendingForTest(t, s, m)
	if pending.Content != "original" || pending.Attempts != 1 || !pending.NextAttemptAt.Equal(due) {
		t.Fatalf("duplicate pending=%+v", pending)
	}
	if err := s.MarkDelivered(t.Context(), pending.Message, "pid"); err != nil {
		t.Fatal(err)
	}
	again, err = s.PutOutboundFailure(t.Context(), m, retry.Failure{Kind: "transient"})
	if err != nil || again != (retry.Decision{}) {
		t.Fatalf("sent duplicate=%+v err=%v", again, err)
	}
	terminal := core.Message{ID: "terminal", ChatJID: "chat", BotMsg: true, Timestamp: now}
	d, err = s.PutOutboundFailure(t.Context(), terminal, retry.Failure{Kind: "permanent"})
	if err != nil || !d.Stop || d.Failures != 1 || !d.NextAt.IsZero() {
		t.Fatalf("terminal=%+v err=%v", d, err)
	}
	key, err := s.deliveryMessageKey(t.Context(), terminal)
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := s.itemAt(t.Context(), MessageDelivery, deliveryKey(core.MessageStatusFailed, key))
	if err != nil || !ok || item.Attrs["next_attempt_at"] != "" {
		t.Fatalf("terminal item=%+v ok=%v err=%v", item, ok, err)
	}
	dueRows, err := s.PendingOutbound(t.Context(), time.Now().Add(24*time.Hour), 10)
	if err != nil || len(dueRows) != 0 {
		t.Fatalf("terminal due=%+v err=%v", dueRows, err)
	}
	dead, err := s.DeadLetters(t.Context(), "", 10)
	if err != nil || len(dead) != 1 || dead[0].Attempts != 1 || !dead[0].NextAttemptAt.IsZero() {
		t.Fatalf("dead=%+v err=%v", dead, err)
	}
}

// One legacy delivery row whose deadline cannot be derived is logged and left
// in place; the migration still finishes and migrates the other rows (BUGS L889).
func TestOutboundRetryMigrationSkipsMalformedRow(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := t.Context()
	if err := s.MigrateStateIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seed := func(id, attempts string) objstore.Key {
		m := core.Message{ID: id, ChatJID: "chat", Sender: "bot", BotMsg: true, Timestamp: now.Add(-time.Hour)}
		key, err := messageKeyOf(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutOutbound(ctx, m, core.MessageStatusPending); err != nil {
			t.Fatal(err)
		}
		source := deliveryKey(core.MessageStatusPending, key)
		if ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{
			{Op: objstore.Put, Entity: MessageDelivery, Key: source, Attrs: map[string]string{attrDelChatJID: m.ChatJID, attrDelAttempts: attempts}},
		}); err != nil || !ok {
			t.Fatalf("seed %s: %v %v", id, ok, err)
		}
		return source
	}
	bad := seed("legacy-bad", "x")
	good := seed("legacy-good", "1")
	if ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Delete, Entity: objstore.IndexEntity, Key: objstore.Key{Partition: "migrations", Sort: "outbound-retry-v1"}}}); err != nil || !ok {
		t.Fatalf("reset marker: %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(ctx); err != nil {
		t.Fatalf("startup failed on a malformed row: %v", err)
	}
	for key, want := range map[objstore.Key]bool{bad: false, good: true} {
		it, ok, err := s.itemAt(ctx, MessageDelivery, key)
		if err != nil || !ok {
			t.Fatalf("row %v: %v %v", key, ok, err)
		}
		if got := it.Attrs["next_attempt_at"] != ""; got != want {
			t.Fatalf("row %v next_attempt_at=%q, migrated want %v", key, it.Attrs["next_attempt_at"], want)
		}
	}
}
