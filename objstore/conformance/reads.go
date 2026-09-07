package conformance

// reads.go holds the bodies about what a Query selects and how much of it comes
// back. Every one of them is derived from a statement a daemon runs today.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// messages is the entity the range and completeness bodies write: routd's
// messages, whose sort key is the timestamp its poll loop compares.
const messages = "messages"

// KeyRangeSelectsAWindowRatherThanAScan runs every sort-key operator, because
// four production reads are made of the four inequalities and the first cut of
// this interface shipped none of them.
//
// GtKey is routd's poll cursor: NewMessages is `WHERE timestamp > ? ORDER BY
// timestamp ASC` and it feeds the whole dispatch loop, so without that operator
// the loop has no shape on this interface at all. The other three carry
// history (routd/reads.go:28), bot activity (routd/db.go:1181) and the retry
// feed (routd/db.go:406); without them every history read is a whole-partition
// transfer, and the largest chat partition measured is 10,688 rows.
//
// The fixture is deliberately dense at the boundary: the bounds below are keys
// that EXIST, so an operator that confuses < with <= is off by a whole row
// rather than by nothing.
func KeyRangeSelectsAWindowRatherThanAScan(t *testing.T, newStore Factory) {
	s := newStore(t, messages)
	base := time.Date(2026, 8, 26, 19, 0, 0, 0, time.UTC)
	at := make([]string, 5)
	for i := range at {
		at[i] = core.TS(base.Add(time.Duration(i) * time.Minute))
		put(t, s, messages, objstore.Key{Partition: tenant, Sort: at[i]},
			map[string]string{"seq": fmt.Sprint(i)})
	}
	for _, c := range []struct {
		name string
		key  objstore.KeyPredicate
		want []string
	}{
		{"AnyKey", objstore.KeyPredicate{Op: objstore.AnyKey}, at},
		{"ExactKey", objstore.KeyPredicate{Op: objstore.ExactKey, Value: at[2]}, at[2:3]},
		{"LtKey", objstore.KeyPredicate{Op: objstore.LtKey, Value: at[2]}, at[:2]},
		{"LeKey", objstore.KeyPredicate{Op: objstore.LeKey, Value: at[2]}, at[:3]},
		{"GtKey", objstore.KeyPredicate{Op: objstore.GtKey, Value: at[2]}, at[3:]},
		{"GeKey", objstore.KeyPredicate{Op: objstore.GeKey, Value: at[2]}, at[2:]},
	} {
		got := s2keys(t, s, objstore.Query{Entity: messages, Partition: tenant, Key: c.key})
		if !sameOrder(got, c.want) {
			t.Errorf("%s selected %v, want %v", c.name, got, c.want)
		}
	}

	// The cursor loop, run as routd runs it: read after the watermark, take the
	// last key as the new watermark, and stop when nothing new arrives. A `>`
	// that were a `>=` replays its last row forever, which is a loop that never
	// idles rather than a wrong answer.
	since := at[0]
	seen := []string{}
	for {
		got := s2keys(t, s, objstore.Query{Entity: messages, Partition: tenant,
			Key: objstore.KeyPredicate{Op: objstore.GtKey, Value: since}, Limit: 2})
		if len(got) == 0 {
			break
		}
		seen = append(seen, got...)
		since = got[len(got)-1]
	}
	if !sameOrder(seen, at[1:]) {
		t.Errorf("the cursor loop read %v, want %v", seen, at[1:])
	}

	// A separator belongs to a subtree read, and a predicate carrying one it has
	// no use for is a caller mistake, not something to ignore.
	if _, err := s.Query(context.Background(), objstore.Query{
		Entity: messages, Partition: tenant,
		Key: objstore.KeyPredicate{Op: objstore.GtKey, Value: at[0], Sep: "/"},
	}); err == nil {
		t.Error("a range predicate carrying a subtree separator was accepted")
	}
}

