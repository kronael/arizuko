package ipc

// MCP connector: spawn a third-party MCP server as a stdio subprocess,
// proxy its tools through the broker. Per spec 9/11 § "Connector
// declaration". v1 is per_call only — subprocess lifetime is one call.
//
// On boot gated calls tools/list once with empty secrets to harvest the
// catalog, then teardown. Each catalog entry is registered as a broker
// tool named "<connector>_<remote_tool>" whose RequiresSecrets is the
// connector's secrets list. On invocation: broker resolves secrets,
// connector spawns subprocess with rendered env, proxies tools/call,
// scrubs secret values from the result, tears down.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ConnectorSpec is one [[mcp_connector]] block. Loaded from
// <data_dir>/connectors.toml at gated boot. Immutable thereafter.
type ConnectorSpec struct {
	Name        string            `toml:"name"`
	Command     []string          `toml:"command"`
	Secrets     []string          `toml:"secrets"`
	EnvTemplate map[string]string `toml:"env_template"`
	Scope       string            `toml:"scope"` // "per_call" (v1)
	CallTimeout time.Duration     `toml:"-"`     // resolved from CONNECTOR_CALL_TIMEOUT_MS
}

// ConnectorTool is a single discovered remote tool plus enough metadata
// to re-invoke. Description + InputSchema travel verbatim from the
// connector so the agent sees the upstream's contract.
type ConnectorTool struct {
	Connector   *ConnectorSpec
	RemoteName  string // tool name as the connector reports it
	LocalName   string // "<connector>.Name>_<RemoteName>"
	Description string
	InputSchema json.RawMessage // raw JSON schema from the connector
}

// rpcReq / rpcResp are the minimal JSON-RPC 2.0 shapes we need.
type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var rpcSeq int64

func nextRPCID() int64 { return atomic.AddInt64(&rpcSeq, 1) }

// DiscoverConnectorTools spawns the connector with empty env, sends an
// initialize handshake + tools/list, parses the catalog, kills the
// process. Returns one ConnectorTool per remote tool.
func DiscoverConnectorTools(ctx context.Context, spec *ConnectorSpec) ([]ConnectorTool, error) {
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("connector %q: empty command", spec.Name)
	}
	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	cmd.Env = []string{} // empty env at discovery; no host env leaks in
	stdin, stdout, stderr, err := startStdio(cmd, spec.Name)
	if err != nil {
		return nil, err
	}
	defer killProc(cmd, spec.Name)

	rdr := bufio.NewReader(stdout)
	if err := initialize(stdin, rdr); err != nil {
		stderrTail(stderr)
		return nil, fmt.Errorf("connector %q initialize: %w", spec.Name, err)
	}
	resp, err := callRPC(stdin, rdr, "tools/list", map[string]any{})
	if err != nil {
		stderrTail(stderr)
		return nil, fmt.Errorf("connector %q tools/list: %w", spec.Name, err)
	}
	var list struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp, &list); err != nil {
		return nil, fmt.Errorf("connector %q tools/list parse: %w", spec.Name, err)
	}
	out := make([]ConnectorTool, 0, len(list.Tools))
	for _, t := range list.Tools {
		out = append(out, ConnectorTool{
			Connector:   spec,
			RemoteName:  t.Name,
			LocalName:   spec.Name + "_" + t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out, nil
}

// CallConnectorTool spawns the connector subprocess with secrets rendered
// into its env, proxies tools/call, scrubs known secret values from the
// result text, and tears down. Per spec 9/11 § "Why the agent can't leak
// the credential" the value scrub is exact-string match against the
// (finite) per-call secret set.
func CallConnectorTool(
	ctx context.Context, tool ConnectorTool, args any, secrets map[string]string,
) (*mcp.CallToolResult, error) {
	spec := tool.Connector
	if spec.CallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.CallTimeout)
		defer cancel()
	}
	env := renderEnv(spec.EnvTemplate, secrets)
	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	cmd.Env = env
	stdin, stdout, stderr, err := startStdio(cmd, spec.Name)
	if err != nil {
		return nil, err
	}
	defer killProc(cmd, spec.Name)
	rdr := bufio.NewReader(stdout)
	if err := initialize(stdin, rdr); err != nil {
		stderrTail(stderr)
		return nil, fmt.Errorf("connector %q initialize: %w", spec.Name, err)
	}
	resp, err := callRPC(stdin, rdr, "tools/call", map[string]any{
		"name":      tool.RemoteName,
		"arguments": args,
	})
	if err != nil {
		stderrTail(stderr)
		return nil, fmt.Errorf("connector %q tools/call: %w", spec.Name, err)
	}
	result := scrubResult(resp, secrets)
	var parsed mcp.CallToolResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, fmt.Errorf("connector %q result parse: %w", spec.Name, err)
	}
	return &parsed, nil
}

// renderEnv resolves "{secret:KEY}" in each template value against the
// resolved-secrets map. Returns an env slice (KEY=VALUE) suitable for
// exec.Cmd.Env. Empty resolution leaves the placeholder empty (broker
// already emitted a "missing" audit row).
func renderEnv(tmpl, secrets map[string]string) []string {
	out := make([]string, 0, len(tmpl))
	for k, v := range tmpl {
		out = append(out, k+"="+expandSecret(v, secrets))
	}
	return out
}

func expandSecret(s string, secrets map[string]string) string {
	for key, val := range secrets {
		ph := "{secret:" + key + "}"
		s = strings.ReplaceAll(s, ph, val)
	}
	return s
}

// scrubResult exact-string-replaces any non-empty resolved secret value
// in the JSON text with "«redacted»". Cheap; the secret set is small per
// call. Only acts on bytes — not on schema/structure.
func scrubResult(b []byte, secrets map[string]string) []byte {
	s := string(b)
	for _, v := range secrets {
		if v == "" {
			continue
		}
		s = strings.ReplaceAll(s, v, "«redacted»")
	}
	return []byte(s)
}

// startStdio wires the subprocess pipes. stderr is line-buffered into
// slog.Debug under the connector name so connector noise never reaches
// the agent. Returns the start error if cmd.Start fails.
func startStdio(cmd *exec.Cmd, name string) (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, nil, nil, err
	}
	go drainStderr(stderr, name)
	return stdin, stdout, stderr, nil
}

func drainStderr(r io.Reader, name string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		slog.Debug("connector stderr", "connector", name, "line", sc.Text())
	}
}

// stderrTail is a best-effort post-mortem when initialize/call fails;
// stderr may already be drained by drainStderr above.
func stderrTail(_ io.Reader) {}

func killProc(cmd *exec.Cmd, name string) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait() // collect zombie + drain pipes
	slog.Debug("connector torn down", "connector", name)
}

func initialize(stdin io.Writer, rdr *bufio.Reader) error {
	_, err := callRPC(stdin, rdr, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "arizuko-broker", "version": "1"},
	})
	if err != nil {
		return err
	}
	// notifications/initialized — fire-and-forget, no id.
	note := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	}
	b, _ := json.Marshal(note)
	if _, err := stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func callRPC(stdin io.Writer, rdr *bufio.Reader, method string, params any) (json.RawMessage, error) {
	id := nextRPCID()
	req := rpcReq{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := stdin.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := rdr.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) == 0 {
			continue
		}
		var resp rpcResp
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		if resp.ID != id {
			// Notification or out-of-order reply; ignore.
			continue
		}
		if resp.Error != nil {
			return nil, errors.New(resp.Error.Message)
		}
		return resp.Result, nil
	}
}
