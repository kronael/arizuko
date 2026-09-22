package main

// Shared fixtures for the CLI's own bodies. Every one of them reaches a real
// owner's tables, so each test takes a namespace of its own on the endpoint
// rather than the instance's.

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// TestMain takes the endpoint for this binary — except in a CHILD.
// runPkgExpectingDeath re-execs this binary to observe a die(), the child
// inherits the parent's DYNAMODB_URL through its environment, and it leaves
// through os.Exit inside the test body. A Setup here would start an endpoint
// that nothing ever shuts down: one container per refusal asserted.
func TestMain(m *testing.M) {
	if os.Getenv("ARIZUKO_PKG_ARGS") != "" {
		os.Exit(m.Run())
	}
	dyntest.Main(m)
}

// routdStore is routd's OWN tables — acl, secrets, routes and the rest the CLI
// writes — through routd's own opener, so the floor a fresh instance is born
// with is there too. The namespace is dropped when the test ends.
func routdStore(t *testing.T) *store.Store {
	t.Helper()
	d, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return store.NewOver(d.Obj())
}

// countItems counts the items of one entity whose coordinate and attributes
// satisfy match. Every assertion that names one of routd's thirteen goes
// through it: a `WHERE principal=` has no column to name, and the question
// those bodies ask is the same question over items.
func countItems(t *testing.T, s *store.Store, entity string, match func(objstore.Item) bool) int {
	t.Helper()
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: entity, Partition: store.OwnerRoutd,
	})
	if err != nil {
		t.Fatalf("read %s: %v", entity, err)
	}
	n := 0
	for _, it := range items {
		if match(it) {
			n++
		}
	}
	return n
}

// instanceStore is the INSTANCE's own routd tables — the ones addressed by
// owner, which is the only set the CLI's own door opens: a cmd* entry point
// resolves its store from the environment the way an operator's shell does, so
// a body that drives one cannot hand it a namespace of its own. The tables are
// dropped when the test ends, so the next body starts on an empty instance.
func instanceStore(t *testing.T) *store.Store {
	t.Helper()
	d, err := routd.Open()
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		dropInstance(t)
	})
	return store.NewOver(d.Obj())
}

// dropInstance removes every table the instance's owners hold, so one body's
// rows are never another's fixture.
func dropInstance(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, owner := range []string{store.OwnerRoutd, store.OwnerOnbod} {
		h, err := store.Handle(owner)
		if err != nil {
			continue
		}
		left, err := h.Entities(ctx)
		if err != nil {
			continue
		}
		for _, e := range left {
			_ = h.DropEntity(ctx, e)
		}
	}
}

// countMessages, countTokens and countInvites answer what ONE store holds,
// through the readers the daemons themselves call. Each table is an entity, so
// there is no column to COUNT — and a read through the real reader also proves
// the row came back as the row the caller wrote.
func countMessages(t *testing.T, s *store.Store, chatJID string) int {
	t.Helper()
	msgs, err := s.MessagesSince(chatJID, time.Time{})
	if err != nil {
		t.Fatalf("read %s: %v", chatJID, err)
	}
	return len(msgs)
}

func countTokens(t *testing.T, s *store.Store) int {
	t.Helper()
	n, err := s.CountRouteTokens(context.Background())
	if err != nil {
		t.Fatalf("CountRouteTokens: %v", err)
	}
	return n
}

func countInvites(t *testing.T, s *store.Store) int {
	t.Helper()
	rows, err := s.ListInvites("")
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	return len(rows)
}

// keysOf answers a group map's folders, sorted, for an assertion that names
// them.
func keysOf(m map[string]core.Group) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
