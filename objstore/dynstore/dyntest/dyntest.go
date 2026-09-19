// Package dyntest lends a DynamoDB endpoint to a test. It is the only place
// that knows how to get one, so the driver's own suite and the daemon suites
// cannot drift about which server they prove anything against.
package dyntest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore/dynstore"
)

// Image is the endpoint a test starts when the environment names none. It is
// ExtendDB's development build — the same server the deployment runs, over
// SQLite instead of PostgreSQL — because it is a native process: this host has
// two cores, and a JVM beside the suite competes for both.
//
// It ships a 250 ms control-plane delay and a 10 ms index delay, which a suite
// that makes an owner's thirty-three entities per fixture pays hundreds of
// times. zeroTheDelays turns both off; with them off the same thirty-three
// tables take about 200 ms.
const Image = "ghcr.io/extenddb/extenddb-dev:0.1.11"

// ImagePort is the port Image listens on inside its container.
const ImagePort = 18080

// devConfig is the configuration `extenddb settings` reads to reach the running
// server's own storage. The server itself is configured by environment, so this
// exists only for that command, and it names the same SQLite file.
const devConfig = `[server]
bind_addr = "0.0.0.0"
port = 18080
[server.tls]
enabled = false
[storage]
backend = "sqlite"
[storage.sqlite]
path = "/var/lib/extenddb/extenddb.sqlite"
`

// EnvEndpoint points the suites at an endpoint that is already running — the
// PostgreSQL deployment, or Amazon's own service — instead of the container.
// AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_REGION come from the same
// environment then, and ARIZUKO_TEST_DYNAMO_CA names that endpoint's
// certificate when it signed its own.
const EnvEndpoint = "ARIZUKO_TEST_DYNAMO_ENDPOINT"

var (
	once      sync.Once
	shared    dynstore.Config
	startErr  error
	container string
)

// Config answers the endpoint for this test process, starting one on the first
// call. A test that cannot have one FAILS: every owner's store lives on a
// server, so a skip here would report a suite that proved nothing as green.
func Config(t *testing.T) dynstore.Config {
	t.Helper()
	once.Do(func() { shared, startErr = start() })
	if startErr != nil {
		t.Fatalf("no endpoint: %v", startErr)
	}
	return shared
}

