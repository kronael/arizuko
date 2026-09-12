package dynstore_test

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/conformance"
	"github.com/kronael/arizuko/objstore/dynstore"
)

const image = "ghcr.io/extenddb/extenddb-dev:0.1.11"

var (
	once     sync.Once
	endpoint string
	startErr error
)

// container starts one endpoint for the whole package and answers its address.
func container(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a container; run it through `make integration`")
	}
	once.Do(func() { endpoint, startErr = start() })
	if startErr != nil {
		t.Skipf("no endpoint: %v", startErr)
	}
	return endpoint
}

func start() (string, error) {
	if got := os.Getenv("ARIZUKO_TEST_DYNAMO_ENDPOINT"); got != "" {
		return got, nil
	}
	docker := "docker"
	if os.Geteuid() != 0 {
		docker = "sudo"
	}
	run := func(args ...string) (string, error) {
		if docker == "sudo" {
			args = append([]string{"docker"}, args...)
		}
		out, err := exec.Command(docker, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	port, err := freePort()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("arizuko-dynstore-%d", os.Getpid())
	_, _ = run("rm", "-f", name)
	if out, err := run("run", "-d", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:18080", port), image); err != nil {
		return "", fmt.Errorf("start %s: %v: %s", image, err, out)
	}
	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
			return addr, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("%s did not listen on %d", image, port)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// newStore hands each test its own table namespace on the shared endpoint.
func newStore(t *testing.T, entities ...string) objstore.Store {
	addr := container(t)
	prefix := fmt.Sprintf("t%d_%d_", time.Now().UnixNano(), rand.Int63())
	s, err := dynstore.Dial(context.Background(), addr, "us-east-1", prefix)
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
		for _, e := range entities {
			_ = s.DropEntity(context.Background(), e)
		}
	})
	return s
}

func TestConformance(t *testing.T) { conformance.Run(t, newStore) }
