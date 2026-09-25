package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"

	"github.com/kronael/arizuko/core"
)

func TestAddRoute_RejectsWebJIDPredicate(t *testing.T) {
	cases := []struct {
		name  string
		match string
	}{
		{"exact", "chat_jid=web:atlas/strengths"},
		{"glob", "chat_jid=web:atlas/strengths/*"},
		{"with-platform", "platform=web chat_jid=web:atlas"},
		{"multi-token", "verb=mention chat_jid=web:foo/bar"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := openMem(t)
			_, err := s.AddRoute(core.Route{Match: c.match, Target: "atlas/strengths"})
			if !errors.Is(err, ErrWebJIDRouted) {
				t.Errorf("AddRoute(%q) err = %v, want ErrWebJIDRouted", c.match, err)
			}
		})
	}
}

func TestUpdateRoute_RejectsWebJIDPredicate(t *testing.T) {
	s := openMem(t)
	id, err := s.AddRoute(core.Route{Match: "chat_jid=telegram:1", Target: "alpha"})
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	_, err = s.UpdateRoute(id, core.Route{Match: "chat_jid=web:alpha", Target: "alpha"})
	if !errors.Is(err, ErrWebJIDRouted) {
		t.Errorf("UpdateRoute(web jid) err = %v, want ErrWebJIDRouted", err)
	}
}

// An update that matches no row is not a mutation: no rows, no audit row.
func TestUpdateRoute_MissingIDRecordsNothing(t *testing.T) {
	s := openMem(t)
	n, err := s.UpdateRoute(404, core.Route{Match: "chat_jid=telegram:1", Target: "alpha"})
	if err != nil || n != 0 {
		t.Fatalf("UpdateRoute(missing) = %d, %v; want 0, nil", n, err)
	}
	if rows := auditRowsFor(t, s, "route.update"); len(rows) != 0 {
		t.Errorf("audit rows for an update that matched nothing: %d", len(rows))
	}
}

// Adding the same rule twice creates one route and records one creation. The
// second call hands back the id the rule already carries, because a second id
// for one rule is a handle that deletes somebody else's rule.
func TestAddRoute_DuplicateCreatesNothingAndRecordsNothing(t *testing.T) {
	s := openMem(t)
	r := core.Route{Match: "chat_jid=telegram:1", Target: "alpha"}
	first, err := s.AddRoute(r)
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	again, err := s.AddRoute(r)
	if err != nil {
		t.Fatalf("AddRoute(duplicate): %v", err)
	}
	if again != first {
		t.Errorf("the duplicate answered id %d, want the rule's own id %d", again, first)
	}
	if rows := auditRowsFor(t, s, "route.create"); len(rows) != 1 {
		t.Errorf("%d route.create rows for one created rule", len(rows))
	}
}

func TestAddRoute_AllowsNonWebJID(t *testing.T) {
	cases := []string{
		"chat_jid=telegram:123",
		"chat_jid=slack:C0123",
		"chat_jid=discord:guild/general",
		"platform=telegram room=123",
		"chat_jid=slink:9f31507f1d5c8017",
		"chat_jid=hook:atlas/strengths/intake",
		"", // empty match — wildcard
	}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			s := openMem(t)
			if _, err := s.AddRoute(core.Route{Match: m, Target: "alpha"}); err != nil {
				t.Errorf("AddRoute(%q) err = %v, want nil", m, err)
			}
		})
	}
}

// TestRouteByID_RefusesADuplicateIDAndDeletesNothing pins BUGS L364. An id is the
// handle both the operator (`arizuko route rm`) and the agent (`delete_route`)
// address a rule by, and it is NOT unique in every live table: sloth carries six
// ids twice because the 2026-09-18 merge joined two partitions each minted from
// its own counter. routeByID used to return the FIRST match, so `rm 37` destroyed
// `main/main ← room=group/5069669817 verb=mention` and reported that it had
// removed 37, which was still there.
//
// The test seeds the collision directly, because AddRoute cannot produce one.
func TestRouteByID_RefusesADuplicateIDAndDeletesNothing(t *testing.T) {
	s := openMem(t)
	// Two rules, one id. The lower seq sorts FIRST, which is the one the old code
	// would have picked and destroyed.
	early := core.Route{ID: 37, Seq: 0, Target: "main/main", Match: "room=group/5069669817 verb=mention"}
	late := core.Route{ID: 37, Seq: 31, Target: "atlas/support#observe", Match: "room=group/1003805633088"}
	for _, r := range []core.Route{early, late} {
		key, err := routeKey(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{
			Op: objstore.Put, Entity: Routes, Key: key, Attrs: routeAttrs(r, "test", "test"),
		}}); err != nil {
			t.Fatalf("seed %q: %v", r.Target, err)
		}
	}

	if _, err := s.DeleteRoute(37); err == nil {
		t.Fatal("a duplicate id must be refused: deleting the first match destroys an arbitrary rule")
	} else if !strings.Contains(err.Error(), "names two rules") {
		t.Errorf("the refusal must say the id is ambiguous, got: %v", err)
	}

	// Both rules survive. This is the assertion that would have caught L364:
	// the old code left the table one rule short and said nothing.
	items, err := s.Obj().Query(t.Context(), objstore.Query{Entity: Routes, Partition: OwnerRoutd})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("both rules must survive a refused delete, got %d", len(items))
	}
}
