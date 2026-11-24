package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const digestPost = "../packages/company-brain/skills/digest/scripts/post_blocks.py"

// fakeMcpc connects and closes, and fails every tool call with an error that
// is not a "message gone" marker, so post_blocks.py records it as a failure.
const fakeMcpc = `#!/bin/sh
case "$*" in
*tools-call*) echo "rate limited" >&2; exit 1 ;;
esac
exit 0
`

// TestDigestPostFailedApplyKeepsState (L934): an --apply whose edit fails must
// leave the state file as it was, so last_end and posted_run do not advance
// past a digest that never landed.
func TestDigestPostFailedApplyKeepsState(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	run := filepath.Join(home, "run")
	for _, dir := range []string{bin, run} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	statePath := filepath.Join(home, "state.json")
	const state = `{"slots": {"intro": "111"}, "last_end": "2026-11-06T09:00:00Z", "posted_run": "20261106", "threads": {}}`
	files := map[string]string{
		filepath.Join(bin, "mcpc"):         fakeMcpc,
		filepath.Join(home, "digest.toml"): "post_jid = \"discord:1/2\"\nbot_user = \"discord:user/3\"\nagent_name = \"agent\"\n[humans]\n",
		statePath:                          state,
		filepath.Join(run, "blocks.json"):  `[{"slot": "intro", "text": "new digest"}]`,
		filepath.Join(run, "window.txt"): "RUN_ID=20261107\nSTART=2026-11-06T09:00:00Z\nEND=2026-11-07T09:00:00Z\n" +
			"STATE=" + statePath + "\nHISTORY=" + filepath.Join(home, "history") + "\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("python3", digestPost, run, "--apply")
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Failed: edit intro 111") {
		t.Fatalf("want exit 1 naming the failed edit, got err=%v out=%s", err, out)
	}
	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != state {
		t.Fatalf("state changed after a failed apply:\n%s\nout=%s", got, out)
	}
}
