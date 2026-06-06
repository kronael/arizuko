package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestRunBudgetSet_Folder(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
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

func TestRunBudgetSet_FolderUncap(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
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
	s, _ := store.OpenMem()
	defer s.Close()
	err := runBudgetSet(s, "folde", "x", 100, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "scope must be") {
		t.Errorf("err = %v", err)
	}
}

func TestRunBudgetShow_Folder(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
	if err := s.PutGroup(core.Group{Folder: "team"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.SetFolderCap("team", 100); err != nil {
		t.Fatalf("SetFolderCap: %v", err)
	}
	if err := s.LogCost(store.CostRow{Folder: "team", Model: "m", Cents: 25}); err != nil {
		t.Fatalf("LogCost: %v", err)
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

func TestRunBudgetShow_Uncapped(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
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
