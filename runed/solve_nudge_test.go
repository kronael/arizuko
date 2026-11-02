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

const solveHook = "solve-nudge.sh"

// The core package is the one thing every turn selects, so the hook it ships
// must reach the guest: mounted under ~/.claude/hooks and named by the merged
// settings.json. Installs the real ant/ the way routd does at start.
func TestCorePackageShipsTheSolveNudgeHook(t *testing.T) {
	cfg, folders := renderWiring(t)
	if _, _, err := container.InstallPackage(cfg.ProjectRoot, "../ant", false); err != nil {
		t.Fatal(err)
	}
	turn, err := renderTurn(cfg, folders, runedv1.RunRequest{Folder: "main", Packages: []string{"core"}}, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	guest := core.ContainerHome + "/.claude/hooks/" + solveHook
	mount := findMount(turn.Mounts, guest)
	if mount == nil || !mount.RO {
		t.Fatalf("core hook not mounted read-only at %s", guest)
	}
	want, err := os.ReadFile("../ant/hooks/" + solveHook)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(mount.Host); err != nil || string(got) != string(want) {
		t.Fatalf("mounted hook differs from ant/hooks/%s: %v", solveHook, err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.GroupsDir, "main", ".claude/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	entries := settings.Hooks["UserPromptSubmit"]
	if len(entries) != 1 || len(entries[0].Hooks) != 1 || !strings.HasSuffix(entries[0].Hooks[0].Command, " "+guest) {
		t.Fatalf("settings.json UserPromptSubmit does not run %s: %s", guest, data)
	}
}

// runSolveHook feeds the hook one UserPromptSubmit payload and returns what it
// adds to the context ("" when it stays quiet).
func runSolveHook(t *testing.T, prompt, transcript string) string {
	t.Helper()
	input, err := json.Marshal(map[string]string{"prompt": prompt, "transcript_path": transcript})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "../ant/hooks/"+solveHook)
	cmd.Stdin = strings.NewReader(string(input))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook exited non-zero (2 would block the prompt): %v", err)
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
	if err := json.Unmarshal(out, &reply); err != nil || reply.Output.Event != "UserPromptSubmit" {
		t.Fatalf("hook output is not a UserPromptSubmit reply: %q %v", out, err)
	}
	return reply.Output.Context
}

// transcriptAged writes a transcript whose newest assistant entry is `age` old,
// after the queue-operation lines Claude Code writes for the current prompt
// before the hook runs.
func transcriptAged(t *testing.T, age time.Duration) string {
	t.Helper()
	stamp := func(d time.Duration) string { return time.Now().Add(-d).UTC().Format("2006-01-02T15:04:05.000Z") }
	body := `{"type":"user","timestamp":"` + stamp(age+time.Minute) + `"}` + "\n" +
		`{"type":"assistant","timestamp":"` + stamp(age) + `"}` + "\n" +
		`{"type":"queue-operation","operation":"enqueue","timestamp":"` + stamp(0) + `"}` + "\n"
	path := filepath.Join(t.TempDir(), "session.jsonl")
	write(t, path, body)
	return path
}

func TestSolveNudgeScript(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is in the ant image; this host has none")
	}
	if got := runSolveHook(t, "fix the bug", filepath.Join(t.TempDir(), "absent.jsonl")); !strings.Contains(got, "first prompt this session") || !strings.Contains(got, "/solve") {
		t.Errorf("first prompt: %q", got)
	}
	queueOnly := filepath.Join(t.TempDir(), "queue.jsonl")
	write(t, queueOnly, `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-02T20:24:46.015Z"}`+"\n")
	if got := runSolveHook(t, "fix the bug", queueOnly); !strings.Contains(got, "first prompt this session") {
		t.Errorf("a transcript with no user or assistant entry is a first prompt: %q", got)
	}
	if got := runSolveHook(t, "fix the bug", transcriptAged(t, 5*time.Minute)); got != "" {
		t.Errorf("recent session must stay quiet: %q", got)
	}
	if got := runSolveHook(t, "fix the bug", transcriptAged(t, 2*time.Hour)); !strings.Contains(got, "idle gap") {
		t.Errorf("idle session: %q", got)
	}
	if got := runSolveHook(t, "please run /solve first", transcriptAged(t, 2*time.Hour)); got != "" {
		t.Errorf("a prompt that names /solve gets no nudge: %q", got)
	}
	if got := runSolveHook(t, "run /solve", filepath.Join(t.TempDir(), "absent.jsonl")); got != "" {
		t.Errorf("a first prompt that names /solve gets no nudge: %q", got)
	}
}
