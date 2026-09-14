// Package dyntest lends a DynamoDB endpoint to a test. It is the only place
// that knows how to get one, so the driver's own suite and the daemon suites
// cannot drift about which server they prove anything against.
package dyntest

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore/dynstore"
)

// Image is the endpoint a test starts when the environment names none. It is
// Amazon's own local build, which answers CreateTable in about 4 ms; the
// ExtendDB development build charges a one-second control-plane delay per
// table, and a suite that makes an owner's thirty-three entities per fixture
// pays that delay 437 times. EnvEndpoint is how a run proves the same code
// against the PostgreSQL deployment this repo ships.
const Image = "amazon/dynamodb-local:latest"

// ImagePort is the port Image listens on inside its container.
const ImagePort = 8000

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
// takes an endpoint calls it from TestMain: a container left behind outlives
// the run and the next one starts beside it (seven were found running a day
// after their suite finished).
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
	_, _ = docker("rm", "-f", name)
	if out, err := docker("run", "-d", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, ImagePort), Image); err != nil {
		return dynstore.Config{}, fmt.Errorf("start %s: %v: %s", Image, err, out)
	}
	container = name
	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
			return dynstore.Config{
				Endpoint: addr,
				KeyID:    dynstore.DevKeyID,
				Secret:   dynstore.DevSecret,
			}, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	Shutdown()
	return dynstore.Config{}, fmt.Errorf("%s did not listen on %d", Image, port)
}

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
