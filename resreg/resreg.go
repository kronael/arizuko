// Package resreg implements the uniform Resource registry from spec 5/17: one
// Handler per (Resource, Action), wrapped by two auto-adapters (REST + MCP) so
// any caller surface reaches the same code.
//
// Four rules hold it together:
//
//   - Per-invocation caller resolution. MCPTools takes a callerFor(ctx, req)
//     resolver, not a captured Caller, so a shared MCP server cannot collapse
//     every call to one principal at registration time.
//   - Canonical ACL gate. Resource.Authz returns (scope, params) and the adapter
//     calls auth.Authorize per specs/5/32. No parallel scope machinery.
//   - Batched audit. A state-changing action HANDS ITS WRITES OVER rather than
//     performing them: the handler buffers them on Execution.Writes and the
//     adapter sends them in one objstore batch with the audit_log row, so the
//     row never outlives the mutation and neither needs a transaction.
//   - Forwarder pattern. Resources with Store nil skip the batch/audit dance —
//     the downstream daemon writes the row.
package resreg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/objstore"
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

// Owner-subsystem keys for Resource.DB — spec 5/8's "one subsystem = one
// owner DB" (routd.db, onbod.db; the only two owner DBs with resreg
// resources per 5/16's owner-DB map).
//
// They ARE store's owner names rather than two look-alike literals, because an
// entity-backed resource's PARTITION is its subsystem (Resource.EntitySortKey)
// and the daemon's own storage layer writes items at that same coordinate. Two
// spellings would put the writer's items in one partition and the engine's read
// in another, and the export would come back empty with nothing to say why.
const (
	SubsystemRoutd = store.OwnerRoutd
	SubsystemOnbod = store.OwnerOnbod
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
	Folder string
	Claims map[string]string
}

// ScopeClaimKey is where a caller-builder parks the verified bearer's scope
// list so the injected Gate can read it. A Gate receives only the Execution,
// and a caller-builder can only fail 401 — so a daemon that authorizes on
// token SCOPE rather than on ACL rows (runed and authd own no acl table) has
// to carry the scope across that seam to answer 403. Encoded space-delimited,
// the OAuth 2.0 §3.3 form.
const ScopeClaimKey = "scope"

// ScopeClaims builds the Claims map carrying scope. Paired with Caller.Scopes
// so the encoding has one owner instead of one copy per daemon.
func ScopeClaims(scope []string) map[string]string {
	if len(scope) == 0 {
		return nil
	}
	return map[string]string{ScopeClaimKey: strings.Join(scope, " ")}
}

// Scopes decodes what ScopeClaims encoded. Empty when the builder set none —
// which a Gate must read as "no scope proven", never as "unrestricted".
func (c Caller) Scopes() []string { return strings.Fields(c.Claims[ScopeClaimKey]) }

// Args is the JSON-decoded argument map an adapter passes in. URL-path
// values (e.g. {path}) are merged into Args under their parameter name
// before the handler sees them, so the handler reads one source.
type Args map[string]any

// Str reads a string arg. Absent or wrong-typed → "".
//
// Deliberately NOT named String: a map type with a String() method becomes a
// fmt.Stringer, and every %v of an Args — in a slog line, in a wrapped error —
// would silently render one key instead of the map.
func (a Args) Str(key string) string {
	s, _ := a[key].(string)
	return s
}

