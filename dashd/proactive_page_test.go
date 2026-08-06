package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// proactiveDB is routdDB plus chat_proactive, the cooldown table the page
// reads (routd migration 0002).
func proactiveDB(t *testing.T) *sql.DB {
	t.Helper()
	db := routdDB(t)
	if _, err := db.Exec(
		`CREATE TABLE chat_proactive (jid TEXT PRIMARY KEY, proactive_last_fired_at TEXT)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// seedGroup registers a group row and writes its CLAUDE.md frontmatter under
// groupsDir. frontmatter "" writes no file at all.
func seedGroup(t *testing.T, db *sql.DB, groupsDir, folder, frontmatter string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO groups(folder, name, added_at, parent) VALUES(?,?,?,?)`,
		folder, folder, "2026-08-06T10:00:00Z", ""); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if frontmatter == "" {
		return
	}
	dir := filepath.Join(groupsDir, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"),
		[]byte("---\n"+frontmatter+"\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// getProactivePage GETs /dash/proactive/ as an operator and returns the body.
func getProactivePage(t *testing.T, d *dash) string {
	t.Helper()
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, asOperator(httptest.NewRequest("GET", "/dash/proactive/", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	return w.Body.String()
}

// sectionBetween returns the slice of body between two markers, failing if
// either is absent. Whole-page Contains is worthless here: the word
// "proactive" appears in the nav link and the intro prose, so an assertion
// against the full body would pass with both tables empty.
func sectionBetween(t *testing.T, body, from, to string) string {
	t.Helper()
	i := strings.Index(body, from)
	if i < 0 {
		t.Fatalf("marker %q missing from page", from)
	}
	rest := body[i+len(from):]
	if to == "" {
		return rest
	}
	j := strings.Index(rest, to)
	if j < 0 {
		t.Fatalf("marker %q missing after %q", to, from)
	}
	return rest[:j]
}

const (
	modesFrom = `<h2>Group settings</h2>`
	modesTo   = `<h2>Chats that spoke first</h2>`
)

// TestProactivePageRendersModes is the view half of BUGS F24: which groups
// lurk, which are silent, and which are broken — the three states that
// previously meant opening every group's CLAUDE.md by hand.
func TestProactivePageRendersModes(t *testing.T) {
	db := proactiveDB(t)
	defer db.Close()
	groupsDir := t.TempDir()
	seedGroup(t, db, groupsDir, "corp/eng", "proactive:\n  mode: lurk\n  quiet_hours: ['22:00-08:00 Europe/Prague']")
	seedGroup(t, db, groupsDir, "corp/sales", "product: support")
	seedGroup(t, db, groupsDir, "corp/ops", "proactive:\n  mode: shout")

	modes := sectionBetween(t, getProactivePage(t, &dash{dbRoutd: db, groupsDir: groupsDir}), modesFrom, modesTo)

	// Every group is listed at all — assert existence before properties.
	for _, folder := range []string{"corp/eng", "corp/sales", "corp/ops"} {
		if !strings.Contains(modes, folder) {
			t.Fatalf("group %q missing from the modes table:\n%s", folder, modes)
		}
	}
	// The lurking group is marked lurk, and its quiet window is shown verbatim.
	if !strings.Contains(modes, `<span class="status-ok">lurk</span>`) {
		t.Errorf("no lurk cell in the modes table:\n%s", modes)
	}
	if !strings.Contains(modes, "22:00-08:00 Europe/Prague") {
		t.Errorf("quiet window not rendered:\n%s", modes)
	}
	// The broken group is broken, and says why.
	if !strings.Contains(modes, "broken") {
		t.Errorf("misconfigured group not marked broken:\n%s", modes)
	}
	if !strings.Contains(modes, "unknown mode shout") {
		t.Errorf("parse error not shown to the operator:\n%s", modes)
	}
	// Negative: exactly one group lurks. If the page defaulted every group to
	// lurk — or the mode cell ignored its argument — this catches it.
	if n := strings.Count(modes, `<span class="status-ok">lurk</span>`); n != 1 {
		t.Errorf("lurk cells = %d, want exactly 1:\n%s", n, modes)
	}
	if !strings.Contains(modes, `<span class="dim">silent</span>`) {
		t.Errorf("the plain group is not rendered silent:\n%s", modes)
	}
}

// TestProactivePageSeparatesBrokenFromSilent pins the distinction spec 5/6
// refuses to collapse: a typo must not read as a deliberate off switch.
func TestProactivePageSeparatesBrokenFromSilent(t *testing.T) {
	db := proactiveDB(t)
	defer db.Close()
	groupsDir := t.TempDir()
	seedGroup(t, db, groupsDir, "corp/broken", "proactive:\n  mode: lurk\n  quiet_hours: ['whenever']")

	modes := sectionBetween(t, getProactivePage(t, &dash{dbRoutd: db, groupsDir: groupsDir}), modesFrom, modesTo)
	if !strings.Contains(modes, "broken") {
		t.Fatalf("broken group not marked broken:\n%s", modes)
	}
	if strings.Contains(modes, `<span class="dim">silent</span>`) {
		t.Errorf("a broken group was coerced to silent:\n%s", modes)
	}
	if !strings.Contains(modes, "quiet_hours whenever") {
		t.Errorf("the reason it is broken is not shown:\n%s", modes)
	}
}

// TestProactivePageRendersCooldown is the other half of F24: when a chat last
// spoke unprompted, previously reachable only by SQL.
func TestProactivePageRendersCooldown(t *testing.T) {
	db := proactiveDB(t)
	defer db.Close()
	groupsDir := t.TempDir()
	seedGroup(t, db, groupsDir, "corp/eng", "proactive:\n  mode: lurk")
	if _, err := db.Exec(
		`INSERT INTO chat_proactive(jid, proactive_last_fired_at) VALUES(?,?)`,
		"slack:T1/C1", "2026-08-06T09:00:00Z"); err != nil {
		t.Fatal(err)
	}

	body := getProactivePage(t, &dash{dbRoutd: db, groupsDir: groupsDir})
	cooldown := sectionBetween(t, body, modesTo, "")
	if !strings.Contains(cooldown, "slack:T1/C1") {
		t.Fatalf("fired chat missing from the cooldown table:\n%s", cooldown)
	}
	if !strings.Contains(cooldown, "2026-08-06T09:00:00Z") {
		t.Errorf("fire timestamp missing from the cooldown table:\n%s", cooldown)
	}
	// The jid must not leak into the modes table above it.
	if strings.Contains(sectionBetween(t, body, modesFrom, modesTo), "slack:T1/C1") {
		t.Error("chat jid rendered in the group modes table")
	}
}

// TestProactivePageEmptyStates: a fresh instance shows why each table is
// empty rather than an unexplained blank.
func TestProactivePageEmptyStates(t *testing.T) {
	db := proactiveDB(t)
	defer db.Close()
	body := getProactivePage(t, &dash{dbRoutd: db, groupsDir: t.TempDir()})
	if !strings.Contains(body, "No groups yet.") {
		t.Errorf("no empty state for the modes table:\n%s", body)
	}
	if !strings.Contains(body, "No chat has spoken unprompted yet.") {
		t.Errorf("no empty state for the cooldown table:\n%s", body)
	}
}

// TestProactivePageStatesFeatureIsOff: CHANGELOG's "not yet switched on" is
// true, and the page must not imply otherwise by rendering modes with no
// caveat. PROACTIVE_ENABLED defaults false (routd/proactive.go) and no
// template sets it.
func TestProactivePageStatesFeatureIsOff(t *testing.T) {
	db := proactiveDB(t)
	defer db.Close()
	body := getProactivePage(t, &dash{dbRoutd: db, groupsDir: t.TempDir()})
	if !strings.Contains(body, "PROACTIVE_ENABLED") {
		t.Errorf("page does not name the env var an operator must set:\n%s", body)
	}
	if !strings.Contains(body, `class="banner-warn"`) {
		t.Errorf("the off-by-default caveat is not a banner:\n%s", body)
	}
}

// TestProactivePageNonOperatorForbidden: the page exposes every folder's
// settings, so it is operator-only like /dash/audit/ and /dash/runed/.
func TestProactivePageNonOperatorForbidden(t *testing.T) {
	db := proactiveDB(t)
	defer db.Close()
	groupsDir := t.TempDir()
	seedGroup(t, db, groupsDir, "corp/eng", "proactive:\n  mode: lurk")

	d := &dash{dbRoutd: db, groupsDir: groupsDir}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/proactive/", nil)
	req.Header.Set("X-User-Sub", "github:regular")
	req.Header.Set("X-User-Groups", `["corp/eng"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "corp/eng") {
		t.Error("a non-operator was shown a group's proactive settings")
	}
}

// TestProactivePageConfinesToGroupsDir: a groups row shaped like a traversal
// must not make the page parse a CLAUDE.md outside the groups tree.
func TestProactivePageConfinesToGroupsDir(t *testing.T) {
	db := proactiveDB(t)
	defer db.Close()
	base := t.TempDir()
	groupsDir := filepath.Join(base, "groups")
	if err := os.MkdirAll(groupsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A lurking CLAUDE.md OUTSIDE the groups tree.
	if err := os.WriteFile(filepath.Join(base, "CLAUDE.md"),
		[]byte("---\nproactive:\n  mode: lurk\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedGroup(t, db, groupsDir, "../", "")

	modes := sectionBetween(t, getProactivePage(t, &dash{dbRoutd: db, groupsDir: groupsDir}), modesFrom, modesTo)
	if strings.Contains(modes, `<span class="status-ok">lurk</span>`) {
		t.Fatalf("read a CLAUDE.md outside the groups dir:\n%s", modes)
	}
}
