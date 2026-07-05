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
	"io"
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

// MCPTool is the DERIVED MCP face of one action — produced by
// deriveMCPTools, never hand-authored. Name is the surfaced tool name
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
// metadata (Endpoints + MCP doc/args). One literal per resource per
// daemon. MCP tools are DERIVED from Endpoints (deriveMCPTools), never
// hand-authored — one tool per endpoint whose Action has an MCPDoc
// entry, so the description map is also the "surface this action as a
// tool" signal.
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
	Authz     func(c Caller, action Action, args Args) (scope string, params map[string]string, err error)
	Handler   Handler
	Store     *store.Store

	// MCPDoc maps an Action to its agent-facing one-liner. An endpoint
	// whose Action has an entry here surfaces as an MCP tool named
	// `<Name>.<action>`; the description is the one irreducible bit
	// reflection can't infer. Actions absent from the map get no tool.
	MCPDoc map[Action]string

	// MCPArgs supplies the tool's parameter list for resources that have
	// NO RowType (forwarders, custom shapes). When RowType is set the
	// args are reflected from it and MCPArgs is ignored.
	MCPArgs map[Action][]MCPArg

	// MCPNames overrides the derived MCP tool name per action. Default is
	// "<Name>.<action>" (the dotted convention). A migration that must keep
	// an agent's existing flat tool name (add_route, set_web_route) sets it
	// here so unifying REST+MCP onto one handler doesn't rename the live
	// agent's tools. nil / absent action → dotted default.
	MCPNames map[Action]string

	// Gate authorizes one Execution after Authz derives (scope, params). nil →
	// defaultGate: the OPERATOR gate, auth.Authorize over the ACL rows
	// (scope/ACL match, no tier). A daemon mounting the resource on the AGENT
	// socket injects the agent's tier-aware gate (db.Authorize with mcp:+tier)
	// — resreg owns the handler/tx/audit plumbing, never the auth policy.
	// Skipped for forwarders (Store == nil); the downstream daemon gates.
	Gate func(x Execution, scope string, params map[string]string) error

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

// deriveMCPTools produces the MCP tools of r from its Endpoints. One
// tool per endpoint whose Action has an MCPDoc entry. Args come from
// RowType reflection when set, else from MCPArgs. Name is
// `<r.Name>.<action>`. This is the single renderer for a resource's
// MCP surface — no hand-authored tool lists.
func deriveMCPTools(r Resource) []MCPTool {
	var out []MCPTool
	for _, e := range r.Endpoints {
		desc, ok := r.MCPDoc[e.Action]
		if !ok {
			continue
		}
		// An explicit per-action override always wins (forwarders, custom shapes).
		// Otherwise derive per-action from the RowType so PK/read-only fields don't
		// leak into delete/update as required args.
		args, ok := r.MCPArgs[e.Action]
		if !ok && r.RowType != nil {
			args = rowMCPArgsFor(r, e.Action)
		}
		name := r.Name + "." + string(e.Action)
		if custom := r.MCPNames[e.Action]; custom != "" {
			name = custom
		}
		out = append(out, MCPTool{
			Name:        name,
			Action:      e.Action,
			Description: desc,
			Args:        args,
		})
	}
	return out
}

// rowMCPArgsFor reflects a RowType into the MCP arg list for ONE action:
//
//   - delete/get: PK fields only, all required (the key identifies the row).
//   - update:     PK fields required + writable non-PK fields optional.
//   - create:     writable non-PK fields (required per omitempty) + PK fields
//     that aren't server-stamped (a client-supplied key like `path`).
//   - list:       no args (the RowType models no filters).
//
// PK-ness is decided by Go field name membership in r.PKFields (the json arg
// name and the db column can differ). Server-stamped fields (r.StampedFields)
// are never a client arg on create or update.
func rowMCPArgsFor(r Resource, action Action) []MCPArg {
	rt := r.RowType
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return nil
	}
	pk := nameSet(r.PKFields)
	stamped := nameSet(r.StampedFields)
	var args []MCPArg
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		name, omit := parseJSONTag(sf)
		if name == "" {
			continue
		}
		isPK := pk[sf.Name]
		isStamped := stamped[sf.Name]
		switch action {
		case ActionList:
			continue
		case ActionGet, ActionDelete:
			if !isPK {
				continue
			}
			omit = false // the key is always required
		case ActionUpdate:
			if isPK {
				omit = false // the key selects the row
			} else if isStamped {
				continue // server-stamped — not a client patch arg
			} else {
				omit = true // updatable fields are optional patches
			}
		case ActionCreate:
			if isStamped {
				continue // server assigns it — never a create arg
			}
			if isPK {
				omit = false // client-supplied key required
			}
		}
		args = append(args, MCPArg{
			Name:     name,
			Type:     kindToMCPType(sf.Type),
			Required: !omit,
		})
	}
	return args
}

// nameSet turns a list of Go field names into a membership set.
func nameSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// kindToMCPType maps a Go type to an MCPArg type string
// ("string"|"number"|"bool"|"array").
func kindToMCPType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	default:
		return "string"
	}
}

// MCPTools registers every MCP tool of r on srv. callerFor is invoked
// per call (not at registration) to avoid privilege confusion. visible,
// when non-nil, gates which tools are ADDED to the server at all: it
// returns false for a tool the caller's tier may not even see in
// tools/list. The agent socket supplies a MatchingRules predicate so a
// tier that couldn't see a tool before still can't — visibility policy
// stays in the mounting daemon, never in resreg. nil → always visible
// (operator sockets, forwarders).
func MCPTools(srv *mcpserver.MCPServer, r Resource, callerFor CallerFromMCPFunc, visible func(name string) bool) {
	for _, t := range deriveMCPTools(r) {
		if visible != nil && !visible(t.Name) {
			continue
		}
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
		gate := r.Gate
		if gate == nil {
			gate = r.defaultGate
		}
		if gerr := gate(x, scope, params); gerr != nil {
			emitAudit(ctx, nil, x, scope, params, audit.OutcomeDenied, gerr.Error(), start, false)
			return nil, errStatus(gerr), gerr
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

// defaultGate is the operator authorization used when Resource.Gate is nil:
// auth.Authorize over the resource's ACL rows (scope/ACL match, no tier). The
// agent socket overrides Gate with a tier-aware closure; resreg itself owns no
// auth policy beyond this default.
func (r Resource) defaultGate(x Execution, scope string, params map[string]string) error {
	if auth.Authorize(r.Store, authCaller(x.Caller), actionKey(r.Name, x.Action), scope, params) {
		return nil
	}
	return Errorf(http.StatusForbidden, "forbidden")
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
		if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
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
