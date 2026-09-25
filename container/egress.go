package container

// egress.go is what every substrate needs to put one egress proxy in front of one
// turn: the port, the translation from a resolved allowlist to the proxy's rules,
// and the health wait that makes "the proxy answers before the agent's first
// request" a fact rather than a hope.
//
// It STARTS nothing. The proxy's listen address is the substrate's own — a tap's
// host side under the VM substrate — so the substrate starts the process and this
// package only says what to pass it (spec 6/41 §"Egress by name, sealed below the
// kernel"). The Docker half that ran `docker run` for a proxy container, carved a
// /24 per folder and swept orphans went with the Docker substrate at step 4 of
// 6/41's order.

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Vars, not consts, so tests can point the probe at a local listener and cut
// the deadline. Production never reassigns them.
var (
	// EgressPort is the proxy's listen port: one proxy per spawn, on its own
	// tap, so this never collides. The QEMU substrate reads it as the port on
	// the host side of the guest's tap.
	EgressPort = 3128
	// egressReadyTimeout bounds the wait for the proxy's /health. The spawn
	// fails if it elapses — never spawn the agent with no proxy answering.
	egressReadyTimeout = 15 * time.Second
	egressReadyPoll    = 20 * time.Millisecond
)

// gatewayHost is the one place that turns the configured gateway URL into an
// allowlist entry. A bare host:port or a full URL both resolve; anything
// unparseable yields "" and is skipped rather than injected raw.
func gatewayHost(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "//") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// EgressRules is the ONE translation from a resolved allowlist to the proxy's
// rules, read by every substrate that starts a proxy.
//
// Both producers of "allow everything" — a folder's `*` network_rules row and
// the renderer's egress-grant append — spell it as a `*` list entry, and the
// egress binary takes an explicit --allow-all mode instead of a magic string in
// a list (spec 6/25 1b), so this is where that becomes a flag.
//
// The gateway is the model endpoint for this deployment, so it is allowed by
// construction. Without it the proxy denies the gateway and EVERY turn fails
// with no model call — the feature would look configured and be dead.
func EgressRules(allowlist []string, gatewayURL string) (names []string, allowAll bool) {
	names = make([]string, 0, len(allowlist))
	for _, e := range allowlist {
		if e == "*" {
			allowAll = true
			continue
		}
		names = append(names, e)
	}
	if h := gatewayHost(gatewayURL); h != "" {
		names = append(names, h)
	}
	return names, allowAll
}

// WaitHealthy polls the proxy's /health until it answers 200. This is what
// makes "proxy ready before the guest's first egress attempt" a fact instead
// of a hope: the caller starts the agent only after this returns nil.
// A TCP connect would not be enough — it succeeds the moment the port is
// bound, which can precede the handler serving.
func WaitHealthy(url string, deadline time.Duration) error {
	c := &http.Client{Timeout: 2 * time.Second}
	end := time.Now().Add(deadline)
	var last error
	for time.Now().Before(end) {
		resp, err := c.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("health %s", resp.Status)
		} else {
			last = err
		}
		time.Sleep(egressReadyPoll)
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return last
}
