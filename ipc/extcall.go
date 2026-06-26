package ipc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

	// AuthMethod is one of: bearer, apikey-header, apikey-query, basic,
	// json-body.
	AuthMethod string
	// SecretKey is the secret-store key for the primary credential.
	SecretKey  string
	SecretKey2 string
	// Header/Header2 are the header names (apikey-header) or JSON body
	// key names (json-body) for the credentials.
	Header  string
	Header2 string
	// Param is the query-param name for apikey-query auth.
	Param string
}

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

// CallExtTool executes one REST-descriptor call and returns the HTTP response
// body as text. Secret values are scrubbed from the response before return.
func CallExtTool(
	ctx context.Context,
	tool ExtTool,
	args map[string]any,
	secrets map[string]string,
) (*mcp.CallToolResult, error) {
	// Copy args so we can delete consumed path params.
	remaining := make(map[string]any, len(args))
	for k, v := range args {
		remaining[k] = v
	}

	// Render path params.
	path := pathParam.ReplaceAllStringFunc(tool.Path, func(m string) string {
		key := m[1 : len(m)-1]
		if v, ok := remaining[key]; ok {
			delete(remaining, key)
			return fmt.Sprintf("%v", v)
		}
		return m
	})

	rawURL := tool.BaseURL + path

	// Build query string for GET/DELETE, or JSON body for mutating methods.
	var body io.Reader
	header := make(http.Header)

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
		for k, v := range remaining {
			bodyMap[k] = v
		}
		if tool.AuthMethod == "json-body" {
			if tool.Header != "" && tool.SecretKey != "" {
				bodyMap[tool.Header] = secrets[tool.SecretKey]
			}
			if tool.Header2 != "" && tool.SecretKey2 != "" {
				bodyMap[tool.Header2] = secrets[tool.SecretKey2]
			}
		}
		b, err := json.Marshal(bodyMap)
		if err != nil {
			return mcp.NewToolResultError("marshal body: " + err.Error()), nil
		}
		body = bytes.NewReader(b)
		header.Set("Content-Type", "application/json")
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return mcp.NewToolResultError("build request: " + err.Error()), nil
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}

	// Wire authentication.
	switch tool.AuthMethod {
	case "bearer":
		if v := secrets[tool.SecretKey]; v != "" {
			req.Header.Set("Authorization", "Bearer "+v)
		}
	case "apikey-header":
		if tool.Header != "" {
			req.Header.Set(tool.Header, secrets[tool.SecretKey])
		}
	case "apikey-query":
		if tool.Param != "" {
			q := req.URL.Query()
			q.Set(tool.Param, secrets[tool.SecretKey])
			req.URL.RawQuery = q.Encode()
		}
	case "basic":
		if v := secrets[tool.SecretKey]; v != "" {
			encoded := base64.StdEncoding.EncodeToString([]byte(v))
			req.Header.Set("Authorization", "Basic "+encoded)
		}
	case "json-body":
		// Credentials already injected into the body above.
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mcp.NewToolResultError("http: " + err.Error()), nil
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return mcp.NewToolResultError("read response: " + err.Error()), nil
	}

	text := scrubSecrets(string(respBytes), secrets)

	if resp.StatusCode >= 400 {
		return mcp.NewToolResultError(
			fmt.Sprintf("HTTP %d: %s", resp.StatusCode, text),
		), nil
	}
	return mcp.NewToolResultText(text), nil
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
