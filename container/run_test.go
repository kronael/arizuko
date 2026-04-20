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

func TestRun_MarkerParsing(t *testing.T) {
	okPayload := `{"status":"success","result":"hi","newSessionId":"s1"}`
	cases := []struct {
		name       string
		stdout     string
		exitCode   int
		wantStatus string
		wantResult string
		wantErr    bool
	}{
		{
			name:       "basic markers",
			stdout:     outputStartMarker + okPayload + outputEndMarker,
			wantStatus: "success",
			wantResult: "hi",
		},
		{
			name:       "text before and after markers",
			stdout:     "noise\n" + outputStartMarker + okPayload + outputEndMarker + "\ntrailing",
			wantStatus: "success",
			wantResult: "hi",
		},
		{
			name:     "no markers → error",
			stdout:   "just some text no markers",
			wantErr:  true,
		},
		{
			name:     "exit code 1 → error",
			stdout:   "",
			exitCode: 1,
			wantErr:  true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, folders, _ := makeCfg(t)
			var captured []string
			restore := fakeExec(t, &captured, c.stdout, c.exitCode)
			defer restore()

			var streamed string
			out := Run(cfg, folders, Input{
				Prompt:   "hello",
				ChatJID:  "tg:1",
				Folder:   "g",
				Name:     "arizuko-test-x",
				OnOutput: func(result, _ string) { streamed = result },
			})

			if c.wantErr {
				if out.Error == "" && out.Status != "error" {
					t.Fatalf("want error, got %+v", out)
				}
				return
			}
			if out.Status != c.wantStatus {
				t.Errorf("status=%q want %q (out=%+v)", out.Status, c.wantStatus, out)
			}
			if streamed != c.wantResult {
				t.Errorf("streamed=%q want %q", streamed, c.wantResult)
			}
		})
	}
}

func TestRun_DockerArgAssembly(t *testing.T) {
	cfg, folders, _ := makeCfg(t)
	var captured []string
	payload := `{"status":"success","result":"ok"}`
	restore := fakeExec(t, &captured, outputStartMarker+payload+outputEndMarker, 0)
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
