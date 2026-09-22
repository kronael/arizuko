package audit

// The guarantees the entity itself carries: the id orders by time, a recorded
// row is never overwritten, an audited write is one commit with its mutation,
// and a database still holding the table an entity replaces refuses to serve it.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
)

// rowsOf reads everything a store holds, newest first, through the method an
// operator's page reads it through.
func rowsOf(t *testing.T, s *dynstore.Store) []Row {
	t.Helper()
	rows, err := Query(context.Background(), s, Filter{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// The declared width has to be the layout's, or every write is refused rather
// than one being guarded. It is a constant here because the driver's ordered
// check needs it at init, and this is what keeps it honest.
func TestTheOrderedWidthIsTheTimestampLayout(t *testing.T) {
	if got := len(core.TS(time.Now())); got != tsWidth {
		t.Fatalf("core.TS renders %d bytes and this package declares %d", got, tsWidth)
	}
	if got := len(NewID(time.Now())); got != IDWidth {
		t.Fatalf("an id is %d bytes and IDWidth says %d", got, IDWidth)
	}
}

// Byte order is time order, which is the whole reason a string id can be the
// page cursor. RFC3339Nano would fail this: it strips trailing zeros, so a whole
// second sorts after a fraction of the same second (BUGS L97).
func TestTheIdSortsByTime(t *testing.T) {
	base := time.Date(2026, 9, 7, 5, 20, 57, 0, time.UTC)
	earlier := NewID(base)
	later := NewID(base.Add(time.Nanosecond))
	if !(earlier < later) {
		t.Fatalf("%q does not sort before %q", earlier, later)
	}
	if got := createdAt(later); got != core.TS(base.Add(time.Nanosecond)) {
		t.Fatalf("the id reads back the instant %q, want %q", got, core.TS(base.Add(time.Nanosecond)))
	}
}

// An audit log is evidence: a second write on an id that is already there must
// refuse rather than replace the row under it.
func TestARecordedRowIsNeverOverwritten(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	id := NewID(time.Now())

	ok, err := s.GuardedBatch(ctx, nil, []objstore.Write{
		writeFor(id, Event{Category: "mcp", Action: "first", Actor: "x"}, ""),
	})
	if err != nil || !ok {
		t.Fatalf("the first write did not land: ok=%v err=%v", ok, err)
	}
	ok, err = s.GuardedBatch(ctx, nil, []objstore.Write{
		writeFor(id, Event{Category: "mcp", Action: "second", Actor: "x"}, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a second write on a recorded id committed; the record is editable")
	}
	if got := only(t, s).Action; got != "first" {
		t.Fatalf("the row now reads %q, so the record was rewritten", got)
	}
}

// THE INVARIANT: the audit row commits or refuses with the mutation it records.
// Break the coupling — record in a second write, or commit the mutation without
// the row — and one of these two halves goes red.
//
// The mutation and the record ride ONE GuardedBatch, which is the shape every
// audited writer in this repo uses: `WriteFor` hands back the record's write and
// the caller appends it to its own batch.
func TestTheAuditRowIsOneCommitWithItsMutation(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	if err := s.CreateEntity(ctx, "routes"); err != nil {
		t.Fatal(err)
	}
	route := func(id string) objstore.Write {
		return objstore.Write{
			Op: objstore.Put, Entity: "routes",
			Key:   objstore.Key{Partition: "routd", Sort: id},
			Attrs: map[string]string{"target": "acme"},
		}
	}
	record := func(id string) objstore.Write {
		_, w := WriteFor(Event{
			Category: CategoryMutation, Action: "route.create", Actor: "operator:o",
			Resource: "routes/" + id,
		})
		return w
	}

	// A batch whose condition does not hold writes NEITHER half.
	refused, err := s.GuardedBatch(ctx, []objstore.Check{{
		Entity: "routes", Key: objstore.Key{Partition: "routd", Sort: "absent"},
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	}}, []objstore.Write{route("abandoned"), record("abandoned")})
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatal("a batch conditioned on an item that is not there committed")
	}
	if n := len(rowsOf(t, s)); n != 0 {
		t.Fatalf("%d audit rows survived the refusal, so the row outlived a write that "+
			"never happened", n)
	}

	committed, err := s.GuardedBatch(ctx, nil, []objstore.Write{route("kept"), record("kept")})
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("the batch was refused; it conditions on nothing")
	}
	rows := rowsOf(t, s)
	if len(rows) != 1 || rows[0].Resource != "routes/kept" {
		t.Fatalf("the commit left %d audit rows (%+v); a mutation without its record is "+
			"the unrecorded mutation this coupling exists to stop", len(rows), rows)
	}
}

// The read fills its limit past a chunk boundary and stops at the end of the
// partition, which is what makes a short page mean "nothing older" rather than
// "the read gave up".
func TestQueryFillsTheLimitAcrossChunks(t *testing.T) {
	s := openTestDB(t)
	was := scanChunk
	scanChunk = 2
	t.Cleanup(func() { scanChunk = was })

	// Twelve rows, every third one in the folder asked for, so the filter that
	// the reader applies has to survive six chunks to find four of them.
	for i := range 12 {
		e := Event{Category: "mcp", Action: fmt.Sprint(i), Actor: "x"}
		if i%3 == 0 {
			e.Folder = "acme"
		}
		seed(t, s, e)
	}
	got, err := Query(context.Background(), s, Filter{Folder: "acme", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want the whole limit of 3: %v", len(got), idsOf(got))
	}
	all, err := Query(context.Background(), s, Filter{Folder: "acme", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d rows, want the 4 in the folder", len(all))
	}
}

// The import carries the order the sequence had, including rows that shared an
// instant — which 59 of krons' 163,750 rows do.
func TestImportKeepsTheOrderTheSequenceHad(t *testing.T) {
	s := openTestDB(t)
	const instant = "2026-09-07T05:20:57.277Z"
	snap := Snapshot{Rows: []LegacyRow{
		{ID: 1, CreatedAt: instant, Category: "mcp", Action: "first", Actor: "x", Outcome: "ok"},
		{ID: 2, CreatedAt: instant, Category: "mcp", Action: "second", Actor: "x", Outcome: "ok",
			ParamsSummary: `{"folder":"acme"}`},
		{ID: 3, CreatedAt: "2026-09-07T05:20:58.000Z", Category: "mcp", Action: "third",
			Actor: "x", Outcome: "ok"},
	}}
	if n, err := CheckSnapshot(snap); err != nil || n != 3 {
		t.Fatalf("CheckSnapshot = %d, %v", n, err)
	}
	if n, err := Import(context.Background(), s, snap); err != nil || n != 3 {
		t.Fatalf("Import = %d, %v", n, err)
	}
	rows := rowsOf(t, s)
	want := []string{"third", "second", "first"}
	for i, w := range want {
		if rows[i].Action != w {
			t.Fatalf("row %d reads %q, want %q — the sequence order was lost",
				i, rows[i].Action, w)
		}
	}
	if rows[1].ParamsSummary != `{"folder":"acme"}` {
		t.Fatalf("params read back as %q, want the document the writer stored",
			rows[1].ParamsSummary)
	}
	if rows[0].CreatedAt != core.TS(mustTime(t, "2026-09-07T05:20:58.000Z")) {
		t.Fatalf("created_at reads back as %q", rows[0].CreatedAt)
	}
}

// A migration is re-runnable, so importing the same source twice leaves the
// same trail rather than a doubled one or a failure.
func TestImportIsIdempotent(t *testing.T) {
	s := openTestDB(t)
	snap := Snapshot{Rows: []LegacyRow{
		{ID: 1, CreatedAt: "2026-09-07T05:20:57.277Z", Category: "mcp", Action: "a",
			Actor: "x", Outcome: "ok"},
	}}
	for range 2 {
		if n, err := Import(context.Background(), s, snap); err != nil || n != 1 {
			t.Fatalf("Import = %d, %v", n, err)
		}
	}
	if rows := rowsOf(t, s); len(rows) != 1 || rows[0].Action != "a" {
		t.Fatalf("the trail holds %d rows after two imports of one: %+v", len(rows), rows)
	}
}

func mustTime(t *testing.T, text string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
