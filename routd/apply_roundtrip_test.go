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
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
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

func TestApply_RoundTripsEveryColumnItRebuilds(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	be := resreg.OnDB(db.SQL())
	ctx := context.Background()

	// A route with the attribution both stamping writers record, and a held call
	// with the mode it was placed under and the notice it waits on.
	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(
		`INSERT INTO routes (seq, match, target, added_by, added_via) VALUES (0,?,?,?,?)`,
		"platform=telegram", "atlas/main", "google:alice", "chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(
		`INSERT INTO pending_actions (id, group_folder, caller_agent, tool, status, created_at, mode, notice_id)
		 VALUES (?,?,?,?,?,?,?,?)`,
		"pa1", "atlas/main", "agent", "send", "held",
		"2026-01-01T00:00:00.000000000Z", "block", "tg:99"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
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
	var by, via string
	var id int
	if err := db.SQL().QueryRow(
		`SELECT id, COALESCE(added_by,''), COALESCE(added_via,'') FROM routes`).
		Scan(&id, &by, &via); err != nil {
		t.Fatal(err)
	}
	if by != "google:alice" || via != "chat" {
		t.Errorf("route attribution after the apply = (%q, %q), want (google:alice, chat): "+
			"an operator's record of who routed a chat where, erased by a config apply", by, via)
	}
	if id != 1 {
		t.Errorf("route id after the apply = %d, want 1: delete_route(id) addresses this "+
			"handle and an apply that renumbers it points the agent at another rule", id)
	}
	var mode, notice string
	if err := db.SQL().QueryRow(`SELECT mode, notice_id FROM pending_actions`).
		Scan(&mode, &notice); err != nil {
		t.Fatal(err)
	}
	if mode != "block" || notice != "tg:99" {
		t.Errorf("held call after the apply = (mode %q, notice %q), want (block, tg:99): "+
			"the mode decides who runs the approved call, and the notice is what turns a "+
			"reaction into its verdict", mode, notice)
	}
}

// A manifest that states no mode still lands on the value the schema would have
// chosen, because an empty mode is not one of the two answers the release path
// reads.
func TestApply_FillsTheHoldModeAManifestOmits(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	be := resreg.OnDB(db.SQL())

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
	var mode string
	if err := db.SQL().QueryRow(`SELECT mode FROM pending_actions`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "async" {
		t.Errorf("mode = %q, want async — the default the schema states", mode)
	}
}
