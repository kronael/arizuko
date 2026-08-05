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
	if err := store.New(s.SQL()).CreateAuthUser("google:bob", "bob", "Bob"); err != nil {
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

// TestRunBudgetShow_Folder also pins the spend half to routd.db: PutCost is
// routd's cost_log writer (cost_cents/recorded_at), and `budget show` must sum
// exactly those rows — the store-schema reader summed ts/cents and saw nothing.
func TestRunBudgetShow_Folder(t *testing.T) {
	s := budgetDB(t)
	if err := s.PutGroup(core.Group{Folder: "team"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.SetFolderCap("team", 100); err != nil {
		t.Fatalf("SetFolderCap: %v", err)
	}
	if err := s.PutCost("team", "t1", "", "m", 0, 0, 25); err != nil {
		t.Fatalf("PutCost: %v", err)
	}
	var buf bytes.Buffer
	if err := runBudgetShow(s, "folder", "team", &buf); err != nil {
		t.Fatalf("runBudgetShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"team", "100 cents/day", "25 cents", "75 cents", "25%"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunBudgetShow_User(t *testing.T) {
	s := budgetDB(t)
	if err := store.New(s.SQL()).CreateAuthUser("google:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := s.SetUserCap("google:bob", 200); err != nil {
		t.Fatalf("SetUserCap: %v", err)
	}
	if err := s.PutCost("team", "t1", "google:bob", "m", 0, 0, 40); err != nil {
		t.Fatalf("PutCost: %v", err)
	}
	var buf bytes.Buffer
	if err := runBudgetShow(s, "user", "google:bob", &buf); err != nil {
		t.Fatalf("runBudgetShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"google:bob", "200 cents/day", "40 cents", "160 cents", "20%"} {
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
	if strings.Contains(buf.String(), "remaining") {
		t.Errorf("uncapped output should not show remaining: %q", buf.String())
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

func TestBudgetStatus_Thresholds(t *testing.T) {
	for _, c := range []struct {
		spent, cap int
		want       string
	}{
		{10, 100, "ok — 10%"},
		{80, 100, "WARN"},
		{100, 100, "EXHAUSTED"},
		{200, 100, "EXHAUSTED"},
	} {
		got := budgetStatus(c.spent, c.cap)
		if !strings.Contains(got, c.want) {
			t.Errorf("status(%d,%d) = %q, want substring %q", c.spent, c.cap, got, c.want)
		}
	}
}
