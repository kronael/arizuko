package runed

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// fakeDockerRun replaces execCommand so the docker half runs against a shell
// stand-in instead of a docker daemon: the whole path — the argument list, the
// stdin write, the stderr scan, the exit mapping — is exercised with no docker
// on the host. A script rather than a re-entered test binary, because the run
// path times out a slow start and this substitute must be as quick as the
// process it stands for.
type fakeDockerRun struct {
	// args holds the last argument list handed to the launcher.
	args atomic.Pointer[[]string]
	// stdinFile receives the bytes the runtime wrote to the guest's stdin.
	stdinFile string
	// waitFile, when set, blocks the stand-in until the file exists.
	waitFile string
	stderr   string
	exitCode int
}

const fakeDockerScript = `#!/bin/sh
cat > "$FAKE_STDIN"
printf '%s' "$FAKE_STDERR" >&2
while [ -n "$FAKE_WAIT" ] && [ ! -e "$FAKE_WAIT" ]; do sleep 0.01; done
exit "$FAKE_EXIT"
`

func (f *fakeDockerRun) install(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	f.stdinFile = filepath.Join(dir, "stdin")
	script := filepath.Join(dir, "fake-docker")
	if err := os.WriteFile(script, []byte(fakeDockerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	execCommand = func(name string, args ...string) *exec.Cmd {
		all := append([]string{name}, args...)
		f.args.Store(&all)
		cmd := exec.Command(script)
		cmd.Env = append(os.Environ(),
			"FAKE_STDIN="+f.stdinFile,
			"FAKE_STDERR="+f.stderr,
			"FAKE_WAIT="+f.waitFile,
			fmt.Sprintf("FAKE_EXIT=%d", f.exitCode),
		)
		return cmd
	}
}

func (f *fakeDockerRun) sawArgs() []string {
	p := f.args.Load()
	if p == nil {
		return nil
	}
	return *p
}

// dockerFixture is a dockerRuntime whose docker calls are all faked: the run
// itself through execCommand, the SIGUSR1 and the kill through their fields.
func dockerFixture(t *testing.T) (*dockerRuntime, *fakeDockerRun) {
	t.Helper()
	root := t.TempDir()
	cfg := &core.Config{
		Name: "test", Image: "arizuko-ant:test", Timezone: "UTC",
		ProjectRoot: root,
		GroupsDir:   filepath.Join(root, "groups"),
		IpcDir:      filepath.Join(root, "ipc"),
		IdleTimeout: 30 * time.Second,
		Timeout:     60 * time.Second,
	}
	fake := &fakeDockerRun{}
	fake.install(t)
	return &dockerRuntime{
		cfg:     cfg,
		folders: &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir},
		signal:  func(string) error { return nil },
		kill:    func(string) error { return nil },
	}, fake
}

// TestDockerExitClassification pins the whole exit mapping this substrate owns:
// which codes are an error, and which of those a retry cannot repair. A clean
// exit with NO output is OK — the agent's reply goes out-of-band over routd's
// MCP socket, so this substrate never sees it, and classifying on missing
// output tripped every folder's breaker after 3 clean turns.
func TestDockerExitClassification(t *testing.T) {
	cases := []struct {
		name         string
		exitCode     int
		wantOutcome  string
		wantTerminal bool
	}{
		{"clean exit, no output", 0, runedv1.OutcomeOK, false},
		{"exit 1 → retryable error", 1, runedv1.OutcomeError, false},
		{"exit 137 (SIGKILL) → retryable error", 137, runedv1.OutcomeError, false},
		{"exit 125 (docker run failed) → terminal", 125, runedv1.OutcomeError, true},
		{"exit 126 (not runnable) → terminal", 126, runedv1.OutcomeError, true},
		{"exit 127 (not found) → terminal", 127, runedv1.OutcomeError, true},
		// The agent container's own EX_CONFIG: the model refused for a reason a
		// retry cannot repair, so the turn must not be re-spawned (BUGS L175).
		{"exit 78 (agent EX_CONFIG) → terminal", 78, runedv1.OutcomeError, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, fake := dockerFixture(t)
			fake.exitCode = c.exitCode
			res := rt.Run(context.Background(), Turn{Handle: "arizuko-test-x", Folder: "demo"})
			if res.Outcome != c.wantOutcome {
				t.Fatalf("outcome=%q want %q (res=%+v)", res.Outcome, c.wantOutcome, res)
			}
			if res.Terminal != c.wantTerminal {
				t.Fatalf("Terminal=%v want %v (res=%+v)", res.Terminal, c.wantTerminal, res)
			}
			if res.ExitCode != c.exitCode {
				t.Fatalf("ExitCode=%d want %d", res.ExitCode, c.exitCode)
			}
		})
	}
}

