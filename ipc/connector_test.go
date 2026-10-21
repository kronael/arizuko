package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// fakeMCP starts a real streamable-HTTP MCP server on an httptest listener. It
// exposes one tool, echo_auth, that echoes the Authorization header the request
// carried — which is how these tests see whether the broker's secret reached the
// wire. Both ends speak the real protocol, so nothing here mocks the transport
// under test.
//
// sawAuth reports whether a value was observed on ANY request rather than on the
// last one: closing the client sends a session-terminate request of its own, so
// a last-write-wins capture reads back empty however the call went.
//
// reqs counts every HTTP request the stub received, which is how a caller sees
// whether the upstream was touched at all — a gate that refuses before dialling
// leaves the count where it was.
func fakeMCP(t *testing.T) (url string, sawAuth func(string) bool, reqs func() int) {
	t.Helper()
	var mu sync.Mutex
	var seen []string

	m := server.NewMCPServer("fakemcp", "1")
	m.AddTool(
		mcp.NewTool("echo_auth", mcp.WithDescription("Echo the Authorization header.")),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			mu.Lock()
			defer mu.Unlock()
			last := ""
			if len(seen) > 0 {
				last = seen[len(seen)-1]
			}
			return mcp.NewToolResultText("auth=" + last), nil
		},
	)
	h := server.NewStreamableHTTPServer(m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth)
		mu.Unlock()
		// A remote MCP server authenticates every request, tools/list included —
		// GitHub's answers 401 without a bearer. Refusing here is what makes an
		// uncredentialed discovery fail the way it does in production.
		if auth == "" {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func(want string) bool {
			mu.Lock()
			defer mu.Unlock()
			return slices.Contains(seen, want)
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return len(seen)
		}
}

