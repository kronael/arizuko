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

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
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
// Authz is a no-op; all policy lives in the injected Gate.
func (a *admin) invitesResource() resreg.Resource {
	return resreg.Resource{
		Name:      "invites",
		Endpoints: resources.InvitesEndpoints,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: a.invitesHandler,
		Gate:    a.invitesRESTGate,
		Store:   store.New(a.db),
	}
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
	held := strings.Fields(x.Caller.Claims["scopes"])
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
// write on resreg's tx (x.Tx) via createInviteTx / the DELETE below — NOT the
// store's audited methods — so the mutation and its single audit_log row land in
// resreg.invoke's one tx (calling store.CreateInvite/RevokeInvite here would open
// a second tx and double-audit). list is read-only (no tx).
func (a *admin) invitesHandler(ctx context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		invs, err := store.New(a.db).ListInvites(argString(x.Args, "issued_by"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		out := make([]inviteJSON, len(invs))
		for i, inv := range invs {
			out[i] = toInviteJSON(inv)
		}
		return map[string]any{"invites": out}, nil

	case resreg.ActionCreate:
		inv, err := createInviteTx(ctx, x.Tx, x.Args)
		if err != nil {
			return nil, err
		}
		return toInviteCreatedJSON(*inv), nil

	case resreg.ActionDelete:
		ref := argString(x.Args, "ref")
		if ref == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "ref required")
		}
		// The caller only ever held the ref, so resolve it back to the PK on
		// resreg's tx — the DELETE and its audit row stay in one transaction.
		token, err := store.ResolveInviteRef(ctx, x.Tx, ref)
		if err != nil {
			if errors.Is(err, store.ErrInviteRefUnknown) {
				return nil, resreg.Errorf(http.StatusNotFound, "no invite with ref %q", ref)
			}
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if _, err := x.Tx.ExecContext(ctx, `DELETE FROM invites WHERE token = ?`, token); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]bool{"ok": true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// createInviteTx mints an invite on tx, mirroring store.CreateInvite's validation
// + INSERT (used_count starts 0). target_glob is required and folder-validated
// (trailing slash = subworld-create mode, which skips folder validation);
// issued_by_sub defaults to "onbod"; max_uses floors at 1; expires_at is optional
// RFC3339. The token is server-generated (core.GenHexToken), same as the store.
func createInviteTx(ctx context.Context, tx *sql.Tx, args resreg.Args) (*store.Invite, error) {
	targetGlob := argString(args, "target_glob")
	if targetGlob == "" {
		return nil, resreg.Errorf(http.StatusBadRequest, "target_glob required")
	}
	check := strings.TrimSuffix(targetGlob, "/")
	if check == "" {
		check = "/"
	}
	if check != "/" && !groupfolder.IsValidFolder(check) {
		return nil, resreg.Errorf(http.StatusBadRequest, "invalid target_glob %q", targetGlob)
	}
	issuedBySub := argString(args, "issued_by_sub")
	if issuedBySub == "" {
		issuedBySub = "onbod"
	}
	maxUses := max(argInt(args, "max_uses"), 1)
	var expiresAt *time.Time
	if s := argString(args, "expires_at"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "expires_at: %v", err)
		}
		expiresAt = &t
	}
	token := core.GenHexToken()
	now := time.Now().UTC()
	var expStr sql.NullString
	if expiresAt != nil {
		expStr = sql.NullString{String: expiresAt.UTC().Format(time.RFC3339), Valid: true}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO invites (token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		token, targetGlob, issuedBySub, now.Format(time.RFC3339), expStr, maxUses); err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return &store.Invite{
		Token:       token,
		TargetGlob:  targetGlob,
		IssuedBySub: issuedBySub,
		IssuedAt:    now,
		ExpiresAt:   expiresAt,
		MaxUses:     maxUses,
	}, nil
}