// FilterNarrowsWhatTheKeyAlreadySelected exercises Where, which NO body in the
// first cut of this suite used — so every claim the interface made about
// attribute filtering was untested on both engines.
//
// A filter narrows and never widens, and it runs on the item's TEXT, so an
// ordered comparison means what the writer's layout means. Absent and Present
// are the two that have no SQL twin a caller would guess: `used_at IS NULL` is
// "the attribute is not there", because absent is the only null.
func FilterNarrowsWhatTheKeyAlreadySelected(t *testing.T, newStore Factory) {
	s := newStore(t, tokens)
	for _, it := range []struct {
		sort  string
		attrs map[string]string
	}{
		{"tok/a", map[string]string{"family": "f1", "sub": "alice", "rank": "10"}},
		{"tok/b", map[string]string{"family": "f1", "sub": "bob", "rank": "20", "used_at": "T"}},
		{"tok/c", map[string]string{"family": "f2", "sub": "carol", "rank": "30"}},
		{"tok/d", map[string]string{"family": "f2", "sub": "dan", "rank": "40", "used_at": "T"}},
	} {
		put(t, s, tokens, objstore.Key{Partition: tenant, Sort: it.sort}, it.attrs)
	}
	for _, c := range []struct {
		name  string
		where []objstore.Cmp
		want  []string
	}{
		{"Eq", []objstore.Cmp{{Attr: "family", Op: objstore.Eq, Value: "f1"}},
			[]string{"tok/a", "tok/b"}},
		{"Ne", []objstore.Cmp{{Attr: "family", Op: objstore.Ne, Value: "f1"}},
			[]string{"tok/c", "tok/d"}},
		{"Lt", []objstore.Cmp{{Attr: "rank", Op: objstore.Lt, Value: "30"}},
			[]string{"tok/a", "tok/b"}},
		{"Le", []objstore.Cmp{{Attr: "rank", Op: objstore.Le, Value: "30"}},
			[]string{"tok/a", "tok/b", "tok/c"}},
		{"Gt", []objstore.Cmp{{Attr: "rank", Op: objstore.Gt, Value: "30"}},
			[]string{"tok/d"}},
		{"Ge", []objstore.Cmp{{Attr: "rank", Op: objstore.Ge, Value: "30"}},
			[]string{"tok/c", "tok/d"}},
		{"Absent", []objstore.Cmp{{Attr: "used_at", Op: objstore.Absent}},
			[]string{"tok/a", "tok/c"}},
		{"Present", []objstore.Cmp{{Attr: "used_at", Op: objstore.Present}},
			[]string{"tok/b", "tok/d"}},
		{"AbsentOnAnAttributeNobodyWrote", []objstore.Cmp{{Attr: "revoked_at", Op: objstore.Absent}},
			[]string{"tok/a", "tok/b", "tok/c", "tok/d"}},
		{"twoConjuncts", []objstore.Cmp{
			{Attr: "family", Op: objstore.Eq, Value: "f2"},
			{Attr: "used_at", Op: objstore.Absent}}, []string{"tok/c"}},
		{"nothingMatches", []objstore.Cmp{{Attr: "family", Op: objstore.Eq, Value: "f9"}}, nil},
	} {
		got := s2keys(t, s, objstore.Query{Entity: tokens, Partition: tenant, Where: c.where})
		if !sameOrder(got, c.want) {
			t.Errorf("filter %s selected %v, want %v", c.name, got, c.want)
		}
	}

	// A filter never widens what the key selected. The key here takes one item
	// and the filter matches four, so a driver that ORed them would return more
	// than one.
	got := s2keys(t, s, objstore.Query{Entity: tokens, Partition: tenant,
		Key:   objstore.KeyPredicate{Op: objstore.ExactKey, Value: "tok/a"},
		Where: []objstore.Cmp{{Attr: "revoked_at", Op: objstore.Absent}}})
	if !sameOrder(got, []string{"tok/a"}) {
		t.Errorf("a filter over a point read selected %v, want only tok/a", got)
	}

	// A filter and a projection are independent: SigningKeyMetadata filters on
	// what it does not return, and a driver that could only compare what it
	// projected would put the private half on the wire to answer the question.
	items, err := s.Query(context.Background(), objstore.Query{
		Entity: tokens, Partition: tenant, Project: []string{"sub"},
		Where: []objstore.Cmp{{Attr: "family", Op: objstore.Eq, Value: "f2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameOrder(keysOf(items), []string{"tok/c", "tok/d"}) {
		t.Errorf("a projected filtered read selected %v, want tok/c and tok/d", keysOf(items))
	}
	for _, it := range items {
		if _, leaked := it.Attrs["family"]; leaked {
			t.Errorf("the attribute the filter compared came back unprojected: %v", it.Attrs)
		}
	}
}

// bigSelectionBytes is the size of one item's body in the completeness body, and
// how many of them are written.
//
// The numbers are chosen so the fixture is LARGER THAN A PAGE on the engine
// whose page is smallest: DynamoDB's is 1 MB — "A single Query operation can
// retrieve a maximum of 1 MB of data. This limit applies before any
// FilterExpression or ProjectionExpression is applied to the results"
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.KeyConditionExpressions.html
// — and 300 items of about 4 KB is a little over 1.2 MB, one full page and a
// remainder. A driver that answered a page and called it the selection fails
// here; at the 13 items the first suite used, it could not.
const (
	bigBodyBytes = 4000
	bigItems     = 300
)

// bigLog writes bigItems items whose sort key is a fixed-width instant, and
// returns their keys in ascending order. The last five carry a `tail`
// attribute, so a filtered read of them matches only at the END of the
// selection.
func bigLog(t *testing.T, s objstore.Store, entity string) []string {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	body := strings.Repeat("x", bigBodyBytes)
	items := make([]objstore.Item, bigItems)
	keys := make([]string, bigItems)
	for i := range items {
		keys[i] = core.TS(base.Add(time.Duration(i) * time.Second))
		attrs := map[string]string{"body": body, "seq": fmt.Sprintf("%04d", i)}
		if i >= bigItems-5 {
			attrs["tail"] = "1"
		}
		items[i] = objstore.Item{Key: objstore.Key{Partition: tenant, Sort: keys[i]}, Attrs: attrs}
	}
	putAll(t, s, entity, items)
	return keys
}

// AReadReturnsTheWholeSelection is the completeness contract, at a size where a
// driver that pages can get it wrong.
//
// A read answers the COMPLETE selection. An engine that caps a single read —
// DynamoDB at 1 MB, measured before the filter — walks its own cursor to
// produce that; the cap is the driver's problem and never the caller's. The
// divergence this body catches is silent on both engines at any fixture small
// enough to be quick, which is why the fixture is not small.
//
// The filtered half is the sharper one, and it is the shape the cap makes
// dangerous: the five matching items are at the END of a selection larger than
// one page, so a driver that stopped after its first internal page returns
// NOTHING and looks exactly like an empty selection. authd is the daemon that
// would have paid for it — authstore.Partition is the constant "authd", one
// partition for the whole daemon, so every read it makes is a point read or a
// whole-partition scan.
//
// The partition is a boundary and not a hint, which is the other half: a read
// names one partition and cannot see another's items, whatever their sort keys.
// That is what makes "one partition per daemon" a capacity decision rather than
// an isolation one.
func AReadReturnsTheWholeSelection(t *testing.T, newStore Factory) {
	s := newStore(t, messages)
	keys := bigLog(t, s, messages)
	for _, k := range keys[:3] {
		put(t, s, messages, objstore.Key{Partition: "other", Sort: k},
			map[string]string{"seq": "elsewhere"})
	}

	whole := s2keys(t, s, objstore.Query{Entity: messages, Partition: tenant})
	if !sameOrder(whole, keys) {
		t.Fatalf("a read of %d items of about %d bytes returned %d keys; the whole selection "+
			"is the answer, and a driver that stopped on its own page bound would return "+
			"a prefix of it and look complete", bigItems, bigBodyBytes, len(whole))
	}

	tail := s2keys(t, s, objstore.Query{Entity: messages, Partition: tenant,
		Where: []objstore.Cmp{{Attr: "tail", Op: objstore.Present}}})
	if !sameOrder(tail, keys[bigItems-5:]) {
		t.Errorf("a filtered read whose matches are at the END of a %d-item selection returned "+
			"%v, want the last five keys; a driver that stopped after one internal page "+
			"answers this with nothing, which reads as an empty selection", bigItems, tail)
	}

	elsewhere := s2keys(t, s, objstore.Query{Entity: messages, Partition: "other"})
	if !sameOrder(elsewhere, keys[:3]) {
		t.Errorf("the other partition holds %v, want %v", elsewhere, keys[:3])
	}
}

// LimitBoundsWhatTheReadReturns pins the caller's own bound, which is the only
// one there is now that a read answers the whole selection.
//
// It counts the items RETURNED, after the filter, because that is what a caller
// asking for "the newest three" means and the only reading a caller can act on:
// there is no cursor to continue with, so a bound on what was EXAMINED would
// hand back fewer items than asked for with no way to ask again. An engine that
// caps its own reads keeps reading until it has this many or the selection runs
// out — the same internal walk completeness already requires of it.
func LimitBoundsWhatTheReadReturns(t *testing.T, newStore Factory) {
	s := newStore(t, tokens)
	for i := 0; i < 10; i++ {
		attrs := map[string]string{"seq": fmt.Sprintf("%02d", i)}
		if i >= 8 {
			attrs["tail"] = "1"
		}
		put(t, s, tokens, objstore.Key{Partition: tenant, Sort: fmt.Sprintf("tok/%02d", i)}, attrs)
	}
	if got := s2keys(t, s, objstore.Query{Entity: tokens, Partition: tenant, Limit: 3}); !sameOrder(
		got, []string{"tok/00", "tok/01", "tok/02"}) {
		t.Errorf("a Limit of 3 returned %v, want the first three", got)
	}

	// The two items the filter keeps are the LAST two, so a Limit of 3 that
	// counted what the read examined would return nothing at all.
	got := s2keys(t, s, objstore.Query{Entity: tokens, Partition: tenant, Limit: 3,
		Where: []objstore.Cmp{{Attr: "tail", Op: objstore.Present}}})
	if !sameOrder(got, []string{"tok/08", "tok/09"}) {
		t.Errorf("a filtered read under a Limit of 3 returned %v, want the two matching items; "+
			"a bound on what was EXAMINED answers this with nothing and gives the caller "+
			"no way to ask again", got)
	}

	// A Limit larger than the selection is not a bound at all.
	if end := s2keys(t, s, objstore.Query{Entity: tokens, Partition: tenant, Limit: 100}); len(end) != 10 {
		t.Errorf("a Limit of 100 over 10 items returned %d", len(end))
	}

	// Descending plus a Limit is the newest-N read, which is what a history page
	// asks for. The direction has to reach the bound, or the newest read returns
	// the oldest rows.
	if newest := s2keys(t, s, objstore.Query{Entity: tokens, Partition: tenant,
		Descending: true, Limit: 2}); !sameOrder(newest, []string{"tok/09", "tok/08"}) {
		t.Errorf("the newest two are %v, want tok/09 then tok/08", newest)
	}
}
