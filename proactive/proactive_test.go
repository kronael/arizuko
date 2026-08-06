package proactive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeClaudeMD lays down a group CLAUDE.md with the given frontmatter body
// and returns its path.
func writeClaudeMD(t *testing.T, frontmatter string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte("---\n"+frontmatter+"\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestParseKeepsRawQuietHours pins the field the dashd view renders: the
// operator's own quiet-hours text, not a re-rendering of the parsed minutes.
// Showing "1320-480 Europe/Prague" back to someone who typed
// "22:00-08:00 Europe/Prague" would make them doubt the page.
func TestParseKeepsRawQuietHours(t *testing.T) {
	m := Parse(writeClaudeMD(t, "proactive:\n  mode: lurk\n  quiet_hours: ['22:00-08:00 Europe/Prague', '13:00-14:00 UTC']"))
	if m.Misconfigured {
		t.Fatalf("unexpected misconfigured: %s", m.Err)
	}
	if len(m.QuietHours) != 2 {
		t.Fatalf("QuietHours = %d windows, want 2", len(m.QuietHours))
	}
	want := []string{"22:00-08:00 Europe/Prague", "13:00-14:00 UTC"}
	for i, w := range want {
		if m.QuietHours[i].Raw != w {
			t.Errorf("QuietHours[%d].Raw = %q, want %q", i, m.QuietHours[i].Raw, w)
		}
	}
	// Raw is decoration only — the parsed bounds must still drive Contains.
	if m.QuietHours[0].StartMin != 22*60 || m.QuietHours[0].EndMin != 8*60 {
		t.Errorf("window 0 = %d-%d, want %d-%d",
			m.QuietHours[0].StartMin, m.QuietHours[0].EndMin, 22*60, 8*60)
	}
}

// TestParseMisconfiguredCarriesReason pins the case BUGS F24 calls the
// expensive one: a broken block must be distinguishable from a group that is
// deliberately silent, and must say why, or the dashboard cannot explain a
// group that went quiet.
func TestParseMisconfiguredCarriesReason(t *testing.T) {
	for _, tc := range []struct{ name, frontmatter, wantIn string }{
		{"unknown mode", "proactive:\n  mode: shout", "unknown mode shout"},
		{"bad window", "proactive:\n  mode: lurk\n  quiet_hours: ['always']", "quiet_hours always"},
		{"bad tz", "proactive:\n  mode: lurk\n  quiet_hours: ['22:00-08:00 Mars/Olympus']", "bad tz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Parse(writeClaudeMD(t, tc.frontmatter))
			if !m.Misconfigured {
				t.Fatalf("Misconfigured = false, want true (Name=%q)", m.Name)
			}
			if m.Err == "" {
				t.Fatal("Err is empty; the view has nothing to show the operator")
			}
			if !strings.Contains(m.Err, tc.wantIn) {
				t.Errorf("Err = %q, want it to mention %q", m.Err, tc.wantIn)
			}
			if m.Eligible() {
				t.Error("a misconfigured group must never be eligible to fire")
			}
		})
	}
}

// TestParseAbsentBlockIsSilentNotMisconfigured separates the two states the
// view must not conflate: no block at all is the default-off case, not an
// operator error.
func TestParseAbsentBlockIsSilentNotMisconfigured(t *testing.T) {
	for _, tc := range []struct{ name, frontmatter string }{
		{"no proactive key", "product: support"},
		{"empty mode", "proactive:\n  mode: ''"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Parse(writeClaudeMD(t, tc.frontmatter))
			if m.Misconfigured {
				t.Fatalf("Misconfigured = true (%s), want a plain silent default", m.Err)
			}
			if m.Name != "silent" {
				t.Errorf("Name = %q, want %q", m.Name, "silent")
			}
			if m.Eligible() {
				t.Error("silent must not be eligible")
			}
		})
	}
}

// TestParseNoFileIsSilent covers a folder with no CLAUDE.md at all.
func TestParseNoFileIsSilent(t *testing.T) {
	m := Parse(filepath.Join(t.TempDir(), "absent.md"))
	if m.Misconfigured || m.Name != "silent" {
		t.Fatalf("Parse(missing) = %+v, want silent and not misconfigured", m)
	}
}
