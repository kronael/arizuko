package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/anteval/pkg/report"
	"github.com/kronael/arizuko/anteval/pkg/run"
	"github.com/kronael/arizuko/anteval/pkg/spec"
)

type fakeLifecycle struct {
	calls     []string
	fail      string
	failedAt  string
	unit      bool
	project   bool
	name      string
	env       []string
	config    map[string]string
	drive     run.Config
	ready     [][]string
	interrupt bool
	host      preflightHost
}
type fakeProcess struct{ cli *fakeLifecycle }

func (f *fakeLifecycle) Preflight(ctx context.Context, cfg map[string]string) error {
	if f.host != nil {
		return preflight(ctx, f.host, cfg)
	}
	return nil
}
func (p fakeProcess) Stop() error { p.cli.calls = append(p.cli.calls, "stop"); return nil }
func (f *fakeLifecycle) UnitExists(_ context.Context, name string) (bool, error) {
	f.name = name
	return f.unit, nil
}
func (f *fakeLifecycle) ProjectExists(context.Context, string) (bool, error) { return f.project, nil }
func (f *fakeLifecycle) Command(ctx context.Context, env, args []string) (string, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	f.env = env
	if len(args) < 3 || args[1] != "-i" || args[2] != f.name {
		return "", errors.New("missing instance")
	}
	if args[0] == "rm" && ctx.Err() != nil {
		return "", errors.New("canceled cleanup")
	}
	operation := args[0]
	if operation == "token" {
		operation = args[4]
	} else if operation == "group" && args[3] == "add" {
		operation = "add web:eval"
	} else if operation == "group" && args[3] == "grant" {
		operation = args[6]
	}
	if f.fail != "" && f.fail == operation {
		f.failedAt = operation
		return "", errors.New("injected failure")
	}
	dir := filepath.Join(envBase(env), "arizuko_"+f.name)
	switch args[0] {
	case "create":
		if err := os.Mkdir(dir, 0700); err != nil {
			return "", err
		}
		if f.fail == "partial-create" {
			f.failedAt = "partial-create"
			return "", errors.New("partial create")
		}
		return "", os.WriteFile(filepath.Join(dir, ".env"), []byte("AUTH_SECRET=seed\n"), 0600)
	case "store":
		var err error
		f.config, err = godotenv.Read(filepath.Join(dir, ".env"))
		return "", err
	case "rm":
		if err := os.Remove(filepath.Join(dir, ".env")); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			return "", err
		}
	case "token":
		return "token: abc\n", nil
	}
	return "", nil
}
func (f *fakeLifecycle) Start(_ context.Context, _ []string, args []string) (lifecycleProcess, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if len(args) != 3 || args[0] != "run" || args[1] != "-i" || args[2] != f.name {
		return nil, errors.New("wrong start instance")
	}
	if f.fail == "run" {
		f.failedAt = "run"
		return nil, errors.New("injected failure")
	}
	return fakeProcess{f}, nil
}
func (f *fakeLifecycle) Ready(ctx context.Context, _ lifecycleProcess, services []string) error {
	f.calls = append(f.calls, "ready")
	f.ready = append(f.ready, services)
	if f.fail == "ready" {
		f.failedAt = "ready"
		return errors.New("injected failure")
	}
	return ctx.Err()
}
func (f *fakeLifecycle) Drive(ctx run.Config) ([]report.Result, error) {
	f.calls = append(f.calls, "drive")
	f.drive = ctx
	if f.interrupt {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Context.Done():
		case <-time.After(time.Second):
			return nil, errors.New("SIGINT did not cancel the run")
		}
	}
	if f.fail == "drive" {
		f.failedAt = "drive"
		return nil, errors.New("injected failure")
	}
	return []report.Result{{ID: ctx.Cases[0].ID, Pass: f.fail != "case"}}, ctx.Context.Err()
}
func lifecycleFixture(t *testing.T) lifecycleOptions {
	t.Helper()
	t.Setenv("ARIZUKO_DATA_DIR", t.TempDir())
	file := filepath.Join(t.TempDir(), "env")
	data := "DYNAMODB_URL=x\nSTORE_CA=x\nAWS_REGION=x\nEXTENDDB_PASSWORD=x\nHOST_APP_DIR=x\nRUNED_KERNEL_DIR=x\nRUNED_ROOTFS_DIR=x\nRUNED_GUEST_MEM=2G\nRUNED_VSOCK_CID_BASE=3\nRUNED_VSOCK_PORT_BASE=5555\nRUNED_TAP_SUBNET=x\nRUNED_PROXY_PORT=3128\nANTHROPIC_API_KEY=x\n"
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return lifecycleOptions{envFile: file, name: "anteval-test",
		sinkURL: "http://sink.test:9099", cases: []spec.Case{{ID: "test-case", MaxWallMs: 1000}}}
}
func TestLifecycleCleanup(t *testing.T) {
	for _, failure := range []string{"", "create", "partial-create", "store", "run", "ready", "package", "add web:eval", "apply", "hold:*", "network", "bearer", "chat", "drive", "case", "rm"} {
		t.Run(failure, func(t *testing.T) {
			opts := lifecycleFixture(t)
			f := &fakeLifecycle{fail: failure}
			err := doLifecycle(context.Background(), f, opts)
			if (err != nil) != (failure != "") {
				t.Fatalf("failure=%q err=%v calls=%v", failure, err, f.calls)
			}
			if failure != "" && failure != "case" && f.failedAt != failure {
				t.Fatalf("failure at %q, want %q", f.failedAt, failure)
			}
			if len(f.calls) == 0 || f.calls[len(f.calls)-1] != "rm -i anteval-test" {
				t.Fatalf("cleanup: %v", f.calls)
			}
			if failure == "drive" || failure == "" {
				if f.calls[len(f.calls)-2] != "stop" {
					t.Fatalf("stop before removal: %v", f.calls)
				}
			}
		})
	}
}

