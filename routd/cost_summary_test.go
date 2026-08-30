package routd

import (
	"testing"
	"time"
)

// seedCost writes one cost_log row.
func seedCost(t *testing.T, d *DB, folder, turnID string, in, out, cents int, at time.Time) {
	t.Helper()
	if _, err := d.SQL().Exec(
		`INSERT INTO cost_log (turn_id, folder, model, input_tokens, output_tokens, cost_cents, recorded_at)
		 VALUES (?,?,?,?,?,?,?)`,
		turnID, folder, "test-model", in, out, cents, at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

// CostSummary answers the axis CostByTurn cannot: what a folder is spending.
// dashd's usage page read cost_log directly for want of it (spec 6/30).
func TestCostSummaryAggregatesPerFolder(t *testing.T) {
	d, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now()
	seedCost(t, d, "acme", "t1", 10, 5, 3, now)
	seedCost(t, d, "acme", "t2", 20, 10, 7, now)
	seedCost(t, d, "other", "t3", 1, 1, 1, now)

	rows, err := d.CostSummary("", now.Add(-time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]CostSummaryRow{}
	for _, r := range rows {
		got[r.Folder] = r
	}
	if got["acme"].Tokens != 45 || got["acme"].CostCents != 10 {
		t.Errorf("acme = %+v, want 45 tokens / 10 cents summed across both turns", got["acme"])
	}
	if got["other"].Tokens != 2 {
		t.Errorf("other = %+v, want its own row, not acme's", got["other"])
	}
}

// The window is the point: without it the summary reports a lifetime total as
// if it were current.
func TestCostSummaryHonoursTheWindow(t *testing.T) {
	d, _ := OpenMem()
	defer d.Close()
	now := time.Now()
	seedCost(t, d, "acme", "old", 100, 100, 99, now.Add(-48*time.Hour))
	seedCost(t, d, "acme", "new", 1, 1, 1, now)

	rows, err := d.CostSummary("", now.Add(-time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Tokens != 2 {
		t.Errorf("rows = %+v, want only the in-window turn", rows)
	}
}

// Folder narrowing is exact-or-subtree via groupfolder.SQLSubtree, never a
// LIKE: a folder named with a metacharacter must not read its neighbours
// (BUGS L122).
func TestCostSummaryFolderFilterIsNotWidened(t *testing.T) {
	d, _ := OpenMem()
	defer d.Close()
	now := time.Now()
	since := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	seedCost(t, d, "a_b/one", "t1", 5, 5, 1, now)
	seedCost(t, d, "aXb/two", "t2", 9, 9, 9, now)
	seedCost(t, d, "Coach/x", "t3", 7, 7, 7, now)
	seedCost(t, d, "coach/y", "t4", 3, 3, 3, now)

	for _, c := range []struct{ folder, leak string }{
		{"a_b", "aXb/two"},
		{"coach", "Coach/x"},
		{"%", "aXb/two"},
	} {
		rows, err := d.CostSummary(c.folder, since)
		if err != nil {
			t.Fatalf("folder %q: %v", c.folder, err)
		}
		for _, r := range rows {
			if r.Folder == c.leak {
				t.Errorf("folder %q read %q's spend", c.folder, c.leak)
			}
		}
	}
	// The folder's own subtree still reads, or the filter is a denial of service.
	rows, _ := d.CostSummary("a_b", since)
	if len(rows) != 1 || rows[0].Folder != "a_b/one" {
		t.Errorf("a_b = %+v, want its own subtree", rows)
	}
}
