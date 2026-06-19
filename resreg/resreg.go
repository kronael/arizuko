// Package resreg implements the uniform Resource registry from spec
// 5/5-uniform-mcp-rest.md: one Handler per (Resource, Action), wrapped
// by two auto-adapters (REST + MCP) so any caller surface reaches the
// same code. Resources using it: proxyd's runtime route table
// (proxyd/resource.go) and webd's operator-side MCP forwarder
// (webd/routes_mcp.go). Migration of ipc/ipc.go is pending.
//
// Design (post oracle critique 2026-05-25):
//
//   - Per-invocation caller resolution. MCPTools takes a callerFor(ctx, req)
//     resolver, not a captured Caller — shared MCP servers no longer
//     collapse every call to one principal at registration time.
//   - Canonical ACL gate. Resource.Authz returns (scope, params); the
//     adapter calls auth.Authorize(...) per specs/4/9-acl-unified.md. No
//     parallel scope predicate machinery.
//   - Tx-bound audit. State-changing actions run inside a SQL
//     transaction: handler does its work via Execution.Tx, the adapter
//     writes one audit_log row via audit.EmitInTx in the SAME tx, and
//     the tx commits as a unit. On any error the tx rolls back; the
//     audit row never outlives the mutation it claims to record.
//   - Forwarder pattern. Resources with Store nil (e.g. webd's HTTP
//     forwarder to proxyd) skip the tx/audit dance — the downstream
//     daemon writes the row. Avoids double-logging.
package resreg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// Action is the short verb constant (Create, Update, List, Get, Delete
// or any resource-specific shape). The composed string
// `<Resource.Name>:<Action>` is the operator-facing contract — used as
// the ACL action key and the audit-log action field.
type Action string

const (
	ActionList   Action = "list"
	ActionGet    Action = "get"
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
)

// Mutates reports whether an action writes state. Read-only actions
// (list, get) emit slog only; mutating actions write one audit_log row
// inside the same tx as the resource mutation.
func (a Action) Mutates() bool {
	switch a {
	case ActionList, ActionGet:
		return false
	}
	return true
}

// Caller is the surface-agnostic principal built by each adapter from
// its identity carrier. Folder is the caller's home folder ("" for
// operators); Claims carry JWT claims used by ACL row predicates.
type Caller struct {
	Sub    string
	Name   string
	Folder string
	Claims map[string]string
}

// Args is the JSON-decoded argument map an adapter passes in. URL-path
// values (e.g. {path}) are merged into Args under their parameter name
// before the handler sees them, so the handler reads one source.
type Args map[string]any

// Execution carries everything a handler needs to act + emit audit.
// The adapter constructs it once per request; handlers honour Tx for
// mutating actions on store-backed resources and ignore it otherwise.
type Execution struct {
	Caller    Caller
	Action    Action
	Resource  string
	Args      Args
	TurnID    string  // X-Turn-Id header (REST) or _meta.turn_id (MCP)
	RequestID string  // X-Request-Id (REST) or _meta.request_id (MCP)
	SourceIP  string  // REST only
	Surface   string  // "rest" | "mcp"
	Tx        *sql.Tx // non-nil only when Resource.Store != nil and action mutates
}

// Handler is the single per-resource entry point. The adapter dispatches
// on Execution.Action; the handler runs its mutation inside Execution.Tx
// when Tx is non-nil.
type Handler func(ctx context.Context, x Execution) (any, error)

// Endpoint declares one REST face of an action. Verb is the HTTP method.
// Path is a stdlib-mux pattern; placeholders ({name}) bind to Args keys
// of the same name. Status is the success response code.
type Endpoint struct {
	Verb   string
	Path   string
	Action Action
	Status int
}

// MCPTool declares one MCP face. Name is the surfaced tool name
// (`<resource>.<action>`); Description is the agent-facing one-liner;
// Args is the JSON-Schema-shaped parameter list the auto-adapter
// materialises into mcp.WithString/Number/etc.
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

