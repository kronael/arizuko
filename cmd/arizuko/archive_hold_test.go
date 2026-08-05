package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/runed"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// recordingHolder is a holdFn that records which folders were held and
// whether each was released — the contract extractGroups must honor.
type recordingHolder struct {
	held     []string
	released map[string]bool
	failOn   string // folder whose claim fails (a live turn, an unreachable runed)
}

func newRecordingHolder() *recordingHolder {
	return &recordingHolder{released: map[string]bool{}}
}

func (h *recordingHolder) fn() holdFn {
	return func(folder string) (func(), error) {
		if folder == h.failOn {
			return nil, errBusyFolder
		}
		h.held = append(h.held, folder)
		return func() { h.released[folder] = true }, nil
	}
}

// errBusyFolder stands in for runed reporting the folder already has a live
// run — the case the whole mechanism exists to catch.
var errBusyFolder = &busyErr{}

type busyErr struct{}

func (*busyErr) Error() string { return "folder has a live run" }

// tarOneFolder seeds folder in a source instance and returns its groups.tar
// bytes — the input extractGroups consumes.
func tarOneFolder(t *testing.T, folders ...string) []byte {
	t.Helper()
	srcDataDir, srcStores := openInstance(t)
	for _, f := range folders {
		seedGroupFiles(t, srcDataDir, f)
	}
	var buf bytes.Buffer
	if _, err := tarGroups(srcDataDir, folders, &buf); err != nil {
		t.Fatalf("tarGroups: %v", err)
	}
	closeStores(srcStores)
	return buf.Bytes()
}

// TestExtractGroups_HoldsEveryExtractedFolder: every folder whose tree is
// actually written gets its run slot claimed first and released after. This
// is the guarantee spec 5/8 asks for — no agent turn can start mid-extraction.
func TestExtractGroups_HoldsEveryExtractedFolder(t *testing.T) {
	tarBytes := tarOneFolder(t, "atlas", "borealis")

	dstDataDir, dstStores := openInstance(t)
	closeStores(dstStores)

	h := newRecordingHolder()
	extracted, skipped, err := extractGroups(dstDataDir, []string{"atlas", "borealis"}, tarBytes, false, h.fn())
	if err != nil {
		t.Fatalf("extractGroups: %v", err)
	}
	if extracted != 2 || skipped != 0 {
		t.Fatalf("extracted=%d skipped=%d want 2/0", extracted, skipped)
	}

	sort.Strings(h.held)
	if strings.Join(h.held, ",") != "atlas,borealis" {
		t.Errorf("held %v, want both extracted folders", h.held)
	}
	for _, f := range []string{"atlas", "borealis"} {
		if !h.released[f] {
			t.Errorf("hold on %q was never released — the folder stays paused until RunTTL", f)
		}
	}
	if got := readGroupFile(t, dstDataDir, "atlas", "PERSONA.md"); got != "# atlas persona\n" {
		t.Errorf("atlas not restored: %q", got)
	}
}

// TestExtractGroups_HoldFailureWritesNothing: a folder whose slot cannot be
// claimed aborts the whole filesystem step BEFORE any byte lands. Proceeding
// unguarded is the exact race the hold exists to prevent, so this must be
// fatal, not a warning — and it must fail before the write, not during it.
func TestExtractGroups_HoldFailureWritesNothing(t *testing.T) {
	tarBytes := tarOneFolder(t, "atlas", "borealis")

	dstDataDir, dstStores := openInstance(t)
	closeStores(dstStores)

	h := newRecordingHolder()
	h.failOn = "borealis"
	_, _, err := extractGroups(dstDataDir, []string{"atlas", "borealis"}, tarBytes, false, h.fn())
	if err == nil {
		t.Fatal("extractGroups proceeded despite an unclaimable run slot")
	}
	if !strings.Contains(err.Error(), "borealis") {
		t.Errorf("error %q does not name the folder that could not be claimed", err)
	}

	for _, f := range []string{"atlas", "borealis"} {
		if _, serr := os.Stat(filepath.Join(dstDataDir, "groups", f, "PERSONA.md")); serr == nil {
			t.Errorf("%q was written even though the run-slot claim failed", f)
		}
	}
	// The folder that WAS claimed before the failure is still released.
	if !h.released["atlas"] {
		t.Error("hold on atlas leaked after the aborted extraction")
	}
}

