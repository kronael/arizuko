package ipc

// Spec 6/12-mcp-firewall: an agent never dials a third-party MCP server. Its whole MCP server
// map is the per-turn gated socket (ant/src/mcp-servers.ts), so a connector's
// remote tool reaches it as a LOCAL tool registered through granted() — and every
// tools/call on it meets db.Authorize before ipc dials the upstream at all.
//
// These tests hold that seam from both ends: the announce filter, the call-time
// gate, and the fact that a refusal leaves the upstream untouched.

import (
	"bufio"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gatedConnectorSocket serves one turn socket in front of the fake upstream, with
// authorize as the injected row-ACL check. callerSub is non-empty because that is
// what switches ipc's row-based authz on for a real turn (routd.turnPrincipal).
func gatedConnectorSocket(t *testing.T, url string, authorize func(folder, action string, params map[string]string) bool) string {
	t.Helper()
	sock := t.TempDir() + "/gated.sock"
	db := StoreFns{
		Connectors: []ConnectorSpec{{
			Name:        "fake",
			URL:         url,
			Secrets:     []string{"GITHUB_TOKEN"},
			Headers:     map[string]string{"Authorization": "Bearer {secret:GITHUB_TOKEN}"},
			CallTimeout: 5 * time.Second,
		}},
		ResolveConnectorSecrets: func(string, string, []string) (map[string]string, error) {
			return map[string]string{"GITHUB_TOKEN": "ghp_x"}, nil
		},
		Authorize: authorize,
	}
	stop, err := ServeMCP(sock, RoutdFns{}, db, "world/acme", false, 0, "folder:world/acme")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	return sock
}

// rpc runs one JSON-RPC request over the socket and returns the raw reply.
// callTool cannot serve here: a connector result is upstream text, not the JSON
// payload it insists on parsing.
func rpc(t *testing.T, sock, method string, params map[string]any) string {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	c.Write(append(b, '\n'))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(resp)
}

func callConnectorTool(t *testing.T, sock string) string {
	t.Helper()
	return rpc(t, sock, "tools/call", map[string]any{
		"name": "fake_echo_auth", "arguments": map[string]any{},
	})
}

// A connector tool the turn does not hold is neither advertised nor callable, and
// the refusal costs the upstream nothing — ipc dials inside the handler, which the
// gate never reaches.
func TestConnectorTool_NotHeld_IsNeitherListedNorDialled(t *testing.T) {
	url, _, reqs := fakeMCP(t)
	sock := gatedConnectorSocket(t, url, func(_, action string, _ map[string]string) bool {
		return action != "mcp:fake_echo_auth"
	})
	afterDiscovery := reqs()

	if got := callConnectorTool(t, sock); strings.Contains(got, "auth=Bearer") {
		t.Errorf("a connector tool the turn does not hold returned the upstream result: %s", got)
	}
	if n := reqs(); n != afterDiscovery {
		t.Errorf("upstream saw %d requests after the refusal, want %d — the gate must refuse before dialling", n, afterDiscovery)
	}
	if list := rpc(t, sock, "tools/list", map[string]any{}); strings.Contains(list, "fake_echo_auth") {
		t.Errorf("tools/list advertised a connector tool the turn does not hold:\n%s", list)
	}
}

// The grant is what opens it: with mcp:fake_echo_auth held the same call reaches
// the upstream and returns its result.
func TestConnectorTool_Held_ReachesUpstream(t *testing.T) {
	url, _, reqs := fakeMCP(t)
	sock := gatedConnectorSocket(t, url, func(string, string, map[string]string) bool { return true })
	afterDiscovery := reqs()

	// The upstream echoes the Authorization header it saw, so the reply proves the
	// call landed there. The token itself comes back redacted: CallConnectorTool
	// scrubs the resolved secret values out of the result (spec 5/13).
	if got := callConnectorTool(t, sock); !strings.Contains(got, "auth=Bearer «redacted»") {
		t.Errorf("granted call did not return the upstream result: %s", got)
	}
	if n := reqs(); n <= afterDiscovery {
		t.Errorf("upstream saw %d requests, want more than %d — the granted call must reach it", n, afterDiscovery)
	}
}

// The call-time gate is separate from the announce filter: a tool registered while
// the grant held is still refused once the row is gone, and still without a dial.
// This is the half that makes the gate an authorization check rather than a
// listing filter. The refusal names the gate ("not permitted") rather than the
// unknown-tool error, which is what shows the tool WAS registered.
func TestConnectorTool_RevokedAfterAnnounce_RefusedAtCall(t *testing.T) {
	url, _, reqs := fakeMCP(t)
	var revoked atomic.Bool
	sock := gatedConnectorSocket(t, url, func(_, action string, _ map[string]string) bool {
		return !revoked.Load() || action != "mcp:fake_echo_auth"
	})
	revoked.Store(true)
	afterDiscovery := reqs()

	got := callConnectorTool(t, sock)
	if !strings.Contains(got, "not permitted") {
		t.Errorf("a revoked connector tool was still called: %s", got)
	}
	if n := reqs(); n != afterDiscovery {
		t.Errorf("upstream saw %d requests after the revoked call, want %d", n, afterDiscovery)
	}
}

// The grant has to survive the LIST as well as the call. A connector tool
// carries the upstream's own schema, and tools/list marshals as ONE document,
// so a tool whose descriptor refuses to marshal costs the turn every other
// tool with it. Both halves are asserted here: the held tool is announced, and
// the built-in surface it stands beside is still there.
func TestConnectorTool_Held_IsListedBesideTheBuiltInTools(t *testing.T) {
	url, _, _ := fakeMCP(t)
	sock := gatedConnectorSocket(t, url, func(string, string, map[string]string) bool { return true })

	names := listToolNames(t, sock)
	if !slices.Contains(names, "fake_echo_auth") {
		t.Errorf("tools/list did not announce the held connector tool; got %v", names)
	}
	if !slices.Contains(names, "reply") {
		t.Errorf("a connector tool took the built-in surface down with it; got %v", names)
	}
}
