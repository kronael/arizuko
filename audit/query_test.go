package audit

import (
	"context"
	"database/sql"
	"testing"
)

// Query is the read side of the compliance record: dashd's audit page, the
// /v1/audit resource and every operator answer to "who did this" go through
// it. It had no test. Its folder predicate is a tenant boundary — a row from
// another tenant's folder appearing here is a cross-tenant disclosure of who
// did what, where.

func seed(t *testing.T, db *sql.DB, rows ...Event) []int64 {
	t.Helper()
	var ids []int64
	for _, e := range rows {
		id, err := EmitDB(context.Background(), db, e)
		if err != nil {
			t.Fatalf("EmitDB: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// A NULL folder — an instance-scope row — scans as the empty string.
func folders(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Folder == "" {
			out = append(out, "<null>")
			continue
		}
		out = append(out, r.Folder)
	}
	return out
}

func has(rows []Row, folder string) bool {
	for _, f := range folders(rows) {
		if f == folder {
			return true
		}
	}
	return false
}

// The filter matches the folder itself and its subtree, and nothing else. A
// sibling whose name merely starts with the same characters is a different
// tenant.
func TestQueryFolderFilterIsSubtreeContainment(t *testing.T) {
	db := openTestDB(t)
	seed(t, db,
		Event{Category: "mcp", Action: "a", Actor: "x", Folder: "acme"},
		Event{Category: "mcp", Action: "b", Actor: "x", Folder: "acme/eng"},
		Event{Category: "mcp", Action: "c", Actor: "x", Folder: "acme/eng/sre"},
		Event{Category: "mcp", Action: "d", Actor: "x", Folder: "acmecorp"},
		Event{Category: "mcp", Action: "e", Actor: "x", Folder: "acme-evil"},
		Event{Category: "mcp", Action: "f", Actor: "x", Folder: "other"},
		Event{Category: "mcp", Action: "g", Actor: "x"}, // instance scope, folder NULL
	)

	got, err := Query(context.Background(), db, Filter{Folder: "acme"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, want := range []string{"acme", "acme/eng", "acme/eng/sre"} {
		if !has(got, want) {
			t.Errorf("the subtree row %q is missing: %v", want, folders(got))
		}
	}
	for _, leaked := range []string{"acmecorp", "acme-evil", "other", "<null>"} {
		if has(got, leaked) {
			t.Errorf("folder=acme returned %q — another tenant's row: %v", leaked, folders(got))
		}
	}
	if n := len(got); n != 3 {
		t.Errorf("got %d rows, want exactly the 3 in the subtree: %v", n, folders(got))
	}

	// A deeper filter narrows further and never widens.
	got, err = Query(context.Background(), db, Filter{Folder: "acme/eng"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || has(got, "acme") {
		t.Errorf("folder=acme/eng returned %v, want only eng and below", folders(got))
	}
}

// An instance-scope row has folder NULL. Surfacing it to a tenant because the
// column is NULL is the same leak in the other direction.
func TestQueryFolderFilterExcludesInstanceScopeRows(t *testing.T) {
	db := openTestDB(t)
	seed(t, db,
		Event{Category: "cli", Action: "restart", Actor: "root"},
		Event{Category: "mcp", Action: "reply", Actor: "agent", Folder: "acme"},
	)
	got, err := Query(context.Background(), db, Filter{Folder: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || has(got, "<null>") {
		t.Errorf("a folder-scoped query returned an instance-scope row: %v", folders(got))
	}

	// With no folder filter the operator sees everything, including those rows.
	all, err := Query(context.Background(), db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("unfiltered query returned %d rows, want both", len(all))
	}
}

// BUGS L122: the folder predicate is built by concatenating the caller's
// folder into a SQL LIKE, and `_` and `%` are LIKE metacharacters while
// isValidFolder permits both. groupfolder.SQLSubtree is the exact,
// case-sensitive, metacharacter-free twin the cascades already use; six read
// paths including this one never adopted it.
//
// Skipped rather than deleted: the entry is open and this is the assertion its
// fix has to satisfy. Un-skip with the fix.
func TestQueryFolderFilterIsNotWidenedByLikeMetacharacters(t *testing.T) {
	t.Skip("BUGS L122 — open: the folder filter is a LIKE, so `_` and `%` widen it")

	db := openTestDB(t)
	seed(t, db,
		Event{Category: "mcp", Action: "a", Actor: "x", Folder: "a_b/one"},
		Event{Category: "mcp", Action: "b", Actor: "x", Folder: "aXb/two"},
	)
	got, err := Query(context.Background(), db, Filter{Folder: "a_b"})
	if err != nil {
		t.Fatal(err)
	}
	if has(got, "aXb/two") {
		t.Errorf("an underscore in the folder acted as a LIKE wildcard: %v", folders(got))
	}
}

func TestQueryFiltersByCategoryAndActor(t *testing.T) {
	db := openTestDB(t)
	seed(t, db,
		Event{Category: "mcp", Action: "reply", Actor: "user:alice", Folder: "f"},
		Event{Category: "cli", Action: "group-add", Actor: "user:alice", Folder: "f"},
		Event{Category: "mcp", Action: "reply", Actor: "user:bob", Folder: "f"},
	)
	ctx := context.Background()

	got, err := Query(ctx, db, Filter{Category: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("category=mcp returned %d rows, want 2", len(got))
	}
	for _, r := range got {
		if r.Category != "mcp" {
			t.Errorf("category filter returned a %q row", r.Category)
		}
	}

	// Actor is a substring search — an operator types a name, not a full sub.
	got, err = Query(ctx, db, Filter{Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("actor=alice returned %d rows, want 2", len(got))
	}

	// The filters combine rather than replace one another.
	got, err = Query(ctx, db, Filter{Category: "mcp", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Actor != "user:alice" || got[0].Category != "mcp" {
		t.Errorf("combined filter returned %d rows: %+v", len(got), got)
	}
}

// Ordering and paging are both on id, never created_at: within one table the
// two are co-monotonic, and id is the one that admits an exact cursor.
// created_at has millisecond resolution, so several rows in one insert burst
// share it and a created_at cursor would skip or repeat them.
func TestQueryOrdersNewestFirstAndPagesOnID(t *testing.T) {
	db := openTestDB(t)
	ids := seed(t, db,
		Event{Category: "mcp", Action: "1", Actor: "x"},
		Event{Category: "mcp", Action: "2", Actor: "x"},
		Event{Category: "mcp", Action: "3", Actor: "x"},
		Event{Category: "mcp", Action: "4", Actor: "x"},
		Event{Category: "mcp", Action: "5", Actor: "x"},
	)
	ctx := context.Background()

	page1, err := Query(ctx, db, Filter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || page1[0].ID != ids[4] || page1[1].ID != ids[3] {
		t.Fatalf("page 1 = %v, want the two newest ids descending", idsOf(page1))
	}

	page2, err := Query(ctx, db, Filter{Limit: 2, BeforeID: page1[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID != ids[2] || page2[1].ID != ids[1] {
		t.Fatalf("page 2 = %v, want the next two", idsOf(page2))
	}

	// No row appears on two pages, and none is skipped.
	seen := map[int64]bool{}
	for _, r := range append(page1, page2...) {
		if seen[r.ID] {
			t.Errorf("row %d appeared on two pages", r.ID)
		}
		seen[r.ID] = true
	}
}

func idsOf(rows []Row) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// A caller-supplied limit is clamped both ways. Zero must not mean "no limit"
// — an unbounded audit query on a live instance reads the whole table into
// memory.
func TestQueryClampsTheLimit(t *testing.T) {
	db := openTestDB(t)
	for i := 0; i < DefaultLimit+5; i++ {
		seed(t, db, Event{Category: "mcp", Action: "x", Actor: "x"})
	}
	ctx := context.Background()

	for name, f := range map[string]Filter{
		"zero":     {Limit: 0},
		"negative": {Limit: -1},
	} {
		got, err := Query(ctx, db, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != DefaultLimit {
			t.Errorf("%s limit returned %d rows, want the default %d", name, len(got), DefaultLimit)
		}
	}

	got, err := Query(ctx, db, Filter{Limit: MaxLimit + 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxLimit {
		t.Errorf("returned %d rows, want at most MaxLimit %d", len(got), MaxLimit)
	}
}

func TestQueryReturnsAnEmptySliceNotNil(t *testing.T) {
	db := openTestDB(t)
	got, err := Query(context.Background(), db, Filter{Folder: "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Error("Query returned nil; a JSON encoder writes null instead of []")
	}
	if len(got) != 0 {
		t.Errorf("got %d rows for an unknown folder", len(got))
	}
}

// EmitDB is the no-package-state writer daemons with their own handle use. It
// must never write through a nil handle silently: an audit call that logs
// nothing and reports success is a hole in the record.
func TestEmitDBRefusesANilHandle(t *testing.T) {
	if _, err := EmitDB(context.Background(), nil, Event{Category: "mcp", Action: "x", Actor: "y"}); err == nil {
		t.Fatal("EmitDB(nil) reported success; the event vanished")
	}
}

// The instance stamp comes from package state when the event does not carry
// one, so a row written by a daemon that called Init is attributable without
// every call site repeating it. An explicit instance always wins.
func TestEmitDBStampsTheInstance(t *testing.T) {
	db := openTestDB(t)
	resetInit(t)
	logMu.Lock()
	logInstance = "krons"
	logMu.Unlock()

	id, err := EmitDB(context.Background(), db, Event{Category: "mcp", Action: "x", Actor: "y"})
	if err != nil {
		t.Fatal(err)
	}
	var inst sql.NullString
	if err := db.QueryRow("SELECT instance FROM audit_log WHERE id = ?", id).Scan(&inst); err != nil {
		t.Fatal(err)
	}
	if inst.String != "krons" {
		t.Errorf("instance = %q, want the package's", inst.String)
	}

	id, err = EmitDB(context.Background(), db, Event{Category: "mcp", Action: "x", Actor: "y", Instance: "sloth"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT instance FROM audit_log WHERE id = ?", id).Scan(&inst); err != nil {
		t.Fatal(err)
	}
	if inst.String != "sloth" {
		t.Errorf("instance = %q, want the event's own", inst.String)
	}
}

// LoadConfig

// audit is off unless AUDIT_ENABLED is exactly "true". Anything else — unset,
// "1", "yes", "TRUE" — leaves it off, so an operator who thinks they enabled
// it must find out from the empty log, not believe a truthy-looking value.
func TestLoadConfigEnabledIsExactlyTrue(t *testing.T) {
	for value, want := range map[string]bool{
		"true":  true,
		"":      false,
		"1":     false,
		"yes":   false,
		"TRUE":  false,
		"false": false,
	} {
		t.Setenv("AUDIT_ENABLED", value)
		if got := LoadConfig("/srv/data/x", "krons").Enabled; got != want {
			t.Errorf("AUDIT_ENABLED=%q -> Enabled=%v, want %v", value, got, want)
		}
	}
}

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	for _, k := range []string{
		"AUDIT_ENABLED", "AUDIT_MAX_BYTES", "AUDIT_ROTATE_HOURS",
		"AUDIT_WEBHOOK_URL", "AUDIT_WEBHOOK_URL_SYSTEM", "AUDIT_WEBHOOK_URL_WEB",
		"AUDIT_WEBHOOK_SECRET",
	} {
		t.Setenv(k, "")
	}
	c := LoadConfig("/srv/data/arizuko_krons", "krons")
	if c.DataDir != "/srv/data/arizuko_krons" || c.Instance != "krons" {
		t.Errorf("dataDir/instance not carried through: %+v", c)
	}
	if c.MaxBytes != 100*1024*1024 {
		t.Errorf("MaxBytes = %d, want 100MiB", c.MaxBytes)
	}
	if c.RotateHours != 24 {
		t.Errorf("RotateHours = %d, want 24", c.RotateHours)
	}
	if c.WebhookURL != "" || c.WebhookSecret != "" {
		t.Errorf("webhooks default to unset, got %+v", c)
	}

	t.Setenv("AUDIT_MAX_BYTES", "4096")
	t.Setenv("AUDIT_ROTATE_HOURS", "6")
	t.Setenv("AUDIT_WEBHOOK_URL", "https://collector.example/audit")
	c = LoadConfig("/x", "y")
	if c.MaxBytes != 4096 || c.RotateHours != 6 {
		t.Errorf("overrides not read: %+v", c)
	}
	if c.WebhookURL != "https://collector.example/audit" {
		t.Errorf("WebhookURL = %q", c.WebhookURL)
	}

	// A non-numeric value falls back to the default rather than to zero: a
	// rotate interval of 0 hours would rotate the log on every write.
	t.Setenv("AUDIT_ROTATE_HOURS", "soon")
	if got := LoadConfig("/x", "y").RotateHours; got != 24 {
		t.Errorf("a non-numeric AUDIT_ROTATE_HOURS gave %d, want the default 24", got)
	}
}
