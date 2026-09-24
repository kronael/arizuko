package container

import (
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/egress"
)

// fakeDocker stubs execCommand, recording every invocation and letting a case
// force one subcommand to fail. `docker inspect` answers with the loopback
// address so the readiness probe can reach a real listener.
type fakeDocker struct {
	mu       sync.Mutex
	calls    [][]string
	failOn   string // first arg (or "network connect") that should exit 1
	inspecIP string
}

func (f *fakeDocker) install(t *testing.T) {
	t.Helper()
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	execCommand = func(name string, args ...string) *exec.Cmd {
		f.mu.Lock()
		f.calls = append(f.calls, args)
		f.mu.Unlock()

		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "network inspect"):
			return exec.Command("/bin/sh", "-c", "echo 10.99.42.0/24")
		case strings.HasPrefix(joined, "inspect"):
			if f.failOn == "inspect" {
				return exec.Command("/bin/sh", "-c", "exit 1")
			}
			return exec.Command("/bin/sh", "-c", "echo "+f.inspecIP)
		case strings.HasPrefix(joined, "network connect"):
			if f.failOn == "network connect" {
				return exec.Command("/bin/sh", "-c", "echo no such network >&2; exit 1")
			}
			return exec.Command("/bin/true")
		case strings.HasPrefix(joined, "run"):
			if f.failOn == "run" {
				return exec.Command("/bin/sh", "-c", "echo no such image >&2; exit 1")
			}
			return exec.Command("/bin/true")
		}
		return exec.Command("/bin/true")
	}
}

func (f *fakeDocker) sawRemoveOf(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) >= 3 && c[0] == "rm" && c[1] == "-f" && c[2] == name {
			return true
		}
	}
	return false
}

func resetNetMgr(t *testing.T) {
	t.Helper()
	prev := defaultNetMgr
	t.Cleanup(func() { defaultNetMgr = prev })
	defaultNetMgr = &netMgr{
		perFolder: map[string]*sync.Mutex{},
		allocated: map[string]bool{},
	}
}

// probeAt points the readiness probe at port for the duration of a test.
func probeAt(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	prevPort, prevTimeout := egressPort, egressReadyTimeout
	t.Cleanup(func() { egressPort, egressReadyTimeout = prevPort, prevTimeout })
	egressPort, egressReadyTimeout = port, timeout
}

// healthyProxy stands up a listener that answers /health 200, standing in for
// the proxy container, and returns its host and port.
func healthyProxy(t *testing.T) (string, int) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == egress.HealthPath {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { _ = srv.Close() })
	host, port, _ := net.SplitHostPort(lis.Addr().String())
	n, _ := strconv.Atoi(port)
	return host, n
}

func testConfig(allow ...string) EgressConfig {
	return EgressConfig{
		NetworkPrefix: "arizuko_test",
		ParentSubnet:  "10.99.0.0/16",
		Image:         "arizuko-egress:test",
		UplinkNetwork: "arizuko_test_default",
		AllowlistFn:   func(string) ([]string, error) { return allow, nil },
	}
}