// Int reads a numeric arg. The two surfaces deliver numbers differently: MCP
// decodes to float64, and REST query params arrive as strings straight off
// url.Values (decodeRESTArgs), so `?limit=50` is "50" and not 50. Both must
// land on the same number or a filter silently ignores itself on one face.
// Absent, unparseable or wrong-typed → 0.
func (a Args) Int(key string) int64 {
	switch v := a[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

// Bool reads a boolean arg, accepting both shapes the two surfaces deliver:
// MCP decodes a JSON `true` to a Go bool, while a REST body or query param
// arrives as the string "true" (decodeRESTArgs). Same reason Int exists —
// without it a flag silently reads false on one face and true on the other.
// Absent, unparseable or wrong-typed → false.
func (a Args) Bool(key string) bool {
	switch v := a[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	}
	return false
}

// Execution carries everything a handler needs to act + emit audit.
// The adapter constructs it once per request; handlers buffer their writes on
// Writes for mutating actions on store-backed resources and ignore it otherwise.
type Execution struct {
	Caller    Caller
	Action    Action
	Resource  string
	Args      Args
	TurnID    string // X-Turn-Id header (REST) or _meta.turn_id (MCP)
	RequestID string // X-Request-Id (REST) or _meta.request_id (MCP)
	SourceIP  string // REST only
	Surface   string // "rest" | "mcp"
	// Writes is the batch the mutation rides. Non-nil only when
	// Resource.Store != nil and the action mutates; invoke sends it with the
	// audit_log row after the handler returns.
	Writes *store.WriteSet
}

// Handler is the single per-resource entry point. The adapter dispatches on
// Execution.Action; a mutating handler DESCRIBES its writes onto
// Execution.Writes instead of performing them, so the adapter can send them in
// one batch with the row that records the call.
//
// It may READ freely before it decides — a read takes no locks — but it must
// never read a value its own buffered writes produce, because none of them has
// happened when it returns.
type Handler func(ctx context.Context, x Execution) (any, error)

// Endpoint declares one REST face of an action. Path is a stdlib-mux pattern;
// placeholders ({name}) bind to Args keys of the same name.
//
// MCPOnly marks an action with NO REST face: RegisterREST does not mount it and
// /openapi.json does not advertise it, but deriveMCPTools still produces its
// agent tool. This keeps ONE endpoint list per resource, so the tool browser and
// the agent socket read the same source the REST face does.
type Endpoint struct {
	Verb    string
	Path    string
	Action  Action
	Status  int
	MCPOnly bool
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

// Resource ties identity (Name + Authz + Handler) to registration metadata
// (Endpoints + MCP doc/args). One literal per resource per daemon. MCP tools are
// DERIVED from Endpoints, never hand-authored — one tool per endpoint whose
// Action has an MCPDoc entry, so the description map is also the "surface this
// action as a tool" signal.
//
// Authz returns (scope, params, err); the adapter then calls auth.Authorize as
// the canonical ACL gate, and a returned err short-circuits without touching
// auth. nil derives nothing: the injected Gate holds the whole policy.
// Store, when non-nil, makes the adapter open a tx for mutating actions and
// write the audit_log row inside it. Set Store nil for forwarder resources.
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

	// MCPArgs supplies the tool's parameter list per action. When an action
	// has an MCPArgs entry it wins; an action absent from the map falls back
	// to RowType reflection (nil args if there is no RowType). Forwarders and
	// custom shapes set MCPArgs for every mutating action.
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

	// Schema half (spec 5/8). All optional. Resources that set
	// RowType+Table are "engine-managed" and get generic CRUD via
	// engine.go. Resources without RowType are forwarders or custom-
	// shape — still valid, just not engine-driven.
	RowType  reflect.Type // canonical row struct (zero-value)
	Table    string       // where the rows live: the SQL table, or the entity of the same name
	PKFields []string     // Go field names making up the natural PK

	// Responses declares what an action's handler ANSWERS WITH — the Go type
	// of the value it returns, or a slice of it for a collection. It is the
	// only source /openapi.json has for a success body: an action with no
	// entry here documents "OK" and no schema at all.
	//
	// It is separate from RowType because a Handler returns `any` and nothing
	// constrains it to the row. The emitter used to pick the shape off the verb
	// — list → array<Row>, GET/POST → Row — and 22 mounted operations answered
	// with something else entirely: GET /v1/routes with []router.RouteView,
	// GET /v1/tasks with []core.Task (Go field names, no json tags), the mints
	// with {token,jid,url}, onbod's mutations with {"ok":true}. A generated
	// client mis-decoded every one of them (BUGS L120).
	//
	// The declared type is the ONE declaration and the handler is its second
	// reader: it constructs that same type, so a field renamed in the struct
	// moves the doc and the wire together. Only a handler returning a
	// DIFFERENT type can still drift, which is what
	// resregtest.AssertDocumentedResponse serves a real request to catch.
	Responses map[Action]reflect.Type

	// EntitySortKey declares this resource's SHAPE. Naming RowType fields here
	// makes the rows objstore ITEMS — Table is an entity, each `db:` tag is an
	// attribute name, and these fields' values joined by EntityKeySep are the
	// item's sort key. Empty is the column path. There is exactly one shape per
	// resource at any moment and this is the only place it is written down; the
	// engine reads it and never probes, and there is no
	// try-the-entity-then-try-the-table (spec 6/37 step 6, BUGS L259).
	//
	// It is a LIST because a coordinate can be composite and two resources have
	// no single-field identity: network_rules is keyed (folder, target) and
	// target alone is not unique in live data — 15 rows over 10 targets on krons
	// — and installed_packages is (folder, name). store/pane_sessions and
	// chanlib/chanstore already build a key this way (spec 6/40 §1).
	//
	// It is SEPARATE from PKFields because they answer different questions: this
	// is the item's coordinate, PKFields is the MANIFEST identity Diff matches
	// rows on and EmitRows sorts by. Separate, but not free — PKFields must name
	// every field of the sort key, or two items share one manifest identity and
	// Diff's index keeps whichever it read last. route_tokens named only `jid`
	// while krons held 43 tokens over 27 jids, so 16 live ingress credentials
	// answered to one identity and the plan under-reported them (BUGS L260).
	// initEntity refuses a registration that does not, so the collapse cannot be
	// re-declared.
	//
	// The PARTITION is not declared: it is DB, the owner subsystem. Every read
	// the engine makes — Export, Checksum, Plan, Diff, Apply — is per subsystem,
	// and the partition is the coordinate a read filters on (objstore.Key). So
	// an entity-backed resource must name its subsystem, and initMeta refuses one
	// that does not rather than issuing a query no driver accepts.
	EntitySortKey []string

	// EntityKeySep joins the fields of a composite EntitySortKey, and no value
	// of any of them may contain it — a write that carries it is REFUSED rather
	// than splitting the key somewhere the read would not put it back together.
	//
	// It is declared per resource because which byte no value carries is a fact
	// about the rows, not about the engine: "/" separates a Slack team, user and
	// thread (store/pane_sessions) and cannot separate a folder from anything,
	// because a folder path is made of them.
	//
	// A single-field key has nothing to join, so a separator declared beside one
	// is refused: it would be a fact no reader uses.
	EntityKeySep string

	Scope ScopeSpec // DeleteScope filter; zero = no per-scope op
	Hooks Hooks     // optional semantics callbacks

	// DB names the owner subsystem (SubsystemRoutd | SubsystemOnbod) whose
	// physical DB file holds Table. Required for any resource with a
	// RowType — Export/Apply/Plan are per-subsystem (spec 5/8 §"Cross-
	// subsystem apply": one subsystem = one owner DB), and there is no way
	// to derive which DB a table lives in from the table name alone.
	// Identity is configured, never derived (root CLAUDE.md).
	DB string

	// RowFilter narrows the manifest-visible projection: the rows an export
	// shows, a checksum covers, and an apply's DELETE reaches. acl_membership
	// leaves out the edges pairing consented to and route_tokens leaves out the
	// 10-minute pairing credentials, because both are runtime state a manifest
	// must never carry or replace.
	//
	// It is a DECLARED PREDICATE and not a SQL string, because the same
	// declaration has to narrow the READ and both of the deletes an apply issues,
	// and a filter the read honours but a delete does not silently widens that
	// delete past the projection it was computed from (spec 6/40 §"What a
	// registered resource still needs", answer 6). The terms are objstore's own,
	// so an item read passes them to Query.Where verbatim and a column read
	// renders them as bound SQL; there is one declaration and two readers, never
	// two spellings.
	//
	// The terms are a CONJUNCTION and the operators are Eq and Ne, which is what
	// both engines answer alike. A predicate needing an OR has no form here, and
	// the answer is an attribute the writer sets rather than a wider read
	// (routd migration 0047). Nil = no filter.
	RowFilter []objstore.Cmp

	// SkipApplyRebuild causes Apply to skip DELETE+INSERT for this resource.
	// Set true for resources whose tables hold operator data that the engine
	// can read (ScanAll for export) but must not rebuild from manifest —
	// e.g. `secrets`, whose enc_value blob is set imperatively. Resources
	// in this state still appear in Export output (metadata only).
	SkipApplyRebuild bool

	// StampedFields names the Go struct fields the STORAGE LAYER owns
	// (created_at, granted_at, added_at, …). Diff ignores them when comparing
	// payloads, so a hand-written manifest that omits them reads as `unchanged`
	// against a live stamped row instead of phantom-updating on every plan
	// (spec 5/8 §"Apply lifecycle" step 3). A row that leaves one empty gets one
	// invented.
	StampedFields []string

	// AbsentStamps is the subset of StampedFields the writer does NOT invent,
	// because ABSENCE IS A VALUE THE ROW STATES rather than a gap to fill.
	//
	// Server-OWNED and server-INVENTED are different claims, and `groups` needs
	// both on one resource: `added_at` is when the group was made and a manifest
	// omitting it must get one, while `updated_at` is when it was last changed
	// and a group nobody has changed legitimately has none. Inventing the second
	// makes the FIRST `export | apply` write a fresh timestamp — a checksum
	// shift the operator did not cause, and the guarantee spec 5/8's
	// cross-subsystem rollback rests on (BUGS F49).
	//
	// It is a DECLARATION because there is nothing else to read it from: an item
	// has no column to override, so the claim has to be stated rather than
	// inferred from a fact about SQL binding that happens to correlate.
	AbsentStamps []string

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
//
// Each face is mounted as a *restMount stamped with the (resource, endpoint) it
// serves, so MountedResources can read a daemon's REST surface off the routing
// table it actually built. OpenAPIHandler used to take a hand-passed name list
// with no reference to the mux, which is what let a daemon advertise paths it
// mounts nowhere and mount paths it never advertised (BUGS F33).
func RegisterREST(mux *http.ServeMux, r Resource, build CallerFromHTTPFunc) {
	for _, e := range r.Endpoints {
		if e.MCPOnly {
			continue
		}
		mux.Handle(e.Verb+" "+e.Path, &restMount{
			resource: r.Name,
			endpoint: e,
			h:        restHandler(r, build, e),
		})
	}
}

// restMount is the mount identity RegisterREST stamps on every REST face. A
// hand-rolled mux.HandleFunc is not one, which is what lets the doc tell resource
// faces from look-alike paths: THREE daemons served GET /v1/sessions over three
// different tables, so deriving the advertised set by probing paths would have
// made routd and runed publish authd's schema. F46 renamed the two squatters.
type restMount struct {
	resource string
	endpoint Endpoint
	h        http.Handler
}

func (m *restMount) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.h.ServeHTTP(w, r) }

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
//   - delete/get: PK fields only, all required.
//   - update:     PK fields required + writable non-PK fields optional.
//   - create:     writable non-PK fields + PK fields that aren't server-stamped.
//   - list:       no args.
//
// PK-ness is decided by Go field name membership in r.PKFields, since the json
// arg name and the db column can differ. Server-stamped fields are never a client
// arg on create or update.
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

func nameSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

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

// invoke is the per-call core: authz → gate → handler → one batch carrying the
// handler's writes and the audit_log row. Shared between REST and MCP adapters
// so the path is provably one-rendered.
func invoke(ctx context.Context, r Resource, x Execution) (any, int, error) {
	start := time.Now()
	forward := r.Store == nil // forwarder: downstream daemon logs
	var scope string
	var params map[string]string
	if r.Authz != nil {
		var err error
		scope, params, err = r.Authz(x.Caller, x.Action, x.Args)
		if err != nil {
			emitAudit(ctx, x, scope, params, outcomeFor(err), err.Error(), start, forward)
			return nil, errStatus(err), err
		}
	}
	if !forward {
		gate := r.Gate
		if gate == nil {
			gate = r.defaultGate
		}
		if gerr := gate(x, scope, params); gerr != nil {
			emitAudit(ctx, x, scope, params, audit.OutcomeDenied, gerr.Error(), start, false)
			return nil, errStatus(gerr), gerr
		}
	}
	if !x.Action.Mutates() || forward {
		res, herr := r.Handler(ctx, x)
		if herr != nil {
			emitAudit(ctx, x, scope, params, audit.OutcomeError, herr.Error(), start, forward)
			return nil, errStatus(herr), herr
		}
		emitAudit(ctx, x, scope, params, audit.OutcomeOK, "", start, forward)
		return res, 0, nil
	}
	x.Writes = &store.WriteSet{}
	res, herr := r.Handler(ctx, x)
	if herr != nil {
		// Nothing was written — the handler only described writes — so the row
		// that records the attempt goes in alone.
		emitAudit(ctx, x, scope, params, audit.OutcomeError, herr.Error(), start, false)
		return nil, errStatus(herr), herr
	}
	// One batch: every write the handler decided on plus the row that records the
	// call. A refused or failed batch wrote NOTHING, so the failure is recorded
	// the same way a handler's error is — alone, and after the fact.
	ev, rec := auditRow(x, scope, params, start)
	if aerr := r.Store.Apply(ctx, x.Writes, rec); aerr != nil {
		emitAudit(ctx, x, scope, params, audit.OutcomeError, aerr.Error(), start, false)
		return nil, errStatus(aerr), aerr
	}
	logCall(ev)
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

// auditRow renders the row that records a SUCCESSFUL mutation. It PRODUCES the
// write rather than performing it, so the row rides the same batch as the
// mutation it records — audit.WriteFor is the single renderer of the item, here
// as it is for store.
//
// The slog line is NOT written here. The batch has not been sent yet, and one
// that is refused must not leave an "ok" line standing in front of the error
// that follows it; the caller logs the event it gets back once the batch lands.
func auditRow(x Execution, scope string, params map[string]string,
	start time.Time) (audit.Event, objstore.Write) {
	e := buildEvent(x, scope, params, audit.OutcomeOK, "", start)
	_, w := audit.WriteFor(e)
	return e, w
}

// emitAudit records an event that rides NO mutation — a denial, a handler's
// error, a refused batch, a read, a forwarder's call — AND writes the slog line.
// Read-only OK outcomes skip the row (volume); denials and errors always land in
// the table so privilege-escalation forensics work. Forwarders skip the row
// entirely — the downstream daemon writes it.
func emitAudit(ctx context.Context, x Execution, scope string, params map[string]string, outcome, errMsg string, start time.Time, forwarder bool) {
	e := buildEvent(x, scope, params, outcome, errMsg, start)
	logCall(e)
	if forwarder {
		return
	}
	if !x.Action.Mutates() && outcome == audit.OutcomeOK {
		return
	}
	audit.Emit(ctx, e)
}

// logCall is the slog line every call gets, whichever sink its row takes.
func logCall(e audit.Event) {
	slog.Info("resreg",
		"caller", e.ActorSub, "resource", e.Resource, "action", e.Action,
		"surface", e.Surface, "outcome", e.Outcome, "duration_ms", e.DurationMS)
}

func buildEvent(x Execution, scope string, params map[string]string, outcome, errMsg string, start time.Time) audit.Event {
	ps := map[string]any{}
	maps.Copy(ps, x.Args)
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
	// Query params are the base layer (GET filters/scoping like ?folder=);
	// a JSON body and path placeholders below override on key collision.
	for k, vs := range req.URL.Query() {
		if len(vs) > 0 {
			args[k] = vs[0]
		}
	}
	if req.Body != nil && (req.Method == "POST" || req.Method == "PATCH" || req.Method == "PUT" || req.Method == "DELETE") {
		var raw map[string]any
		dec := json.NewDecoder(req.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		maps.Copy(args, raw)
	}
	for seg := range strings.SplitSeq(e.Path, "/") {
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
	case string:
		// A model that stringifies a numeric argument is the common tool-call
		// slip. REST reads the same value off url.Values as a string and Args.Int
		// parses it, so coercing here makes both faces land on one number
		// (BUGS L123). A string that is not a number still reports not-ok.
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}
