package conformance

// reads.go holds the bodies about what a Query selects and how much of it comes
// back. Every one of them is derived from a statement a daemon runs today, and
// three of them exist because the first cut of the suite could not have caught
// the thing they test: no body used Where at all, one used Limit without a
// filter, and the largest fixture was 13 items — a size at which two engines
// cannot disagree about a page.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// messages is the entity the range and paging bodies write: routd's messages,
// whose sort key is the timestamp its poll loop compares.
const messages = "messages"

// KeyRangeSelectsAWindowRatherThanAScan runs every sort-key operator, because
// the first cut of this interface shipped two of DynamoDB's seven and the five
// it left out are the ones arizuko's reads are made of.
//
// GtKey is routd's poll cursor: NewMessages is `WHERE timestamp > ? ORDER BY
// timestamp ASC` and it feeds the whole dispatch loop, so without that operator
// the loop has no shape on this interface at all. BetweenKey is the time window
// — the shape a retention sweep and an audit export both ask for — and it is
// inclusive at both ends on every engine, which is where an off-by-one costs a
// row.
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
		{"BetweenKey", objstore.KeyPredicate{Op: objstore.BetweenKey, Value: at[1], Upper: at[3]}, at[1:4]},
		{"BetweenKeyOnOneKey", objstore.KeyPredicate{Op: objstore.BetweenKey, Value: at[2], Upper: at[2]}, at[2:3]},
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

	// An inverted range is refused rather than answered with nothing: an empty
	// answer to a swapped pair reads as "no data", which sends the reader to
	// investigate the wrong thing.
	if _, err := s.Query(context.Background(), objstore.Query{
		Entity: messages, Partition: tenant,
		Key: objstore.KeyPredicate{Op: objstore.BetweenKey, Value: at[3], Upper: at[1]},
	}); err == nil {
		t.Error("a BETWEEN whose lower bound is above its upper was accepted")
	}
	// A separator belongs to a subtree read and an upper bound to a range, and a
	// predicate carrying one it has no use for is a caller mistake, not
	// something to ignore.
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
	items, err := objstore.QueryAll(context.Background(), s, objstore.Query{
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

// pageFixture is the size of one item's body in the paging bodies, and how many
// of them are written.
//
// The numbers are chosen from MaxPageBytes so the fixture PAGES rather than so
// it is large: 300 items of about 4 KB is a little over 1.2 MB, which is one
// full page and a remainder. A body written at the 13 items the first suite used
// could not distinguish a driver that pages from one that returns everything —
// which is exactly the divergence a key-value driver would introduce and no
// existing body would catch.
const (
	pageBodyBytes = 4000
	pageItems     = 300
)

// pagedLog writes pageItems items whose sort key is a fixed-width instant, and
// returns their keys in ascending order.
func pagedLog(t *testing.T, s objstore.Store, entity string) []string {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	body := strings.Repeat("x", pageBodyBytes)
	items := make([]objstore.Item, pageItems)
	keys := make([]string, pageItems)
	for i := range items {
		keys[i] = core.TS(base.Add(time.Duration(i) * time.Second))
		attrs := map[string]string{"body": body, "seq": fmt.Sprintf("%04d", i)}
		if i >= pageItems-5 {
			attrs["tail"] = "1"
		}
		items[i] = objstore.Item{Key: objstore.Key{Partition: tenant, Sort: keys[i]}, Attrs: attrs}
	}
	putAll(t, s, entity, items)
	return keys
}

// PagingIsBoundedBeforeTheFilter is the body the first suite could not have
// written, and the divergence it catches is silent on both engines at any
// fixture small enough to be quick.
//
// A read of more than MaxPageBytes comes back PARTIAL with a cursor, because
// that is what the engine this interface is measured against does. And the bound
// applies to the items EXAMINED, before the filter narrows them, which has a
// consequence a SQL driver would never produce on its own: a page can return
// ZERO items and still have more to come. A driver that put the filter in a
// WHERE clause would answer this read completely in one page, a caller would
// depend on that, and the dependency would break on the other engine with no
// error anywhere.
func PagingIsBoundedBeforeTheFilter(t *testing.T, newStore Factory) {
	s := newStore(t, messages)
	keys := pagedLog(t, s, messages)

	first := page(t, s, objstore.Query{Entity: messages, Partition: tenant})
	if first.More == "" {
		t.Fatalf("a read of %d items of about %d bytes came back whole; a page is bounded at "+
			"%d bytes and a driver that returns everything is offering completeness the "+
			"engine it is modelled on does not give", pageItems, pageBodyBytes, objstore.MaxPageBytes)
	}
	if len(first.Items) == 0 || len(first.Items) >= pageItems {
		t.Fatalf("the first page carried %d of %d items", len(first.Items), pageItems)
	}
	if first.More != first.Items[len(first.Items)-1].Key.Sort {
		t.Errorf("the cursor is %q and the last item is %q; a cursor that is not the last item "+
			"examined either replays a row or skips one",
			first.More, first.Items[len(first.Items)-1].Key.Sort)
	}

	// Walking the cursor reads the selection exactly once: no row twice, no row
	// missed, and the order preserved across the boundary.
	whole := s2keys(t, s, objstore.Query{Entity: messages, Partition: tenant})
	if !sameOrder(whole, keys) {
		t.Fatalf("the paged walk read %d keys, want %d, in order", len(whole), len(keys))
	}

	// The filter matches the last five items only, so every page before the last
	// returns NOTHING and still has a cursor. That is the assertion: a page with
	// no items is not an empty selection.
	tail := objstore.Query{Entity: messages, Partition: tenant,
		Where: []objstore.Cmp{{Attr: "tail", Op: objstore.Present}}}
	firstFiltered := page(t, s, tail)
	if firstFiltered.More == "" {
		t.Fatalf("a filtered read of %d items came back whole; the page bound applies to what "+
			"the read EXAMINED, before the filter, so this read has to page", pageItems)
	}
	if len(firstFiltered.Items) != 0 {
		t.Errorf("the first filtered page returned %d items; the five that match are at the "+
			"END of the selection, so a page that returns them applied the filter BEFORE the "+
			"bound and would page differently on a key-value engine", len(firstFiltered.Items))
	}
	if firstFiltered.Examined != first.Examined {
		t.Errorf("the filtered read examined %d items and the unfiltered one %d; the bound is "+
			"on what was examined, so a filter must not change how far a page reaches",
			firstFiltered.Examined, first.Examined)
	}
	if got := s2keys(t, s, tail); !sameOrder(got, keys[pageItems-5:]) {
		t.Errorf("the paged filtered walk read %v, want the last five keys", got)
	}
}

// LimitBoundsWhatTheReadExaminesRatherThanWhatItReturns pins the other half of
// the page boundary, and it is the half a SQL driver gets wrong by default:
// SQL's LIMIT counts the rows a WHERE already kept.
//
// DynamoDB counts the rows READ — "Query applies the 1 MB/Limit page cap to the
// items it reads before applying the filter, so a page can return zero matching
// items and still include a LastEvaluatedKey". A driver that capped the returned
// rows would answer a filtered read completely on one engine and partially on
// the other, at any fixture size at all.
func LimitBoundsWhatTheReadExaminesRatherThanWhatItReturns(t *testing.T, newStore Factory) {
	s := newStore(t, tokens)
	for i := 0; i < 10; i++ {
		attrs := map[string]string{"seq": fmt.Sprintf("%02d", i)}
		if i >= 8 {
			attrs["tail"] = "1"
		}
		put(t, s, tokens, objstore.Key{Partition: tenant, Sort: fmt.Sprintf("tok/%02d", i)}, attrs)
	}
	got := page(t, s, objstore.Query{Entity: tokens, Partition: tenant, Limit: 3,
		Where: []objstore.Cmp{{Attr: "tail", Op: objstore.Present}}})
	if got.Examined != 3 {
		t.Errorf("a Limit of 3 examined %d items", got.Examined)
	}
	if len(got.Items) != 0 {
		t.Errorf("a Limit of 3 returned %d items; the three it may examine are the first "+
			"three, none of which the filter keeps", len(got.Items))
	}
	if got.More != "tok/02" {
		t.Errorf("the cursor after a Limit of 3 is %q, want tok/02; a read that stopped on "+
			"its Limit has more to come and must say so", got.More)
	}

	// A Limit larger than the selection is not a page boundary, so the read ends
	// and says so — otherwise every caller pages one round trip too many, and a
	// caller that trusted a cursor to mean "more data" would loop.
	if end := page(t, s, objstore.Query{Entity: tokens, Partition: tenant, Limit: 100}); end.More != "" {
		t.Errorf("a read that exhausted its selection returned the cursor %q", end.More)
	}

	// Descending is the direction the newest-row read asks for, and the cursor
	// has to follow it: an ascending resume would replay the page it just
	// returned, forever.
	newest := page(t, s, objstore.Query{Entity: tokens, Partition: tenant,
		Descending: true, Limit: 1})
	if !sameOrder(keysOf(newest.Items), []string{"tok/09"}) {
		t.Fatalf("the newest page is %v, want tok/09", keysOf(newest.Items))
	}
	next := page(t, s, objstore.Query{Entity: tokens, Partition: tenant,
		Descending: true, Limit: 1, After: newest.More})
	if !sameOrder(keysOf(next.Items), []string{"tok/08"}) {
		t.Errorf("resuming a descending read gave %v, want tok/08", keysOf(next.Items))
	}
}

// WholePartitionScanPagesRatherThanReturningEverything is the body authd would
// have failed, and it is here because the first daemon on this interface made
// the partition-key rule vacuous: authstore.Partition is the constant "authd",
// one partition for the whole daemon, so every read it makes is a point read or
// a whole-partition scan sorted in Go.
//
// That is safe at 78 refresh tokens and silently wrong at the first partition
// that outgrows a page: a scan-and-sort-in-Go returns the first page and calls
// it the answer. So a whole-partition read is a PAGED WALK, and this body says
// so at a size where the difference is visible.
//
// The other half is that the partition is a boundary and not a hint. A read
// names one partition and cannot see another's items, whatever their sort keys —
// which is what makes "one partition per daemon" a capacity decision rather than
// an isolation one.
func WholePartitionScanPagesRatherThanReturningEverything(t *testing.T, newStore Factory) {
	s := newStore(t, messages)
	keys := pagedLog(t, s, messages)
	for _, k := range keys[:3] {
		put(t, s, messages, objstore.Key{Partition: "other", Sort: k},
			map[string]string{"seq": "elsewhere"})
	}

	scan := page(t, s, objstore.Query{Entity: messages, Partition: tenant})
	if scan.More == "" {
		t.Errorf("a whole-partition read of %d items came back whole; a daemon that sorts a "+
			"scan in Go then believes the first page is the answer", pageItems)
	}
	if walked := s2keys(t, s, objstore.Query{Entity: messages, Partition: tenant}); !sameOrder(walked, keys) {
		t.Errorf("the walked partition holds %d items, want %d", len(walked), len(keys))
	}
	for _, it := range scan.Items {
		if it.Key.Partition != tenant {
			t.Fatalf("a read of partition %q returned an item from %q", tenant, it.Key.Partition)
		}
	}
	elsewhere := s2keys(t, s, objstore.Query{Entity: messages, Partition: "other"})
	if !sameOrder(elsewhere, keys[:3]) {
		t.Errorf("the other partition holds %v, want %v", elsewhere, keys[:3])
	}
}
