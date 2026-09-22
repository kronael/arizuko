package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/resreg"
)

// AuditEndpoints is the single owner of the audit-log read surface on every
// daemon that owns an audit_log. READ-ONLY BY DESIGN: a write face would let
// a caller forge or erase the record of an act. ONE registration serves FOUR
// daemons without violating "two daemons must NEVER register the same
// resource name": that rule forbids two DIFFERENT tables sharing a wire name,
// and here one table shape is deliberately replicated per owner (specs/5/I),
// so `/v1/audit` means "THIS daemon's audit log". There is no GET
// /v1/audit/{id}: an id is unique inside one daemon's log only.
var AuditEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/audit", Action: resreg.ActionList, Status: http.StatusOK},
}

// AuditResponses: audit.Query answers []audit.Row, the registered RowType, on
// all four daemons that mount this resource.
var AuditResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]audit.Row](),
}

// AuditMCPNames maps the action to the flat agent tool name (specs/5/I).
var AuditMCPNames = map[resreg.Action]string{
	resreg.ActionList: "query_audit",
}

// AuditMCPArgs is the explicit arg list: these are FILTERS over the table,
// not columns of a row, so reflection would offer the agent all eighteen
// columns as create-style args.
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
		{Name: "before_id", Type: "string",
			Description: "Pagination cursor: the id of the oldest row already seen, so the call " +
				"returns only rows older than it. Omit for the newest page."},
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
		Name:      AuditName,
		Table:     "audit_log",
		RowType:   reflect.TypeFor[audit.Row](),
		PKFields:  []string{"ID"},
		Endpoints: AuditEndpoints,
		Responses: AuditResponses,
		MCPDoc:    AuditMCPDoc,
		MCPArgs:   AuditMCPArgs,
		MCPNames:  AuditMCPNames,
		// DB is deliberately EMPTY, so Export/Plan/Apply never touch it: the
		// table lives in four owner DBs at once, and an audit log is evidence,
		// not declarative config.
		DB: "",
	})
}