// Store hands the test its own table namespace on that endpoint and makes the
// entities it names, dropping them when the test ends.
func Store(t *testing.T, entities ...string) *dynstore.Store {
	t.Helper()
	cfg := Config(t)
	cfg.Prefix = dynstore.TestPrefix()
	s, err := dynstore.Dial(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, e := range entities {
		if err := s.CreateEntity(ctx, e); err != nil {
			t.Fatalf("create %s: %v", e, err)
		}
	}
	t.Cleanup(func() {
		// Everything under this test's prefix, not just what it named: a test
		// that makes entities through an owner's declaration would otherwise
		// leave them on a server that outlives the run.
		ctx := context.Background()
		left, err := s.Entities(ctx)
		if err != nil {
			left = entities
		}
		for _, e := range left {
			_ = s.DropEntity(ctx, e)
		}
	})
	return s
}

// Shutdown removes the container this process started. Every test package that
// takes an endpoint calls it from TestMain, so the container goes when the
// suite ends rather than when the next one starts. A process that dies without
// returning never reaches it; reap collects what such a process leaves.
func Shutdown() {
	if container == "" {
		return
	}
	_, _ = docker("rm", "-f", container)
	container = ""
}

// Main is TestMain for a package whose tests take an endpoint. It puts the
// endpoint in the process environment before the first test runs, because the
// fixture doors the daemons export — routd.OpenMem and its siblings — read the
// same DYNAMODB_URL a daemon reads and know nothing about a container.
//
// A package that owns its own TestMain calls Setup and Shutdown instead.
func Main(m *testing.M) {
	if err := Setup(); err != nil {
		fmt.Fprintf(os.Stderr, "dyntest: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	Shutdown()
	os.Exit(code)
}

// Setup starts the endpoint and exports it, so every store this process opens
// dials it. It is Main's first half, for a package whose TestMain does more.
func Setup() error {
	once.Do(func() { shared, startErr = start() })
	if startErr != nil {
		return startErr
	}
	for k, v := range map[string]string{
		dynstore.EnvURL:    shared.Endpoint,
		dynstore.EnvRegion: region(shared.Region),
		dynstore.EnvKeyID:  shared.KeyID,
		dynstore.EnvSecret: shared.Secret,
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

func region(got string) string {
	if got == "" {
		return "us-east-1"
	}
	return got
}

func start() (dynstore.Config, error) {
	if got := os.Getenv(EnvEndpoint); got != "" {
		// A served endpoint carries its own certificate; the fixture takes it as
		// a file because a person runs the suite from a shell, and hands the
		// bytes on the way a daemon's environment does.
		var ca []byte
		if path := os.Getenv("ARIZUKO_TEST_DYNAMO_CA"); path != "" {
			var err error
			if ca, err = os.ReadFile(path); err != nil {
				return dynstore.Config{}, err
			}
		}
		cfg := dynstore.Config{
			Endpoint: got,
			Region:   os.Getenv(dynstore.EnvRegion),
			KeyID:    os.Getenv(dynstore.EnvKeyID),
			Secret:   os.Getenv(dynstore.EnvSecret),
			CA:       ca,
		}
		if cfg.KeyID == "" {
			cfg.KeyID, cfg.Secret = dynstore.DevKeyID, dynstore.DevSecret
		}
		return cfg, nil
	}
	port, err := freePort()
	if err != nil {
		return dynstore.Config{}, err
	}
	name := fmt.Sprintf("arizuko-dynstore-%d", os.Getpid())
	if err := reap(); err != nil {
		return dynstore.Config{}, err
	}
	// reap spares a live pid and this process is one, so a container that an
	// older run left under this pid goes by name.
	_, _ = docker("rm", "-f", name)
	if out, err := docker("run", "-d", "--name", name,
		"--label", fmt.Sprintf("%s=%d", pidLabel, os.Getpid()),
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, ImagePort),
		"-e", "EXTENDDB__SERVER__BIND_ADDR=0.0.0.0",
		"-e", fmt.Sprintf("EXTENDDB__SERVER__PORT=%d", ImagePort),
		"-e", "EXTENDDB__STORAGE__SQLITE__PATH=/var/lib/extenddb/extenddb.sqlite",
		"-e", "EXTENDDB_DEV_ALLOW_ANY_BIND=1",
		Image, "serve", "--foreground"); err != nil {
		return dynstore.Config{}, fmt.Errorf("start %s: %v: %s", Image, err, out)
	}
	container = name
	cfg := dynstore.Config{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", port),
		KeyID:    dynstore.DevKeyID,
		Secret:   dynstore.DevSecret,
	}
	// The probe is a REQUEST, not a connect. Both images accept the port before
	// the server answers on it, and a fixture that started on the open port met
	// a connection reset in the middle of the first concurrent body.
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		s, err := dynstore.Dial(cfg)
		if err != nil {
			Shutdown()
			return dynstore.Config{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, last = s.Entities(ctx)
		cancel()
		if last == nil {
			if err := zeroTheDelays(name); err != nil {
				Shutdown()
				return dynstore.Config{}, err
			}
			return cfg, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	Shutdown()
	return dynstore.Config{}, fmt.Errorf("%s did not answer on %d: %w", Image, port, last)
}

// pidLabel carries the pid of the process that started a container. The name
// carries the same pid for a person reading `docker ps`, and the reap reads the
// label: a label filter is an exact key match, where a name filter is a
// substring regex over a field anyone is free to reuse.
const pidLabel = "arizuko-dynstore-pid"

// reap removes every container this fixture started whose process is gone. It
// runs before a new one starts, which is the only moment the fixture holds
// nothing of its own to lose. Shutdown covers the process that returns; a
// process that does not — `go test -timeout` kills it, a panic, Ctrl-C — never
// reaches Shutdown, and the container it started keeps a published port and a
// SQLite file until someone looks (eleven were found up for 32 hours).
//
// A container whose pid still runs belongs to a sibling test binary: `go test
// ./...` runs several at once and each owns its own endpoint.
func reap() error {
	out, err := docker("ps", "-a", "--filter", "label="+pidLabel,
		"--format", fmt.Sprintf("{{.Names}} {{.Label %q}}", pidLabel))
	if err != nil {
		return fmt.Errorf("list the %s containers: %v: %s", pidLabel, err, out)
	}
	for _, name := range orphans(out, alive) {
		// A sibling that exits between the listing and here removes its own
		// container first; losing that race gives what this wanted anyway.
		_, _ = docker("rm", "-f", name)
	}
	return nil
}

// orphans answers the containers in a `docker ps` listing whose creating
// process is gone. A row is "<name> <pid>". A row that does not parse names no
// process this can ask about, so it stays.
func orphans(listing string, alive func(int) bool) []string {
	var dead []string
	for line := range strings.SplitSeq(listing, "\n") {
		name, label, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(label))
		if err != nil || pid <= 0 || alive(pid) {
			continue
		}
		dead = append(dead, name)
	}
	return dead
}

// alive answers whether a process still runs. Signal zero asks the kernel about
// the pid and delivers nothing: the answer is "no such process" for a pid that
// is gone, and a permission error for one this user does not own — which is a
// running process, so its container stays.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// zeroTheDelays turns off the two the server simulates for a real control
// plane. They are what a deployment wants and what a suite cannot afford: one
// fixture makes thirty-three tables, and 250 ms each is eight seconds per test.
//
// `extenddb settings` reads a configuration FILE to find the storage, and the
// image is configured by environment and carries none — so the file goes in
// here, for that command and nothing else.
func zeroTheDelays(container string) error {
	f, err := os.CreateTemp("", "extenddb-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(devConfig); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The server refuses a configuration file other users can read.
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		return err
	}
	if out, err := docker("cp", f.Name(), container+":"+configPath); err != nil {
		return fmt.Errorf("place the settings config: %v: %s", err, out)
	}
	for _, kv := range [][2]string{
		{"control_plane_delay_seconds", "0"},
		{"index_propagation_delay_ms", "0"},
	} {
		if out, err := docker("exec", container, "extenddb", "settings",
			"--config", configPath, "set", kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s=%s: %v: %s", kv[0], kv[1], err, out)
		}
	}
	return nil
}

// configPath is where the image expects its configuration file.
const configPath = "/var/lib/extenddb/extenddb.toml"

func docker(args ...string) (string, error) {
	bin := "docker"
	if os.Geteuid() != 0 {
		bin, args = "sudo", append([]string{"docker"}, args...)
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
