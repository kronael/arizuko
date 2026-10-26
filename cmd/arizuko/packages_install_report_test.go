package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// The install summary counted files and routes and said nothing about grants —
// the one asset kind that changes who can do what, and the only one that is live
// the moment it lands rather than at the next `generate` (BUGS L36). The audit
// trail was already per-grant (Q5); the operator's terminal is the other half of
// the same argument, so it names the principal and the scope rather than a count.
func TestPackageInstallSummaryNamesEachGrant(t *testing.T) {
	out := captureStdout(t, func() { installGrantPkg(t, "alice") })

	if !strings.Contains(out, "acl: 2 row(s)") {
		t.Errorf("summary does not count the grants beside the files: %q", out)
	}
	// installGrantPkg's package hands @ops two different authorities on two
	// different scopes. A count cannot distinguish them; the point of the line is
	// that `admin on eng/tts` is visible without querying the DB.
	for _, want := range []string{"@ops mcp:reply on eng/*", "@ops admin on eng/tts"} {
		if !strings.Contains(out, want) {
			t.Errorf("install summary does not name the grant %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "allow:") {
		t.Errorf("grant line omits the effect, so a deny row would read as a grant: %q", out)
	}
}

// The summary counted a hand-picked pair of asset kinds — routes and grants —
// while the apply writes whatever the manifest engine understands. So `add` of
// a catalog package declaring a scheduled task and an egress rule installed
// both, live, and printed "0 route(s), 0 grant(s) — run `arizuko generate` to
// apply": the operator was told nothing had happened and that the effect was
// still ahead of them (BUGS L199). The report is derived from the apply's own
// result now, so every resource a package declares is named by construction.
func TestPackageAddSummaryNamesEveryAppliedResource(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	dataDir := seedSyncInstance(t, "rep",
		map[string]string{
			"crondpkg.yml": "services:\n  crondpkg:\n    image: x\n",
			"crondpkg/arizuko.yml": "name: crondpkg\n" + ("network_rules:\n  - folder: ''\n    target: api.crond.example\n" +
				"scheduled_tasks:\n  - id: crondpkg-nightly\n    owner: crondpkg\n" +
				"    chat_jid: web:ops\n    prompt: run the nightly report\n    cron: '0 3 * * *'\n"),
		},
		map[string]string{})
	seedRoutdDB(t, dataDir)

	out := captureStdout(t, func() { cmdPackages([]string{"rep", "add", "crondpkg"}) })

	for _, want := range []string{"network_rules: 1 row(s)", "scheduled_tasks: 1 row(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("add summary does not name %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "applied now") {
		t.Errorf("add summary does not say the rows are already applied: %q", out)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stdout = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return <-done
}
