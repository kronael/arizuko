package routd

// An apply rebuilds a resource wholesale — DeleteAll then InsertAll — so the
// rows it writes back carry exactly the columns the RowType names. A column the
// declaration leaves out is reset to its DDL default by any apply that mentions
// the resource, which is data loss on the column path today and total loss under
// an entity, where a Put replaces the item's whole attribute set (BUGS L265).
//
// The check is the one that proved onbod lost nothing: export, apply the export,
// export again, and compare the bytes.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// routdManifest is one subsystem's export rendered the way a manifest is.
func routdManifest(t *testing.T, be resreg.Backend) ([]byte, map[string]any) {
	t.Helper()
	m, err := resreg.Export(be, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	b, err := resreg.EmitYAML(m)
	if err != nil {
		t.Fatalf("EmitYAML: %v", err)
	}
	return b, m
}

// routeRows reads the routes resource's manifest-visible projection, which is
// where `added_by`, `added_via` and the minted `id` live: they are attributes of
// the item, not fields of core.Route.
func routeRows(t *testing.T, be resreg.Backend) []resources.RoutesRow {
	t.Helper()
	r := resreg.Lookup(resources.RoutesName)
	if r == nil {
		t.Fatal("routes is not registered")
	}
	got, err := r.ScanAll(be)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	return got.([]resources.RoutesRow)
}

func TestApply_RoundTripsEveryColumnItRebuilds(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	be := resreg.On(db.Obj())
	ctx := context.Background()

	// A route with the attribution both stamping writers record, and a held call
	// with the notice it waits on — each written by the face production writes it
	// with.
	ws := &store.WriteSet{}
	if _, err := db.store().PutRouteInto(ctx, ws, core.Route{
		Seq: 0, Match: "platform=telegram", Target: "atlas/main",
	}, "google:alice", "chat", errors.New("the rule was created under this call")); err != nil {
		t.Fatal(err)
	}
	if err := db.store().Apply(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if err := db.PutPendingAction(PendingAction{
		ID: "pa1", GroupFolder: "atlas/main", CallerAgent: "agent", Tool: "send",
		Status: "held", CreatedAt: "2026-01-01T00:00:00.000000000Z",
		NoticeID: "tg:99",
	}); err != nil {
		t.Fatal(err)
	}

	before, rows := routdManifest(t, be)
	sum, err := resreg.Checksum(be, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if _, err := resreg.Apply(ctx, be, resreg.SubsystemRoutd, sum, false, rows, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	after, _ := routdManifest(t, be)
	if !bytes.Equal(before, after) {
		t.Errorf("the apply changed the export it was built from.\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
	if sum2, err := resreg.Checksum(be, resreg.SubsystemRoutd); err != nil {
		t.Fatalf("Checksum 2: %v", err)
	} else if sum2 != sum {
		t.Errorf("checksum %s -> %s across an apply of its own export", sum, sum2)
	}

	// Named, so the failure says which fact was erased rather than only that the
	// bytes differ.
	routes := routeRows(t, be)
	if len(routes) != 1 {
		t.Fatalf("the apply left %d routes, want the one that was written", len(routes))
	}
	by, via, id := routes[0].AddedBy, routes[0].AddedVia, routes[0].ID
	if by != "google:alice" || via != "chat" {
		t.Errorf("route attribution after the apply = (%q, %q), want (google:alice, chat): "+
			"an operator's record of who routed a chat where, erased by a config apply", by, via)
	}
	if id != 1 {
		t.Errorf("route id after the apply = %d, want 1: delete_route(id) addresses this "+
			"handle and an apply that renumbers it points the agent at another rule", id)
	}
	held, ok := db.GetPendingAction("pa1")
	if !ok {
		t.Fatal("the apply removed the held call it was built from")
	}
	if notice := held.NoticeID; notice != "tg:99" {
		t.Errorf("held call after the apply carries notice %q, want tg:99: the notice is "+
			"what turns a reaction into this call's verdict", notice)
	}
}

// A manifest states the fields a held call carries and nothing more. The row it
// writes has to come back as a held call the review queue can answer.
func TestApply_WritesAHeldCallAManifestStates(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	be := resreg.On(db.Obj())

	sum, err := resreg.Checksum(be, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resreg.Apply(context.Background(), be, resreg.SubsystemRoutd, sum, false,
		map[string]any{"pending_actions": []resources.PendingActionsRow{{
			ID: "pa1", GroupFolder: "atlas/main", Tool: "send", Status: "held",
			CreatedAt: "2026-01-01T00:00:00.000000000Z",
		}}}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	held, ok := db.GetPendingAction("pa1")
	if !ok {
		t.Fatal("the apply wrote no held call")
	}
	if held.Status != PendingHeld || held.Tool != "send" {
		t.Errorf("held call = %+v, want the held send the manifest states", held)
	}
}
