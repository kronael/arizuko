package main

// invites_resource.go folds onbod's /v1/invites REST face onto the shared resreg
// handler (spec 5/16), retiring the three hand-rolled invite handlers
// (handleInvite{Create,List,Revoke}). resreg owns the plumbing (handler dispatch
// + one tx wrapping the mutation AND its audit_log row); onbod owns the auth
// POLICY via the injected REST Caller + Gate. onbod has no agent socket, so this
// is REST-only — there is no MCP twin to share the handler with.
//
// The Endpoints mount at the REAL served paths (/v1/invites, /v1/invites/{ref})
// from resources.InvitesEndpoints — the single source the /openapi.json doc also
// reads, so served routes and doc cannot drift.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// mountInvites wires the /v1/invites REST surface (create/list/revoke) onto the
// shared resreg handler with onbod's bearer-scope Caller + Gate injected.
func (a *admin) mountInvites(mux *http.ServeMux) {
	resreg.RegisterREST(mux, a.invitesResource(), a.gatesRESTCaller)
}

// invitesResource is the serving resreg.Resource for invites. Endpoints come
// from resreg/resources (the same list the OpenAPI doc reads). Store is a
// store.Store over onbod.db so resreg.invoke opens the mutation+audit tx there.
// All policy lives in the injected Gate.
func (a *admin) invitesResource() resreg.Resource {
	r := resreg.Mount(resources.InvitesName)
	r.Handler = a.invitesHandler
	r.Gate = a.invitesRESTGate
	r.Store = store.New(a.db)
	return r
}

// invitesRESTGate reproduces the retired handlers' bearer-scope checks VERBATIM:
// invites:write to create/revoke; invites:read OR invites:write to list. Invites
// are instance-global (not folder-scoped) — scope IS the whole gate, so there is
// no folder containment. nil KeySet → open (mirrors gatesRESTGate / the nil-ks
// path in gatesRESTCaller).
func (a *admin) invitesRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if a.ks == nil {
		return nil
	}
	held := x.Caller.Scopes()
	want := []string{"invites:read", "invites:write"}
	if x.Action.Mutates() {
		want = []string{"invites:write"}
	}
	if !hasAnyScope(held, want...) {
		return resreg.Errorf(http.StatusForbidden, "missing scope %s", strings.Join(want, " or "))
	}
	return nil
}

// invitesHandler runs create/list/revoke against onbod.db. The mutating verbs
// write on resreg's tx (x.Tx) via the store's *Tx methods — NOT its audited ones
// — so the mutation and its single audit_log row land in resreg.invoke's one tx
// (calling store.CreateInvite/RevokeInviteByRef here would open a second tx and
// double-audit). The invite is an objstore item and the audit row is a SQL
// insert, and both ride that same transaction. list is read-only (no tx).
func (a *admin) invitesHandler(ctx context.Context, x resreg.Execution) (any, error) {
	st := store.New(a.db)
	switch x.Action {
	case resreg.ActionList:
		invs, err := st.ListInvites(argString(x.Args, "issued_by"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		out := make([]inviteJSON, len(invs))
		for i, inv := range invs {
			out[i] = toInviteJSON(inv)
		}
		return map[string]any{"invites": out}, nil

	case resreg.ActionCreate:
		inv, rawToken, err := createInviteTx(ctx, st, x.Tx, x.Args)
		if err != nil {
			return nil, err
		}
		return toInviteCreatedJSON(inv, rawToken), nil

	case resreg.ActionDelete:
		ref := argString(x.Args, "ref")
		if ref == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "ref required")
		}
		// ref IS the coordinate (I1) — a direct delete, no resolve step.
		if err := st.RevokeInviteByRefTx(ctx, x.Tx, ref); err != nil {
			if errors.Is(err, store.ErrInviteRefUnknown) {
				return nil, resreg.Errorf(http.StatusNotFound, "no invite with ref %q", ref)
			}
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]bool{"ok": true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// createInviteTx mints an invite on resreg's tx. The mint's RULES are
// store.MintInvite's — target_glob required and folder-validated (trailing
// slash = subworld-create mode, which skips folder validation), max_uses
// floored at 1, the token server-generated and only its hash persisted (I1) —
// so this face and the CLI's cannot disagree about what a valid invite is.
//
// What stays here is the HTTP shape: the two arguments the wire carries but the
// mint does not take (issued_by_sub defaulting to "onbod", an RFC3339
// expires_at) and the status codes. A rejected mint is the caller's fault, so
// it is a 400 — the same answer the validation this delegates to used to give
// inline.
func createInviteTx(ctx context.Context, st *store.Store, tx *sql.Tx,
	args resreg.Args) (store.Invite, string, error) {
	issuedBySub := argString(args, "issued_by_sub")
	if issuedBySub == "" {
		issuedBySub = "onbod"
	}
	var expiresAt *time.Time
	if s := argString(args, "expires_at"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return store.Invite{}, "", resreg.Errorf(http.StatusBadRequest, "expires_at: %v", err)
		}
		expiresAt = &t
	}
	inv, token, err := store.MintInvite(argString(args, "target_glob"), issuedBySub,
		argInt(args, "max_uses"), expiresAt)
	if err != nil {
		return store.Invite{}, "", resreg.Errorf(http.StatusBadRequest, "%v", err)
	}
	if err := st.InsertInviteTx(ctx, tx, inv); err != nil {
		return store.Invite{}, "", resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return inv, token, nil
}
