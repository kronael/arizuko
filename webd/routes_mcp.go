package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
)

// registerRoutesMCP attaches the five `routes.*` MCP tools to srv. The
// tools are a thin operator surface over proxyd's `/v1/routes` REST API.
// One handler in proxyd, two adapters: REST (proxyd's own mux) and MCP
// (here, via webd). Spec 5/5 §"Single source of truth".
//
// Authorization is enforced by proxyd: webd signs identity headers from
// the operator's session; proxyd's REST resreg gate runs auth.Authorize.
// webd's role is identity bridging — Resource.Store is nil here so the
// adapter skips the tx/audit-row dance (proxyd writes the audit row on
// the downstream call; we don't double-log).
func registerRoutesMCP(srv *mcpserver.MCPServer, c *proxydClient, sub, name string, groups []string) {
	if !isOperator(groups) {
		return // tools become invisible to non-operators
	}
	// Per-invocation caller is fixed across the lifetime of this MCP
	// server instance (one session per webd connection). The resolver
	// closes over (sub, name, groups) which webd builds from the
	// authenticated session each handleMCP call — privilege confusion
	// is structurally precluded by the spawn pattern, not by trusting
	// resreg's per-call resolver. Documented in webd/mcp.go.
	callerFor := func(_ context.Context, _ mcp.CallToolRequest) (resreg.Caller, error) {
		claims := map[string]string{}
		for _, g := range groups {
			if g == "**" {
				claims["operator"] = "1"
				break
			}
		}
		return resreg.Caller{Sub: sub, Name: name, Claims: claims}, nil
	}
	resreg.MCPTools(srv, routesForwarder(c, sub, name, groups), callerFor)
}

// routesForwarder is the resreg.Resource that forwards every action to
// proxyd over HTTP. Same MCPTools shape as proxyd's local declaration
// (parallel reaches drift over time — that's the cost; gain is one
// audit row + one auth gate at the downstream daemon).
func routesForwarder(c *proxydClient, sub, name string, groups []string) resreg.Resource {
	handler := func(ctx context.Context, x resreg.Execution) (any, error) {
		method, path, body, err := routesRESTSpec(x.Action, x.Args)
		if err != nil {
			return nil, err
		}
		raw, status, callErr := c.call(ctx, method, path, body, sub, name, groups)
		if callErr != nil {
			return nil, resreg.Errorf(http.StatusBadGateway, "proxyd call: %v", callErr)
		}
		if status >= 400 {
			return nil, resreg.Errorf(status, "%s %s: %s", method, path, strings.TrimSpace(string(raw)))
		}
		if len(raw) == 0 {
			return nil, nil
		}
		return json.RawMessage(raw), nil
	}
	return resreg.Resource{
		Name:      "routes",
		MCPTools:  routesMCPTools(),
		Authz:     func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) { return "", nil, nil },
		Handler:   handler,
		Store:     nil, // forwarder — proxyd writes the audit row
	}
}