// Resource ties identity (Name + Authz + Handler) to registration
// metadata (Endpoints + MCPTools). One literal per resource per daemon.
//
// Authz returns (scope, params, err). The adapter then calls
// auth.Authorize(Store, authCaller, "<Name>:<action>", scope, params)
// as the canonical ACL gate. Returning err short-circuits the call
// (e.g. validation -> 400) without touching auth.
//
// Store, when non-nil, makes the adapter open a tx for mutating actions
// and pass it through Execution.Tx, then write the audit_log row inside
// the same tx via audit.EmitInTx. Set Store nil for forwarder resources
// (webd → proxyd) — the downstream daemon writes the audit row.
type Resource struct {
	Name      string
	Endpoints []Endpoint
	MCPTools  []MCPTool
	Authz     func(c Caller, action Action, args Args) (scope string, params map[string]string, err error)
	Handler   Handler
	Store     *store.Store

	// Schema half (spec 5/36). All optional. Resources that set
	// RowType+Table are "engine-managed" and get generic CRUD via
	// engine.go. Resources without RowType are forwarders or custom-
	// shape — still valid, just not engine-driven.
	RowType  reflect.Type // canonical row struct (zero-value)
	Table    string       // physical SQL table
	PKFields []string     // Go field names making up the natural PK
	Scope    ScopeSpec    // DeleteScope filter; zero = no per-scope op
	Hooks    Hooks        // optional semantics callbacks

	// SkipApplyRebuild causes Apply to skip DELETE+INSERT for this resource.
	// Set true for resources whose tables hold operator data that the engine
	// can read (ScanAll for export) but must not rebuild from manifest —
	// e.g. `secrets`, whose enc_value blob is set imperatively. Resources
	// in this state still appear in Export output (metadata only).
	SkipApplyRebuild bool

	// StampedFields names the Go struct fields a BeforeInsert hook stamps
	// server-side (created_at, granted_at, added_at, …). Diff ignores them
	// when comparing payloads, so a hand-written manifest that omits them
	// reads as `unchanged` against a live stamped row instead of phantom-
	// updating on every plan (spec 5/36 §"Apply lifecycle" step 3).
	StampedFields []string

	meta *resourceMeta // populated by Register; reflection-derived
}

// HandlerError carries the HTTP status / MCP error code a handler wants
// to surface. The adapters translate Code into HTTP status and MCP
// error result; non-HandlerError errors map to 500.
type HandlerError struct {
	Code int
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

// CallerFromHTTPFunc builds a Caller from one HTTP request. Called
// per-invocation by the REST adapter.
type CallerFromHTTPFunc func(r *http.Request) (Caller, error)

// CallerFromMCPFunc builds a Caller from one MCP CallToolRequest + ctx.
// Called per-invocation by the MCP adapter — the resolver runs every
// time the agent invokes the tool, never at registration time.
type CallerFromMCPFunc func(ctx context.Context, req mcp.CallToolRequest) (Caller, error)

// RegisterREST mounts every endpoint of r on mux. build derives the
// surface-specific Caller from each request.
func RegisterREST(mux *http.ServeMux, r Resource, build CallerFromHTTPFunc) {
	for _, e := range r.Endpoints {
		mux.Handle(e.Verb+" "+e.Path, restHandler(r, build, e))
	}
}

func restHandler(r Resource, build CallerFromHTTPFunc, e Endpoint) http.Handler {
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
		x := Execution{
			Caller:    caller,
			Action:    e.Action,
			Resource:  r.Name,
			Args:      args,
			TurnID:    req.Header.Get("X-Turn-Id"),
			RequestID: req.Header.Get("X-Request-Id"),
			SourceIP:  clientIP(req),
			Surface:   audit.SurfaceREST,
		}
		res, status, err := invoke(req.Context(), r, x)
		if err != nil {
			writeREST(w, status, map[string]any{"error": err.Error()})
			return
		}
		out := e.Status
		if out == 0 {
			out = http.StatusOK
		}
		if res == nil {
			w.WriteHeader(out)
			return
		}
		writeREST(w, out, res)
	})
}

