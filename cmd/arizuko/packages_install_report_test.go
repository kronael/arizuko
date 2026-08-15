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

	if !strings.Contains(out, "3 grant(s)") && !strings.Contains(out, "2 grant(s)") {
		t.Errorf("summary does not count grants beside files and routes: %q", out)
	}
	// installGrantPkg's package hands @ops two different authorities on two
	// different scopes. A count cannot distinguish them; the point of the line is
	// that `admin on eng/tts` is visible without querying the DB.
	for _, want := range []string{"@ops reply on eng/*", "@ops admin on eng/tts"} {
		if !strings.Contains(out, want) {
			t.Errorf("install summary does not name the grant %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "allow:") {
		t.Errorf("grant line omits the effect, so a deny row would read as a grant: %q", out)
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
