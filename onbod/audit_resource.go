package main

// audit_resource.go publishes onbod's own audit_log at GET /v1/audit — the
// fourth and last owner (BUGS F35). routd, runed and authd already serve theirs
// through the shared resreg resource; onbod's rows needed sqlite3 on the box, so
// /dash/audit/ federated three of four and was silently incomplete.
//
// WHAT THE ROWS CARRY. A new read surface earns a column-by-column pass over
// every writer, and onbod has five:
//
//   - `daemon.start` (main.go) — params {addr}, the container listen address.
//   - `onboarding.refuse` / `.queue` / `.approve` (auditAdmission) — params
//     {jid, gate}. Both already render on /dash/onbod/ and the dashd onboarding
//     page; neither is credential material.
//   - `onboarding.approve` from the admission queue (admitFromQueue) — same two.
//   - `invite.consume` (store.ConsumeInvite) — params {target_glob, used_count},
//     resource `invites/<ref[:8]>`. The raw bearer is NEVER on the row: `ref` is
//     store.TokenRef, which admin.go's inviteJSON already publishes precisely
//     because it is not redeemable.
//   - `acl.add` (store.AddACLRow, the invite-accept grant) — params
//     {principal, action, scope}: grant metadata, no secret.
//
// So no writer here puts key material in `params_summary`, and the read adds no
// path that could: audit.Query selects from audit_log and nothing else.
//
// The resource is registered ONCE in resreg/resources/audit.go, so onbod's
// mounted face and the doc /openapi.json emits from the mux cannot disagree.
// No action mutates, so resreg opens no tx and writes no audit row for a
// successful read — load-bearing, not a saving: a read that audited itself into
// the table it reads would append rows on every operator page-load.

import (
	"context"
	"net/http"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// mountAudit wires GET /v1/audit onto onbod's mux.
func (a *admin) mountAudit(mux *http.ServeMux) {
	resreg.RegisterREST(mux, a.auditResource(), a.auditRESTCaller)
}

// auditResource is onbod's mounted decl. Store is non-nil only because a nil
// Store marks a forwarder and resreg.invoke skips authorization for those; list
// does not mutate, so no transaction is ever opened on it. Authz is a no-op —
// all policy is the injected Gate, as with every other onbod resource.
func (a *admin) auditResource() resreg.Resource {
	return resreg.Resource{
		Name:      resources.AuditName,
		Endpoints: resources.AuditEndpoints,
		MCPDoc:    resources.AuditMCPDoc,
		MCPArgs:   resources.AuditMCPArgs,
		MCPNames:  resources.AuditMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Gate:    a.auditRESTGate,
		Handler: a.auditHandler,
		Store:   store.New(a.db),
	}
}

// auditRESTCaller builds the resreg Caller from the bearer, verified against
// authd's JWKS — the same mechanism gatesRESTCaller uses, not a second one. The
// scopes ride in Claims because only the Gate may answer 403; a Caller builder
// can fail 401 and nothing else (resreg.restHandler). The folder claim rides
// too so auditHandler can pin a folder-scoped caller to its own subtree. nil
// KeySet (AUTHD_URL unset / local-dev) is open, mirroring gatesRESTCaller.
func (a *admin) auditRESTCaller(r *http.Request) (resreg.Caller, error) {
	if a.ks == nil {
		return resreg.Caller{}, nil
	}
	sub, err := auth.VerifyHTTP(r, a.ks)
	if err != nil {
		return resreg.Caller{}, err
	}
	return resreg.Caller{
		Sub:    sub.Sub,
		Folder: sub.Extra["arz/folder"],
		Claims: resreg.ScopeClaims(sub.Scope),
	}, nil
}

// auditRESTGate is the injected authorization: hold audit:read, or 403.
//
// This is fail-closed against a human bearer by construction, which is why it
// can be the whole gate. A user token's scope list holds FOLDER GLOBS (routd's
// UserScopes) and auth.scopeMatches rejects any held value without a colon, so
// neither `acme/**` nor an operator's `**` can satisfy `audit:read`. The only
// holder is service:dashd (authd's service-scope ceiling), whose own audit page
// is operator-gated. nil KeySet → open, mirroring gatesRESTGate.
func (a *admin) auditRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if a.ks == nil {
		return nil
	}
	if !auth.HasScope(x.Caller.Scopes(), "audit", "read") {
		return resreg.Errorf(http.StatusForbidden, "missing scope audit:read")
	}
	return nil
}

// auditHandler serves the read off onbod.db through audit.Query — the same
// renderer routd, runed and authd serve theirs through.
//
// CONTAINMENT is resolved here rather than in the Gate because it bounds the
// ROWS, not the call: a token carrying a folder claim is pinned to that subtree
// and its `folder` argument cannot widen it. The converse — no claim means
// instance-wide — is safe only because the Gate already proved audit:read,
// which no human bearer can hold. An empty folder claim is NOT evidence of
// operator authority in general (routd claims a folder only when the sub holds
// exactly one scope, so a two-grant tenant is equally claimless), and keying a
// list-all on it is the recorded cross-tenant leak.
func (a *admin) auditHandler(ctx context.Context, x resreg.Execution) (any, error) {
	if x.Action != resreg.ActionList {
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
	f := audit.Filter{
		Folder:   x.Args.Str("folder"),
		Category: x.Args.Str("category"),
		Actor:    x.Args.Str("actor"),
		BeforeID: x.Args.Int("before_id"),
		Limit:    int(x.Args.Int("limit")),
	}
	if x.Caller.Folder != "" {
		f.Folder = x.Caller.Folder
	}
	rows, err := audit.Query(ctx, a.db, f)
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return rows, nil
}