func TestLifecycleFreshNameAndSetup(t *testing.T) {
	opts := lifecycleFixture(t)
	opts.name, opts.product, opts.nonce = "", "support", "chosen-nonce"
	opts.cases[0].Requires = []string{"mcp:register_group"}
	opts.md = filepath.Join(t.TempDir(), "report.md")
	opts.json = filepath.Join(t.TempDir(), "report.json")
	input, err := os.OpenFile(opts.envFile, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := input.WriteString("DATA_DIR=/serving-instance\n" +
		"RUNED_VM_DIR=/serving-instance/vms\nAPP_SRC_DEV=1\n" +
		"AUTH_SECRET=serving-secret\nARIZUKO_BACKEND=claude\n")
	if err := errors.Join(writeErr, input.Close()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_DIR", "/serving-instance")
	t.Setenv("AGENTEVAL_TOKEN", "serving-token")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "serving-credential")
	f := &fakeLifecycle{}
	if err := doLifecycle(context.Background(), f, opts); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^anteval-[0-9]+$`).MatchString(f.name) {
		t.Fatalf("name = %q", f.name)
	}
	for _, entry := range f.env {
		if strings.Contains(entry, "serving-") {
			t.Fatalf("inherited instance configuration: %q", entry)
		}
	}
	for key, want := range map[string]string{
		"APP_SRC_DEV": "", "MAX_CONCURRENT_AGENTS": "1", "RUNED_GUEST_MEM": "2G",
		"AUTH_SECRET": "seed", "ASSISTANT_NAME": f.name,
		"ONBOARDING_ENABLED": "true", "ANTHROPIC_API_KEY": "x",
		"ARIZUKO_BACKEND": "claude",
	} {
		if f.config[key] != want {
			t.Errorf("%s = %q, want %q", key, f.config[key], want)
		}
	}
	if f.config["API_PORT"] == f.config["WEB_PORT"] {
		t.Fatal("API and web share a port")
	}
	if f.config["RUNED_VM_DIR"] != "" || f.config["DATA_DIR"] != "" {
		t.Fatal("guest state escapes disposable data")
	}
	for _, path := range []string{opts.md, opts.json} {
		if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
			t.Fatalf("saved report %s: %q, %v", path, data, err)
		}
	}
	if f.drive.Nonce != opts.nonce || f.drive.Chat != "web:eval" ||
		f.drive.Folder != "eval" || f.drive.Cases[0].ID != "test-case" ||
		f.drive.TargetBase != f.config["WEB_HOST"] || f.drive.SinkBind != ":9099" {
		t.Fatalf("drive configuration = %+v", f.drive)
	}
	target := f.drive.Target.(*run.HTTPTarget)
	if target.Token != "abc" || !strings.HasSuffix(target.MCPURL, "/chat/abc/mcp") {
		t.Fatalf("target = %+v", target)
	}
	for _, call := range []string{
		"create -i " + f.name + " --product support",
		"group -i " + f.name + " add web:eval eval --product support",
		"group -i " + f.name + " grant folder:eval eval/** mcp:register_group",
	} {
		if !strings.Contains(strings.Join(f.calls, "\n"), call) {
			t.Errorf("missing %q in %v", call, f.calls)
		}
	}
	if len(f.ready) != 2 || len(f.ready[0]) != 6 || len(f.ready[1]) != 8 {
		t.Fatalf("service readiness = %v", f.ready)
	}
}

func TestLifecycleSIGINTCleansUp(t *testing.T) {
	opts := lifecycleFixture(t)
	opts.md = filepath.Join(t.TempDir(), "report.md")
	opts.json = filepath.Join(t.TempDir(), "report.json")
	cases := t.TempDir()
	data := "[[case]]\nid='interrupt'\nprompt='noop'\nmax_wall_ms=1000\n" +
		"[case.check]\nkind='callback'\n"
	if err := os.WriteFile(filepath.Join(cases, "case.toml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLifecycle{interrupt: true}
	err := doRun([]string{"--env", opts.envFile, "--target", opts.name,
		"--sink", opts.sinkURL, "--cases", cases, "--md", opts.md,
		"--json", opts.json}, f)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupt error = %v", err)
	}
	if got := strings.Join(f.calls[len(f.calls)-2:], ","); got != "stop,rm -i anteval-test" {
		t.Fatal(got)
	}
	for _, path := range []string{opts.md, opts.json} {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "interrupt") {
			t.Errorf("partial report %s: %q, %v", path, data, err)
		}
	}
}

func TestLifecycleWaitsForEveryService(t *testing.T) {
	services := []string{"routd", "authd", "onbod"}
	for _, out := range []string{
		"", "routd running healthy\nauthd running healthy\n",
		"routd running healthy\nauthd running healthy\nonbod running starting\n",
		"routd running healthy\nauthd running healthy\nonbod exited\n",
	} {
		if areServicesReady(out, services) {
			t.Fatalf("premature readiness for %q", out)
		}
	}
	if !areServicesReady("routd running healthy\nauthd running healthy\nonbod running healthy\n", services) {
		t.Fatal("healthy services never become ready")
	}
}

func TestLifecycleRealProbesUseFakes(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' \"$PROBE_OUTPUT\"\n"
	for _, name := range []string{"docker", "systemctl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	cli := realCLI{}
	t.Setenv("PROBE_OUTPUT", "loaded\n")
	if exists, err := cli.UnitExists(context.Background(), "anteval-test"); err != nil || !exists {
		t.Fatalf("inactive installed unit: %v, %v", exists, err)
	}
	t.Setenv("PROBE_OUTPUT", "not-found\n")
	if exists, err := cli.UnitExists(context.Background(), "anteval-test"); err != nil || exists {
		t.Fatalf("absent unit: %v, %v", exists, err)
	}
	t.Setenv("PROBE_OUTPUT", `[{"Name":"arizuko_unrelated"},{"Name":"arizuko_anteval-test"}]`)
	if exists, err := cli.ProjectExists(context.Background(), "anteval-test"); err != nil || !exists {
		t.Fatalf("existing project: %v, %v", exists, err)
	}
	if exists, err := cli.ProjectExists(context.Background(), "anteval-other"); err != nil || exists {
		t.Fatalf("unrelated project: %v, %v", exists, err)
	}
}

func TestLifecycleStopsAndReapsOwnProcess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "arizuko"),
		[]byte("#!/bin/sh\nexec /bin/sleep 600\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	process, err := (realCLI{}).Start(context.Background(),
		[]string{"ARIZUKO_DATA_DIR=" + dir}, []string{"run", "-i", "anteval-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Stop(); err != nil {
		t.Fatal(err)
	}
	if process.(*realProcess).cmd.ProcessState == nil {
		t.Fatal("run child was not reaped")
	}
}

func TestLifecycleReportFailure(t *testing.T) {
	opts := lifecycleFixture(t)
	opts.md = filepath.Join(t.TempDir(), "missing", "report.md")
	f := &fakeLifecycle{}
	if err := doLifecycle(context.Background(), f, opts); err == nil {
		t.Fatal("expected report error")
	}
	if got := f.calls[len(f.calls)-2:]; strings.Join(got, ",") != "stop,rm -i anteval-test" {
		t.Fatal(got)
	}
}
func TestLifecycleRefusesExisting(t *testing.T) {
	for _, existing := range []string{"directory", "unit", "project", "generated-unit"} {
		t.Run(existing, func(t *testing.T) {
			opts := lifecycleFixture(t)
			f := &fakeLifecycle{unit: strings.HasSuffix(existing, "unit"), project: existing == "project"}
			if existing == "generated-unit" {
				opts.name = ""
			}
			if existing == "directory" {
				if err := os.Mkdir(filepath.Join(os.Getenv("ARIZUKO_DATA_DIR"), "arizuko_"+opts.name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := doLifecycle(context.Background(), f, opts); err == nil {
				t.Fatal("accepted existing instance")
			}
			if len(f.calls) != 0 {
				t.Fatalf("mutated: %v", f.calls)
			}
		})
	}
}
func TestLifecycleCanceledCleanup(t *testing.T) {
	opts := lifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeLifecycle{}
	if err := doLifecycle(ctx, f, opts); err == nil {
		t.Fatal("expected cancellation")
	}
	if f.calls[len(f.calls)-1] != "rm -i anteval-test" {
		t.Fatal(f.calls)
	}
}

func TestLifecycleModelCredentialFollowsBackend(t *testing.T) {
	login := t.TempDir()
	if err := os.WriteFile(filepath.Join(login, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, backend, codexDir, refusal string
		hasAnthropic                     bool
	}{
		{"claude without key", "", "", "backend claude needs ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN", false},
		{"codex without login dir", "codex", "", "backend codex needs HOST_CODEX_DIR", true},
		{"codex without auth.json", "codex", t.TempDir(), "backend codex needs a Codex login", true},
		{"codex with login", "codex", login, "", false},
		{"claude with key", "claude", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := lifecycleFixture(t)
			cfg, err := godotenv.Read(opts.envFile)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.hasAnthropic {
				delete(cfg, "ANTHROPIC_API_KEY")
			}
			cfg["ARIZUKO_BACKEND"], cfg["HOST_CODEX_DIR"] = tc.backend, tc.codexDir
			if err := godotenv.Write(cfg, opts.envFile); err != nil {
				t.Fatal(err)
			}
			f := &fakeLifecycle{}
			err = doLifecycle(context.Background(), f, opts)
			if tc.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refusal) {
					t.Fatalf("err = %v, want %q", err, tc.refusal)
				}
				if len(f.calls) != 0 {
					t.Fatalf("refused run mutated: %v", f.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Join(f.calls, "\n")
			isGateway := tc.backend != "codex"
			if strings.Contains(calls, "package -i anteval-test add catalog:litellm") != isGateway {
				t.Errorf("gateway installed = %v, want %v: %v", !isGateway, isGateway, f.calls)
			}
			if !isGateway {
				if len(f.ready) != 1 || len(f.ready[0]) != 6 {
					t.Errorf("service readiness = %v, want one start without the gateway", f.ready)
				}
				if f.config["HOST_CODEX_DIR"] != login {
					t.Errorf("HOST_CODEX_DIR = %q, want %q", f.config["HOST_CODEX_DIR"], login)
				}
				for _, host := range []string{"chatgpt.com", "auth.openai.com"} {
					if !strings.Contains(calls, "network -i anteval-test allow eval "+host) {
						t.Errorf("missing egress for %s: %v", host, f.calls)
					}
				}
			}
		})
	}
}
