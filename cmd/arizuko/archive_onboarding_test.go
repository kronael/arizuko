package main

// Pending onboarding admissions in the archive (spec 5/8 §"Message history",
// BUGS Z3). They cannot ride the config lane — `onboarding` is
// SkipApplyRebuild, so `arizuko apply` never writes the table — and they
// cannot be rederived on import, because setting agent_cursor marks the
// route-missed message seen. So they get an archive-only document with its
// own UPSERT lane.

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
)

// seedAdmission inserts one pending admission, then proves it landed.
func seedAdmission(t *testing.T, db *sql.DB, jid string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO onboarding (jid, status, created, gate, queued_at)
		 VALUES (?, 'queued', '2026-08-06T00:00:00Z', 'invite_required', '2026-08-06T00:01:00Z')`,
		jid); err != nil {
		t.Fatalf("seed admission: %v", err)
	}
	if got := scalar(t, db, `SELECT COUNT(*) FROM onboarding WHERE jid='`+jid+`'`); got != 1 {
		t.Fatalf("seeded admission did not land (count=%d)", got)
	}
}

// TestArchiveOnboarding_TravelsAndRestores: the whole point — an admission
// waiting in the queue when the archive was taken is in the queue again after
// a DR restore, verdict and gate intact.
func TestArchiveOnboarding_TravelsAndRestores(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	seedAdmission(t, srcStores[resreg.SubsystemOnbod].DB(), "telegram:user/12345")

	var buf bytes.Buffer
	if _, err := buildArchive(ctx, srcStores, srcDataDir, false, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("telegram:user/12345")) {
		t.Fatal("the archive does not carry the admission at all")
	}

	dstDataDir, dstStores := openInstance(t)
	dstDB := dstStores[resreg.SubsystemOnbod].DB()
	if got := scalar(t, dstDB, `SELECT COUNT(*) FROM onboarding`); got != 0 {
		t.Fatalf("destination is not fresh (count=%d)", got)
	}

	// --force onto a proven-empty target: the same one gate route_tokens and
	// invites ride, because this document carries a credential verifier too.
	report, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(buf.Bytes()), true, nil)
	if err != nil {
		t.Fatalf("applyArchive: %v\n%s", err, report)
	}
	if got := scalar(t, dstDB, `SELECT COUNT(*) FROM onboarding WHERE jid='telegram:user/12345'`); got != 1 {
		t.Fatalf("admission not restored (count=%d)\n%s", got, report)
	}
	var status, gate, queuedAt string
	if err := dstDB.QueryRow(
		`SELECT status, gate, COALESCE(queued_at,'') FROM onboarding WHERE jid='telegram:user/12345'`).
		Scan(&status, &gate, &queuedAt); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || gate != "invite_required" {
		t.Errorf("admission state lost: status=%q gate=%q", status, gate)
	}
	if queuedAt != "2026-08-06T00:01:00Z" {
		t.Errorf("queued_at = %q, want it carried verbatim", queuedAt)
	}
	// F40: the document must not carry the dropped columns. Naming them would
	// fail the INSERT outright against onbod's post-0006 schema — which this
	// fixture does not have, since openInstance bootstraps from onbodSchema at
	// its pinned version 4, so the assertion is on the DOCUMENT, not the table.
	if bytes.Contains(buf.Bytes(), []byte("token_ref:")) {
		t.Error("the archive still carries onboarding.token_ref")
	}
}

// TestArchiveOnboarding_SkippedWithoutForce: the document carries admission
// VERDICTS, so it rides the same off-by-default gate route_tokens and invites
// do — importing them onto a live instance is a merge, not a restore.
func TestArchiveOnboarding_SkippedWithoutForce(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	seedAdmission(t, srcStores[resreg.SubsystemOnbod].DB(), "telegram:user/12345")

	var buf bytes.Buffer
	if _, err := buildArchive(ctx, srcStores, srcDataDir, false, &buf); err != nil {
		t.Fatal(err)
	}
	dstDataDir, dstStores := openInstance(t)
	dstDB := dstStores[resreg.SubsystemOnbod].DB()

	report, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(buf.Bytes()), false, nil)
	// force=false also rejects the config CAS across two independently
	// bootstrapped instances, so the config phase may fail first; either way
	// the admission must not have been written.
	_ = err
	if got := scalar(t, dstDB, `SELECT COUNT(*) FROM onboarding`); got != 0 {
		t.Errorf("admission imported without --force (count=%d)\n%s", got, report)
	}
}

// TestArchiveOnboarding_RefusesNonEmptyTarget: even --force refuses a target
// that already has admissions — that is a merge, not DR, and would clobber a
// post-export denial.
func TestArchiveOnboarding_RefusesNonEmptyTarget(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	seedAdmission(t, srcStores[resreg.SubsystemOnbod].DB(), "telegram:user/12345")

	var buf bytes.Buffer
	if _, err := buildArchive(ctx, srcStores, srcDataDir, false, &buf); err != nil {
		t.Fatal(err)
	}
	dstDataDir, dstStores := openInstance(t)
	dstDB := dstStores[resreg.SubsystemOnbod].DB()
	seedAdmission(t, dstDB, "telegram:user/99999") // target already populated

	report, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(buf.Bytes()), true, nil)
	if err != nil {
		t.Fatalf("applyArchive: %v\n%s", err, report)
	}
	if !strings.Contains(report, "onboarding: skipped") {
		t.Errorf("report must say the admissions were skipped: %s", report)
	}
	if got := scalar(t, dstDB, `SELECT COUNT(*) FROM onboarding WHERE jid='telegram:user/12345'`); got != 0 {
		t.Errorf("admission imported onto a non-empty target (count=%d)", got)
	}
	if got := scalar(t, dstDB, `SELECT COUNT(*) FROM onboarding WHERE jid='telegram:user/99999'`); got != 1 {
		t.Errorf("the target's own admission was disturbed (count=%d)", got)
	}
}

// TestArchiveOnboarding_ConfigApplyStillNeverTouchesIt guards the flag the
// spec calls load-bearing: `onboarding` stays SkipApplyRebuild, so an ordinary
// config apply must not DELETE+INSERT the table. It applies a manifest whose
// onboarding list is EMPTY — the shape an operator hand-writing config
// produces — because a manifest exported from the same DB would restore the
// row identically and could not tell the flag from its absence.
func TestArchiveOnboarding_ConfigApplyStillNeverTouchesIt(t *testing.T) {
	_, stores := openInstance(t)
	db := stores[resreg.SubsystemOnbod].DB()
	seedAdmission(t, db, "telegram:user/12345")

	manifest, err := resreg.Export(db, resreg.SubsystemOnbod)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest["onboarding"]; !ok {
		t.Fatal("fixture is vacuous: onboarding must be in the export projection")
	}
	manifest["onboarding"] = []any{} // the operator declared no admissions
	sum, err := resreg.Checksum(db, resreg.SubsystemOnbod)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resreg.Apply(context.Background(), db, resreg.SubsystemOnbod, sum, false, manifest, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM onboarding WHERE jid='telegram:user/12345'`).Scan(&status); err != nil {
		t.Fatalf("config apply deleted the live admission — SkipApplyRebuild is off: %v", err)
	}
	if status != "queued" {
		t.Errorf("status = %q, want queued", status)
	}
}
