package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildFakeMCP compiles testdata/fakemcp into a temp binary and returns
// the absolute path. testdata is go-test-excluded so go test ./... never
// tries to compile it as part of the package's own build.
func buildFakeMCP(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakemcp")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fakemcp/")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fakemcp: %v\n%s", err, out)
	}
	return bin
}

func TestDiscoverConnectorTools_Namespacing(t *testing.T) {
	bin := buildFakeMCP(t)
	spec := &ConnectorSpec{
		Name:    "fake",
		Command: []string{bin},
		Scope:   "per_call",
	}
	tools, err := DiscoverConnectorTools(context.Background(), spec)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if got := tools[0].LocalName; got != "fake_echo_env" {
		t.Errorf("LocalName = %q, want fake_echo_env", got)
	}
	if tools[0].RemoteName != "echo_env" {
		t.Errorf("RemoteName = %q, want echo_env", tools[0].RemoteName)
	}
	if !strings.Contains(tools[0].Description, "Echo") {
		t.Errorf("Description lost: %q", tools[0].Description)
	}
	if len(tools[0].InputSchema) == 0 {
		t.Errorf("InputSchema not preserved")
	}
}

func TestCallConnectorTool_EnvInjection(t *testing.T) {
	bin := buildFakeMCP(t)
	spec := &ConnectorSpec{
		Name:    "fake",
		Command: []string{bin},
		Secrets: []string{"GITHUB_TOKEN"},
		EnvTemplate: map[string]string{
			"FAKEMCP_KEY":  "GITHUB_TOKEN",
			"GITHUB_TOKEN": "{secret:GITHUB_TOKEN}",
		},
		Scope:       "per_call",
		CallTimeout: 5 * time.Second,
	}
	tools, err := DiscoverConnectorTools(context.Background(), spec)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	res, err := CallConnectorTool(context.Background(), tools[0], map[string]any{},
		map[string]string{"GITHUB_TOKEN": "ghp_secrettoken"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("no content: %+v", res)
	}
	text := contentText(t, res.Content[0])
	// Scrubber must redact the raw secret value out of the echoed payload.
	if strings.Contains(text, "ghp_secrettoken") {
		t.Errorf("unscrubbed token in result: %q", text)
	}
	if !strings.Contains(text, "«redacted»") {
		t.Errorf("expected scrub marker in %q", text)
	}
}

func TestCallConnectorTool_MissingSecretIsEmpty(t *testing.T) {
	bin := buildFakeMCP(t)
	spec := &ConnectorSpec{
		Name:    "fake",
		Command: []string{bin},
		Secrets: []string{"NEVER_SET"},
		EnvTemplate: map[string]string{
			"FAKEMCP_KEY": "NEVER_SET",
			"NEVER_SET":   "{secret:NEVER_SET}",
		},
		Scope:       "per_call",
		CallTimeout: 5 * time.Second,
	}
	tools, _ := DiscoverConnectorTools(context.Background(), spec)
	res, err := CallConnectorTool(context.Background(), tools[0], map[string]any{},
		map[string]string{"NEVER_SET": ""})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	text := contentText(t, res.Content[0])
	if !strings.Contains(text, "env[NEVER_SET]=") {
		t.Errorf("expected echo with empty value, got %q", text)
	}
	// Empty secret should NOT be scrubbed (we'd replace every empty string).
	if strings.Contains(text, "«redacted»") {
		t.Errorf("scrubbed empty secret: %q", text)
	}
}

func TestConnector_EndToEndThroughMCPSocket(t *testing.T) {
	bin := buildFakeMCP(t)
	spec := &ConnectorSpec{
		Name:    "fake",
		Command: []string{bin},
		Secrets: []string{"GITHUB_TOKEN"},
		EnvTemplate: map[string]string{
			"FAKEMCP_KEY":  "GITHUB_TOKEN",
			"GITHUB_TOKEN": "{secret:GITHUB_TOKEN}",
		},
		Scope:       "per_call",
		CallTimeout: 5 * time.Second,
	}
	tools, err := DiscoverConnectorTools(context.Background(), spec)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	store := map[[3]string]string{
		{"folder", "atlas", "GITHUB_TOKEN"}: "ghp_folder_tok",
	}
	var auditRows []SecretUseRow
	db := StoreFns{
		LookupSecret: func(scope, scopeID, key string) (string, bool) {
			v, ok := store[[3]string{scope, scopeID, key}]
			return v, ok
		},
		LogSecretUse: func(r SecretUseRow) error {
			auditRows = append(auditRows, r)
			return nil
		},
		Connectors: tools,
	}

	dir := t.TempDir()
	sock := dir + "/gated.sock"
	stop, err := ServeMCP(sock, GatedFns{}, db, "atlas", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	// Call the connector tool via raw JSON-RPC over the socket; the
	// shared callTool helper expects JSON in content[0].text, but our
	// fake echoes plain text.
	text := callConnectorRaw(t, sock, "fake_echo_env")
	if strings.Contains(text, "ghp_folder_tok") {
		t.Errorf("unscrubbed token in result: %q", text)
	}
	if !strings.Contains(text, "«redacted»") {
		t.Errorf("expected scrub marker in %q", text)
	}
	if len(auditRows) != 1 || auditRows[0].Tool != "fake_echo_env" || auditRows[0].Scope != "folder" {
		t.Errorf("audit row mismatch: %+v", auditRows)
	}
}

func callConnectorRaw(t *testing.T, sock, name string) string {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": map[string]any{}},
	}
	b, _ := json.Marshal(req)
	c.Write(append(b, '\n'))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
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
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse %q: %v", resp, err)
	}
	if parsed.Error != nil {
		t.Fatalf("rpc error: %s", parsed.Error.Message)
	}
	if parsed.Result.IsError {
		t.Fatalf("isError: %s", parsed.Result.Content[0].Text)
	}
	if len(parsed.Result.Content) == 0 {
		t.Fatalf("empty content: %s", resp)
	}
	return parsed.Result.Content[0].Text
}

func TestCallConnectorTool_Timeout(t *testing.T) {
	// Build a sleep wrapper that ignores stdin forever.
	spec := &ConnectorSpec{
		Name:        "sleeper",
		Command:     []string{"sleep", "60"},
		Scope:       "per_call",
		CallTimeout: 200 * time.Millisecond,
	}
	tool := ConnectorTool{
		Connector:  spec,
		RemoteName: "noop",
		LocalName:  "sleeper_noop",
	}
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

// contentText extracts .text from one mcp.Content element. mcp-go's
// Content is a polymorphic interface; reflect via JSON to dodge the
// type assertion ladder.
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