// MCPTools registers every MCP tool of r on srv. callerFor is invoked
// per call (not at registration) to avoid privilege confusion.
func MCPTools(srv *mcpserver.MCPServer, r Resource, callerFor CallerFromMCPFunc) {
	for _, t := range r.MCPTools {
		opts := []mcp.ToolOption{mcp.WithDescription(t.Description)}
		for _, a := range t.Args {
			opts = append(opts, mcpArgOption(a))
		}
		tool := mcp.NewTool(t.Name, opts...)
		action := t.Action
		argSpec := append([]MCPArg(nil), t.Args...)
		srv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			caller, err := callerFor(ctx, req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			x := Execution{
				Caller:    caller,
				Action:    action,
				Resource:  r.Name,
				Args:      decodeMCPArgs(req, argSpec),
				TurnID:    mcpMetaString(req, "turn_id"),
				RequestID: mcpMetaString(req, "request_id"),
				Surface:   audit.SurfaceMCP,
			}
			res, _, err := invoke(ctx, r, x)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if res == nil {
				return mcp.NewToolResultText("{}"), nil
			}
			b, jerr := json.Marshal(res)
			if jerr != nil {
				slog.Warn("resreg: marshal mcp result", "resource", r.Name, "action", action, "err", jerr)
				return mcp.NewToolResultError("encode result: " + jerr.Error()), nil
			}
			return mcp.NewToolResultText(string(b)), nil
		})
	}
}

// invoke is the per-call core: authz → (optional tx) → handler →
// audit → commit/rollback. Shared between REST and MCP adapters so
// the path is provably one-rendered.
func invoke(ctx context.Context, r Resource, x Execution) (any, int, error) {
	start := time.Now()
	forward := r.Store == nil // forwarder: downstream daemon logs
	scope, params, err := r.Authz(x.Caller, x.Action, x.Args)
	if err != nil {
		emitAudit(ctx, nil, x, scope, params, outcomeFor(err), err.Error(), start, forward)
		return nil, errStatus(err), err
	}
	if !forward {
		if !auth.Authorize(r.Store, authCaller(x.Caller), actionKey(r.Name, x.Action), scope, params) {
			derr := Errorf(http.StatusForbidden, "forbidden")
			emitAudit(ctx, nil, x, scope, params, audit.OutcomeDenied, "forbidden", start, false)
			return nil, http.StatusForbidden, derr
		}
	}
	if !x.Action.Mutates() || forward {
		res, herr := r.Handler(ctx, x)
		if herr != nil {
			emitAudit(ctx, nil, x, scope, params, audit.OutcomeError, herr.Error(), start, forward)
			return nil, errStatus(herr), herr
		}
		emitAudit(ctx, nil, x, scope, params, audit.OutcomeOK, "", start, forward)
		return res, 0, nil
	}
	tx, txErr := r.Store.DB().BeginTx(ctx, nil)
	if txErr != nil {
		emitAudit(ctx, nil, x, scope, params, audit.OutcomeError, txErr.Error(), start, false)
		return nil, http.StatusInternalServerError, Errorf(http.StatusInternalServerError, "begin tx: %v", txErr)
	}
	x.Tx = tx
	res, herr := r.Handler(ctx, x)
	if herr != nil {
		_ = tx.Rollback()
		emitAudit(ctx, nil, x, scope, params, audit.OutcomeError, herr.Error(), start, false)
		return nil, errStatus(herr), herr
	}
	if aerr := emitAudit(ctx, tx, x, scope, params, audit.OutcomeOK, "", start, false); aerr != nil {
		_ = tx.Rollback()
		slog.Error("resreg: audit emit failed; mutation rolled back",
			"resource", r.Name, "action", x.Action, "err", aerr)
		return nil, http.StatusInternalServerError, Errorf(http.StatusInternalServerError, "audit emit: %v", aerr)
	}
	if cerr := tx.Commit(); cerr != nil {
		emitAudit(ctx, nil, x, scope, params, audit.OutcomeError, cerr.Error(), start, false)
		return nil, http.StatusInternalServerError, Errorf(http.StatusInternalServerError, "commit: %v", cerr)
	}
	return res, 0, nil
}

func actionKey(resource string, action Action) string {
	return resource + ":" + string(action)
}

