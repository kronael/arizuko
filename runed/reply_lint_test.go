package runed

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

const replyHook = "reply_lint.py"

// The reply ceiling hook reaches the guest like the other core hooks, and the
// merged settings.json runs it before the two chat-sending MCP tools only.
func TestCorePackageShipsReplyLint(t *testing.T) {
	cfg, folders := renderWiring(t)
	if _, _, err := container.InstallPackage(cfg.ProjectRoot, "../ant", false); err != nil {
		t.Fatal(err)
	}
	turn, err := renderTurn(cfg, folders, runedv1.RunRequest{Folder: "main", Packages: []string{"core"}}, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	guest := core.ContainerHome + "/.claude/hooks/" + replyHook
	if mount := findMount(turn.Mounts, guest); mount == nil || !mount.RO {
		t.Fatalf("reply hook not mounted read-only at %s", guest)
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
	pre := settings.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "mcp__arizuko__reply|mcp__arizuko__send" ||
		len(pre[0].Hooks) != 1 || pre[0].Hooks[0].Command != "python3 "+guest {
		t.Fatalf("settings.json PreToolUse = %+v, want one entry running python3 %s for the reply and send tools: %s", pre, guest, data)
	}
}

// runReplyHook feeds the hook one stdin payload and returns its stdout.
func runReplyHook(t *testing.T, input string) string {
	t.Helper()
	cmd := exec.Command("python3", "../ant/hooks/"+replyHook)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook exited non-zero (2 would block the call): %v", err)
	}
	return string(out)
}

func replyInput(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	input, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": args})
	if err != nil {
		t.Fatal(err)
	}
	return string(input)
}

func TestReplyLintScript(t *testing.T) {
	reasonOf := func(out string) string {
		t.Helper()
		var reply struct {
			Output struct {
				Event    string `json:"hookEventName"`
				Decision string `json:"permissionDecision"`
				Reason   string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(out), &reply); err != nil || reply.Output.Event != "PreToolUse" || reply.Output.Decision != "deny" {
			t.Fatalf("not a PreToolUse deny: %q %v", out, err)
		}
		return reply.Output.Reason
	}
	send := func(text string) string {
		return runReplyHook(t, replyInput(t, "mcp__arizuko__reply", map[string]any{"chatJid": "discord:1/2", "text": text}))
	}

	long := strings.Repeat("word ", 120)
	reason := reasonOf(send(long))
	if !strings.Contains(reason, "599 chars / 1 lines") || !strings.Contains(reason, "ewrite it shorter") {
		t.Errorf("deny reason lacks the measured counts or the instruction: %q", reason)
	}
	if !strings.Contains(reason, "every fact, link and number") {
		t.Errorf("deny reason must say to keep every fact, link and number: %q", reason)
	}
	if !strings.Contains(reasonOf(send("a\nb\nc\nd\ne\nf\ng")), "7 lines") {
		t.Error("seven short lines must be denied on the line count")
	}
	if !strings.Contains(reasonOf(runReplyHook(t, replyInput(t, "mcp__arizuko__send", map[string]any{"text": long}))), "chars") {
		t.Error("send must be linted like reply")
	}

	if out := send("short answer"); out != "" {
		t.Errorf("under the ceiling must stay silent: %q", out)
	}
	if out := send(strings.Repeat("x", 500)); out != "" {
		t.Errorf("exactly the ceiling must stay silent: %q", out)
	}
	fenced := "see:\n```go\n" + strings.Repeat("code line\n", 40) + "```\ndone"
	if out := send(fenced); out != "" {
		t.Errorf("a code fence must not count: %q", out)
	}
	if out := send("see:\n```\n" + strings.Repeat("code line\n", 40)); out != "" {
		t.Errorf("an unclosed code fence must not count: %q", out)
	}
	if out := runReplyHook(t, replyInput(t, "mcp__arizuko__send_file", map[string]any{"caption": long})); out != "" {
		t.Errorf("a file caption is not linted: %q", out)
	}

	for _, garbage := range []string{"", "not json", "[]", "null", `{"tool_input":null}`, `{"tool_input":"x"}`, `{"tool_input":{"text":7}}`, `{"tool_input":{"text":null}}`} {
		if out := runReplyHook(t, garbage); out != "" {
			t.Errorf("garbage stdin %q must produce no output: %q", garbage, out)
		}
	}
}

// The number the hook enforces is the number the manual states.
func TestReplyLintCeilingMatchesManual(t *testing.T) {
	reason := regexp.MustCompile(`ceiling (\d+) chars / (\d+) lines`).FindStringSubmatch(
		runReplyHook(t, replyInput(t, "mcp__arizuko__reply", map[string]any{"text": strings.Repeat("x", 5000)})))
	if reason == nil {
		t.Fatal("deny reason does not state the ceiling")
	}
	manual, err := os.ReadFile("../ant/CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Ceiling ~%s chars / %s lines", reason[1], reason[2])
	if !strings.Contains(string(manual), want) {
		t.Fatalf("ant/CLAUDE.md does not state %q, the ceiling reply_lint.py enforces", want)
	}
}
