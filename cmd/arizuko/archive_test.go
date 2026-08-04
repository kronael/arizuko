package main

// E2E tests for `arizuko archive export`/`archive apply` (spec 5/8 "The
// full-instance archive"). Reuses openInstance (apply_test.go) for real
// routd.db/onbod.db bootstraps — the REAL schema, unlike resreg/
// archive_test.go's store.Migrate frozen-messages.db fixture, which
// predates routd/migrations/0019 (messages.link_context). That's exactly
// why the full-column message fidelity test (Finding 4's neighbor) lives
// here instead.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// seedGroupFiles writes a minimal group dir under dataDir/groups/<folder> —
// standing in for what container.SetupGroup/the agent would have written,
// without spinning up a container in a test.
func seedGroupFiles(t *testing.T, dataDir, folder string) {
	t.Helper()
	dir := filepath.Join(dataDir, "groups", folder)
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PERSONA.md"), []byte("# atlas persona\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("remembers everything\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "note.md"), []byte("a skill file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readGroupFile(t *testing.T, dataDir, folder, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataDir, "groups", folder, rel))
	if err != nil {
		t.Fatalf("read %s/%s: %v", folder, rel, err)
	}
	return string(b)
}

// TestArchive_RoundTrip_ConfigSecretsMessagesGroups is the real round-trip
// the task asks for: archive a seeded source instance, apply it to a fresh
// empty target, assert equivalence across every document — driven through
// buildArchive/applyArchive (the same functions cmdArchiveExport/
// cmdArchiveApply call; TestCLI_ArchiveExportApply_RealFiles below drives
// the actual CLI entry points on top of this).
func TestArchive_RoundTrip_ConfigSecretsMessagesGroups(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	srcRoutd := srcStores[resreg.SubsystemRoutd]
	srcOnbod := srcStores[resreg.SubsystemOnbod]
	srcRoutd.SetSecretKeys([]byte("archive-round-trip-key"))

	// Config: a group + a route.
	c0 := routdChecksum(t, srcRoutd)
	if _, err := resreg.Apply(ctx, srcRoutd.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
		"groups": []resources.GroupsRow{{Folder: "atlas", Product: "assistant"}},
		"routes": []resources.RoutesRow{{Seq: 0, Match: "platform=tele", Target: "atlas"}},
	}, nil); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	seedGroupFiles(t, srcDataDir, "atlas")

	// Secrets.
	if err := srcRoutd.SetSecret(store.ScopeFolder, "atlas", "API_KEY", "sk-source-secret"); err != nil {
		t.Fatal(err)
	}

	// route_tokens (kind=route) + a pairing token that must NOT travel.
	if _, err := srcRoutd.DB().Exec(`INSERT INTO route_tokens(token_hash, jid, owner_folder, created_at, kind)
		VALUES (x'aabbcc', 'web:atlas', 'atlas', '2026-01-01T00:00:00Z', 'route')`); err != nil {
		t.Fatal(err)
	}
	if _, err := srcRoutd.DB().Exec(`INSERT INTO route_tokens(token_hash, jid, owner_folder, created_at, kind)
		VALUES (x'ddeeff', 'telegram:user/1', 'atlas', '2026-01-01T00:00:00Z', 'pair')`); err != nil {
		t.Fatal(err)
	}

	// invites.
	if _, err := srcOnbod.DB().Exec(`INSERT INTO invites(ref, target_glob, issued_by_sub, issued_at, max_uses, used_count)
		VALUES ('ref1', 'atlas/*', 'user:admin', '2026-01-01T00:00:00Z', 5, 0)`); err != nil {
		t.Fatal(err)
	}

	// Messages, including a column routd's own agent-facing read surface
	// omits (link_context) — the real schema has it, unlike resreg/
	// archive_test.go's fixture.
	if _, err := srcRoutd.DB().Exec(`INSERT INTO messages
		(id, chat_jid, sender, content, timestamp, topic, routed_to, link_context)
		VALUES ('m1','web:atlas','user1','hello','2026-01-01T00:00:00Z','general','atlas','ctx-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := srcRoutd.DB().Exec(`INSERT INTO messages
		(id, chat_jid, sender, content, timestamp, topic, routed_to)
		VALUES ('m2','web:atlas','user1','world','2026-01-02T00:00:00Z','general','atlas')`); err != nil {
		t.Fatal(err)
	}

	var archiveBuf bytes.Buffer
	exportReport, err := buildArchive(ctx, srcStores, srcDataDir, false, &archiveBuf)
	if err != nil {
		t.Fatalf("buildArchive: %v", err)
	}
	if !strings.Contains(exportReport, "messages: 2 rows") {
		t.Errorf("export report missing message count:\n%s", exportReport)
	}
	if !strings.Contains(exportReport, "route_tokens: 1") {
		t.Errorf("export report should count only the kind=route token:\n%s", exportReport)
	}

	// --- Restore onto a FRESH, empty target ---
	dstDataDir, dstStores := openInstance(t)
	dstRoutd := dstStores[resreg.SubsystemRoutd]
	dstRoutd.SetSecretKeys([]byte("archive-round-trip-key")) // same key as source

	// force=true: a fresh, different instance's empty tables never share
	// the source's content-hash checksum, so restoring across instances
	// always needs it — the same reason TestCLI_ExportApply_RealFiles uses
	// --force for ordinary (config-only) apply onto a different instance.
	// The target is genuinely empty, so this is also the scenario where
	// route_tokens/invites are ALLOWED to restore (Finding 3's
	// proven-empty-target case); TestArchive_Apply_ForceRestoresTokensOntoEmptyTarget
	// covers the off-by-default (no --force) half in isolation.
	applyReport, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(archiveBuf.Bytes()), true)
	if err != nil {
		t.Fatalf("applyArchive: %v\nreport so far:\n%s", err, applyReport)
	}

	// Config landed.
	groupsGot, err := resreg.Lookup("groups").ScanAll(dstRoutd.DB())
	if err != nil {
		t.Fatal(err)
	}
	if rows := groupsGot.([]resources.GroupsRow); len(rows) != 1 || rows[0].Folder != "atlas" {
		t.Errorf("groups after restore = %+v", rows)
	}
	routesGot, _ := resreg.Lookup("routes").ScanAll(dstRoutd.DB())
	if rows := routesGot.([]resources.RoutesRow); len(rows) != 1 || rows[0].Target != "atlas" {
		t.Errorf("routes after restore = %+v", rows)
	}

	// Secret decrypts on the target.
	got, err := dstRoutd.GetSecret(store.ScopeFolder, "atlas", "API_KEY")
	if err != nil {
		t.Fatalf("GetSecret on target: %v", err)
	}
	if got.Value != "sk-source-secret" {
		t.Errorf("restored secret = %q, want sk-source-secret", got.Value)
	}

	// route_tokens/invites: --force onto a PROVEN-EMPTY target restores
	// them (Finding 3) — only the kind=route token, never the pairing one.
	var tokenJID, tokenKind string
	if err := dstRoutd.DB().QueryRow("SELECT jid, kind FROM route_tokens").Scan(&tokenJID, &tokenKind); err != nil {
		t.Fatalf("route_token not restored: %v", err)
	}
	if tokenJID != "web:atlas" || tokenKind != "route" {
		t.Errorf("restored route_token = jid=%q kind=%q", tokenJID, tokenKind)
	}
	var tokenCount int
	dstRoutd.DB().QueryRow("SELECT COUNT(*) FROM route_tokens").Scan(&tokenCount)
	if tokenCount != 1 {
		t.Errorf("route_tokens count = %d, want exactly 1 (pairing token must never restore)", tokenCount)
	}
	if !strings.Contains(applyReport, "route_tokens: 1 rows restored") {
		t.Errorf("apply report should report the restore:\n%s", applyReport)
	}
	dstOnbod := dstStores[resreg.SubsystemOnbod]
	var inviteGlob string
	if err := dstOnbod.DB().QueryRow("SELECT target_glob FROM invites WHERE ref='ref1'").Scan(&inviteGlob); err != nil {
		t.Fatalf("invite not restored: %v", err)
	}
	if inviteGlob != "atlas/*" {
		t.Errorf("restored invite target_glob = %q", inviteGlob)
	}

	// Messages present, full column fidelity (link_context survived).
	var n int
	dstRoutd.DB().QueryRow("SELECT COUNT(*) FROM messages").Scan(&n)
	if n != 2 {
		t.Errorf("messages after restore = %d, want 2", n)
	}
	var linkCtx string
	if err := dstRoutd.DB().QueryRow("SELECT link_context FROM messages WHERE id='m1'").Scan(&linkCtx); err != nil {
		t.Fatal(err)
	}
	if linkCtx != "ctx-a" {
		t.Errorf("link_context lost: got %q, want ctx-a", linkCtx)
	}

	// Finding 4: agent_cursor derived from the imported history's MAX
	// timestamp for the touched chat.
	var cursor string
	if err := dstRoutd.DB().QueryRow("SELECT agent_cursor FROM chats WHERE jid='web:atlas'").Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != "2026-01-02T00:00:00Z" {
		t.Errorf("agent_cursor = %q, want the MAX imported timestamp", cursor)
	}

	// groups.tar landed on the (previously nonexistent) target folder.
	if got := readGroupFile(t, dstDataDir, "atlas", "PERSONA.md"); got != "# atlas persona\n" {
		t.Errorf("PERSONA.md = %q", got)
	}
	if got := readGroupFile(t, dstDataDir, "atlas", "MEMORY.md"); got != "remembers everything\n" {
		t.Errorf("MEMORY.md = %q", got)
	}
	if got := readGroupFile(t, dstDataDir, "atlas", "skills/note.md"); got != "a skill file\n" {
		t.Errorf("skills/note.md = %q", got)
	}

	// --- Re-apply the SAME archive a second time: messages must stay
	// idempotent (INSERT OR IGNORE) and config apply must succeed against
	// the now-matching checksum (no --force needed the second time).
	if _, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(archiveBuf.Bytes()), false); err != nil {
		t.Fatalf("second applyArchive (idempotent re-run): %v", err)
	}
	dstRoutd.DB().QueryRow("SELECT COUNT(*) FROM messages").Scan(&n)
	if n != 2 {
		t.Errorf("messages after re-apply = %d, want 2 (no duplicates)", n)
	}
}

// TestArchive_Apply_ForceRestoresTokensOntoEmptyTarget covers Finding 3's
// --force path: route_tokens/invites restore only when the target table is
// PROVEN EMPTY, even under --force.
// This calls applySecretsDoc directly rather than the full applyArchive —
// the config-manifest's own checksum ALWAYS includes route_tokens content
// (it's a registered resreg resource; SkipApplyRebuild only exempts it from
// the DELETE+INSERT rebuild, not from Export/Checksum's projection), so a
// target that doesn't already hold the same tokens can never match the
// source's checksum without --force regardless of this gate — the two
// "reasons for --force" are entangled at the config layer. Testing the
// token gate in isolation, independent of that unrelated entanglement, is
// exactly why archive.go keeps applySecretsDoc a separate, directly
// callable step from the routd.yaml/onbod.yaml apply step.
func TestArchive_Apply_ForceRestoresTokensOntoEmptyTarget(t *testing.T) {
	ctx := context.Background()
	doc := archiveSecretsDoc{
		RouteTokens: []resreg.ArchiveRouteTokenRow{
			{TokenHash: "aabbcc", JID: "web:atlas", OwnerFolder: "atlas", CreatedAt: "2026-01-01T00:00:00Z"},
		},
		Invites: []resreg.ArchiveInviteRow{
			{Ref: "ref1", TargetGlob: "atlas/*", IssuedBySub: "user:admin", IssuedAt: "2026-01-01T00:00:00Z", MaxUses: 5},
		},
	}

	_, dstStores := openInstance(t)
	dstRoutd := dstStores[resreg.SubsystemRoutd]
	dstOnbod := dstStores[resreg.SubsystemOnbod]
	c0 := routdChecksum(t, dstRoutd)
	if _, err := resreg.Apply(ctx, dstRoutd.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
		"groups": []resources.GroupsRow{{Folder: "atlas", Product: "assistant"}},
	}, nil); err != nil {
		t.Fatal(err) // route_tokens.owner_folder FK-references groups(folder)
	}

	// Without --force: both skipped.
	var report strings.Builder
	if err := applySecretsDoc(ctx, dstStores, doc, false, &report); err != nil {
		t.Fatalf("applySecretsDoc (no force): %v", err)
	}
	var tokenCount, inviteCount int
	dstRoutd.DB().QueryRow("SELECT COUNT(*) FROM route_tokens").Scan(&tokenCount)
	dstOnbod.DB().QueryRow("SELECT COUNT(*) FROM invites").Scan(&inviteCount)
	if tokenCount != 0 || inviteCount != 0 {
		t.Fatalf("landed without --force: tokens=%d invites=%d", tokenCount, inviteCount)
	}
	if !strings.Contains(report.String(), "revival risk") {
		t.Errorf("report missing the revival-risk explanation:\n%s", report.String())
	}

	// With --force onto the still-empty target: both restored.
	report.Reset()
	if err := applySecretsDoc(ctx, dstStores, doc, true, &report); err != nil {
		t.Fatalf("applySecretsDoc (force): %v\n%s", err, report.String())
	}
	dstRoutd.DB().QueryRow("SELECT COUNT(*) FROM route_tokens").Scan(&tokenCount)
	dstOnbod.DB().QueryRow("SELECT COUNT(*) FROM invites").Scan(&inviteCount)
	if tokenCount != 1 {
		t.Errorf("route_tokens after --force = %d, want 1", tokenCount)
	}
	if inviteCount != 1 {
		t.Errorf("invites after --force = %d, want 1", inviteCount)
	}

	// Re-running --force again: target is no longer empty, so it must
	// refuse the revive (Finding 3: "even with --force it refuses unless
	// the target's route_tokens/invites tables are already empty").
	report.Reset()
	if err := applySecretsDoc(ctx, dstStores, doc, true, &report); err != nil {
		t.Fatalf("applySecretsDoc (force, 2nd run): %v", err)
	}
	if !strings.Contains(report.String(), "requires an EMPTY target table") {
		t.Errorf("second --force run should refuse a non-empty target:\n%s", report.String())
	}
	dstRoutd.DB().QueryRow("SELECT COUNT(*) FROM route_tokens").Scan(&tokenCount)
	if tokenCount != 1 {
		t.Errorf("route_tokens after refused re-force = %d, want still 1", tokenCount)
	}
}

// TestArchive_Apply_RefusesNewerFormatVersion is the "refuse an older
// target binary" rule (spec 5/8 "Cross-instance portability").
func TestArchive_Apply_RefusesNewerFormatVersion(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	var buf bytes.Buffer
	if _, err := buildArchive(ctx, srcStores, srcDataDir, false, &buf); err != nil {
		t.Fatal(err)
	}
	// Corrupt the archive.yaml entry's format_version by round-tripping
	// through the tar and rewriting just that one field's bytes is fragile;
	// instead, prove the check fires by re-marshaling a manifest with a
	// bumped version through the same path applyArchive reads.
	patched := bytes.Replace(buf.Bytes(),
		[]byte("format_version: 1"), []byte("format_version: 999"), 1)
	if bytes.Equal(patched, buf.Bytes()) {
		t.Fatal("test setup: format_version: 1 not found in archive.yaml entry (tar packs it uncompressed, so a literal replace should work)")
	}
	dstDataDir, dstStores := openInstance(t)
	_, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(patched), false)
	if err == nil {
		t.Fatal("expected an error for a newer format_version")
	}
	if !strings.Contains(err.Error(), "format_version") {
		t.Errorf("error should name format_version, got: %v", err)
	}
}

// TestArchive_Apply_SkipsNonEmptyFolderUnlessForce is the filesystem half
// of "Restoring onto a populated instance": ONE flag governs both the
// checksum override and the filesystem-non-empty override (spec 5/8: "the
// same flag apply --force already uses... not a second flag for a second
// concept") — so without --force a non-empty target folder is preserved,
// and with --force it is overwritten.
// This calls tarGroups/extractGroups directly rather than the full
// buildArchive/applyArchive pipeline — a genuine, pre-existing landmine
// (unrelated to this spec, found chasing a cross-agent flake report) makes
// that pipeline's config-checksum half nondeterministic across two
// independently-bootstrapped instances: routd/migrations/0005-network-
// rules.sql seeds two default rows with `created_at = CURRENT_TIMESTAMP`
// (SQLite's own wall-clock now(), second resolution), and `network_rules`
// is a registered resreg resource whose row content — including that
// timestamp — is part of Export/Checksum's projection for EVERY apply, not
// just the resources a given manifest mentions. Two openInstance(t) calls
// straddling a clock second therefore produce two checksums that can never
// match without --force, for reasons that have nothing to do with the
// groups.tar filesystem behavior this test actually checks. Calling the
// filesystem primitives directly sidesteps the whole config layer.
func TestArchive_Apply_SkipsNonEmptyFolderUnlessForce(t *testing.T) {
	srcDataDir, srcStores := openInstance(t)
	seedGroupFiles(t, srcDataDir, "atlas")
	if err := os.WriteFile(filepath.Join(srcDataDir, "groups", "atlas", "PERSONA.md"), []byte("SOURCE VERSION\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var groupsTar bytes.Buffer
	if _, err := tarGroups(srcDataDir, []string{"atlas"}, &groupsTar); err != nil {
		t.Fatalf("tarGroups: %v", err)
	}
	closeStores(srcStores)

	dstDataDir, dstStores := openInstance(t)
	closeStores(dstStores)
	// Target folder already has real content — a live agent's own writes,
	// in spirit.
	if err := os.MkdirAll(filepath.Join(dstDataDir, "groups", "atlas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDataDir, "groups", "atlas", "PERSONA.md"), []byte("TARGET VERSION — DO NOT CLOBBER\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	extracted, skipped, err := extractGroups(dstDataDir, []string{"atlas"}, groupsTar.Bytes(), false)
	if err != nil {
		t.Fatalf("extractGroups (no force): %v", err)
	}
	if extracted != 0 || skipped != 1 {
		t.Errorf("extractGroups (no force) = extracted=%d skipped=%d, want 0/1", extracted, skipped)
	}
	if got := readGroupFile(t, dstDataDir, "atlas", "PERSONA.md"); got != "TARGET VERSION — DO NOT CLOBBER\n" {
		t.Errorf("non-empty target folder was clobbered on the non-force pass: %q", got)
	}

	// A second, identical no-force re-run: still refuses.
	extracted, skipped, err = extractGroups(dstDataDir, []string{"atlas"}, groupsTar.Bytes(), false)
	if err != nil {
		t.Fatalf("extractGroups (no force, 2nd run): %v", err)
	}
	if extracted != 0 || skipped != 1 {
		t.Errorf("extractGroups (no force, 2nd run) = extracted=%d skipped=%d, want 0/1", extracted, skipped)
	}
	if got := readGroupFile(t, dstDataDir, "atlas", "PERSONA.md"); got != "TARGET VERSION — DO NOT CLOBBER\n" {
		t.Errorf("non-empty target folder was clobbered on a no-force re-run: %q", got)
	}
}

// TestArchive_Apply_ForceOverwritesNonEmptyFolder proves the OTHER half:
// --force DOES overwrite a non-empty target folder (the same flag, not a
// silent no-op).
func TestArchive_Apply_ForceOverwritesNonEmptyFolder(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	srcRoutd := srcStores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, srcRoutd)
	if _, err := resreg.Apply(ctx, srcRoutd.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
		"groups": []resources.GroupsRow{{Folder: "atlas", Product: "assistant"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	seedGroupFiles(t, srcDataDir, "atlas")
	if err := os.WriteFile(filepath.Join(srcDataDir, "groups", "atlas", "PERSONA.md"), []byte("SOURCE VERSION\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := buildArchive(ctx, srcStores, srcDataDir, false, &buf); err != nil {
		t.Fatal(err)
	}

	dstDataDir, dstStores := openInstance(t)
	if err := os.MkdirAll(filepath.Join(dstDataDir, "groups", "atlas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDataDir, "groups", "atlas", "PERSONA.md"), []byte("stale target content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(buf.Bytes()), true)
	if err != nil {
		t.Fatalf("applyArchive: %v", err)
	}
	if !strings.Contains(report, "1 folders extracted, 0 skipped") {
		t.Errorf("--force should extract into the non-empty folder:\n%s", report)
	}
	if got := readGroupFile(t, dstDataDir, "atlas", "PERSONA.md"); got != "SOURCE VERSION\n" {
		t.Errorf("--force should have overwritten PERSONA.md, got %q", got)
	}
}

// TestCLI_ArchiveExportApply_RealFiles drives the actual cmdArchiveExport/
// cmdArchiveApply entry points via ARIZUKO_DATA_DIR — the same proof
// TestCLI_ExportApply_RealFiles gives the config-only verbs.
func TestCLI_ArchiveExportApply_RealFiles(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)

	srcDir, srcStores := openInstance(t)
	srcRoutd := srcStores[resreg.SubsystemRoutd]
	c0 := routdChecksum(t, srcRoutd)
	if _, err := resreg.Apply(context.Background(), srcRoutd.DB(), resreg.SubsystemRoutd, c0, false, map[string]any{
		"groups": []resources.GroupsRow{{Folder: "atlas", Product: "assistant"}},
		"routes": []resources.RoutesRow{{Seq: 0, Match: "platform=tele", Target: "atlas"}},
	}, nil); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedGroupFiles(t, srcDir, "atlas")
	closeStores(srcStores)

	instDir := filepath.Join(base, "arizuko_srcinst")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(srcDir, "store"), filepath.Join(instDir, "store")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(srcDir, "groups"), filepath.Join(instDir, "groups")); err != nil {
		t.Fatal(err)
	}

	archiveFile := filepath.Join(base, "backup.tar")
	cmdArchiveExport([]string{"srcinst", archiveFile})
	if fi, err := os.Stat(archiveFile); err != nil || fi.Size() == 0 {
		t.Fatalf("archive file missing or empty: %v", err)
	}

	dstDataDir, dstStores := openInstance(t)
	closeStores(dstStores)
	dstInstDir := filepath.Join(base, "arizuko_dstinst")
	if err := os.MkdirAll(dstInstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dstDataDir, "store"), filepath.Join(dstInstDir, "store")); err != nil {
		t.Fatal(err)
	}
	cmdArchiveApply([]string{"dstinst", archiveFile, "--force"})

	dstSt, err := store.OpenRoutd(filepath.Join(dstInstDir, "store"))
	if err != nil {
		t.Fatalf("open destination routd.db: %v", err)
	}
	defer dstSt.Close()
	got, err := resreg.Lookup("routes").ScanAll(dstSt.DB())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range got.([]resources.RoutesRow) {
		if r.Match == "platform=tele" && r.Target == "atlas" {
			found = true
		}
	}
	if !found {
		t.Errorf("applied route missing from destination routd.db: %v", got)
	}
	if got := readGroupFile(t, dstInstDir, "atlas", "PERSONA.md"); got != "# atlas persona\n" {
		t.Errorf("groups.tar not extracted via the real CLI path: PERSONA.md = %q", got)
	}
}
