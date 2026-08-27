package main

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/kronael/arizuko/egress"
)

// splitCSV parses EGRESS_ALLOW / EGRESS_DENY. A parse bug here is a policy
// bug: a rule that silently becomes empty, or one that keeps its surrounding
// whitespace and therefore never matches, is an allowlist entry that does not
// protect anything. New's validation catches a malformed rule but cannot catch
// a rule that was dropped before it got there.
func TestSplitCSVTrimsAndDropsEmpties(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{",", nil},
		{"  ,  ,", nil},
		{"example.com", []string{"example.com"}},
		{" example.com ", []string{"example.com"}},
		{"a.example,b.example", []string{"a.example", "b.example"}},
		{"a.example, b.example ,, c.example", []string{"a.example", "b.example", "c.example"}},
		{"\ta.example\n", []string{"a.example"}},
	}
	for _, c := range cases {
		if got := splitCSV(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCSV(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestEnvOrFallsBackOnUnsetAndEmpty(t *testing.T) {
	const key = "ARIZUKO_EGRESS_TEST_VAR"

	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("unset: envOr = %q, want the default", got)
	}

	// An empty value is a variable someone set to nothing, which must read as
	// "not configured" rather than as an empty listen address.
	t.Setenv(key, "")
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("empty: envOr = %q, want the default", got)
	}

	t.Setenv(key, "set")
	if got := envOr(key, "fallback"); got != "set" {
		t.Errorf("set: envOr = %q, want the value", got)
	}
}

// main is the binary runed starts beside every spawn. Its contract is narrow
// and load-bearing: bind, become healthy only once the allowlist is loaded,
// enforce it, and shut down on SIGTERM so a spawn teardown does not leave a
// proxy holding the port. None of it was covered.
func TestMainServesTheAllowlistAndShutsDownOnSIGTERM(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()

	origArgs := os.Args
	os.Args = []string{"egress", "--listen", addr, "--allow", "example.com", "--allow-loopback"}
	t.Cleanup(func() { os.Args = origArgs })

	done := make(chan struct{})
	go func() { main(); close(done) }()

	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://" + addr
	var up bool
	for i := 0; i < 100; i++ {
		resp, err := client.Get(base + egress.HealthPath)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				up = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !up {
		t.Fatalf("egress never answered %s on %s", egress.HealthPath, addr)
	}

	// Healthy means enforcing. A host off the list must already be refused at
	// the moment the probe first says 200 — a proxy that binds before loading
	// its rules would pass this request.
	proxyURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	through := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	resp, err := through.Get("http://not-on-the-list.example/")
	if err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a host off the allowlist got %s, want 403", resp.Status)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("main did not return on SIGTERM; the port stays held after a spawn teardown")
	}
	if resp, err := client.Get(base + egress.HealthPath); err == nil {
		resp.Body.Close()
		t.Error("the proxy still answers after shutdown")
	}
}

// An empty allowlist is a valid rule — "deny everything" — so main must still
// come up and be healthy on it rather than refuse to start. Health reporting
// 503 here would fail every such spawn's readiness probe.
func TestMainComesUpHealthyOnAnEmptyAllowlist(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()

	origArgs := os.Args
	os.Args = []string{"egress", "--listen", addr}
	t.Cleanup(func() { os.Args = origArgs })

	done := make(chan struct{})
	go func() { main(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	})

	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 100; i++ {
		resp, err := client.Get("http://" + addr + egress.HealthPath)
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a deny-everything proxy never reported healthy")
}