// TestStartEgressProxyRefusesHalfConfig — an image names the proxy but the
// uplink is empty: the operator asked for isolation and would get none. The
// spawn must fail naming the missing input, before any docker call.
func TestStartEgressProxyRefusesHalfConfig(t *testing.T) {
	f := &fakeDocker{}
	f.install(t)

	cfg := testConfig()
	cfg.UplinkNetwork = ""
	_, err := StartEgressProxy(cfg, "atlas", "arizuko-test-atlas-1")
	if err == nil || !strings.Contains(err.Error(), "uplink network") {
		t.Fatalf("a half config must fail naming the missing input, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("no docker call may precede the refusal: %v", f.calls)
	}
}

// TestStartEgressProxyInertWithoutConfig — no egress configured means no
// proxy, no network and no error: the spawn runs exactly as it did before
// egress existed. Preserving this is why `Effective` gates the whole path.
func TestStartEgressProxyInertWithoutConfig(t *testing.T) {
	f := &fakeDocker{}
	f.install(t)

	eg, err := StartEgressProxy(EgressConfig{}, "atlas", "arizuko-test-atlas-1")
	if err != nil {
		t.Fatalf("inert config must not error: %v", err)
	}
	if eg.Network != "" || eg.ProxyURL != "" || eg.IP != "" {
		t.Errorf("inert config must produce no egress plumbing: %+v", eg)
	}
	eg.Stop() // must be safe on the zero value
	if len(f.calls) != 0 {
		t.Errorf("inert config must run no docker commands; ran %v", f.calls)
	}
}

// TestStartEgressProxyBakesAllowlistAndPin — the happy path. The proxy is
// started with THIS folder's allowlist and pinned to the address the spawn
// will hold, and the spawn is pointed at the proxy's address on the internal
// network (never the uplink, which the spawn cannot reach).
func TestStartEgressProxyBakesAllowlistAndPin(t *testing.T) {
	resetNetMgr(t)
	host, port := healthyProxy(t)
	probeAt(t, port, 5*time.Second)
	f := &fakeDocker{inspecIP: host}
	f.install(t)

	cfg := testConfig("api.anthropic.com", "github.com")
	eg, err := StartEgressProxy(cfg, "atlas/support", "arizuko-test-atlas-1")
	if err != nil {
		t.Fatalf("startEgressProxy: %v", err)
	}
	if eg.Network != "arizuko_test_atlas-support" {
		t.Errorf("network = %q", eg.Network)
	}
	if eg.IP == "" || eg.ProxyURL == "" {
		t.Fatalf("missing plumbing: %+v", eg)
	}
	if !strings.HasPrefix(eg.ProxyURL, "http://10.99.42.") {
		t.Errorf("proxy URL %q must be the proxy's address on the internal network", eg.ProxyURL)
	}
	if strings.Contains(eg.ProxyURL, eg.IP) {
		t.Errorf("proxy and spawn share an address: %s", eg.IP)
	}

	var run []string
	for _, c := range f.calls {
		if c[0] == "run" {
			run = c
		}
	}
	if run == nil {
		t.Fatal("no docker run for the proxy")
	}
	joined := strings.Join(run, " ")
	if !strings.Contains(joined, "--allow api.anthropic.com,github.com") {
		t.Errorf("allowlist not baked into the proxy: %s", joined)
	}
	if !strings.Contains(joined, "--client "+eg.IP) {
		t.Errorf("proxy not pinned to the spawn's address: %s", joined)
	}
	if !strings.Contains(joined, "--network arizuko_test_atlas-support") {
		t.Errorf("proxy not on the folder network: %s", joined)
	}
	if strings.Contains(joined, "--allow-all") {
		t.Errorf("no * in the allowlist, yet allow-all was passed: %s", joined)
	}
}

// TestStartEgressProxyTranslatesStarToAllowAll — the DB's `*` network_rules
// row and runner.go's elevated-egress append both spell "allow everything" as
// a `*` list entry; the binary takes an explicit --allow-all mode and refuses
// `*` as a list entry (spec 6/25 1b). This seam must translate, or every
// elevated turn and every `*`-row folder (krons eval, marinade atlas) fails
// its spawn.
func TestStartEgressProxyTranslatesStarToAllowAll(t *testing.T) {
	resetNetMgr(t)
	host, port := healthyProxy(t)
	probeAt(t, port, 5*time.Second)
	f := &fakeDocker{inspecIP: host}
	f.install(t)

	cfg := testConfig("api.anthropic.com", "*")
	if _, err := StartEgressProxy(cfg, "atlas", "arizuko-test-atlas-1"); err != nil {
		t.Fatalf("startEgressProxy: %v", err)
	}

	var run []string
	for _, c := range f.calls {
		if c[0] == "run" {
			run = c
		}
	}
	if run == nil {
		t.Fatal("no docker run for the proxy")
	}
	joined := strings.Join(run, " ")
	if !strings.Contains(joined, "--allow api.anthropic.com ") && !strings.HasSuffix(joined, "--allow api.anthropic.com") {
		t.Errorf("named rules must survive the translation: %s", joined)
	}
	if strings.Contains(joined, "*") {
		t.Errorf("`*` must never reach the binary's list: %s", joined)
	}
	if !strings.Contains(joined, "--allow-all") {
		t.Errorf("`*` entry must become the explicit --allow-all mode: %s", joined)
	}
}

// TestStartEgressProxyFailsSpawnWhenProxyCannotStart — the security-critical
// branch. A proxy that will not start must surface an error so the caller
// fails the spawn. Returning a zero egressSpawn with a nil error would spawn
// an unfiltered container, inverting the property egress exists for.
func TestStartEgressProxyFailsSpawnWhenProxyCannotStart(t *testing.T) {
	resetNetMgr(t)
	f := &fakeDocker{failOn: "run", inspecIP: "127.0.0.1"}
	f.install(t)

	eg, err := StartEgressProxy(testConfig("github.com"), "atlas", "arizuko-test-atlas-1")
	if err == nil {
		t.Fatal("a proxy that fails to start MUST fail the spawn, not spawn unfiltered")
	}
	if eg.Network != "" {
		t.Errorf("failed start must return no network to spawn on: %+v", eg)
	}
	if !strings.Contains(err.Error(), "no such image") {
		t.Errorf("error must carry docker's reason; got %v", err)
	}
}

// TestStartEgressProxyTearsDownOnUplinkFailure — a proxy with no route out
// would deny every allowed host. The half-built proxy is removed and the
// spawn fails rather than running against it.
func TestStartEgressProxyTearsDownOnUplinkFailure(t *testing.T) {
	resetNetMgr(t)
	f := &fakeDocker{failOn: "network connect", inspecIP: "127.0.0.1"}
	f.install(t)

	name := "arizuko-test-atlas-1"
	if _, err := StartEgressProxy(testConfig("github.com"), "atlas", name); err == nil {
		t.Fatal("uplink failure must fail the spawn")
	}
	if !f.sawRemoveOf(name + "-egress") {
		t.Errorf("half-built proxy not torn down; calls: %v", f.calls)
	}
}

// TestStartEgressProxyFailsWhenProxyNeverHealthy — the ordering guarantee. The
// proxy container started, but nothing answers /health, so the agent is never
// started against it.
func TestStartEgressProxyFailsWhenProxyNeverHealthy(t *testing.T) {
	resetNetMgr(t)
	// A closed port on loopback refuses immediately, so a short deadline
	// proves the branch without stalling the suite.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	closed, _ := strconv.Atoi(portStr)
	_ = lis.Close()
	probeAt(t, closed, 200*time.Millisecond)
	f := &fakeDocker{inspecIP: "127.0.0.1"}
	f.install(t)

	name := "arizuko-test-atlas-1"
	_, err = StartEgressProxy(testConfig("github.com"), "atlas", name)
	if err == nil {
		t.Fatal("a proxy that never reports healthy must fail the spawn")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("error must say the proxy was not ready; got %v", err)
	}
	if !f.sawRemoveOf(name + "-egress") {
		t.Errorf("unhealthy proxy not torn down; calls: %v", f.calls)
	}
}

// TestSweepEgressProxiesFiltersByInstance — an orphan proxy holds a live
// allowlist, so a starting runed reaps them; the label value keeps the sweep
// off another instance's proxies on the same docker host.
func TestSweepEgressProxiesFiltersByInstance(t *testing.T) {
	f := &fakeDocker{}
	f.install(t)

	SweepEgressProxies("")
	if len(f.calls) != 0 {
		t.Errorf("no prefix must sweep nothing; ran %v", f.calls)
	}

	SweepEgressProxies("arizuko_krons")
	if len(f.calls) == 0 {
		t.Fatal("sweep ran no docker command")
	}
	joined := strings.Join(f.calls[0], " ")
	if !strings.Contains(joined, "label=arizuko.egress=arizuko_krons") {
		t.Errorf("sweep must filter by this instance's label; got %s", joined)
	}
}

// TestGatewayHostJoinsAllowlist pins the rule that a configured gateway is
// reachable by construction. Without it the per-spawn proxy denies the gateway
// and every turn fails with no model call, on an instance that looks correctly
// configured.
func TestGatewayHostJoinsAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name, url, want string
	}{
		{"full URL", "http://litellm:4000", "litellm"},
		{"https with path", "https://gw.example.com/v1", "gw.example.com"},
		{"bare host:port", "litellm:4000", "litellm"},
		{"unset", "", ""},
		{"unparseable", "://nope", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayHost(tc.url); got != tc.want {
				t.Errorf("gatewayHost(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
