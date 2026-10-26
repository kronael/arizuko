package main

// E2E tests for `arizuko archive export`/`archive apply` (spec 5/8 "The
// full-instance archive"). Reuses openInstance (apply_test.go) for real
// routd.db/onbod.db bootstraps — the REAL schema, unlike resreg/
// archive_test.go's store.Migrate frozen-messages.db fixture, which
// predates routd/migrations/0019 (messages.link_context). That's exactly
// why the full-column message fidelity test (Finding 4's neighbor) lives
// here instead.

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// msgAt parses a fixture day. Timestamps reach messages.timestamp through
// core.TS: a fixture that hand-formats that column brings a second format to a
// column whose guarantee is that its string order is its time order (BUGS L97).
func msgAt(t *testing.T, day string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, day+"T00:00:00Z")
	if err != nil {
		t.Fatalf("fixture day %q: %v", day, err)
	}
	return v
}

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
// cmdArchiveApply call; those wrappers os.Exit on error, so the bodies drive
// the actual CLI entry points on top of this).
func TestArchive_RoundTrip_ConfigSecretsMessagesGroups(t *testing.T) {
	ctx := context.Background()
	srcDataDir, srcStores := openInstance(t)
	srcRoutd := srcStores[resreg.SubsystemRoutd]
	srcOnbod := srcStores[resreg.SubsystemOnbod]
	srcRoutd.SetSecretKeys([]byte("archive-round-trip-key"))

	c0 := routdChecksum(t, srcRoutd)
	if _, err := resreg.Apply(ctx, resreg.On(srcRoutd.Obj()), resreg.SubsystemRoutd, c0, false, map[string]any{
		"groups": []resources.GroupsRow{{Folder: "atlas", Product: "assistant"}},
		"routes": []resources.RoutesRow{{Seq: 0, Match: "platform=tele", Target: "atlas"}},
	}, nil); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	seedGroupFiles(t, srcDataDir, "atlas")

	if err := srcRoutd.SetSecret(store.ScopeFolder, "atlas", "API_KEY", "sk-source-secret"); err != nil {
		t.Fatal(err)
	}

	// route_tokens (kind=route) + a pairing token that must NOT travel. `kind`
	// is the resource's own row filter, and the mint API picks the kind itself,
	// so the fixture seeds both coordinates directly.
	seededAt := core.TS(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for _, tok := range []struct{ hash, jid, kind string }{
		{"aabbcc", "web:atlas", store.RouteTokenKindRoute},
		{"ddeeff", "telegram:user/1", store.RouteTokenKindPair},
	} {
		if err := srcRoutd.SeedItem(t.Context(), store.RouteTokens, []string{tok.hash},
			map[string]string{
				"jid": tok.jid, "owner_folder": "atlas",
				"created_at": seededAt, "kind": tok.kind,
			}); err != nil {
			t.Fatal(err)
		}
	}

	// An invite is an item, so the fixture writes it through import.go rather
	// than an INSERT the entity has no columns for.
	if _, err := srcOnbod.ImportOnbod(t.Context(), store.OnbodSnapshot{
		Invites: []store.ArchiveInviteRow{{
			Ref: "ref1", TargetGlob: "atlas/*", IssuedBySub: "user:admin",
			IssuedAt: "2026-01-01T00:00:00Z", MaxUses: 5,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// Messages, including a field routd's own agent-facing read surface omits
	// (link_context). A message is an item, so the fixture writes it through
	// the writer routd itself uses.
	srcMsgs := srcRoutd
	for _, m := range []core.Message{
		{ID: "m1", ChatJID: "web:atlas", Sender: "user1", Content: "hello",
			Timestamp: msgAt(t, "2026-01-01"), Topic: "general", RoutedTo: "atlas",
			LinkContext: "ctx-a"},
		{ID: "m2", ChatJID: "web:atlas", Sender: "user1", Content: "world",
			Timestamp: msgAt(t, "2026-01-02"), Topic: "general", RoutedTo: "atlas"},
	} {
		if err := srcMsgs.PutMessage(m); err != nil {
			t.Fatal(err)
		}
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
	applyReport, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(archiveBuf.Bytes()), true, nil)
	if err != nil {
		t.Fatalf("applyArchive: %v\nreport so far:\n%s", err, applyReport)
	}

	// Config landed.
	groupsGot, err := resreg.Lookup("groups").ScanAll(resreg.On(dstRoutd.Obj()))
	if err != nil {
		t.Fatal(err)
	}
	if rows := groupsGot.([]resources.GroupsRow); len(rows) != 1 || rows[0].Folder != "atlas" {
		t.Errorf("groups after restore = %+v", rows)
	}
	routesGot, _ := resreg.Lookup("routes").ScanAll(resreg.On(dstRoutd.Obj()))
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
	// The read is ExportRouteTokens', which is kind-scoped to 'route' — so the
	// count below is the whole table, and the two together say the delivery
	// bearer came back and the pairing credential did not.
	tokens, err := dstRoutd.ExportRouteTokens(t.Context())
	if err != nil {
		t.Fatalf("route_token not restored: %v", err)
	}
	if len(tokens) != 1 || tokens[0].JID != "web:atlas" {
		t.Errorf("restored route_tokens = %v, want one web:atlas delivery bearer", tokens)
	}
	tokenCount, err := dstRoutd.CountRouteTokens(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if tokenCount != 1 {
		t.Errorf("route_tokens count = %d, want exactly 1 (pairing token must never restore)", tokenCount)
	}
	if !strings.Contains(applyReport, "route_tokens: 1 rows restored") {
		t.Errorf("apply report should report the restore:\n%s", applyReport)
	}
	dstOnbod := dstStores[resreg.SubsystemOnbod]
	restored, err := dstOnbod.ListInvites("")
	if err != nil {
		t.Fatalf("read the restored invites: %v", err)
	}
	inviteGlob := ""
	for _, inv := range restored {
		if inv.Ref == "ref1" {
			inviteGlob = inv.TargetGlob
		}
	}
	if inviteGlob != "atlas/*" {
		t.Errorf("restored invite target_glob = %q, want atlas/*", inviteGlob)
	}

	// Messages present, full field fidelity (link_context survived).
	dstMsgs := dstRoutd
	n, err := dstMsgs.CountMessages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("messages after restore = %d, want 2", n)
	}
	restoredMsg, ok := dstMsgs.MessageByID("web:atlas", "m1")
	if !ok {
		t.Fatal("the restored message is gone")
	}
	if restoredMsg.LinkContext != "ctx-a" {
		t.Errorf("link_context lost: got %q, want ctx-a", restoredMsg.LinkContext)
	}

	// Finding 4: agent_cursor derived from the imported history's MAX
	// timestamp for the touched chat.
	cursor, err := dstMsgs.AgentCursor("web:atlas")
	if err != nil {
		t.Fatal(err)
	}
	if want := store.MsgStamp(core.TS(msgAt(t, "2026-01-02")), "m2"); cursor != want {
		t.Errorf("agent_cursor = %q, want the newest imported message's key %q", cursor, want)
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
	if _, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(archiveBuf.Bytes()), false, nil); err != nil {
		t.Fatalf("second applyArchive (idempotent re-run): %v", err)
	}
	n = countMessages(t, dstRoutd, "web:atlas")
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
		RouteTokens: []store.ArchiveRouteTokenRow{
			{TokenHash: "aabbcc", JID: "web:atlas", OwnerFolder: "atlas", CreatedAt: "2026-01-01T00:00:00Z"},
		},
		Invites: []store.ArchiveInviteRow{
			{Ref: "ref1", TargetGlob: "atlas/*", IssuedBySub: "user:admin", IssuedAt: "2026-01-01T00:00:00Z", MaxUses: 5},
		},
	}

	_, dstStores := openInstance(t)
	dstRoutd := dstStores[resreg.SubsystemRoutd]
	dstOnbod := dstStores[resreg.SubsystemOnbod]
	c0 := routdChecksum(t, dstRoutd)
	if _, err := resreg.Apply(ctx, resreg.On(dstRoutd.Obj()), resreg.SubsystemRoutd, c0, false, map[string]any{
		"groups": []resources.GroupsRow{{Folder: "atlas", Product: "assistant"}},
	}, nil); err != nil {
		t.Fatal(err) // route_tokens.owner_folder FK-references groups(folder)
	}

	// Without --force: both skipped.
	var report strings.Builder
	if err := applySecretsDoc(ctx, dstStores, doc, false, &report); err != nil {
		t.Fatalf("applySecretsDoc (no force): %v", err)
	}
	tokenCount, inviteCount := countTokens(t, dstRoutd), countInvites(t, dstOnbod)
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
	tokenCount, inviteCount = countTokens(t, dstRoutd), countInvites(t, dstOnbod)
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
	tokenCount = countTokens(t, dstRoutd)
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
	_, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(patched), false, nil)
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

	extracted, skipped, err := extractGroups(dstDataDir, []string{"atlas"}, groupsTar.Bytes(), false, nil)
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
	extracted, skipped, err = extractGroups(dstDataDir, []string{"atlas"}, groupsTar.Bytes(), false, nil)
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
	if _, err := resreg.Apply(ctx, resreg.On(srcRoutd.Obj()), resreg.SubsystemRoutd, c0, false, map[string]any{
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

	report, err := applyArchive(ctx, dstStores, dstDataDir, bytes.NewReader(buf.Bytes()), true, nil)
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

func TestArchive_ExportFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.tar")
	existing := filepath.Join(dir, "existing.tar")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{fresh, existing} {
		if err := writeArchiveFile(file, []byte("archive")); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s: mode %o, want 600", filepath.Base(file), got)
		}
		if b, _ := os.ReadFile(file); string(b) != "archive" {
			t.Errorf("%s: content %q", filepath.Base(file), b)
		}
	}
}

func TestArchive_SecretsEntryIsOwnerOnly(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeTarEntry(tw, archiveSecretsEntry, []byte("secrets: []\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	hdr, err := tar.NewReader(&buf).Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Mode&0o777 != 0o600 {
		t.Errorf("entry mode %o, want 600", hdr.Mode&0o777)
	}
}
