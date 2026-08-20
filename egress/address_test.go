package egress

import (
	"context"
	"net"
	"strings"
	"testing"
)

var errStub = &net.DNSError{Err: "stub", IsNotFound: true}

// proxyResolving builds a proxy whose name lookups answer from answers. A real
// resolver cannot be made to return the metadata address on demand, and the
// policy worth testing is exactly what happens for a given ANSWER.
func proxyResolving(answers map[string][]net.IP) *Proxy {
	p := New([]string{"*"}, "")
	p.lookup = func(_ context.Context, host string) ([]net.IP, error) {
		if ips, ok := answers[host]; ok {
			return ips, nil
		}
		return nil, errStub
	}
	return p
}

func ips(list ...string) []net.IP {
	out := make([]net.IP, 0, len(list))
	for _, s := range list {
		out = append(out, net.ParseIP(s))
	}
	return out
}

// The prize an SSRF through a forward proxy is after: cloud instance metadata.
// The agent's own network is `--internal` so it cannot reach it directly; this
// process can, which is what makes it worth confusing. An allowlisted NAME that
// resolves to the metadata address must not be dialled.
func TestResolveDest_RejectsInstanceMetadata(t *testing.T) {
	for _, addr := range []string{"169.254.169.254", "fd00:ec2::254"} {
		p := proxyResolving(map[string][]net.IP{"metadata.example.com": ips(addr)})
		if _, err := p.resolveDest(context.Background(), "metadata.example.com"); err == nil {
			t.Errorf("resolved to %s and was allowed; instance metadata must never be reachable", addr)
		}
	}
}

// The rebinding window the old code left open: it matched the NAME, then dialled
// the NAME, resolving a second time. A host is refused when ANY answer is
// blocked, so the outcome cannot depend on which answer a resolver happens to
// order first.
func TestResolveDest_RejectsWhenAnyAnswerIsBlocked(t *testing.T) {
	p := proxyResolving(map[string][]net.IP{
		"split.example.com": ips("93.184.216.34", "169.254.169.254"),
	})
	_, err := p.resolveDest(context.Background(), "split.example.com")
	if err == nil {
		t.Fatal("a host answering with one public and one metadata address was allowed")
	}
	if !strings.Contains(err.Error(), "169.254.169.254") {
		t.Errorf("error names the wrong address: %v", err)
	}
}

// Loopback is refused by default — a spawn's proxy has no business dialling its
// own container — and permitted only when a local-dev deployment says so.
func TestResolveDest_LoopbackNeedsTheOption(t *testing.T) {
	answers := map[string][]net.IP{"localhost": ips("127.0.0.1")}

	p := proxyResolving(answers)
	if _, err := p.resolveDest(context.Background(), "localhost"); err == nil {
		t.Error("loopback allowed by default")
	}

	p = proxyResolving(answers)
	p.SetAllowLoopback(true)
	if _, err := p.resolveDest(context.Background(), "localhost"); err != nil {
		t.Errorf("loopback refused with the option set: %v", err)
	}
}

// A private answer is ordinary here: the per-spawn proxy shares the compose
// network with the daemons. Only a deployment that says so refuses it.
func TestResolveDest_PrivateIsOptIn(t *testing.T) {
	answers := map[string][]net.IP{"svc.internal": ips("10.99.15.177")}

	p := proxyResolving(answers)
	if _, err := p.resolveDest(context.Background(), "svc.internal"); err != nil {
		t.Errorf("private address refused by default: %v", err)
	}

	p = proxyResolving(answers)
	p.SetDenyPrivate(true)
	if _, err := p.resolveDest(context.Background(), "svc.internal"); err == nil {
		t.Error("private address allowed with --deny-private")
	}
}

// The address that passed the check IS the address returned to dial. Without
// this the check and the socket are two independent resolutions.
func TestResolveDest_ReturnsTheCheckedAddress(t *testing.T) {
	p := proxyResolving(map[string][]net.IP{"ok.example.com": ips("93.184.216.34")})
	got, err := p.resolveDest(context.Background(), "ok.example.com")
	if err != nil {
		t.Fatalf("resolveDest: %v", err)
	}
	if got.String() != "93.184.216.34" {
		t.Errorf("got %s, want the address that passed the check", got)
	}
}

// hostPort supplies the implicit CONNECT port without touching a host that
// already carries one.
func TestHostPort(t *testing.T) {
	if got := hostPort("example.com:8443"); got != "example.com:8443" {
		t.Errorf("got %q, want the authority unchanged", got)
	}
	if got := hostPort("example.com"); got != "example.com:443" {
		t.Errorf("got %q, want the implicit 443", got)
	}
}