func TestDiscoverConnectorTools_Namespacing(t *testing.T) {
	url, _, _ := fakeMCP(t)
	spec := &ConnectorSpec{
		Name:        "fake",
		URL:         url,
		Secrets:     []string{"GITHUB_TOKEN"},
		Headers:     map[string]string{"Authorization": "Bearer {secret:GITHUB_TOKEN}"},
		CallTimeout: 5 * time.Second,
	}
	tools, err := DiscoverConnectorTools(context.Background(), spec,
		map[string]string{"GITHUB_TOKEN": "ghp_x"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if got := tools[0].LocalName; got != "fake_echo_auth" {
		t.Errorf("LocalName = %q, want fake_echo_auth", got)
	}
	if tools[0].RemoteName != "echo_auth" {
		t.Errorf("RemoteName = %q, want echo_auth", tools[0].RemoteName)
	}
	if !strings.Contains(tools[0].Description, "Echo") {
		t.Errorf("Description lost: %q", tools[0].Description)
	}
	if len(tools[0].InputSchema) == 0 {
		t.Errorf("InputSchema not preserved")
	}
}

// The secret must reach the upstream as a request header — the sink that
// replaced the subprocess env when the connector became HTTP (BUGS L132).
func TestCallConnectorTool_SecretRidesInHeader(t *testing.T) {
	url, sawAuth, _ := fakeMCP(t)
	spec := &ConnectorSpec{
		Name:        "fake",
		URL:         url,
		Secrets:     []string{"GITHUB_TOKEN"},
		Headers:     map[string]string{"Authorization": "Bearer {secret:GITHUB_TOKEN}"},
		CallTimeout: 5 * time.Second,
	}
	secrets := map[string]string{"GITHUB_TOKEN": "ghp_secrettoken"}
	tools, err := DiscoverConnectorTools(context.Background(), spec, secrets)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, err := CallConnectorTool(context.Background(), tools[0], map[string]any{}, secrets); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !sawAuth("Bearer ghp_secrettoken") {
		t.Error("upstream never saw the rendered bearer")
	}
}

// The result scrub is the guarantee that survived the transport swap: a secret
// value echoed back by the upstream must never reach the agent.
func TestCallConnectorTool_ScrubsSecretFromResult(t *testing.T) {
	url, _, _ := fakeMCP(t)
	spec := &ConnectorSpec{
		Name:        "fake",
		URL:         url,
		Secrets:     []string{"GITHUB_TOKEN"},
		Headers:     map[string]string{"Authorization": "Bearer {secret:GITHUB_TOKEN}"},
		CallTimeout: 5 * time.Second,
	}
	secrets := map[string]string{"GITHUB_TOKEN": "ghp_secrettoken"}
	tools, err := DiscoverConnectorTools(context.Background(), spec, secrets)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	res, err := CallConnectorTool(context.Background(), tools[0], map[string]any{}, secrets)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("no content: %+v", res)
	}
	text := contentText(t, res.Content[0])
	if strings.Contains(text, "ghp_secrettoken") {
		t.Errorf("unscrubbed token in result: %q", text)
	}
	if !strings.Contains(text, "«redacted»") {
		t.Errorf("expected scrub marker in %q", text)
	}
}

// An unresolved placeholder must not be sent as a half-rendered credential:
// "Bearer {secret:NEVER_SET}" upstream is worse than no header at all.
func TestRenderHeaders_DropsUnresolved(t *testing.T) {
	got := RenderSecrets(map[string]string{
		"Authorization": "Bearer {secret:NEVER_SET}",
		"X-Static":      "keep-me",
	}, map[string]string{"NEVER_SET": ""})
	if _, ok := got["Authorization"]; ok {
		t.Errorf("unresolved placeholder was sent: %q", got["Authorization"])
	}
	if got["X-Static"] != "keep-me" {
		t.Errorf("static header dropped: %+v", got)
	}
}

// An empty secret must not be scrubbed — replacing "" would redact every string.
func TestScrubCallResult_IgnoresEmptySecret(t *testing.T) {
	res := mcp.NewToolResultText("nothing secret here")
	out, err := scrubCallResult(res, map[string]string{"NEVER_SET": ""})
	if err != nil {
		t.Fatalf("scrub: %v", err)
	}
	if text := contentText(t, out.Content[0]); strings.Contains(text, "«redacted»") {
		t.Errorf("scrubbed on an empty secret: %q", text)
	}
}

// The catalog is discovered per folder with that folder's credential, and the
// tools land on the agent's socket. This is the end-to-end shape L132 broke.
func TestConnector_EndToEndThroughMCPSocket(t *testing.T) {
	url, sawAuth, _ := fakeMCP(t)
	spec := ConnectorSpec{
		Name:        "fake",
		URL:         url,
		Secrets:     []string{"GITHUB_TOKEN"},
		Headers:     map[string]string{"Authorization": "Bearer {secret:GITHUB_TOKEN}"},
		CallTimeout: 5 * time.Second,
	}
	var resolvedFor []string
	db := StoreFns{
		Connectors: []ConnectorSpec{spec},
		ResolveConnectorSecrets: func(folder, tool string, required []string) (map[string]string, error) {
			resolvedFor = append(resolvedFor, tool)
			return map[string]string{"GITHUB_TOKEN": "ghp_folder_token"}, nil
		},
	}
	sock := t.TempDir() + "/gated.sock"
	stop, err := ServeMCP(sock, RoutdFns{}, db, "atlas", true, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	text := callConnectorRaw(t, sock, "fake_echo_auth")
	if !strings.Contains(text, "«redacted»") {
		t.Errorf("token not scrubbed out of the socket result: %q", text)
	}
	if !sawAuth("Bearer ghp_folder_token") {
		t.Error("upstream never saw the folder's broker-resolved token")
	}
	// Discovery spends the credential upstream, so it is audited like a call —
	// and it must SPEND it, not merely resolve it: the upstream refuses an
	// unauthenticated tools/list, so a catalog exists only if the token was sent.
	if len(resolvedFor) == 0 || resolvedFor[0] != "fake_tools/list" {
		t.Errorf("discovery did not resolve through the audited broker seam: %v", resolvedFor)
	}
}

// A connector whose endpoint is unreachable registers no tools and does not take
// the rest of the agent's toolset down with it.
func TestConnector_UnreachableRegistersNothing(t *testing.T) {
	db := StoreFns{Connectors: []ConnectorSpec{{
		Name:        "dead",
		URL:         "http://127.0.0.1:1/mcp",
		CallTimeout: time.Second,
	}}}
	sock := t.TempDir() + "/gated.sock"
	stop, err := ServeMCP(sock, RoutdFns{}, db, "atlas", true, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()
	if names := listToolNames(t, sock); len(names) == 0 {
		t.Fatal("socket served no tools at all")
	} else {
		for _, n := range names {
			if strings.HasPrefix(n, "dead_") {
				t.Errorf("unreachable connector registered %q", n)
			}
		}
	}
}

func TestCallConnectorTool_Timeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer slow.Close()
	spec := &ConnectorSpec{Name: "sleeper", URL: slow.URL, CallTimeout: 200 * time.Millisecond}
	tool := ConnectorTool{Connector: spec, RemoteName: "noop", LocalName: "sleeper_noop"}
	start := time.Now()
	_, err := CallConnectorTool(context.Background(), tool, nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout fired too late: %v", elapsed)
	}
}

func callConnectorRaw(t *testing.T, sock, name string) string {
	t.Helper()
	var parsed struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	rpcOverSocket(t, sock, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": map[string]any{}},
	}, &parsed)
	if parsed.Error != nil {
		t.Fatalf("rpc error: %s", parsed.Error.Message)
	}
	if parsed.Result.IsError {
		t.Fatalf("isError: %s", parsed.Result.Content[0].Text)
	}
	if len(parsed.Result.Content) == 0 {
		t.Fatalf("empty content")
	}
	return parsed.Result.Content[0].Text
}

func listToolNames(t *testing.T, sock string) []string {
	t.Helper()
	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	rpcOverSocket(t, sock, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	}, &parsed)
	out := make([]string, 0, len(parsed.Result.Tools))
	for _, tool := range parsed.Result.Tools {
		out = append(out, tool.Name)
	}
	return out
}

func rpcOverSocket(t *testing.T, sock string, req map[string]any, into any) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	b, _ := json.Marshal(req)
	c.Write(append(b, '\n'))
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(resp, into); err != nil {
		t.Fatalf("parse %q: %v", resp, err)
	}
}

// contentText extracts .text from one mcp.Content element. mcp-go's Content is a
// polymorphic interface; reflect via JSON to dodge the type assertion ladder.
func contentText(t *testing.T, c any) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	var tt struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &tt); err != nil {
		t.Fatalf("parse content: %v", err)
	}
	return tt.Text
}
