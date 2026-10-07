package ipc

// MCP connector: call a third-party MCP server over streamable HTTP and proxy
// its tools through the broker. Per spec 5/13 § "Shape 2 — MCP HTTP connector".
//
// The transport is HTTP, never a subprocess. The stdio shape was removed
// 2026-08-30: every command `template/connectors/github.toml` offered needed a
// runtime absent from routd's image, and the connector spawns inside routd
// (BUGS L132). An endpoint needs no runtime, so the credential is a request
// HEADER the broker renders per call and never a process environment.
//
// Discovery is PER FOLDER, not per boot, because the credential is per folder:
// routd's boot has no folder and therefore no token, and a remote MCP server
// answers tools/list only to an authenticated caller. Each catalog entry
// registers as a broker tool "<connector>_<remote_tool>". On invocation: the
// broker resolves the folder's secrets narrowed to the connector's declared
// keys, this file renders them into headers, proxies tools/call, and scrubs the
// secret values out of the result.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// ConnectorSpec is one [[connector]] block with transport "mcp". Loaded from
// <data_dir>/connectors.toml at routd boot. Immutable thereafter, which is what
// makes the file the destination allowlist: the operator writes the reachable
// endpoints, the agent only ever picks a tool.
type ConnectorSpec struct {
	Name string `toml:"name"`
	// Transport is "mcp" (this file) or "rest" (routd/ext.go). One block shape,
	// two dispatch paths, spec 6/19. Empty means mcp: the transport that
	// discovers its own catalogue is the one a bare block cannot be mistaken for.
	Transport string `toml:"transport"`
	// URL is the server's streamable-HTTP MCP endpoint.
	URL string `toml:"url"`
	// Secrets names the keys the broker resolves for this connector; the
	// folder's other secrets are never visible to it.
	Secrets []string `toml:"secrets"`
	// Headers carries "{secret:KEY}" placeholders, rendered per call against
	// the broker-resolved values. This is the sink the subprocess env used to be.
	Headers     map[string]string `toml:"headers"`
	CallTimeout time.Duration     `toml:"-"` // resolved from CONNECTOR_CALL_TIMEOUT_MS
}

// ConnectorTool is a single discovered remote tool plus enough metadata to
// re-invoke. Description + InputSchema travel verbatim from the connector so
// the agent sees the upstream's contract.
type ConnectorTool struct {
	Connector   *ConnectorSpec
	RemoteName  string // tool name as the connector reports it
	LocalName   string // "<connector.Name>_<RemoteName>"
	Description string
	InputSchema json.RawMessage // raw JSON schema from the connector
}

// dial opens an initialized MCP session against the connector's endpoint with
// the resolved secrets rendered into request headers. The caller closes it.
func dial(ctx context.Context, spec *ConnectorSpec, secrets map[string]string) (*client.Client, error) {
	if spec.URL == "" {
		return nil, fmt.Errorf("connector %q: empty url", spec.Name)
	}
	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(RenderSecrets(spec.Headers, secrets)),
	}
	if spec.CallTimeout > 0 {
		opts = append(opts, transport.WithHTTPTimeout(spec.CallTimeout))
	}
	c, err := client.NewStreamableHttpClient(spec.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("connector %q dial: %w", spec.Name, err)
	}
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("connector %q start: %w", spec.Name, err)
	}
	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "arizuko-broker", Version: "1"}
	if _, err := c.Initialize(ctx, req); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("connector %q initialize: %w", spec.Name, err)
	}
	return c, nil
}