// routesRESTSpec maps a resreg action+args to the REST call shape that
// proxyd's /v1/routes resource expects. Single point of translation.
func routesRESTSpec(action resreg.Action, args resreg.Args) (method, path string, body map[string]any, err error) {
	switch action {
	case resreg.ActionList:
		return "GET", "/v1/routes", nil, nil
	case resreg.ActionGet:
		p, _ := args["path"].(string)
		if strings.TrimSpace(p) == "" {
			return "", "", nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		return "GET", "/v1/routes/" + url.PathEscape(p), nil, nil
	case resreg.ActionCreate:
		return "POST", "/v1/routes", argsToBody(args), nil
	case resreg.ActionUpdate:
		p, _ := args["path"].(string)
		if strings.TrimSpace(p) == "" {
			return "", "", nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		b := argsToBody(args)
		delete(b, "path")
		return "PATCH", "/v1/routes/" + url.PathEscape(p), b, nil
	case resreg.ActionDelete:
		p, _ := args["path"].(string)
		if strings.TrimSpace(p) == "" {
			return "", "", nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		return "DELETE", "/v1/routes/" + url.PathEscape(p), nil, nil
	}
	return "", "", nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", action)
}

func argsToBody(args resreg.Args) map[string]any {
	body := map[string]any{}
	for _, key := range []string{"path", "backend", "auth", "gated_by"} {
		if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
			body[key] = v
		}
	}
	if v, ok := args["preserve_headers"].([]string); ok {
		body["preserve_headers"] = v
	}
	if v, ok := args["strip_prefix"].(bool); ok {
		body["strip_prefix"] = v
	}
	return body
}

func routesMCPTools() []resreg.MCPTool {
	return []resreg.MCPTool{
		{Name: "routes.list", Action: resreg.ActionList,
			Description: "List proxyd's runtime route table."},
		{Name: "routes.get", Action: resreg.ActionGet,
			Description: "Read one proxyd route by path.",
			Args: []resreg.MCPArg{{Name: "path", Type: "string", Required: true}}},
		{Name: "routes.create", Action: resreg.ActionCreate,
			Description: "Create a proxyd route. Body fields mirror the TOML proxyd_route block.",
			Args: []resreg.MCPArg{
				{Name: "path", Type: "string", Required: true},
				{Name: "backend", Type: "string", Required: true},
				{Name: "auth", Type: "string", Required: true, Description: "public | user | operator"},
				{Name: "gated_by", Type: "string"},
				{Name: "preserve_headers", Type: "array"},
				{Name: "strip_prefix", Type: "bool"},
			}},
		{Name: "routes.update", Action: resreg.ActionUpdate,
			Description: "Update fields on an existing proxyd route. Path is the key.",
			Args: []resreg.MCPArg{
				{Name: "path", Type: "string", Required: true},
				{Name: "backend", Type: "string"},
				{Name: "auth", Type: "string"},
				{Name: "gated_by", Type: "string"},
				{Name: "preserve_headers", Type: "array"},
				{Name: "strip_prefix", Type: "bool"},
			}},
		{Name: "routes.delete", Action: resreg.ActionDelete,
			Description: "Delete a proxyd route. Idempotent.",
			Args: []resreg.MCPArg{{Name: "path", Type: "string", Required: true}}},
	}
}

// isOperator: the `**` marker in the caller's groups claim is the operator
// gate (auth.MatchGroups). Mirrors proxyd/resource.go callerFromHTTP.
func isOperator(groups []string) bool {
	for _, g := range groups {
		if g == "**" {
			return true
		}
	}
	return false
}

// proxydClient is webd's tiny outbound HTTP client to proxyd. Signs
// identity headers with the shared hmacSecret so proxyd's resreg gate
// accepts the request.
type proxydClient struct {
	base       string
	hmacSecret string
	hc         *http.Client
}

func newProxydClient(base, hmacSecret string) *proxydClient {
	return &proxydClient{
		base:       strings.TrimRight(base, "/"),
		hmacSecret: hmacSecret,
		hc:         &http.Client{Timeout: 10 * time.Second},
	}
}

// call sends a signed REST request to proxyd. Returns the raw body, the
// HTTP status, and a transport error (if any). 4xx/5xx status codes do
// NOT produce an error — the caller maps them through resreg.Errorf.
func (c *proxydClient) call(ctx context.Context, method, path string, body map[string]any, sub, name string, groups []string) ([]byte, int, error) {
	var bodyR io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("encode body: %w", err)
		}
		bodyR = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyR)
	if err != nil {
		return nil, 0, fmt.Errorf("build req: %w", err)
	}
	if bodyR != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	groupsJSON, _ := json.Marshal(groups)
	req.Header.Set("X-User-Sub", sub)
	req.Header.Set("X-User-Name", name)
	req.Header.Set("X-User-Groups", string(groupsJSON))
	req.Header.Set("X-User-Sig",
		auth.SignHMAC(c.hmacSecret, auth.UserSigMessage(sub, name, string(groupsJSON))))
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, nil
}