// TestExtractGroups_SkippedFolderNotHeld: a folder that is refused (non-empty
// target, no --force) has its tree left alone, so pausing its agent would be
// gratuitous denial of service. Only folders actually written are held.
func TestExtractGroups_SkippedFolderNotHeld(t *testing.T) {
	tarBytes := tarOneFolder(t, "atlas")

	dstDataDir, dstStores := openInstance(t)
	closeStores(dstStores)
	if err := os.MkdirAll(filepath.Join(dstDataDir, "groups", "atlas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDataDir, "groups", "atlas", "PERSONA.md"),
		[]byte("TARGET VERSION\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newRecordingHolder()
	extracted, skipped, err := extractGroups(dstDataDir, []string{"atlas"}, tarBytes, false, h.fn())
	if err != nil {
		t.Fatalf("extractGroups: %v", err)
	}
	if extracted != 0 || skipped != 1 {
		t.Fatalf("extracted=%d skipped=%d want 0/1", extracted, skipped)
	}
	if len(h.held) != 0 {
		t.Errorf("held %v — a folder that is never written must not be paused", h.held)
	}
}

// TestExtractGroups_NilHoldStillExtracts: --stopped (nil holdFn) is the
// explicit no-live-agent path and must extract exactly as before.
func TestExtractGroups_NilHoldStillExtracts(t *testing.T) {
	tarBytes := tarOneFolder(t, "atlas")

	dstDataDir, dstStores := openInstance(t)
	closeStores(dstStores)

	extracted, skipped, err := extractGroups(dstDataDir, []string{"atlas"}, tarBytes, false, nil)
	if err != nil {
		t.Fatalf("extractGroups: %v", err)
	}
	if extracted != 1 || skipped != 0 {
		t.Fatalf("extracted=%d skipped=%d want 1/0", extracted, skipped)
	}
	if got := readGroupFile(t, dstDataDir, "atlas", "PERSONA.md"); got != "# atlas persona\n" {
		t.Errorf("atlas not restored under --stopped: %q", got)
	}
}

// liveRuned stands up a real runed Server (KindHold registered, auth open) and
// returns its base URL — so the CLI's holdFn is exercised against the actual
// handler and Manager, not a stub of them.
func liveRuned(t *testing.T) (*runed.DB, string) {
	t.Helper()
	db, err := runed.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := runed.NewManager(db, runed.FakeRuntime{}, runed.ManagerConfig{
		Instance: "test", MaxConcurrent: 5, RunTTL: time.Minute,
	})
	mgr.RegisterExecutor(runed.KindHold, runed.NewHoldRuntime())
	srv := httptest.NewServer(runed.NewServer(mgr, db, nil).Handler())
	t.Cleanup(srv.Close)
	return db, srv.URL
}

// TestRunedHolder_EndToEnd drives runedHolder against a REAL runed: the
// filesystem step claims the folder over POST /v1/holds, the folder is closed
// to agent turns while the restore runs, and the release frees it again. This
// is the cross-daemon contract Z4 was missing — asserted through the wire,
// not through the Manager directly.
func TestRunedHolder_EndToEnd(t *testing.T) {
	ctx := context.Background()
	db, baseURL := liveRuned(t)

	dataDir, stores := openInstance(t)
	closeStores(stores)
	writeEnvFile(t, dataDir, "RUNED_URL="+baseURL+"\n")

	hold, err := runedHolder(ctx, dataDir)
	if err != nil {
		t.Fatalf("runedHolder: %v", err)
	}

	release, err := hold("atlas")
	if err != nil {
		t.Fatalf("hold atlas: %v", err)
	}

	// The slot is really claimed: an agent turn for atlas is refused busy.
	client := runedv1.NewClient(baseURL, "", 10*time.Second)
	out, err := client.Run(ctx, runedv1.RunRequest{Folder: "atlas", MessageBatch: "hi"})
	if err != nil {
		t.Fatalf("run during hold: %v", err)
	}
	if !out.Busy {
		t.Errorf("agent turn during restore outcome=%+v want busy=true", out)
	}
	if n, cerr := db.ActiveCount(); cerr != nil || n != 1 {
		t.Fatalf("ActiveCount=%d err=%v want 1 (the hold)", n, cerr)
	}

	// A second restore of the same folder cannot start.
	if _, err := hold("atlas"); err == nil {
		t.Error("a second hold on a held folder succeeded")
	}

	release()

	out, err = client.Run(ctx, runedv1.RunRequest{Folder: "atlas", MessageBatch: "hi"})
	if err != nil {
		t.Fatalf("run after release: %v", err)
	}
	if out.Busy {
		t.Errorf("agent turn still busy after the restore released the folder: %+v", out)
	}
}

// TestRunedHolder_UnreachableRunedFails: an unreachable runed must surface as
// an error the caller turns into a hard stop, never a silent unguarded
// restore. cmdArchiveApply dies on this; here we assert the holdFn produces it.
func TestRunedHolder_UnreachableRunedFails(t *testing.T) {
	// A listener we close immediately: a routable address that refuses.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	dataDir, stores := openInstance(t)
	closeStores(stores)
	writeEnvFile(t, dataDir, "RUNED_URL="+deadURL+"\n")

	hold, err := runedHolder(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("runedHolder: %v", err)
	}
	release, err := hold("atlas")
	if err == nil {
		t.Fatal("hold against an unreachable runed returned no error — the restore would proceed unguarded")
	}
	if release != nil {
		t.Error("a failed hold returned a release func")
	}
	if !strings.Contains(err.Error(), deadURL) {
		t.Errorf("error %q does not name the runed URL that failed", err)
	}
}

// writeEnvFile writes the instance .env runedHolder reads RUNED_URL from.
func writeEnvFile(t *testing.T, dataDir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
