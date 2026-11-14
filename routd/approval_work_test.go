package routd

import (
	"context"
	"fmt"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
	"sync/atomic"
	"testing"
	"time"
)

func TestApprovalWaitReadsOnlyOnTransition(t *testing.T) {
	waitForVerdict(t)
	base := memDB(t)
	if err := base.PutPendingAction(PendingAction{ID: "one", GroupFolder: "atlas", Tool: "delete"}); err != nil {
		t.Fatal(err)
	}
	c := &countingStore{Store: base.obj, reads: map[string]int{}}
	db := OpenOver(c)
	defer db.Close()
	done := make(chan bool, 1)
	go func() { _, held := (&Server{db: db}).awaitVerdict("one", "delete"); done <- held }()
	awaitCond(t, "initial read missing", func() bool { return c.count(store.PendingActions) > 0 })
	time.Sleep(30 * time.Millisecond)
	if got := c.count(store.PendingActions); got != 1 {
		t.Fatalf("unchanged held row read %d times", got)
	}
	key, _ := store.PendingActionKey("one")
	if err := store.NewOver(base.obj).WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: store.PendingActions, Key: key, Attrs: map[string]string{store.AttrPendStatus: PendingApproved}}); err != nil {
		t.Fatal(err)
	}
	select {
	case held := <-done:
		if held {
			t.Fatal("external verdict refused")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external writer did not wake waiter")
	}
}

type approvalReadHook struct {
	objstore.Store
	once         atomic.Bool
	hook         func()
	fail         atomic.Bool
	afterVersion bool
	markers      atomic.Int32
}

func (s *approvalReadHook) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	rows, err := s.Store.Query(ctx, q)
	if s.afterVersion && q.Entity == store.ScopeEntity && s.markers.Add(1) == 2 && s.hook != nil {
		s.hook()
	}
	if q.Entity == store.PendingActions && q.Key.Op == objstore.ExactKey {
		if !s.afterVersion && s.once.CompareAndSwap(false, true) && s.hook != nil {
			s.hook()
		}
		if s.fail.Swap(false) {
			return nil, fmt.Errorf("transient pending read")
		}
	}
	return rows, err
}
func TestApprovalRegistrationPrecedesRead(t *testing.T) {
	waitForVerdict(t)
	base := memDB(t)
	if err := base.PutPendingAction(PendingAction{ID: "one", GroupFolder: "atlas", Tool: "delete"}); err != nil {
		t.Fatal(err)
	}
	hook := &approvalReadHook{Store: base.obj, afterVersion: true}
	db := OpenOver(hook)
	defer db.Close()
	hook.hook = func() {
		key, _ := store.PendingActionKey("one")
		if err := store.NewOver(base.obj).WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: store.PendingActions, Key: key, Attrs: map[string]string{store.AttrPendStatus: PendingApproved}}); err != nil {
			t.Error(err)
		}
		db.publishApprovalWork(t.Context())
	}
	done := make(chan bool, 1)
	go func() { _, held := (&Server{db: db}).awaitVerdict("one", "delete"); done <- held }()
	select {
	case held := <-done:
		if held {
			t.Fatal("approval refused")
		}
	case <-time.After(time.Second):
		t.Fatal("verdict between registration and first read was lost")
	}
}
func TestApprovalWaitRetriesReadFailure(t *testing.T) {
	waitForVerdict(t)
	base := memDB(t)
	if err := base.PutPendingAction(PendingAction{ID: "one", GroupFolder: "atlas", Tool: "delete", Status: PendingApproved}); err != nil {
		t.Fatal(err)
	}
	base.publishApprovalWork(t.Context())
	hook := &approvalReadHook{Store: base.obj}
	hook.fail.Store(true)
	db := OpenOver(hook)
	defer db.Close()
	done := make(chan bool, 1)
	go func() { _, held := (&Server{db: db}).awaitVerdict("one", "delete"); done <- held }()
	select {
	case held := <-done:
		if held {
			t.Fatal("approval refused after transient read")
		}
	case <-time.After(time.Second):
		t.Fatal("read error lost the committed verdict")
	}
}

func TestApprovalRemovalEndsWait(t *testing.T) {
	waitForVerdict(t)
	db := memDB(t)
	if err := db.PutPendingAction(PendingAction{ID: "one", GroupFolder: "atlas", Tool: "delete"}); err != nil {
		t.Fatal(err)
	}
	c := &countingStore{Store: db.obj, reads: map[string]int{}}
	waiting := OpenOver(c)
	defer waiting.Close()
	done := make(chan bool, 1)
	go func() { _, held := (&Server{db: waiting}).awaitVerdict("one", "delete"); done <- held }()
	awaitCond(t, "waiter missing", func() bool { return c.count(store.PendingActions) > 0 })
	key, _ := store.PendingActionKey("one")
	if err := db.store().WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Delete, Entity: store.PendingActions, Key: key}); err != nil {
		t.Fatal(err)
	}
	select {
	case held := <-done:
		if !held {
			t.Fatal("deleted approval ran")
		}
	case <-time.After(time.Second):
		t.Fatal("deleted approval kept waiting")
	}
}

type blockedApprovalStore struct {
	objstore.Store
	entered chan struct{}
	once    atomic.Bool
}

func (b *blockedApprovalStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == objstore.IndexEntity && q.Partition == "routd_work/approvals" {
		if b.once.CompareAndSwap(false, true) {
			close(b.entered)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.Store.Query(ctx, q)
}
func TestApprovalCloseCancelsWorkRead(t *testing.T) {
	base := memDB(t)
	blocked := &blockedApprovalStore{Store: base.obj, entered: make(chan struct{})}
	db := OpenOver(blocked)
	_, unsubscribe := db.waitForApproval("one")
	defer unsubscribe()
	<-blocked.entered
	done := make(chan struct{})
	go func() { _ = db.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel in-flight work read")
	}
}

func TestApprovalWaitDefersHeldReplacement(t *testing.T) {
	waitForVerdict(t)
	base := memDB(t)
	if err := base.PutPendingAction(PendingAction{ID: "one", GroupFolder: "atlas", Tool: "delete"}); err != nil {
		t.Fatal(err)
	}
	c := &countingStore{Store: base.obj, reads: map[string]int{}}
	db := OpenOver(c)
	defer db.Close()
	done := make(chan bool, 1)
	go func() { _, held := (&Server{db: db}).awaitVerdict("one", "delete"); done <- held }()
	awaitCond(t, "initial read missing", func() bool { return c.count(store.PendingActions) > 0 })
	sc := store.OwnerScope(store.OwnerRoutd)
	if ok, err := base.obj.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: sc.Entity, Key: sc.Key, Attrs: map[string]string{"holder": "replace"}}}); err != nil || !ok {
		t.Fatalf("hold scope %v %v", ok, err)
	}
	key, _ := store.PendingActionKey("one")
	if err := base.store().WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Delete, Entity: store.PendingActions, Key: key}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("partial replacement answered the call")
	case <-time.After(25 * time.Millisecond):
	}
	if err := base.PutPendingAction(PendingAction{ID: "one", GroupFolder: "atlas", Tool: "delete", Status: PendingApproved}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("held replacement authorized the call")
	case <-time.After(25 * time.Millisecond):
	}
	if ok, err := base.obj.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Update, Entity: sc.Entity, Key: sc.Key, Remove: []string{"holder"}}}); err != nil || !ok {
		t.Fatalf("release scope %v %v", ok, err)
	}
	select {
	case held := <-done:
		if held {
			t.Fatal("settled approval refused")
		}
	case <-time.After(time.Second):
		t.Fatal("settled approval lost")
	}
}