// TestDockerStartFailureTerminal: a cmd.Start failure (the docker CLI is
// missing or not executable) is a host misconfiguration with ExitCode 0 — the
// F73 neighbouring case. It must carry Terminal so routd never burns retries
// on it (spec 5/12).
func TestDockerStartFailureTerminal(t *testing.T) {
	rt, _ := dockerFixture(t)
	missing := filepath.Join(t.TempDir(), "missing-docker-cli")
	execCommand = func(string, ...string) *exec.Cmd { return exec.Command(missing) }

	res := rt.Run(context.Background(), Turn{Handle: "arizuko-startfail", Folder: "demo"})
	if !strings.HasPrefix(res.Error, "start: ") {
		t.Fatalf("Error=%q want a start: prefix (res=%+v)", res.Error, res)
	}
	if !res.Terminal {
		t.Fatalf("start failure not marked Terminal (res=%+v)", res)
	}
}

// TestDockerDeliversTheTurn is the substrate's half of the seam: every field of
// the Turn reaches `docker run` and nothing else does. The expectations are
// literals, not values read back off the Turn.
func TestDockerDeliversTheTurn(t *testing.T) {
	rt, fake := dockerFixture(t)
	turn := Turn{
		Handle: "arizuko-test-atlas-support-1700000000000",
		Folder: "atlas/support",
		Mounts: []core.Mount{
			{Host: "/srv/groups/atlas/support", Container: "/home/node"},
			{Host: "/srv/app-src", Container: "/opt/arizuko", RO: true},
		},
		Env:   map[string]string{"TZ": "Europe/Prague", "ARIZUKO_MODEL": "opus"},
		Stdin: []byte(`{"prompt":"hello world","groupFolder":"atlas/support"}`),
	}
	rt.Run(context.Background(), turn)

	want := []string{
		"docker", "run", "-i", "--rm",
		"--name", "arizuko-test-atlas-support-1700000000000",
		"--shm-size=1g",
		"-e", "ARIZUKO_MODEL=opus",
		"-e", "TZ=Europe/Prague",
		"-v", "/srv/groups/atlas/support:/home/node",
		"-v", "/srv/app-src:/opt/arizuko:ro",
		"arizuko-ant:test",
	}
	got := fake.sawArgs()
	if uid, explicit := container.RunUID(); explicit {
		// This host's uid is neither root nor the image's, so the substrate maps
		// it and HOME with it. Sorted env keeps HOME before TZ.
		want = append(want[:7], append([]string{
			"--user", fmt.Sprintf("%d:%d", uid, os.Getgid()),
			"-e", "ARIZUKO_MODEL=opus",
			"-e", "HOME=/home/node",
			"-e", "TZ=Europe/Prague",
		}, want[11:]...)...)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("docker args\n got: %s\nwant: %s", strings.Join(got, " "), strings.Join(want, " "))
	}

	sent, err := os.ReadFile(fake.stdinFile)
	if err != nil {
		t.Fatalf("read the guest's stdin: %v", err)
	}
	if string(sent) != `{"prompt":"hello world","groupFolder":"atlas/support"}` {
		t.Fatalf("guest stdin=%q want the Turn's Stdin verbatim", sent)
	}
}

