package store

// coordinates_test.go pins the two rules that make a composite coordinate safe,
// on THIS package's writers.
//
// resreg has its own copies of both and its own tests for them, and those tests
// do not reach here: `resreg/entity.go` renders an item from a manifest row and
// the files beside this one render one from a daemon's call, so breaking either
// guard leaves the other's tests green. That is what a mutation showed — the
// separator refusal and the padded ordering were both mutable in `store` with
// every package still passing.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// TestCoordinateRefusesTheSeparatorInAValue: a write whose value carries the
// byte that joins the coordinate is REFUSED, not joined.
//
// Joined, two different rows address one item: the last write wins and the read
// that splits the key apart answers a tuple neither row states. The write is the
// only end that can see it — the read gets a key that splits cleanly into the
// wrong parts.
func TestCoordinateRefusesTheSeparatorInAValue(t *testing.T) {
	s := openEntities(t)
	bad := "corp" + KeySep + "eng"

	t.Run("acl", func(t *testing.T) {
		err := s.PutACLRow(core.ACLRow{Principal: bad, Action: "mcp:send", Scope: "**"})
		requireSeparatorRefusal(t, err)
	})
	t.Run("acl_membership", func(t *testing.T) {
		requireSeparatorRefusal(t, s.PutMembership("folder:"+bad, RoleMember, "test"))
	})
	t.Run("network_rules", func(t *testing.T) {
		requireSeparatorRefusal(t, s.AddNetworkRule(bad, "example.com", "test"))
	})
	t.Run("routes", func(t *testing.T) {
		_, err := s.PutRouteRow(core.Route{Seq: 0, Target: bad, Match: "*"})
		requireSeparatorRefusal(t, err)
	})
	t.Run("secrets", func(t *testing.T) {
		// The keyring first: storeValue refuses a plaintext write before the
		// coordinate is ever rendered, so without it this would pass on the
		// wrong refusal.
		sealed := openEntities(t)
		sealed.SetSecretKeys([]byte("coordinate-test-key"))
		requireSeparatorRefusal(t, sealed.SetSecret(SecretScopeFolder, bad, "API_KEY", "v"))
	})
}

// requireSeparatorRefusal insists the write failed AND said why. A refusal with
// some other reason would pass a bare `err != nil` while the coordinate went in
// split.
func requireSeparatorRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the write joined a value carrying the separator, so two rows now " +
			"address one item and the last one wins")
	}
	if !strings.Contains(err.Error(), "separat") && !strings.Contains(err.Error(), "coordinate") {
		t.Errorf("err = %v, want it to name the separator or the coordinate", err)
	}
}

// TestRouteSeqSortsNumerically: the route table reads back in precedence order.
//
// A sort key is compared as TEXT, so an unpadded number puts 10 before 2 — and
// the whole meaning of the table is "lower seq wins, first match". The read
// sorts by (seq, id) afterwards, so this asserts on the KEY: the items come out
// of the driver in the order the coordinate puts them, and a wrong rendering
// there is a wrong answer everywhere the raw order is used.
func TestRouteSeqSortsNumerically(t *testing.T) {
	s := openEntities(t)
	for _, seq := range []int{100, 2, 10, 0} {
		if _, err := s.PutRouteRow(core.Route{Seq: seq, Target: "main", Match: "s"}); err != nil {
			t.Fatalf("seed seq %d: %v", seq, err)
		}
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Routes, Partition: OwnerRoutd,
	})
	if err != nil {
		t.Fatalf("read the route items: %v", err)
	}
	var keys []string
	var seqs []int
	for _, it := range items {
		keys = append(keys, it.Key.Sort)
		n, aerr := strconv.Atoi(strings.SplitN(it.Key.Sort, KeySep, 2)[0])
		if aerr != nil {
			t.Fatalf("route key %q carries a seq that is not a number", it.Key.Sort)
		}
		seqs = append(seqs, n)
	}
	want := []int{0, 2, 10, 100}
	if len(seqs) != len(want) {
		t.Fatalf("read %d routes, want %d: %v", len(seqs), len(want), keys)
	}
	for i, n := range want {
		if seqs[i] != n {
			t.Fatalf("coordinate order = %v, want %v — the key is compared as text, so "+
				"the number has to be rendered at a fixed width", seqs, want)
		}
	}
}

// TestRouteSeqRefusesWhatItCannotOrder: a value the padding cannot order is
// refused rather than written, at the write, which is the only end that can see
// it.
func TestRouteSeqRefusesWhatItCannotOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		seq  int
		want string
	}{
		{"negative", -1, "is negative"},
		{"wider than the padding", 12345678901, "wider than the 10 digits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := openEntities(t).PutRouteRow(core.Route{Seq: tc.seq, Target: "main", Match: "s"})
			if err == nil {
				t.Fatal("the write rendered a seq that does not sort in numeric order")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