// DiscoverConnectorTools opens a session with the folder's resolved secrets and
// lists the remote catalog. Secrets are required because a remote MCP server
// authenticates tools/list — GitHub's answers 401 without a bearer — which is
// why this runs per folder at socket build and not once at routd boot.
func DiscoverConnectorTools(ctx context.Context, spec *ConnectorSpec, secrets map[string]string) ([]ConnectorTool, error) {
	if spec.CallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.CallTimeout)
		defer cancel()
	}
	c, err := dial(ctx, spec, secrets)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("connector %q tools/list: %w", spec.Name, err)
	}
	out := make([]ConnectorTool, 0, len(res.Tools))
	for _, t := range res.Tools {
		// The upstream's schema travels verbatim to the agent. mcp.Tool hides
		// RawInputSchema from JSON, so the decoded InputSchema is re-marshalled
		// rather than read back off the wire.
		schema, merr := json.Marshal(t.InputSchema)
		if merr != nil {
			return nil, fmt.Errorf("connector %q tool %q schema: %w", spec.Name, t.Name, merr)
		}
		out = append(out, ConnectorTool{
			Connector:   spec,
			RemoteName:  t.Name,
			LocalName:   spec.Name + "_" + t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out, nil
}

// CallConnectorTool opens a session with the secrets rendered into headers,
// proxies tools/call, and scrubs known secret values from the result. Per spec
// 5/13 § "Why the agent can't leak the credential" the value scrub is exact
// string match against the (finite) per-call secret set.
func CallConnectorTool(
	ctx context.Context, tool ConnectorTool, args any, secrets map[string]string,
) (*mcp.CallToolResult, error) {
	spec := tool.Connector
	if spec.CallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.CallTimeout)
		defer cancel()
	}
	c, err := dial(ctx, spec, secrets)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	var req mcp.CallToolRequest
	req.Params.Name = tool.RemoteName
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("connector %q tools/call: %w", spec.Name, err)
	}
	return scrubCallResult(res, secrets)
}

// RenderSecrets resolves "{secret:KEY}" in each value against the
// resolved-secrets map. It serves all three sinks a credential can land in —
// headers, query params and JSON body — because one placeholder vocabulary over
// three sinks is what let the `[ext.auth]` enum (method/secret/secret2/header/
// header2/param, ceiling of two credentials) be deleted. An entry whose
// placeholder resolves to empty is DROPPED
// rather than sent blank: an `Authorization: Bearer ` with no token reads to the
// upstream as a malformed credential instead of an absent one.
func RenderSecrets(tmpl, secrets map[string]string) map[string]string {
	out := make(map[string]string, len(tmpl))
	for k, v := range tmpl {
		rendered := expandSecret(v, secrets)
		if rendered == "" || hasUnresolvedSecret(rendered) {
			continue
		}
		out[k] = rendered
	}
	return out
}

func expandSecret(s string, secrets map[string]string) string {
	for key, val := range secrets {
		if val == "" {
			continue
		}
		s = strings.ReplaceAll(s, "{secret:"+key+"}", val)
	}
	return s
}

// hasUnresolvedSecret reports whether a rendered value still carries a
// "{secret:" placeholder — the key was declared but the broker resolved nothing.
func hasUnresolvedSecret(s string) bool {
	return strings.Contains(s, "{secret:")
}

// scrubCallResult re-marshals the result, replaces any non-empty resolved secret
// value with "«redacted»", and decodes it back. Round-tripping through JSON is
// what lets one exact-string pass cover every text field the upstream returns.
func scrubCallResult(res *mcp.CallToolResult, secrets map[string]string) (*mcp.CallToolResult, error) {
	if res == nil {
		return nil, fmt.Errorf("connector: nil result")
	}
	if !anySecretSet(secrets) {
		return res, nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("connector result marshal: %w", err)
	}
	cleaned := scrubSecrets(string(b), secrets)
	var out mcp.CallToolResult
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, fmt.Errorf("connector result parse: %w", err)
	}
	return &out, nil
}

func anySecretSet(secrets map[string]string) bool {
	for _, v := range secrets {
		if v != "" {
			return true
		}
	}
	return false
}

// discoverConnector resolves the folder's credential for one connector and lists
// its remote catalog. The credential is spent on a real tools/list request
// upstream, so it goes through the SAME audited broker seam a call does — the
// `<name>_tools/list` tool string is what `secret_use_log` records it as.
//
// A connector that cannot be reached or authenticated registers no tools. That
// is logged at error and never fails the turn: one unreachable third party must
// not cost the agent every other tool it has.
func discoverConnector(spec *ConnectorSpec, db StoreFns, folder string) []ConnectorTool {
	var secrets map[string]string
	if db.ResolveConnectorSecrets != nil {
		var err error
		secrets, err = db.ResolveConnectorSecrets(folder, spec.Name+"_tools/list", spec.Secrets)
		if err != nil {
			slog.Error("connector: resolve discovery secrets",
				"connector", spec.Name, "folder", folder, "err", err)
			return nil
		}
	}
	tools, err := DiscoverConnectorTools(context.Background(), spec, secrets)
	if err != nil {
		slog.Error("connector: discovery failed; no tools registered",
			"connector", spec.Name, "folder", folder, "url", spec.URL, "err", err)
		return nil
	}
	slog.Info("connector discovered",
		"connector", spec.Name, "folder", folder, "tools", len(tools))
	return tools
}
