package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/kronael/arizuko/anteval/pkg/report"
	"github.com/kronael/arizuko/anteval/pkg/run"
	"github.com/kronael/arizuko/anteval/pkg/spec"
)

type lifecycleOptions struct {
	envFile, name, product, nonce, sinkBind, sinkURL, md, json string
	cases                                                      []spec.Case
}

type lifecycleProcess interface{ Stop() error }
type lifecycleCLI interface {
	Preflight(context.Context, map[string]string) error
	Command(context.Context, []string, []string) (string, error)
	Start(context.Context, []string, []string) (lifecycleProcess, error)
	UnitExists(context.Context, string) (bool, error)
	ProjectExists(context.Context, string) (bool, error)
	Ready(context.Context, lifecycleProcess, []string) error
	Drive(run.Config) ([]report.Result, error)
}

type realCLI struct{}
type realProcess struct {
	cmd     *exec.Cmd
	done    chan error
	compose string
}

func envBase(env []string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "ARIZUKO_DATA_DIR="); ok {
			return value
		}
	}
	return ""
}

func (realCLI) Command(ctx context.Context, env, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, "arizuko", args...)
	cmd.Env = env
	cmd.Dir = envBase(env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("arizuko %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}
func (realCLI) Start(ctx context.Context, env, args []string) (lifecycleProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command("arizuko", args...)
	cmd.Env = env
	cmd.Dir = envBase(env)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &realProcess{cmd: cmd, done: make(chan error, 1),
		compose: filepath.Join(envBase(env), "arizuko_"+args[2], "docker-compose.yml")}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
}
func (p *realProcess) Stop() error {
	select {
	case err := <-p.done:
		if err != nil {
			return fmt.Errorf("run exited: %w", err)
		}
		return nil
	default:
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(15 * time.Second):
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	<-p.done
	return errors.New("run did not stop within 15 seconds")
}
func (realCLI) UnitExists(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", "--property=LoadState", "--value", "arizuko_"+name+".service").Output()
	if err != nil {
		return false, fmt.Errorf("check systemd unit: %w", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "loaded":
		return true, nil
	case "not-found":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected systemd load state %q", strings.TrimSpace(string(out)))
	}
}
func (realCLI) ProjectExists(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "compose", "ls", "--all", "--format", "json").Output()
	if err != nil {
		return false, fmt.Errorf("check compose projects: %w", err)
	}
	var projects []struct{ Name string }
	if err := json.Unmarshal(out, &projects); err != nil {
		return false, fmt.Errorf("read compose projects: %w", err)
	}
	for _, project := range projects {
		if project.Name == "arizuko_"+name {
			return true, nil
		}
	}
	return false, nil
}
func (realCLI) Ready(ctx context.Context, process lifecycleProcess, services []string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	p := process.(*realProcess)
	for {
		args := append([]string{"compose", "-f", p.compose, "ps", "--all",
			"--format", "{{.Service}} {{.State}} {{.Health}}"}, services...)
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Env, cmd.Dir = p.cmd.Env, p.cmd.Dir
		out, err := cmd.Output()
		if err == nil && areServicesReady(string(out), services) {
			return nil
		}
		select {
		case err := <-p.done:
			p.done <- err
			return fmt.Errorf("run exited before readiness: %v", err)
		case <-ctx.Done():
			return fmt.Errorf("wait for services %v: %w", services, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func areServicesReady(out string, services []string) bool {
	isReady := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "running" &&
			(len(fields) == 2 || fields[2] == "healthy") {
			isReady[fields[0]] = true
		}
	}
	for _, service := range services {
		if !isReady[service] {
			return false
		}
	}
	return true
}
func (realCLI) Drive(cfg run.Config) ([]report.Result, error) { return run.Drive(cfg) }

func freePorts() (api, web int, err error) {
	apiListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, 0, err
	}
	defer func() { err = errors.Join(err, apiListener.Close()) }()
	webListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, 0, err
	}
	defer func() { err = errors.Join(err, webListener.Close()) }()
	return apiListener.Addr().(*net.TCPAddr).Port,
		webListener.Addr().(*net.TCPAddr).Port, nil
}
func tokenValue(out string) (string, error) {
	token := ""
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && k == "token" {
			if token != "" {
				return "", errors.New("multiple token lines")
			}
			token = strings.TrimSpace(v)
		}
	}
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("invalid token output")
	}
	return token, nil
}
func doLifecycle(ctx context.Context, cli lifecycleCLI, opts lifecycleOptions) (err error) {
	cfg, err := godotenv.Read(opts.envFile)
	if err != nil {
		return err
	}
	for _, key := range []string{"DYNAMODB_URL", "STORE_CA", "AWS_REGION", "EXTENDDB_PASSWORD", "HOST_APP_DIR", "RUNED_KERNEL_DIR", "RUNED_ROOTFS_DIR", "RUNED_VSOCK_CID_BASE", "RUNED_VSOCK_PORT_BASE", "RUNED_TAP_SUBNET", "RUNED_PROXY_PORT"} {
		if cfg[key] == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	if cfg["ANTHROPIC_API_KEY"] == "" && cfg["CLAUDE_CODE_OAUTH_TOKEN"] == "" {
		return errors.New("model credential is required")
	}
	sink, err := url.Parse(opts.sinkURL)
	if err != nil || sink.Scheme != "http" || sink.Hostname() == "" ||
		sink.Port() == "" || net.ParseIP(sink.Hostname()) != nil {
		return errors.New("sink must use a DNS hostname and port")
	}
	if opts.sinkBind == "" {
		opts.sinkBind = ":" + sink.Port()
	}
	if opts.name == "" {
		opts.name = "anteval-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	name := opts.name
	if !regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,31}$`).MatchString(name) {
		return errors.New("invalid instance name")
	}
	base := os.Getenv("ARIZUKO_DATA_DIR")
	if base == "" {
		base = filepath.Join(os.Getenv("PREFIX"), "data")
		if os.Getenv("PREFIX") == "" {
			base = "/srv/data"
		}
	}
	for _, root := range []string{base, "/srv/data"} {
		dir := filepath.Join(root, "arizuko_"+name)
		if _, err := os.Lstat(dir); err == nil {
			return fmt.Errorf("instance %s exists at %s", name, dir)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	exists, err := cli.UnitExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("systemd unit for %s exists", name)
	}
	exists, err = cli.ProjectExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("compose project for %s exists", name)
	}
	if err := cli.Preflight(ctx, cfg); err != nil {
		return err
	}
	private, err := os.MkdirTemp(base, ".anteval-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(private)) }()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"), "ARIZUKO_DATA_DIR=" + private,
		"HOST_APP_DIR=" + cfg["HOST_APP_DIR"],
		"EXTENDDB_PASSWORD=" + cfg["EXTENDDB_PASSWORD"]}
	command := func(ctx context.Context, args ...string) (string, error) {
		return cli.Command(ctx, env, append([]string{args[0], "-i", name}, args[1:]...))
	}
	var process lifecycleProcess
	defer func() {
		if process != nil {
			err = errors.Join(err, process.Stop())
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, removeErr := command(cleanupCtx, "rm")
		if removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove instance %s in %s: %w", name, private, removeErr))
		}
	}()
	var productArgs []string
	if opts.product != "" {
		productArgs = []string{"--product", opts.product}
	}
	if _, err = command(ctx, append([]string{"create"}, productArgs...)...); err != nil {
		return err
	}
	apiPort, webPort, err := freePorts()
	if err != nil {
		return err
	}
	allowed := map[string]string{}
	for _, key := range []string{"DYNAMODB_URL", "STORE_CA", "AWS_REGION", "HOST_APP_DIR", "RUNED_KERNEL_DIR", "RUNED_ROOTFS_DIR", "RUNED_VSOCK_CID_BASE", "RUNED_VSOCK_PORT_BASE", "RUNED_TAP_SUBNET", "RUNED_PROXY_PORT", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ARIZUKO_DEFAULT_MODEL"} {
		if cfg[key] != "" {
			allowed[key] = cfg[key]
		}
	}
	cfg = allowed
	cfg["ASSISTANT_NAME"] = name
	cfg["WEB_HOST"] = "http://" + net.JoinHostPort(sink.Hostname(), strconv.Itoa(webPort))
	cfg["API_PORT"] = strconv.Itoa(apiPort)
	cfg["WEB_PORT"] = strconv.Itoa(webPort)
	cfg["APP_SRC_DEV"] = ""
	cfg["MAX_CONCURRENT_CONTAINERS"] = "1"
	cfg["ONBOARDING_ENABLED"] = "true"
	path := filepath.Join(private, "arizuko_"+name, ".env")
	seed, err := godotenv.Read(path)
	if err != nil {
		return err
	}
	for key, value := range cfg {
		seed[key] = value
	}
	if err = godotenv.Write(seed, path); err != nil {
		return err
	}
	if _, err = command(ctx, "store", "enroll"); err != nil {
		return err
	}
	start := func(services []string) error {
		var e error
		process, e = cli.Start(ctx, env, []string{"run", "-i", name})
		if e != nil {
			return e
		}
		return cli.Ready(ctx, process, services)
	}
	services := []string{"authd", "routd", "runed", "onbod", "webd", "proxyd"}
	if err = start(services); err != nil {
		return err
	}
	if _, err = command(ctx, "package", "add", "catalog:litellm"); err != nil {
		return err
	}
	if err = process.Stop(); err != nil {
		process = nil
		return err
	}
	process = nil
	if err = start(append(services, "litellm", "litellm_db")); err != nil {
		return err
	}
	if _, err = command(ctx, append([]string{"group", "add", "web:eval", "eval"}, productArgs...)...); err != nil {
		return err
	}
	if _, err = command(ctx, "package", "sync"); err != nil {
		return err
	}
	rows := filepath.Join(private, "grant.yml")
	if err = os.WriteFile(rows, []byte("acl:\n  - principal: folder:eval\n    action: web:publish\n    scope: eval/**\n    effect: allow\n    grant_option: true\n"), 0600); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(rows)) }()
	if _, err = command(ctx, "apply", rows); err != nil {
		return err
	}
	if _, err = command(ctx, "group", "grant", "folder:eval", "eval/**", "hold:*", "--effect", "deny"); err != nil {
		return err
	}
	for _, c := range opts.cases {
		for _, required := range c.Requires {
			if strings.HasPrefix(required, "mcp:") {
				if _, err = command(ctx, "group", "grant", "folder:eval", "eval/**", required); err != nil {
					return err
				}
			}
		}
	}
	if _, err = command(ctx, "network", "allow", "eval", sink.Hostname()); err != nil {
		return err
	}
	walls := make([]int, 0, len(opts.cases))
	for _, c := range opts.cases {
		walls = append(walls, c.MaxWallMs)
	}
	ttl := worstCaseWall(walls, run.DefaultSettleWait) + 10*time.Minute
	out, err := command(ctx, "token", "issue", "bearer", "eval", "--scope", "messages:write,messages:read,cost:read", "--ttl", ttl.String())
	if err != nil {
		return err
	}
	bearer, err := tokenValue(out)
	if err != nil {
		return err
	}
	out, err = command(ctx, "token", "issue", "chat", "eval")
	if err != nil {
		return err
	}
	chatToken, err := tokenValue(out)
	if err != nil {
		return err
	}
	api := "http://127.0.0.1:" + strconv.Itoa(apiPort)
	web := "http://127.0.0.1:" + strconv.Itoa(webPort)
	target := &run.HTTPTarget{API: api, MCPURL: web + "/chat/" + chatToken + "/mcp", Token: bearer, Folder: "eval"}
	results, driveErr := cli.Drive(run.Config{Context: ctx, Target: target, Cases: opts.cases, Nonce: opts.nonce, TargetBase: cfg["WEB_HOST"], Chat: "web:eval", Folder: "eval", SinkBind: opts.sinkBind, SinkURL: opts.sinkURL})
	md := report.Markdown(results)
	fmt.Print(md)
	if driveErr != nil {
		return driveErr
	}
	if opts.md != "" {
		if err = os.WriteFile(opts.md, []byte(md), 0644); err != nil {
			return err
		}
	}
	if opts.json != "" {
		var b []byte
		b, err = report.JSON(results)
		if err != nil {
			return err
		}
		if err = os.WriteFile(opts.json, b, 0644); err != nil {
			return err
		}
	}
	if !report.AllPassed(results) {
		return errors.New("cases failed")
	}
	return nil
}
