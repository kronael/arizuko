package store

import (
	"context"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestPaneSessions_OpenAndReadByChannel(t *testing.T) {
	s := openEntities(t)

	if err := s.OpenPane("T1", "U1", "1700000000.000100", "D1"); err != nil {
		t.Fatalf("open: %v", err)
	}
	p, ok := s.PaneInChannel("D1")
	if !ok {
		t.Fatalf("read by channel: missing")
	}
	if p.TeamID != "T1" || p.UserID != "U1" || p.ThreadTS != "1700000000.000100" || p.ChannelID != "D1" {
		t.Fatalf("read by channel: bad item %+v", p)
	}
	if p.OpenedAt == "" {
		t.Fatalf("read by channel: opened_at empty")
	}
	if p.ContextJID != "" {
		t.Fatalf("read by channel: unexpected value on an absent attribute %+v", p)
	}
}

func TestPaneSessions_OpenIsIdempotent(t *testing.T) {
	s := openEntities(t)

	if err := s.OpenPane("T1", "U1", "1700.001", "D1"); err != nil {
		t.Fatal(err)
	}
	first, _ := s.PaneInChannel("D1")
	if err := s.OpenPane("T1", "U1", "1700.001", "D2"); err != nil {
		t.Fatal(err)
	}
	// channel_id refreshes; the lookup by D2 now succeeds, D1 does not.
	if _, ok := s.PaneInChannel("D1"); ok {
		t.Fatalf("D1 should no longer map")
	}
	got, ok := s.PaneInChannel("D2")
	if !ok {
		t.Fatalf("D2 missing after the channel refresh")
	}
	if got.OpenedAt != first.OpenedAt {
		t.Fatalf("opened_at must not change when a pane is re-opened")
	}
}

func TestPaneSessions_SetContext(t *testing.T) {
	s := openEntities(t)
	if err := s.OpenPane("T1", "U1", "1700.001", "D1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneContext("T1", "U1", "1700.001", "slack:T1/channel/C99"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.PaneInChannel("D1")
	if p.ContextJID != "slack:T1/channel/C99" {
		t.Fatalf("context not set: %q", p.ContextJID)
	}
	if err := s.SetPaneContext("T1", "U1", "1700.001", ""); err != nil {
		t.Fatal(err)
	}
	p, _ = s.PaneInChannel("D1")
	if p.ContextJID != "" {
		t.Fatalf("context not cleared: %q", p.ContextJID)
	}
}

// TestPaneSessions_ClearingTheContextRemovesTheAttribute pins the difference
// between an empty value and an absent one. Absent is the only null this model
// has, so a cleared context must leave nothing behind — an empty string is a
// value that Present still finds.
func TestPaneSessions_ClearingTheContextRemovesTheAttribute(t *testing.T) {
	s := openEntities(t)
	if err := s.OpenPane("T1", "U1", "1700.001", "D1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneContext("T1", "U1", "1700.001", "slack:T1/channel/C99"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaneContext("T1", "U1", "1700.001", ""); err != nil {
		t.Fatal(err)
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: PaneSessions, Partition: panePartition,
		Where: []objstore.Cmp{{Attr: attrPaneContext, Op: objstore.Present}},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("%d panes still carry a context_jid attribute; clearing it must REMOVE the "+
			"attribute, not write an empty value", len(items))
	}
}

func TestPaneSessions_ReadByChannelMissing(t *testing.T) {
	s := openEntities(t)
	if _, ok := s.PaneInChannel("Dnope"); ok {
		t.Fatalf("expected missing")
	}
}

// TestPaneSessions_AChannelNamingSeveralPanesReadsTheNewest is the guarantee
// `ORDER BY opened_at DESC LIMIT 1` carried and the reader now carries. It is
// not hypothetical: 79 of marinade's 81 live panes share five channel ids, one
// of them across 31 panes.
func TestPaneSessions_AChannelNamingSeveralPanesReadsTheNewest(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	// Written directly so the instants are chosen rather than raced: three panes
	// in one channel, and the newest is deliberately NOT the last one written.
	for _, tc := range []struct{ thread, at string }{
		{"1700.001", "2026-01-01T00:00:00.000000000Z"},
		{"1700.003", "2026-03-03T00:00:00.000000000Z"},
		{"1700.002", "2026-02-02T00:00:00.000000000Z"},
	} {
		key, err := paneKey("T1", "U1", tc.thread)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Obj().GuardedBatch(ctx, nil, []objstore.Write{{
			Op: objstore.Put, Entity: PaneSessions, Key: key,
			Attrs: map[string]string{attrPaneChannel: "C1", attrPaneOpenedAt: tc.at},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := s.PaneInChannel("C1")
	if !ok {
		t.Fatalf("C1 names no pane")
	}
	if got.ThreadTS != "1700.003" {
		t.Fatalf("C1 reads back pane %s, want the newest (1700.003)", got.ThreadTS)
	}
}

// TestPaneSessions_SetContextByChannelReachesEveryPane pins what the UPDATE it
// replaces did: the statement carried no LIMIT, so every pane the channel names
// took the context. A reader-side loop that stopped at the first would silently
// leave the other thirty.
func TestPaneSessions_SetContextByChannelReachesEveryPane(t *testing.T) {
	s := openEntities(t)
	for _, thread := range []string{"1700.001", "1700.002", "1700.003"} {
		if err := s.OpenPane("T1", "U1", thread, "C1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPaneContextByChannel("C1", "slack:T1/channel/C42"); err != nil {
		t.Fatal(err)
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: PaneSessions, Partition: panePartition,
		Where: []objstore.Cmp{
			{Attr: attrPaneContext, Op: objstore.Eq, Value: "slack:T1/channel/C42"},
		},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("%d of 3 panes in C1 took the context", len(items))
	}
}

// TestPaneSessions_SetContextByChannelIsANoOpForAnUnknownChannel keeps the
// answer the UPDATE gave by affecting no rows: slakd opens the pane and routd
// sets the context, and the two arrive over different transports.
func TestPaneSessions_SetContextByChannelIsANoOpForAnUnknownChannel(t *testing.T) {
	s := openEntities(t)
	if err := s.SetPaneContextByChannel("Cnope", "slack:T1/channel/C42"); err != nil {
		t.Fatalf("a channel naming no pane must not be an error: %v", err)
	}
}

// TestPaneSessions_SetContextOnAPaneThatIsNotThereIsNotAnError is the same
// answer reached by the triple. objstore's Update REFUSES a missing item, and
// the refusal must not become an error the caller sees.
func TestPaneSessions_SetContextOnAPaneThatIsNotThereIsNotAnError(t *testing.T) {
	s := openEntities(t)
	if err := s.SetPaneContext("T1", "U1", "1700.001", "slack:T1/channel/C42"); err != nil {
		t.Fatalf("setting the context of an unopened pane must not be an error: %v", err)
	}
}

// TestPaneSessions_TheKeySeparatorIsRefusedInAField is what stops two panes
// addressing one item. Slack ids carry no slash today; a field that did would
// otherwise join into a key some other triple also builds.
func TestPaneSessions_TheKeySeparatorIsRefusedInAField(t *testing.T) {
	s := openEntities(t)
	err := s.OpenPane("T1", "U1/U2", "1700.001", "D1")
	if err == nil {
		t.Fatalf("a user_id carrying %q was accepted", paneKeySep)
	}
	if !strings.Contains(err.Error(), "user_id") {
		t.Fatalf("the error does not name the field: %v", err)
	}
}

// TestPaneSessions_TheOrderedWidthIsTheTimestampLayout keeps the declaration and
// the layout from drifting: a constant that no longer matched core.TS would
// refuse every write instead of guarding one.
func TestPaneSessions_TheOrderedWidthIsTheTimestampLayout(t *testing.T) {
	if got := len(core.NowTS()); got != tsWidth {
		t.Fatalf("core.TS renders %d bytes and the declared ordered width is %d", got, tsWidth)
	}
}

// TestPaneSessions_AShortOpenedAtIsRefused is the declaration doing its job.
// Eight of marinade's 81 live rows carry a stripped fraction, and a value of
// another width sorts by bytes into the wrong place rather than failing — which
// is BUGS L97 on the attribute the channel read picks its winner by.
func TestPaneSessions_AShortOpenedAtIsRefused(t *testing.T) {
	s := openEntities(t)
	key, err := paneKey("T1", "U1", "1700.001")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Obj().GuardedBatch(context.Background(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: PaneSessions, Key: key,
		Attrs: map[string]string{
			attrPaneChannel: "D1",
			// 29 bytes: the shape 8 live rows carry.
			attrPaneOpenedAt: "2026-06-19T12:28:46.74770804Z",
		},
	}})
	if err == nil {
		t.Fatalf("a 29-byte opened_at was accepted; the ordered declaration must refuse it")
	}
	if !strings.Contains(err.Error(), "opened_at") {
		t.Fatalf("the error does not name the attribute: %v", err)
	}
}
