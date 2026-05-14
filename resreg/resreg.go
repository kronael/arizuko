// Package resreg implements the uniform Resource registry from spec
// 6/5-uniform-mcp-rest.md: one Handler per (Resource, Action), wrapped
// by two auto-adapters (REST + MCP) so any caller surface reaches the
// same code. Today the only resource is proxyd's runtime route table
// (spec 6/2 Phase-3); the package is built so additional resources
// (grants, scheduled_tasks, ...) drop in as struct literals.
//
// The spec-6/5 Go sketch is the contract; this file is the
// implementation. Surface-specific concerns (HTTP status mapping, MCP
// tool argument validation) live next to the adapters.
package resreg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Action is the short verb constant (Create, Update, List, Get, Delete
// or any resource-specific shape). The composed string
// `<Resource.Name>.<Action>` is the operator-facing contract — used as
// MCP tool name and audit-log action= field.
type Action string

const (
	ActionList   Action = "list"
	ActionGet    Action = "get"
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
)

// Caller is the surface-agnostic principal built by each adapter from
// its identity carrier. Folder is the caller's home folder ("" for
// operators); Scope is the flat list of `<resource>:<verb>[:own_group]`
// strings minted at session bind.
type Caller struct {
	Sub    string
	Name   string
	Folder string
	Tier   int
	Scope  []string
}

// ScopePred decides whether the caller is allowed to invoke an action
// against a target folder. Returning false → 403/error. Target is the
// folder the action operates on; pass "" for global resources where
// :own_group has no meaning (routes is one such resource).
type ScopePred func(c Caller, target string) bool

// Args is the JSON-decoded argument map an adapter passes in. URL-path
// values (e.g. {path}) are merged into Args under their parameter name
// before the handler sees them, so the handler reads one source.
type Args map[string]any

// Handler is the single per-resource entry point. It dispatches on the
// Action; the adapters never call resource-specific code directly.
type Handler func(ctx context.Context, c Caller, action Action, args Args) (any, error)

// Endpoint declares one REST face of an action. Verb is the HTTP method.
// Path is a stdlib-mux pattern; placeholders ({name}) are bound to Args
// keys of the same name. Status is the success response code (200, 201,
// 204).
type Endpoint struct {
	Verb   string
	Path   string
	Action Action
	Status int
}

// MCPTool declares one MCP face. Name is the surfaced tool name
// (spec-6/5 convention: `<resource>.<action>`); Description is the
// agent-facing one-liner; ArgSpec is the JSON-Schema-shaped parameter
// list the auto-adapter materialises into mcp.WithString/Number/etc.
type MCPTool struct {
	Name        string
	Action      Action
	Description string
	Args        []MCPArg
}

type MCPArg struct {
	Name        string
	Type        string // "string" | "number" | "bool" | "array"
	Description string
	Required    bool
}

// Resource ties the three faces together: identity (Name + Policy +
// Handler) and registration metadata (Endpoints + MCPTools). One literal
// per resource per daemon; spec 6/5 §"Single source of truth".
type Resource struct {
	Name      string
	Endpoints []Endpoint
	MCPTools  []MCPTool
	Policy    map[Action]ScopePred
	Handler   Handler
}

// HandlerError is the canonical error type a Handler returns to signal
// HTTP status / MCP error mapping. The adapters translate Code into
// HTTP status and MCP error result.
type HandlerError struct {
	Code int // matches HTTP semantics: 400/404/409/403/500
	Msg  string
}

func (e *HandlerError) Error() string { return e.Msg }

func Errorf(code int, format string, a ...any) error {
	return &HandlerError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

func errStatus(err error) int {
	var he *HandlerError
	if errors.As(err, &he) && he.Code != 0 {
		return he.Code
	}
	return http.StatusInternalServerError
}

// CallerFromHTTP builds a Caller from proxyd's signed identity headers.
// Operators (carrying the `**` user_groups marker) receive the wildcard
// scope set automatically — JWT scope claims are a future hook.
type CallerFromHTTPFunc func(r *http.Request) (Caller, error)

// RESTHandler emits an http.Handler that dispatches every endpoint of
// r. Caller of RESTHandler mounts the returned handler on the mux using
// the same method+path patterns r.Endpoints declares (cf. RegisterREST).
func RESTHandler(r Resource, build CallerFromHTTPFunc, e Endpoint) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		caller, err := build(req)
		if err != nil {
			writeREST(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return
		}
		args, err := decodeRESTArgs(req, e)
		if err != nil {
			writeREST(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		pred, ok := r.Policy[e.Action]
		if !ok {
			writeREST(w, http.StatusForbidden, map[string]any{"error": "no policy for action"})
			return
		}
		target := stringArg(args, "folder")
		if !pred(caller, target) {
			audit(caller, r.Name, e.Action, "rest", target, "denied")
			writeREST(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
			return
		}
		res, err := r.Handler(req.Context(), caller, e.Action, args)
		if err != nil {
			audit(caller, r.Name, e.Action, "rest", target, "error")
			writeREST(w, errStatus(err), map[string]any{"error": err.Error()})
			return
		}
		audit(caller, r.Name, e.Action, "rest", target, "allowed")
		status := e.Status
		if status == 0 {
			status = http.StatusOK
		}
		if res == nil {
			w.WriteHeader(status)
			return
		}
		writeREST(w, status, res)
	})
}

// RegisterREST mounts every endpoint of r on mux using the build func
// to derive a Caller from the request. Pattern is "<VERB> <PATH>" per
// stdlib mux conventions; path placeholders bind to Args keys.
func RegisterREST(mux *http.ServeMux, r Resource, build CallerFromHTTPFunc) {
	for _, e := range r.Endpoints {
		pattern := e.Verb + " " + e.Path
		mux.Handle(pattern, RESTHandler(r, build, e))
	}
}

func decodeRESTArgs(req *http.Request, e Endpoint) (Args, error) {
	args := Args{}
	// JSON body (POST/PATCH/PUT).
	if req.Body != nil && (req.Method == "POST" || req.Method == "PATCH" || req.Method == "PUT") {
		var raw map[string]any
		dec := json.NewDecoder(req.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&raw); err != nil && err.Error() != "EOF" {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		for k, v := range raw {
			args[k] = v
		}
	}
	// Path placeholders ({name}, {name...}) — overwrite body values so the URL wins.
	for _, seg := range strings.Split(e.Path, "/") {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		name := strings.Trim(seg, "{}")
		name = strings.TrimSuffix(name, "...")
		if v := req.PathValue(name); v != "" {
			args[name] = v
		}
	}
	return args, nil
}

func writeREST(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func stringArg(args Args, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// MCPTools registers every MCP tool of r on srv. build maps the
// surface-specific identity (capability token, etc.) to a Caller.
func MCPTools(srv *mcpserver.MCPServer, r Resource, caller Caller) {
	for _, t := range r.MCPTools {
		opts := []mcp.ToolOption{mcp.WithDescription(t.Description)}
		for _, a := range t.Args {
			opts = append(opts, mcpArgOption(a))
		}
		tool := mcp.NewTool(t.Name, opts...)
		action := t.Action
		args := append([]MCPArg(nil), t.Args...)
		srv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := Args{}
			for _, spec := range args {
				switch spec.Type {
				case "number":
					if v, ok := numArg(req, spec.Name); ok {
						a[spec.Name] = v
					}
				case "bool":
					a[spec.Name] = req.GetBool(spec.Name, false)
				case "array":
					if v := req.GetStringSlice(spec.Name, nil); v != nil {
						a[spec.Name] = v
					}
				default:
					if v := req.GetString(spec.Name, ""); v != "" {
						a[spec.Name] = v
					}
				}
			}
			pred, ok := r.Policy[action]
			if !ok {
				return mcp.NewToolResultError("no policy for action"), nil
			}
			target := stringArg(a, "folder")
			if !pred(caller, target) {
				audit(caller, r.Name, action, "mcp", target, "denied")
				return mcp.NewToolResultError("forbidden"), nil
			}
			res, err := r.Handler(ctx, caller, action, a)
			if err != nil {
				audit(caller, r.Name, action, "mcp", target, "error")
				return mcp.NewToolResultError(err.Error()), nil
			}
			audit(caller, r.Name, action, "mcp", target, "allowed")
			if res == nil {
				return mcp.NewToolResultText("{}"), nil
			}
			b, _ := json.Marshal(res)
			return mcp.NewToolResultText(string(b)), nil
		})
	}
}

func mcpArgOption(a MCPArg) mcp.ToolOption {
	var opts []mcp.PropertyOption
	if a.Description != "" {
		opts = append(opts, mcp.Description(a.Description))
	}
	if a.Required {
		opts = append(opts, mcp.Required())
	}
	switch a.Type {
	case "number":
		return mcp.WithNumber(a.Name, opts...)
	case "bool":
		return mcp.WithBoolean(a.Name, opts...)
	case "array":
		return mcp.WithArray(a.Name, opts...)
	default:
		return mcp.WithString(a.Name, opts...)
	}
}

// numArg pulls a number arg out of the MCP request without GetFloat,
// which the upstream mcp-go API doesn't expose uniformly across types.
func numArg(req mcp.CallToolRequest, name string) (float64, bool) {
	v, ok := req.GetArguments()[name]
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	}
	return 0, false
}

// audit emits the spec-6/5 fixed-shape audit row. Single sink so every
// adapter logs identically; grep `resource=routes action=create` to find
// every mutation regardless of surface.
func audit(c Caller, resource string, action Action, surface, target, result string) {
	slog.Info("resreg",
		"caller", c.Sub, "resource", resource, "action", string(action),
		"surface", surface, "target", target, "result", result)
}

// HasScope checks whether c has the named scope. `<resource>:*` matches
// any verb on the resource; explicit `<resource>:<verb>` matches the
// verb. `:own_group` suffix matching is not relevant for the routes
// resource (global); add when the first per-folder resource lands.
func HasScope(c Caller, resource, verb string) bool {
	full := resource + ":" + verb
	wildcard := resource + ":*"
	for _, s := range c.Scope {
		if s == full || s == wildcard {
			return true
		}
	}
	return false
}
