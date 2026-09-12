package store

// cycle_guard_test.go is the adoption half of objstore's cycle-guard
// conformance body. That one proves the PROTOCOL serialises two edges closing a
// ring; these prove the four writers that used to hold a transaction instead
// now go through it (BUGS L256).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/conformance"
)

// TestTwoEdgesClosingARingAreSerialisedByTheMarker runs the guard over THIS
// package's own configuration: its edge write, its parents callback and its
// scope. The conformance body proves the protocol with an edge shape of its own;
// this proves the shape these four writers hand it is still one the guard can
// serialise.
//
// The interleave is FORCED rather than raced. conformance.SlipAt fires after the
// WALK's query returns — acl_membership, not the marker — so the second walker
// decides on a graph the first one's edge is not in yet, which is the one window
// the marker compare-and-swaps across. Raced, this collision falls in that
// window a few times in twelve and the body stays green against broken code.
func TestTwoEdgesClosingARingAreSerialisedByTheMarker(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	obj := s.Obj()
	sc := OwnerScope(OwnerRoutd)

	edgeBA, err := membershipPut("b", "a", "test")
	if err != nil {
		t.Fatal(err)
	}
	edgeAB, err := membershipPut("a", "b", "test")
	if err != nil {
		t.Fatal(err)
	}

	var firstOK bool
	var firstErr error
	second := conformance.SlipAt(obj, ACLMembership, 1, func() {
		firstOK, firstErr = objstore.AddEdge(ctx, obj, sc, MembershipParents(obj),
			"b", "a", []objstore.Write{edgeBA})
	})
	_, err = objstore.AddEdge(ctx, second, sc, MembershipParents(second),
		"a", "b", []objstore.Write{edgeAB})

	if firstErr != nil || !firstOK {
		t.Fatalf("the first edge reported ok=%v err=%v", firstOK, firstErr)
	}
	if !errors.Is(err, objstore.ErrCycle) {
		t.Errorf("the second edge reported %v, want ErrCycle: it walked a graph without the "+
			"first one's edge, so only the marker it compare-and-swaps can make it walk "+
			"again (BUGS L256)", err)
	}
	edges, err := s.MemberEdges("a")
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Errorf("the graph holds %d edges, want 1: two is the ring both walkers were allowed "+
			"to close", len(edges))
	}
}

// dropMarker takes routd's scope marker away, so the next writer that walks the
// graph has nothing to serialise two concurrent inserts on.
func dropMarker(t *testing.T, s *Store) {
	t.Helper()
	sc := OwnerScope(OwnerRoutd)
	committed, err := s.Obj().GuardedBatch(context.Background(), nil, []objstore.Write{{
		Op: objstore.Delete, Entity: sc.Entity, Key: sc.Key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	}})
	if err != nil || !committed {
		t.Fatalf("drop routd's marker: committed=%v err=%v", committed, err)
	}
}

// TestEveryEdgeWriterRefusesAGraphWithNoMarker is the adoption probe, and it is
// what goes red if one of these writers slips back onto PutMembershipTx.
//
// objstore.AddEdge refuses a scope with no marker, loudly, because two inserts
// closing a ring would have nothing to serialise on. PutMembershipTx has no such
// refusal: it walks inside the caller's transaction and rests on SQLite's single
// writer, which Postgres at READ COMMITTED does not give. So "does this writer
// refuse when the marker is gone" is exactly "does this writer walk under the
// marker".
func TestEveryEdgeWriterRefusesAGraphWithNoMarker(t *testing.T) {
	for _, c := range []struct {
		name string
		draw func(t *testing.T, s *Store) error
	}{
		{"AddMembership", func(t *testing.T, s *Store) error {
			return s.AddMembership("jid:1", "google:u1", "test")
		}},
		{"PutMembership", func(t *testing.T, s *Store) error {
			return s.PutMembership("jid:1", "google:u1", "test")
		}},
		{"PutGroupRow", func(t *testing.T, s *Store) error {
			return s.PutGroupRow(core.Group{Folder: "atlas", AddedAt: time.Now()})
		}},
		{"RedeemPairing", func(t *testing.T, s *Store) error {
			raw, err := s.IssuePairingLink(context.Background(), nil, "telegram:user/7", "")
			if err != nil {
				t.Fatalf("mint the pairing link: %v", err)
			}
			dropMarker(t, s)
			_, err = s.RedeemPairing(raw, "google:u1")
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := openMem(t)
			if c.name != "RedeemPairing" {
				dropMarker(t, s)
			}
			err := c.draw(t, s)
			if !errors.Is(err, objstore.ErrNoMarker) {
				t.Errorf("%s answered %v with routd's marker gone, want ErrNoMarker: it drew "+
					"an edge without walking under the version that serialises two of them "+
					"(BUGS L256)", c.name, err)
			}
		})
	}
}

// TestTheFolderCreationRefusesAGraphWithNoMarker is the same probe for the two
// daemons that assemble the batch themselves. They live in their own packages,
// so what is checked here is the door they call.
func TestTheFolderCreationRefusesAGraphWithNoMarker(t *testing.T) {
	s := openMem(t)
	dropMarker(t, s)
	row, err := GroupCreateWrite(core.Group{Folder: "atlas", AddedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SeedMemberRole(context.Background(), s, "atlas", row); !errors.Is(
		err, objstore.ErrNoMarker) {
		t.Errorf("SeedMemberRole answered %v with routd's marker gone, want ErrNoMarker", err)
	}
	if _, ok := s.GroupByFolder("atlas"); ok {
		t.Error("the groups row landed even though the edge it implies was refused; the two " +
			"are one batch (BUGS L119)")
	}
}