// TestDockerAddsTheProxyAddress: HTTP_PROXY and its three companions are the
// substrate's own address, so the substrate adds them — the renderer never
// names a proxy, and the guest is pinned to the network and IP the proxy
// accepts.
func TestDockerAddsTheProxyAddress(t *testing.T) {
	rt, _ := dockerFixture(t)
	eg := container.EgressSpawn{
		Network: "arizuko_test_atlas-support", IP: "10.99.7.104",
		ProxyURL: "http://10.99.7.105:3128",
	}
	args := strings.Join(buildArgs(rt.cfg, Turn{Handle: "c1", Env: map[string]string{"TZ": "UTC"}}, eg), " ")
	for _, want := range []string{
		"--network arizuko_test_atlas-support",
		"--ip 10.99.7.104",
		"-e HTTP_PROXY=http://10.99.7.105:3128",
		"-e HTTPS_PROXY=http://10.99.7.105:3128",
		"-e NO_PROXY=localhost,127.0.0.1",
		"-e NODE_OPTIONS=--require=/app/proxy-shim.js",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args missing %q: %s", want, args)
		}
	}
	plain := strings.Join(buildArgs(rt.cfg, Turn{Handle: "c2", Env: map[string]string{"TZ": "UTC"}}, container.EgressSpawn{}), " ")
	for _, never := range []string{"--network", "--ip", "HTTP_PROXY", "NODE_OPTIONS"} {
		if strings.Contains(plain, never) {
			t.Errorf("no-egress spawn carries %q: %s", never, plain)
		}
	}
}

// TestDockerCountsAntLines: the [ant] lines on stderr are the per-spawn
// activity count runed echoes into session_log (spec 5/P-runed § envelope 6).
func TestDockerCountsAntLines(t *testing.T) {
	rt, fake := dockerFixture(t)
	fake.stderr = "[ant] one\nnoise\n[ant] two\n[ant] three\n"
	res := rt.Run(context.Background(), Turn{Handle: "c-msgs", Folder: "demo"})
	if res.MessageCount != 3 {
		t.Fatalf("MessageCount=%d want 3 ([ant] lines only)", res.MessageCount)
	}
}

// TestDockerWritesTheTurnLogWithoutSecrets: the log is what an operator reads
// after a failure, so it names the mounts and the frame — but never the model
// credential the frame carries.
func TestDockerWritesTheTurnLogWithoutSecrets(t *testing.T) {
	rt, fake := dockerFixture(t)
	fake.exitCode = 1 // an error, so the log takes the verbose branch
	turn := Turn{
		Handle: "c-log", Folder: "demo",
		Mounts: []core.Mount{{Host: "/srv/groups/demo", Container: "/home/node"}},
		Stdin:  []byte(`{"prompt":"p","secrets":{"ANTHROPIC_API_KEY":"sk-do-not-log"}}`),
	}
	rt.Run(context.Background(), turn)

	logs, _ := filepath.Glob(filepath.Join(rt.cfg.GroupsDir, "demo", "logs", "container-*.log"))
	if len(logs) != 1 {
		t.Fatalf("turn logs=%v want exactly 1", logs)
	}
	body, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "sk-do-not-log") {
		t.Fatalf("the turn log carries the model credential:\n%s", body)
	}
	for _, want := range []string{"/srv/groups/demo -> /home/node", `"prompt": "p"`, "Exit Code: 1"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("turn log missing %q:\n%s", want, body)
		}
	}
}

// TestProdSteerPath exercises the PRODUCTION steer wiring: dockerRuntime.Run
// registers the steer closure (IPC write + SIGUSR1) before the guest starts, so
// a concurrent POST /v1/runs for the busy folder steers into the live guest
// (steered:true) instead of a fresh spawn (spec 5/P-runed § steer).
func TestProdSteerPath(t *testing.T) {
	rt, fake := dockerFixture(t)
	release := filepath.Join(t.TempDir(), "release")
	fake.waitFile = release
	var signaled atomic.Int32
	rt.signal = func(string) error { signaled.Add(1); return nil }

	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := NewManager(db, rt, ManagerConfig{
		Instance: "test", MaxConcurrent: 5,
		Cfg: rt.cfg, Folders: rt.folders,
	})

	done := make(chan runedv1.RunOutcome, 1)
	go func() {
		out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "acme/eng", MessageBatch: "first"})
		done <- out
	}()
	// Wait for the guest to be live: the steer closure is registered before the
	// process starts, so the argument list appearing means both are up.
	for fake.sawArgs() == nil {
		time.Sleep(2 * time.Millisecond)
	}

	steer, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "acme/eng", MessageBatch: "second batch"})
	if !steer.Steered {
		t.Fatalf("second Run steered=%v want true (out=%+v)", steer.Steered, steer)
	}
	if n := signaled.Load(); n != 1 {
		t.Fatalf("SIGUSR1 sent %d times, want 1", n)
	}
	in, _ := os.ReadDir(filepath.Join(rt.cfg.IpcDir, "acme", "eng", "input"))
	if len(in) != 1 {
		t.Fatalf("ipc input files=%d want 1 (the steered batch)", len(in))
	}

	os.WriteFile(release, nil, 0o644)
	first := <-done
	if first.Outcome != runedv1.OutcomeOK {
		t.Fatalf("first outcome=%q want ok", first.Outcome)
	}
}

