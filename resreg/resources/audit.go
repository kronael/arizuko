package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/resreg"
)

// AuditEndpoints is the single owner of the audit-log read surface, driving
// BOTH faces on every daemon that owns an audit_log: the mounted handler
// (routd/audit_resource.go, runed/audit_resource.go, authd/audit_resource.go)
// and each of their /openapi.json docs. READ-ONLY BY DESIGN, and more
// strictly so than installed_packages: audit_log is append-only by contract.
// The row is written in the same transaction as the mutation it records
// (resreg.invoke), so a create/update/delete face would let a caller forge or
// erase the record of an act that did or did not happen — the one table where
// a write API is not a missing feature but a defeat of the feature.
//
// ONE registration serves THREE daemons, and that is not a violation of root
// CLAUDE.md's "two daemons must NEVER register the same resource name". That
// rule forbids two DIFFERENT tables sharing a wire name (routd's `routes` vs
// proxyd's `proxyd_routes`). Here there is one table shape, `audit_log`,
// deliberately replicated per owner DB — specs/5/I "audit_log is per-daemon;
// correlation across them is the turn_id, not a shared table". `/v1/audit`
// therefore means "THIS daemon's audit log" on whichever daemon answers, which
// is exactly what makes dashd's federation a fan-out of one path rather than
// three bespoke clients.
//
// There is no GET /v1/audit/{id}. `id` is a per-DB AUTOINCREMENT, so
// /v1/audit/5 names a different row on each of the three daemons — a path key
// that is not an identity would be a lie in the federated page, and an
// append-only log's unit of use is a filtered window anyway.
var AuditEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/audit", Action: resreg.ActionList, Status: http.StatusOK},
}

// AuditMCPNames maps the action to the flat agent tool name, matching the flat
// convention every other agent-facing resource uses (list_packages, add_route)
// rather than the dotted `audit.list` default. `query_audit` is the name
// specs/5/I open question 2 proposed for it.
var AuditMCPNames = map[resreg.Action]string{
	resreg.ActionList: "query_audit",
}

// AuditMCPArgs is the explicit arg list. Required because a resource has TWO
// declarations — this catalog entry (RowType, drives /openapi.json) and each
// daemon's mounted decl (no RowType) — and only the first can reflect. It
// cannot be derived from RowType regardless: these are FILTERS over the table,
// not columns of a row being written, and reflection would offer the agent all
// eighteen columns as create-style args.
var AuditMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionList: {
		{Name: "folder", Type: "string",
			Description: "Restrict to this group folder and everything under it (e.g. atlas/support). " +
				"Omit only if you hold instance-wide read; otherwise omitting it is denied, " +
				"not silently narrowed."},
		{Name: "category", Type: "string",
			Description: "Exact category: authn, authz, access, mutation, system, network, channel, agent, secret, scheduler."},
		{Name: "actor", Type: "string",
			Description: "Substring match on the actor principal (e.g. google:114alice, service:dashd)."},
		{Name: "before_id", Type: "number",
			Description: "Pagination cursor: return only rows older than this row id. Omit for the newest page."},
		{Name: "limit", Type: "number",
			Description: "Rows to return, newest first. Default 50, maximum 200."},
	},
}

// AuditMCPDoc is the single owner of the tool's agent-facing one-liner;
// openapi.go folds the same string in as x-mcp-when.
var AuditMCPDoc = map[resreg.Action]string{
	resreg.ActionList: "Read this daemon's audit log — one row per state-changing call plus every " +
		"denial and error, newest first: who acted, on what resource, from which " +
		"surface, with what outcome. Answers 'what did I do last turn' and 'who " +
		"killed that run' without a shell on the box. Scoped: a folder grant sees " +
		"its own subtree, and omitting `folder` asks for the whole instance, which " +
		"needs an instance-wide grant. Read-only — the log is append-only and each " +
		"row is written inside the transaction of the act it records, so there is " +
		"nothing to create, edit or delete. Spec 5/I.",
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "audit",
		Table:     "audit_log",
		RowType:   reflect.TypeFor[audit.Row](),
		PKFields:  []string{"ID"},
		Endpoints: AuditEndpoints,
		MCPDoc:    AuditMCPDoc,
		MCPArgs:   AuditMCPArgs,
		MCPNames:  AuditMCPNames,
		// DB is deliberately EMPTY, so BySubsystem never returns this resource
		// and Export/Plan/Apply never touch it. Two independent reasons, either
		// sufficient: the table lives in four owner DBs at once, so no single
		// subsystem key is true; and an audit log is evidence, not declarative
		// config — round-tripping it through a manifest would let `arizuko apply`
		// rewrite the record of what was applied.
		DB: "",
	})
}
