package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ExtTool is a REST-backed MCP tool loaded from [[ext]] TOML blocks.
type ExtTool struct {
	LocalName   string
	Description string
	Scope       string
	InputSchema json.RawMessage

	BaseURL string
	Method  string
	Path    string

	// Secrets names the keys the broker resolves for this tool; the folder's
	// other secrets are never visible to it. Same field, same meaning as
	// ConnectorSpec.Secrets — one declaration, both transports.
	Secrets []string

	// Headers, Query and Body are the three sinks a credential can land in.
	// Each value may carry "{secret:KEY}" placeholders, resolved per call by
	// RenderSecrets against the broker-resolved secrets — the SAME vocabulary
	// an `mcp` connector uses for its headers, which is what makes one
	// declaration serve both transports (spec 6/19).
	Headers map[string]string
	Query   map[string]string
	Body    map[string]string
}

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

// CallExtTool executes one REST-descriptor call and returns the HTTP response
// body as text. Secret values are scrubbed from the response before return.
//
// refresh, when non-nil, force-refreshes the tool's credentials and returns the
// re-resolved set. It is called AT MOST ONCE and ONLY on a 401: spec 5/15
// specifies a reactive retry for surrogate-OAuth providers whose `expires_in`
// is optimistic, where the proactive near-expiry refresh cannot fire because
// the stored expires_at still looks healthy. An expired access token is the
// transient case the repo's retry policy allows; a 401 that survives the
// refresh is NOT transient and surfaces to the agent unchanged. The retry is
// also skipped when the refresh returns the same values it was given — nothing
// was renewed, so re-sending them would only spend a second 401 (the common
// case: a pasted PAT has nothing to refresh).
func CallExtTool(
	ctx context.Context,
	tool ExtTool,
	args map[string]any,
	secrets map[string]string,
	refresh func(context.Context) (map[string]string, error),
) (*mcp.CallToolResult, error) {
	// Copy args so we can delete consumed path params.
	remaining := make(map[string]any, len(args))
	maps.Copy(remaining, args)

	// Render path params.
	path := pathParam.ReplaceAllStringFunc(tool.Path, func(m string) string {
		key := m[1 : len(m)-1]
		if v, ok := remaining[key]; ok {
			delete(remaining, key)
			return fmt.Sprintf("%v", v)
		}
		return m
	})

	status, text, fail := extAttempt(ctx, tool, tool.BaseURL+path, remaining, secrets)
	if fail != nil {
		return fail, nil
	}
	if status == http.StatusUnauthorized && refresh != nil {
		if fresh, err := refresh(ctx); err == nil && !maps.Equal(fresh, secrets) {
			secrets = fresh
			status, text, fail = extAttempt(ctx, tool, tool.BaseURL+path, remaining, secrets)
			if fail != nil {
				return fail, nil
			}
		}
	}
	if status >= 400 {
		return mcp.NewToolResultError(fmt.Sprintf("HTTP %d: %s", status, text)), nil
	}
	return mcp.NewToolResultText(text), nil
}

// extAttempt performs ONE request. Credentials ride the query string or the
// JSON body for some auth methods, so the whole request is rebuilt per attempt
// rather than reused. A non-nil result is a terminal failure (transport, encode
// or read) already shaped for the agent; otherwise status+text are the response.
func extAttempt(
	ctx context.Context,
	tool ExtTool,
	baseURL string,
	remaining map[string]any,
	secrets map[string]string,
) (int, string, *mcp.CallToolResult) {
	rawURL := baseURL

	// Build query string for GET/DELETE, or JSON body for mutating methods.
	var body io.Reader

	method := strings.ToUpper(tool.Method)
	switch method {
	case "GET", "DELETE", "HEAD":
		if len(remaining) > 0 {
			q := url.Values{}
			for k, v := range remaining {
				q.Set(k, fmt.Sprintf("%v", v))
			}
			rawURL += "?" + q.Encode()
		}
	default:
		// POST/PUT/PATCH: build JSON body.
		bodyMap := make(map[string]any, len(remaining))
		maps.Copy(bodyMap, remaining)
		for k, v := range RenderSecrets(tool.Body, secrets) {
			bodyMap[k] = v
		}
		b, err := json.Marshal(bodyMap)
		if err != nil {
			return 0, "", mcp.NewToolResultError("marshal body: " + err.Error())
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return 0, "", mcp.NewToolResultError("build request: " + err.Error())
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Wire authentication: the declared sinks, rendered. There is no auth-method
	// enum to switch on — a bearer IS `Authorization = "Bearer {secret:K}"`, and
	// a fifth provider needs no new case here.
	for k, v := range RenderSecrets(tool.Headers, secrets) {
		req.Header.Set(k, v)
	}
	if q := RenderSecrets(tool.Query, secrets); len(q) > 0 {
		vals := req.URL.Query()
		for k, v := range q {
			vals.Set(k, v)
		}
		req.URL.RawQuery = vals.Encode()
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// *url.Error.Error() embeds the full request URL, which for
		// apikey-query carries the secret in RawQuery — scrub before returning.
		return 0, "", mcp.NewToolResultError(scrubSecrets("http: "+err.Error(), secrets))
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", mcp.NewToolResultError("read response: " + err.Error())
	}
	return resp.StatusCode, scrubSecrets(string(respBytes), secrets), nil
}

// scrubSecrets replaces every non-empty secret value in s with «redacted».
func scrubSecrets(s string, secrets map[string]string) string {
	for _, v := range secrets {
		if v == "" {
			continue
		}
		s = strings.ReplaceAll(s, v, "«redacted»")
	}
	return s
}
