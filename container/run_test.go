package container

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/groupfolder"
)

// fakeExec replaces execCommand so Run() spawns a helper binary
// (go test harness re-entry) instead of the real docker CLI. The helper
// reads ARIZUKO_TEST_OUT from env and writes it to stdout with the exit
// code in ARIZUKO_TEST_EXIT.
func fakeExec(t *testing.T, capture *[]string, stdout string, exitCode int) func() {
	t.Helper()
	prev := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		all := append([]string{name}, args...)
		*capture = append(*capture, strings.Join(all, " "))
		helper := []string{"-test.run=TestRunHelperProcess", "--"}
		helper = append(helper, args...)
		cmd := exec.Command(os.Args[0], helper...)
		cmd.Env = append(os.Environ(),
			"ARIZUKO_TEST_HELPER=1",
			"ARIZUKO_TEST_OUT="+stdout,
			fmt.Sprintf("ARIZUKO_TEST_EXIT=%d", exitCode),
		)
		return cmd
	}
	return func() { execCommand = prev }
}

// TestRunHelperProcess is the re-entry target for fakeExec. It emits
// ARIZUKO_TEST_OUT on stdout and exits with ARIZUKO_TEST_EXIT.
func TestRunHelperProcess(t *testing.T) {
	if os.Getenv("ARIZUKO_TEST_HELPER") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, os.Getenv("ARIZUKO_TEST_OUT"))
	code := 0
	fmt.Sscanf(os.Getenv("ARIZUKO_TEST_EXIT"), "%d", &code)
	os.Exit(code)
}

func makeCfg(t *testing.T) (*core.Config, *groupfolder.Resolver, string) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &core.Config{
		Name:        "testinst",
		Image:       "arizuko-ant:test",
		GroupsDir:   filepath.Join(tmp, "groups"),
		IpcDir:      filepath.Join(tmp, "ipc"),
		HostAppDir:  filepath.Join(tmp, "app"),
		ProjectRoot: tmp,
		Timezone:    "UTC",
		IdleTimeout: 30 * time.Second,
		Timeout:     60 * time.Second,
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	return cfg, folders, tmp
}

func TestRun_ExitCodes(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
		wantErr  bool
	}{
		{name: "clean exit", exitCode: 0, wantErr: false},
		{name: "exit code 1 -> error", exitCode: 1, wantErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, folders, _ := makeCfg(t)
			var captured []string
			restore := fakeExec(t, &captured, "", c.exitCode)
			defer restore()

			out := Run(cfg, folders, Input{
				Prompt:  "hello",
				ChatJID: "tg:1",
				Folder:  "g",
				Name:    "arizuko-test-x",
			})

			if c.wantErr {
				if out.Error == "" && out.Status != "error" {
					t.Fatalf("want error, got %+v", out)
				}
				return
			}
			if out.Status != "success" {
				t.Errorf("status=%q want success (out=%+v)", out.Status, out)
			}
		})
	}
}

func TestRun_DockerArgAssembly(t *testing.T) {
	cfg, folders, _ := makeCfg(t)
	var captured []string
	restore := fakeExec(t, &captured, "", 0)
	defer restore()

	out := Run(cfg, folders, Input{
		Prompt:  "p",
		ChatJID: "tg:1",
		Folder:  "g",
		Name:    "arizuko-argtest",
	})
	if out.Status != "success" {
		t.Fatalf("unexpected output: %+v", out)
	}

	if len(captured) == 0 {
		t.Fatal("no commands captured")
	}
	cmd := captured[0]
	// Required pieces of the docker invocation.
	for _, want := range []string{
		"docker", "run", "-i", "--rm",
		"--name", "arizuko-argtest",
		"--shm-size=1g",
		"-e", "TZ=UTC",
		cfg.Image,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("docker args missing %q:\n%s", want, cmd)
		}
	}
	// Expect an IPC mount (/workspace/ipc) since resolver gives a path.
	if !strings.Contains(cmd, "/workspace/ipc") {
		t.Errorf("expected /workspace/ipc mount in args:\n%s", cmd)
	}
	// Expect the workspace/self read-only mount to the HostAppDir.
	if !strings.Contains(cmd, "/workspace/self:ro") {
		t.Errorf("expected /workspace/self:ro mount:\n%s", cmd)
	}
}

func TestRun_CodexDirMountWhenSet(t *testing.T) {
	cfg, folders, _ := makeCfg(t)
	// Use a literal HOST path; runner does not stat it (gated runs
	// in-container, cfg.HostCodexDir is resolved by the docker
	// daemon at agent-spawn time).
	cfg.HostCodexDir = "/host/codex"

	var captured []string
	restore := fakeExec(t, &captured, "", 0)
	defer restore()

	out := Run(cfg, folders, Input{
		Prompt: "p", ChatJID: "tg:1", Folder: "g", Name: "arizuko-codex",
	})
	if out.Status != "success" {
		t.Fatalf("unexpected output: %+v", out)
	}
	cmd := captured[0]
	// Per-group writable .codex/ first.
	wantGroup := filepath.Join(cfg.GroupsDir, "g", ".codex") + ":/home/node/.codex"
	if !strings.Contains(cmd, wantGroup) {
		t.Errorf("expected per-group codex mount %q:\n%s", wantGroup, cmd)
	}
	// RO overmounts of auth.json + config.toml from host.
	wantAuth := "/host/codex/auth.json:/home/node/.codex/auth.json:ro"
	if !strings.Contains(cmd, wantAuth) {
		t.Errorf("expected ro auth.json overmount %q:\n%s", wantAuth, cmd)
	}
	wantCfg := "/host/codex/config.toml:/home/node/.codex/config.toml:ro"
	if !strings.Contains(cmd, wantCfg) {
		t.Errorf("expected ro config.toml overmount %q:\n%s", wantCfg, cmd)
	}
	// Order matters: parent dir mount must precede file overmounts.
	gIdx := strings.Index(cmd, wantGroup)
	aIdx := strings.Index(cmd, wantAuth)
	if gIdx < 0 || aIdx < 0 || gIdx > aIdx {
		t.Errorf("group dir mount must precede file overmounts:\n%s", cmd)
	}
}

func TestRun_CodexDirMountSkippedWhenUnset(t *testing.T) {
	cfg, folders, _ := makeCfg(t)
	// HostCodexDir intentionally empty (default).

	var captured []string
	restore := fakeExec(t, &captured, "", 0)
	defer restore()

	Run(cfg, folders, Input{
		Prompt: "p", ChatJID: "tg:1", Folder: "g", Name: "arizuko-nocodex",
	})
	cmd := captured[0]
	if strings.Contains(cmd, "/home/node/.codex") {
		t.Errorf("did not expect codex dir mount when HostCodexDir is empty:\n%s", cmd)
	}
}
