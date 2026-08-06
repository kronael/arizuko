package main

// audit_resource.go publishes authd's own audit_log at GET /v1/audit.
//
// authd writes exactly two kinds of row into auth.db: `daemon.start` (main.go)
// and `login` (oauth.go) — the latter being the ONLY place that still knows
// which provider identity was presented before everything downstream collapses
// to the account's canonical sub. Until now neither could be read without
// sqlite3 on the host (BUGS F29).
//
// WHY THIS IS SAFE ON THE TOKEN AUTHORITY. authd is the sole ES256 signer, so
// a new read surface on it earns a column-by-column audit, not a shrug:
//
//   - `params_summary` had ONE writer, daemon.start's
//     {dsn, serving_keys, service_subs}. The two counts are len() values. The
//     DSN was a host path — not key material, but not something a published
//     endpoint should carry either — so audit.redactRE now matches `dsn` at the
//     WRITER (one renderer, every daemon) and migration 0007 scrubs the rows
//     written before that. The login row sets no params at all.
//   - No handler here touches a signing key, a refresh token, or a service
//     secret. Those live in signing_keys / refresh_tokens, which this resource
//     cannot name: audit.Query selects from audit_log and nothing else.
//   - This is NOT authd's first /v1 surface, whatever BUGS F29 says. http.go
//     already mounts five, one of which — GET /v1/identities/{sub} — is a
//     bearer-plus-scope-gated READ of auth.db. This reuses that exact gate
//     rather than inventing a second one.

import (
	"context"
	"net/http"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// auditResource is authd's mounted decl of the shared catalog resource
// (resreg/resources/audit.go). Store is non-nil only because a nil Store marks
// a forwarder and resreg.invoke skips authorization for those; list does not
// mutate, so no transaction is ever opened on it.
func (s *server) auditResource() resreg.Resource {
	return resreg.Resource{
		Name:      "audit",
		Endpoints: resources.AuditEndpoints,
		MCPDoc:    resources.AuditMCPDoc,
		MCPArgs:   resources.AuditMCPArgs,
		MCPNames:  resources.AuditMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Gate:    s.auditGate,
		Handler: s.auditHandler,
		Store:   store.New(s.a.db),
	}
}

// mountAudit wires GET /v1/audit onto authd's mux.
func (s *server) mountAudit(m *http.ServeMux) {
	resreg.RegisterREST(m, s.auditResource(), s.auditCaller)
}

// auditCaller verifies the bearer against authd's own in-process key set —
// the same LocalKeySet handleTokens and handleIdentity verify against, no JWKS
// fetch, because authd holds the private keys. The scope rides in Claims so
// the Gate can answer 403; a builder can only fail 401.
func (s *server) auditCaller(r *http.Request) (resreg.Caller, error) {
	caller, err := auth.VerifyHTTP(r, s.a.LocalKeySet())
	if err != nil {
		return resreg.Caller{}, err
	}
	return resreg.Caller{
		Sub:    caller.Sub,
		Folder: caller.Extra["arz/folder"],
		Claims: resreg.ScopeClaims(caller.Scope),
	}, nil
}

// auditGate is the operator-only check, expressed the way authd already
// expresses one: a literal resource:verb scope, matching handleIdentity's
// identity:read.
//
// `audit:read` is unreachable by any human bearer, and that is the mechanism
// rather than a convention. A user token's scope list holds FOLDER GLOBS
// (routd's UserScopes → oauth.issueSession), and auth.scopeMatches rejects
// every held value without a colon — so `acme/**` fails, and so does an
// operator's own `**`. The single holder is service:dashd, whose /dash/audit/
// page is requireOperator-gated. Operator-only is therefore enforced twice,
// and neither check is this handler's own invention.
//
// Note there is no local-dev open branch here, unlike runed's gate: authd
// always has a key set (it holds the signing keys), so there is no
// verifier-absent state to fall through.
func (s *server) auditGate(x resreg.Execution, _ string, _ map[string]string) error {
	if !auth.HasScope(x.Caller.Scopes(), "audit", "read") {
		return resreg.Errorf(http.StatusForbidden, "missing scope audit:read")
	}
	return nil
}

// auditHandler serves the read off auth.db through audit.Query — the same
// renderer routd and runed serve theirs through.
//
// A token carrying a folder claim is pinned to that subtree and its `folder`
// argument cannot widen it. The converse — no claim means instance-wide — is
// safe here ONLY because the Gate already proved audit:read. An absent folder
// claim is not evidence of operator authority on its own: routd stamps a
// folder only when the sub holds exactly one scope, so a two-grant tenant is
// equally claimless, and keying a list-all on that is the recorded
// cross-tenant leak. The claim narrows an authorized call; it never grants one.
func (s *server) auditHandler(ctx context.Context, x resreg.Execution) (any, error) {
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
	rows, err := audit.Query(ctx, s.a.db, f)
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return rows, nil
}