func authCaller(c Caller) auth.Caller {
	return auth.Caller{Principal: c.Sub, Claims: c.Claims}
}

// emitAudit writes one audit_log row (via tx when non-nil, via the
// package-level DB otherwise) AND a slog line. Read-only OK outcomes
// skip the DB row (volume); denials and errors always land in the
// table so privilege-escalation forensics work. Forwarders skip the
// DB row entirely — the downstream daemon writes it. Returns the
// EmitInTx error so the caller can roll back the mutation if audit
// insert fails.
func emitAudit(ctx context.Context, tx *sql.Tx, x Execution, scope string, params map[string]string, outcome, errMsg string, start time.Time, forwarder bool) error {
	e := buildEvent(x, scope, params, outcome, errMsg, start)
	slog.Info("resreg",
		"caller", e.ActorSub, "resource", e.Resource, "action", e.Action,
		"surface", e.Surface, "outcome", e.Outcome, "duration_ms", e.DurationMS)
	if tx != nil {
		return audit.EmitInTx(ctx, tx, e)
	}
	if forwarder {
		return nil
	}
	if !x.Action.Mutates() && outcome == audit.OutcomeOK {
		return nil
	}
	audit.Emit(ctx, e)
	return nil
}

func buildEvent(x Execution, scope string, params map[string]string, outcome, errMsg string, start time.Time) audit.Event {
	ps := map[string]any{}
	for k, v := range x.Args {
		ps[k] = v
	}
	if scope != "" {
		ps["_scope"] = scope
	}
	for k, v := range params {
		ps["_p_"+k] = v
	}
	cat := audit.CategoryMutation
	if !x.Action.Mutates() {
		cat = audit.CategoryAccess
	}
	if outcome == audit.OutcomeDenied {
		cat = audit.CategoryAuthZ
	}
	return audit.Event{
		Category:      cat,
		Action:        actionKey(x.Resource, x.Action),
		Actor:         x.Caller.Sub,
		ActorSub:      x.Caller.Sub,
		Resource:      x.Resource,
		Scope:         scope,
		Surface:       x.Surface,
		ParamsSummary: ps,
		Outcome:       outcome,
		ErrorMsg:      errMsg,
		DurationMS:    time.Since(start).Milliseconds(),
		TurnID:        x.TurnID,
		Folder:        x.Caller.Folder,
		RequestID:     x.RequestID,
		SourceIP:      x.SourceIP,
	}
}

func outcomeFor(err error) string {
	if errStatus(err) == http.StatusForbidden {
		return audit.OutcomeDenied
	}
	return audit.OutcomeError
}

// decodeRESTArgs merges JSON body fields and URL path placeholders.
// The path wins (URL is authoritative for {path...}).
func decodeRESTArgs(req *http.Request, e Endpoint) (Args, error) {
	args := Args{}
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

func decodeMCPArgs(req mcp.CallToolRequest, spec []MCPArg) Args {
	a := Args{}
	for _, s := range spec {
		switch s.Type {
		case "number":
			if v, ok := numArg(req, s.Name); ok {
				a[s.Name] = v
			}
		case "bool":
			if _, present := req.GetArguments()[s.Name]; present {
				a[s.Name] = req.GetBool(s.Name, false)
			}
		case "array":
			if v := req.GetStringSlice(s.Name, nil); v != nil {
				a[s.Name] = v
			}
		default:
			if v := req.GetString(s.Name, ""); v != "" {
				a[s.Name] = v
			}
		}
	}
	return a
}

func mcpMetaString(req mcp.CallToolRequest, key string) string {
	meta, ok := req.GetArguments()["_meta"].(map[string]any)
	if !ok {
		return ""
	}
	v, _ := meta[key].(string)
	return v
}

func writeREST(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("resreg: rest encode failed", "code", code, "err", err)
	}
}

// clientIP picks the best-effort source IP off RemoteAddr (form host:port).
// X-Forwarded-For is intentionally not consulted — proxyd injects
// identity headers; backends never trust client-provided headers.
func clientIP(req *http.Request) string {
	host := req.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
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