// TestDockerCtxDeadlineKills: the ceiling is no longer armed inside
// dockerRuntime.Run — Manager.spawn wraps ctx with the turn deadline and
// armCancel kills on ctx.Done() regardless of cause (spec 5/8 "wedge
// protection... uniform across kinds"). This proves the substrate's half: a ctx
// that dies mid-run gets its guest Kill'd by handle, with the kill armed only
// once the run is underway.
func TestDockerCtxDeadlineKills(t *testing.T) {
	rt, fake := dockerFixture(t)
	release := filepath.Join(t.TempDir(), "release")
	fake.waitFile = release
	killed := make(chan string, 4)
	rt.kill = func(name string) error {
		select {
		case killed <- name:
		default:
		}
		os.WriteFile(release, nil, 0o644) // the real kill ends the container
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res := rt.Run(ctx, Turn{Handle: "arizuko-test-demo-ttl", Folder: "demo"})
	select {
	case name := <-killed:
		if name != "arizuko-test-demo-ttl" {
			t.Fatalf("deadline Kill got handle %q want arizuko-test-demo-ttl", name)
		}
	default:
		t.Fatal("the ctx deadline never Kill'd the over-ceiling guest")
	}
	if res.Outcome != runedv1.OutcomeError {
		t.Fatalf("outcome=%q want error (the killed run)", res.Outcome)
	}
}

// TestDockerNoKillOnFastRunWithinDeadline: a run that finishes before its ctx
// deadline is NOT Kill'd, and no kill fires after the run returns — the watcher
// stops deterministically on completion (the late-kill race).
func TestDockerNoKillOnFastRunWithinDeadline(t *testing.T) {
	rt, _ := dockerFixture(t)
	var kills atomic.Int32
	rt.kill = func(string) error { kills.Add(1); return nil }

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	rt.Run(ctx, Turn{Handle: "arizuko-test-demo-fast", Folder: "demo"})
	// Wait past the (already-disarmed) deadline to prove no late kill fires.
	time.Sleep(80 * time.Millisecond)
	if n := kills.Load(); n != 0 {
		t.Fatalf("fast run got %d kills, want 0 (the deadline must not fire after completion)", n)
	}
}

// TestDockerCancelKills: when routd drops the POST /v1/runs request mid-run
// (ctx cancelled), armCancel Kills the live guest so it does not orphan to the
// ceiling while the turn is re-fed into a second guest (double execution).
func TestDockerCancelKills(t *testing.T) {
	rt, fake := dockerFixture(t)
	release := filepath.Join(t.TempDir(), "release")
	fake.waitFile = release
	var kills atomic.Int32
	rt.kill = func(string) error {
		kills.Add(1)
		os.WriteFile(release, nil, 0o644)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan RunResult, 1)
	go func() {
		done <- rt.Run(ctx, Turn{Handle: "arizuko-test-demo-cancel", Folder: "demo"})
	}()
	for fake.sawArgs() == nil {
		time.Sleep(2 * time.Millisecond)
	}
	cancel() // routd dropped the request.

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not Kill the live guest; Run never returned")
	}
	if n := kills.Load(); n == 0 {
		t.Fatalf("cancel fired %d kills, want at least 1", n)
	}
}
