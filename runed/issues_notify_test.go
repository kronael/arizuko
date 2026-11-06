package runed

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

const issuesHook = "issues_notify.py"

// The core package mounts the hook and the merged settings.json runs it after
// every tool call, with no matcher: a Bash `cat >>` is a tool call too.
func TestCorePackageShipsIssuesNotify(t *testing.T) {
	cfg, folders := renderWiring(t)
	if _, _, err := container.InstallPackage(cfg.ProjectRoot, "../ant", false); err != nil {
		t.Fatal(err)
	}
	turn, err := renderTurn(cfg, folders, runedv1.RunRequest{Folder: "main", Packages: []string{"core"}}, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	guest := core.ContainerHome + "/.claude/hooks/" + issuesHook
	if mount := findMount(turn.Mounts, guest); mount == nil || !mount.RO {
		t.Fatalf("issues hook not mounted read-only at %s", guest)
	}
	data, err := os.ReadFile(filepath.Join(cfg.GroupsDir, "main", ".claude/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	post := settings.Hooks["PostToolUse"]
	if len(post) != 1 || post[0].Matcher != "" || len(post[0].Hooks) != 1 || post[0].Hooks[0].Command != "python3 "+guest {
		t.Fatalf("settings.json PostToolUse = %+v, want one unmatched entry running python3 %s: %s", post, guest, data)
	}
}

// runIssuesHook runs the hook once with `home` as $HOME and returns what it adds
// to the context ("" when it stays quiet).
func runIssuesHook(t *testing.T, home string) string {
	t.Helper()
	cmd := exec.Command("python3", "../ant/hooks/"+issuesHook)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"PostToolUse","tool_name":"Bash"}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook exited non-zero: %v", err)
	}
	if len(out) == 0 {
		return ""
	}
	var reply struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &reply); err != nil || reply.Output.Event != "PostToolUse" {
		t.Fatalf("hook output is not a PostToolUse reply: %q %v", out, err)
	}
	return reply.Output.Context
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestIssuesNotify(t *testing.T) {
	home := t.TempDir()
	issues := filepath.Join(home, "issues.md")

	if got := runIssuesHook(t, home); got != "" {
		t.Errorf("missing file: %q", got)
	}

	write(t, issues, "- [a/b/20261101/old] old entry\n")
	if got := runIssuesHook(t, home); got != "" {
		t.Errorf("first run on an existing file records the length and stays quiet: %q", got)
	}
	if got := runIssuesHook(t, home); got != "" {
		t.Errorf("unchanged file: %q", got)
	}

	appendTo(t, issues, "- [a/b/20261106/new] new entry\n")
	got := runIssuesHook(t, home)
	if !strings.Contains(got, "- [a/b/20261106/new] new entry") || !strings.Contains(got, `"#issues"`) || strings.Contains(got, "old entry") {
		t.Errorf("grown file: want the new line only and the #issues instruction: %q", got)
	}
	if got := runIssuesHook(t, home); got != "" {
		t.Errorf("the same growth nudges once: %q", got)
	}

	write(t, issues, "- [a/b/20261107/x] x\n")
	if got := runIssuesHook(t, home); got != "" {
		t.Errorf("a wiped and shorter file resets without a nudge: %q", got)
	}
}
