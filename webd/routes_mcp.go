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
)

// registerRoutesMCP attaches the five `routes.*` MCP tools to srv. The
// tools are a thin operator surface over proxyd's `/v1/routes` REST API
// (spec 6/2 Phase-3). One handler in proxyd, two adapters: REST (proxyd's
// own mux) and MCP (here, via webd). Spec 6/5 §"Single source of truth".
//
// Authorization is enforced by proxyd: webd signs identity headers from
// the operator's session; proxyd's resreg.RESTHandler checks the operator
// scope (`routes:*`) before dispatch. webd's role is identity bridging.
func registerRoutesMCP(srv *mcpserver.MCPServer, c *proxydClient, sub, name string, groups []string) {
	// Operator-only: tools become invisible to non-operators. Cheap gate.
	if !isOperator(groups) {
		return
	}

	srv.AddTool(mcp.NewTool("routes.list",
		mcp.WithDescription("List proxyd's runtime route table."),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return c.call(ctx, "GET", "/v1/routes", nil, sub, name, groups)
	})

	srv.AddTool(mcp.NewTool("routes.get",
		mcp.WithDescription("Read one proxyd route by path."),
		mcp.WithString("path", mcp.Required()),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := strings.TrimSpace(req.GetString("path", ""))
		if path == "" {
			return mcp.NewToolResultError("path required"), nil
		}
		return c.call(ctx, "GET", "/v1/routes/"+url.PathEscape(path), nil, sub, name, groups)
	})

	srv.AddTool(mcp.NewTool("routes.create",
		mcp.WithDescription("Create a proxyd route. Body fields mirror the TOML proxyd_route block."),
		mcp.WithString("path", mcp.Required()),
		mcp.WithString("backend", mcp.Required()),
		mcp.WithString("auth", mcp.Required(), mcp.Description("public | user | operator")),
		mcp.WithString("gated_by"),
		mcp.WithArray("preserve_headers"),
		mcp.WithBoolean("strip_prefix"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body := routeArgs(req)
		return c.call(ctx, "POST", "/v1/routes", body, sub, name, groups)
	})

	srv.AddTool(mcp.NewTool("routes.update",
		mcp.WithDescription("Update fields on an existing proxyd route. Path is the key."),
		mcp.WithString("path", mcp.Required()),
		mcp.WithString("backend"),
		mcp.WithString("auth"),
		mcp.WithString("gated_by"),
		mcp.WithArray("preserve_headers"),
		mcp.WithBoolean("strip_prefix"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := strings.TrimSpace(req.GetString("path", ""))
		if path == "" {
			return mcp.NewToolResultError("path required"), nil
		}
		body := routeArgs(req)
		delete(body, "path")
		return c.call(ctx, "PATCH", "/v1/routes/"+url.PathEscape(path), body, sub, name, groups)
	})

	srv.AddTool(mcp.NewTool("routes.delete",
		mcp.WithDescription("Delete a proxyd route. Idempotent."),
		mcp.WithString("path", mcp.Required()),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path := strings.TrimSpace(req.GetString("path", ""))
		if path == "" {
			return mcp.NewToolResultError("path required"), nil
		}
		return c.call(ctx, "DELETE", "/v1/routes/"+url.PathEscape(path), nil, sub, name, groups)
	})
}

// routeArgs assembles a map[string]any from the MCP request, skipping
// unset fields. Sent verbatim as the REST body.
func routeArgs(req mcp.CallToolRequest) map[string]any {
	body := map[string]any{}
	for _, key := range []string{"path", "backend", "auth", "gated_by"} {
		if v := strings.TrimSpace(req.GetString(key, "")); v != "" {
			body[key] = v
		}
	}
	if hs := req.GetStringSlice("preserve_headers", nil); hs != nil {
		body["preserve_headers"] = hs
	}
	if _, present := req.GetArguments()["strip_prefix"]; present {
		body["strip_prefix"] = req.GetBool("strip_prefix", false)
	}
	return body
}

// isOperator: the `**` marker in user_groups is today's operator gate
// (auth.MatchGroups). Mirrors proxyd/resource.go callerFromHTTP.
func isOperator(groups []string) bool {
	for _, g := range groups {
		if g == "**" {
			return true
		}
	}
	return false
}

// proxydClient is webd's tiny outbound HTTP client to proxyd. Signs
// identity headers with the shared hmacSecret so proxyd's
// resreg.callerFromHTTP accepts the request.
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

func (c *proxydClient) call(ctx context.Context, method, path string, body map[string]any, sub, name string, groups []string) (*mcp.CallToolResult, error) {
	var bodyR io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("encode body: %v", err)), nil
		}
		bodyR = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyR)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("build req: %v", err)), nil
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
		return mcp.NewToolResultError(fmt.Sprintf("proxyd call: %v", err)), nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return mcp.NewToolResultError(fmt.Sprintf("proxyd %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))), nil
	}
	if len(raw) == 0 {
		return mcp.NewToolResultText("{}"), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}
