package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// budgetDB is an in-memory ROUTD.DB — the DB routd's budgetGate enforces from.
// The verb used to run against store.OpenMem (the frozen messages.db schema),
// which is why every cap it wrote was invisible to enforcement (BUGS.md Q1).
func budgetDB(t *testing.T) *routd.DB {
	t.Helper()
	db, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRunBudgetSet_Folder(t *testing.T) {
	s := budgetDB(t)
	if err := s.PutGroup(core.Group{Folder: "atlas/eng"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var buf bytes.Buffer
	if err := runBudgetSet(s, "folder", "atlas/eng", 200, &buf); err != nil {
		t.Fatalf("runBudgetSet: %v", err)
	}
	got, err := s.FolderCap("atlas/eng")
	if err != nil {
		t.Fatal(err)
	}
	if got != 200 {
		t.Errorf("FolderCap = %d, want 200", got)
	}
	if !strings.Contains(buf.String(), "capped at 200 cents") {
		t.Errorf("output = %q", buf.String())
	}
}

// TestRunBudgetSet_ReachesEnforcementReads is the Q1 regression: the cap the
// verb writes must be visible to the EXACT readers routd's budgetGate calls
// (DB.FolderCap / DB.UserCap on routd.db). Against the frozen messages.db this
// read returned 0 no matter what `budget set` wrote.
func TestRunBudgetSet_ReachesEnforcementReads(t *testing.T) {
	s := budgetDB(t)
	if err := s.PutGroup(core.Group{Folder: "team"}); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := store.NewOver(s.Obj()).CreateUserProfile("google:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if err := runBudgetSet(s, "folder", "team", 250, new(bytes.Buffer)); err != nil {
		t.Fatalf("runBudgetSet folder: %v", err)
	}
	if err := runBudgetSet(s, "user", "google:bob", 75, new(bytes.Buffer)); err != nil {
		t.Fatalf("runBudgetSet user: %v", err)
	}

	// The two reads budgetGate makes, on the same handle routd opens.
	if got, err := s.FolderCap("team"); err != nil || got != 250 {
		t.Errorf("budgetGate's FolderCap = %d (err %v), want 250", got, err)
	}
	if got, err := s.UserCap("google:bob"); err != nil || got != 75 {
		t.Errorf("budgetGate's UserCap = %d (err %v), want 75", got, err)
	}
}

func TestRunBudgetSet_FolderUncap(t *testing.T) {
	s := budgetDB(t)
	if err := s.PutGroup(core.Group{Folder: "f"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var buf bytes.Buffer
	if err := runBudgetSet(s, "folder", "f", 0, &buf); err != nil {
		t.Fatalf("runBudgetSet: %v", err)
	}
	if !strings.Contains(buf.String(), "uncapped") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunBudgetSet_RejectsBadScope(t *testing.T) {
	s := budgetDB(t)
	err := runBudgetSet(s, "folde", "x", 100, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "scope must be") {
		t.Errorf("err = %v", err)
	}
}

// `budget show` reports the CAP, which is arizuko's own policy row, and says
// where the spend lives. It cannot report spend: the gateway meters it and
// reaching it needs the master key this CLI deliberately does not hold, so a
// number here would either be stale or be a second ledger (spec 6/39).
func TestRunBudgetShow_Folder(t *testing.T) {
	s := budgetDB(t)
	if err := s.PutGroup(core.Group{Folder: "team"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.SetFolderCap("team", 100); err != nil {
		t.Fatalf("SetFolderCap: %v", err)
	}
	var buf bytes.Buffer
	if err := runBudgetShow(s, "folder", "team", &buf); err != nil {
		t.Fatalf("runBudgetShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"team", "100 cents/day", "metered by the model gateway"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunBudgetShow_User(t *testing.T) {
	s := budgetDB(t)
	if err := store.NewOver(s.Obj()).CreateUserProfile("google:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := s.SetUserCap("google:bob", 200); err != nil {
		t.Fatalf("SetUserCap: %v", err)
	}
	var buf bytes.Buffer
	if err := runBudgetShow(s, "user", "google:bob", &buf); err != nil {
		t.Fatalf("runBudgetShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"google:bob", "200 cents/day"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunBudgetShow_Uncapped(t *testing.T) {
	s := budgetDB(t)
	if err := s.PutGroup(core.Group{Folder: "f"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var buf bytes.Buffer
	if err := runBudgetShow(s, "folder", "f", &buf); err != nil {
		t.Fatalf("runBudgetShow: %v", err)
	}
	if !strings.Contains(buf.String(), "uncapped") {
		t.Errorf("output = %q", buf.String())
	}
}

// TestParseBudgetSet proves flexParse + the -d alias: --daily/-d parses whether
// before, after, or interspersed with the <scope> <target> positionals; a wrong
// positional count or an unset (negative) --daily errors instead of silently
// dropping a misplaced flag.
func TestParseBudgetSet(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantErr    bool
		wantScope  string
		wantTarget string
		wantDaily  int
	}{
		{name: "long flag before positionals", args: []string{"--daily", "200", "folder", "f"}, wantScope: "folder", wantTarget: "f", wantDaily: 200},
		{name: "short flag after positionals", args: []string{"folder", "f", "-d", "200"}, wantScope: "folder", wantTarget: "f", wantDaily: 200},
		{name: "long flag after positionals", args: []string{"user", "u", "--daily", "0"}, wantScope: "user", wantTarget: "u", wantDaily: 0},
		{name: "flag interspersed", args: []string{"folder", "--daily", "50", "f"}, wantScope: "folder", wantTarget: "f", wantDaily: 50},
		{name: "missing daily errors", args: []string{"folder", "f"}, wantErr: true},
		{name: "one positional errors", args: []string{"folder", "-d", "10"}, wantErr: true},
		{name: "three positionals errors", args: []string{"folder", "f", "extra", "-d", "10"}, wantErr: true},
		{name: "unknown flag errors", args: []string{"folder", "f", "--nope", "1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope, target, daily, err := parseBudgetSet(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseBudgetSet(%v) = nil error, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBudgetSet(%v) error: %v", tc.args, err)
			}
			if scope != tc.wantScope || target != tc.wantTarget || daily != tc.wantDaily {
				t.Errorf("parseBudgetSet(%v) = (%q, %q, %d), want (%q, %q, %d)",
					tc.args, scope, target, daily, tc.wantScope, tc.wantTarget, tc.wantDaily)
			}
		})
	}
}
