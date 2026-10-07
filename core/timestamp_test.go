package core

import (
	"sort"
	"testing"
	"time"
)

// TestTSStringOrderIsTimeOrder is the property the whole layout exists for: for
// any two instants, comparing the rendered strings gives the same answer as
// comparing the times. RFC3339Nano fails it inside one second.
func TestTSStringOrderIsTimeOrder(t *testing.T) {
	base := time.Date(2026, 8, 26, 19, 6, 41, 0, time.UTC)
	var times []time.Time
	for _, ns := range []int{0, 1, 10, 100, 1000, 500000000, 882458214, 999999999} {
		times = append(times, base.Add(time.Duration(ns)))
		times = append(times, base.Add(time.Second).Add(time.Duration(ns)))
	}
	// A non-UTC instant must sort by the instant, not by the wall clock.
	times = append(times, base.Add(30*time.Minute).In(time.FixedZone("plus1", 3600)))

	for _, a := range times {
		for _, b := range times {
			got := TS(a) < TS(b)
			want := a.Before(b)
			if got != want {
				t.Errorf("TS(%s)=%q < TS(%s)=%q → %v, want %v",
					a, TS(a), b, TS(b), got, want)
			}
		}
	}
}

// TestTSFixedWidth pins the property that makes the comparison total: every
// rendered value is the same length, so no prefix ever loses to a longer string.
func TestTSFixedWidth(t *testing.T) {
	base := time.Date(2026, 8, 26, 19, 6, 41, 0, time.UTC)
	for _, ns := range []int{0, 1, 10, 100, 500000000, 999999999} {
		s := TS(base.Add(time.Duration(ns)))
		if len(s) != 30 {
			t.Errorf("TS(ns=%d) = %q: width %d, want 30", ns, s, len(s))
		}
	}
	if s := TS(base.In(time.FixedZone("plus1", 3600))); len(s) != 30 {
		t.Errorf("TS of a non-UTC instant = %q: width %d, want 30", s, len(s))
	}
}

// TestTSRoundTripsThroughRFC3339Nano pins that no reader has to change: the
// parse layout already in every scan site reads what TS writes, exactly.
func TestTSRoundTripsThroughRFC3339Nano(t *testing.T) {
	for _, ns := range []int{0, 1, 100, 882458214, 999999999} {
		want := time.Date(2026, 8, 26, 19, 6, 41, ns, time.UTC)
		got, err := time.Parse(time.RFC3339Nano, TS(want))
		if err != nil {
			t.Fatalf("parse TS(ns=%d): %v", ns, err)
		}
		if !got.Equal(want) {
			t.Errorf("ns=%d: round trip %s, want %s", ns, got, want)
		}
	}
	// And it still reads a stripped-fraction row, so a column holding both
	// widths parses either way.
	got, err := time.Parse(time.RFC3339Nano, "2026-08-26T19:06:41Z")
	if err != nil {
		t.Fatalf("parse stripped-fraction: %v", err)
	}
	if want := time.Date(2026, 8, 26, 19, 6, 41, 0, time.UTC); !got.Equal(want) {
		t.Errorf("stripped-fraction parsed as %s, want %s", got, want)
	}
}

// TestTSSortsTheL97Case is the reported defect in miniature: an approval's
// nanosecond trigger against the whole-second cursor of its own chat.
func TestTSSortsTheL97Case(t *testing.T) {
	cursor := time.Date(2026, 8, 26, 19, 6, 41, 0, time.UTC)
	trigger := time.Date(2026, 8, 26, 19, 6, 41, 882458214, time.UTC)

	if TS(trigger) <= TS(cursor) {
		t.Fatalf("the re-issue trigger %q must sort after the cursor %q",
			TS(trigger), TS(cursor))
	}
	strs := []string{TS(trigger), TS(cursor)}
	sort.Strings(strs)
	if strs[0] != TS(cursor) {
		t.Errorf("sorted %v, want the cursor first", strs)
	}
}
