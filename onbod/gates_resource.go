package main

// gates_resource.go is the /v1/gates REST face folded onto the shared resreg
// handler (spec 5/16). It retires onbod's three hand-rolled gate handlers
// (handleGate{List,Put,Delete}); resreg owns the plumbing (handler dispatch +
// one tx wrapping the mutation AND its audit_log row), onbod owns the auth
// POLICY via the injected REST Caller + Gate. onbod has NO agent socket, so this
// is REST-only — there is no MCP twin to share the handler with; "shared" here
// means the resreg engine path instead of raw mux handlers.
//
// The Endpoints mount at the REAL served path /v1/gates/{gate} (from
// resources.OnboardingGatesEndpoints, the single source the /openapi.json doc
// also reads), NOT the PK-CRUD convention /v1/onboarding_gates.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// gateJSON is the wire shape for one gate row. enabled is a bool (the routd
// httpOnbod client parses GET /v1/gates as {"gates":[{...,enabled:bool}]}).
type gateJSON struct {
	Gate        string `json:"gate"`
	LimitPerDay int    `json:"limit_per_day"`
	Enabled     bool   `json:"enabled"`
}

// mountGates wires the /v1/gates REST surface (list/update/delete) onto the
// shared resreg handler with onbod's bearer-scope Caller + Gate injected.
func (a *admin) mountGates(mux *http.ServeMux) {
	resreg.RegisterREST(mux, a.gatesResource(), a.gatesRESTCaller)
}

// gatesResource is the serving resreg.Resource for onboarding_gates. Endpoints
// come from resreg/resources (the same list the OpenAPI doc reads, so served
// routes and doc cannot drift). Store is a store.Store over onbod.db so
// resreg.invoke opens the mutation+audit tx there. Authz is a no-op; all policy
// lives in the injected Gate.
func (a *admin) gatesResource() resreg.Resource {
	return resreg.Resource{
		Name:      resources.OnboardingGatesName,
		Endpoints: resources.OnboardingGatesEndpoints,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: a.gatesHandler,
		Gate:    a.gatesRESTGate,
		Store:   store.New(a.db),
	}
}

// gatesRESTCaller builds the resreg Caller for /v1/gates. It verifies the bearer
// against authd's JWKS and carries the token's scopes in Claims for
// gatesRESTGate. nil KeySet (AUTHD_URL unset / monolith / local-dev) is open,
// mirroring admin.authed's nil-ks path: empty principal, no scopes.
func (a *admin) gatesRESTCaller(r *http.Request) (resreg.Caller, error) {
	if a.ks == nil {
		return resreg.Caller{}, nil
	}
	sub, err := auth.VerifyHTTP(r, a.ks)
	if err != nil {
		return resreg.Caller{}, err
	}
	return resreg.Caller{
		Sub:    sub.Sub,
		Claims: map[string]string{"scopes": strings.Join(sub.Scope, " ")},
	}, nil
}

// gatesRESTGate is the operator/human REST twin gate for onboarding_gates: an
// any-of bearer-scope check on the caller's held scopes. Gates are instance-
// global (github:org=acme, google:domain=…), not folder-scoped, so there is no
// folder containment — scope IS the whole gate. It reproduces the retired
// handlers' auth verbatim: gates:read OR gates:write to list, gates:write to
// mutate. nil KeySet → open (mirrors gatesRESTCaller).
func (a *admin) gatesRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if a.ks == nil {
		return nil
	}
	held := strings.Fields(x.Caller.Claims["scopes"])
	want := []string{"gates:read", "gates:write"}
	if x.Action.Mutates() {
		want = []string{"gates:write"}
	}
	if !hasAnyScope(held, want...) {
		return resreg.Errorf(http.StatusForbidden, "missing scope %s", strings.Join(want, " or "))
	}
	return nil
}

// gatesHandler runs list/update(upsert)/delete against onbod.db. The mutating
// verbs write on resreg's tx (x.Tx) via the *Tx helpers below — NOT the store's
// audited methods — so the mutation and its single audit_log row land in
// resreg.invoke's one tx (calling store.PutGate/EnableGate/DeleteGate here would
// open a second tx and double-audit).
func (a *admin) gatesHandler(ctx context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		gates, err := store.New(a.db).ListGates()
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		out := make([]gateJSON, len(gates))
		for i, g := range gates {
			out[i] = gateJSON{Gate: g.Gate, LimitPerDay: g.LimitPerDay, Enabled: g.Enabled}
		}
		return map[string]any{"gates": out}, nil

	case resreg.ActionUpdate:
		gate := argString(x.Args, "gate")
		if gate == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "gate required")
		}
		// Tri-state upsert (preserved from the retired handleGatePut): a body with
		// limit_per_day>0 upserts the limit; enabled present flips the flag; enabled
		// ABSENT leaves enablement untouched; both does both. resreg decodes the JSON
		// body into Args, so an absent "enabled" key is simply not in the map.
		if limit := argInt(x.Args, "limit_per_day"); limit > 0 {
			if err := putGateTx(ctx, x.Tx, gate, limit); err != nil {
				return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
			}
		}
		if enabled, ok := x.Args["enabled"].(bool); ok {
			if err := enableGateTx(ctx, x.Tx, gate, enabled); err != nil {
				return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
			}
		}
		slog.Info("gate set", "gate", gate)
		return map[string]bool{"ok": true}, nil

	case resreg.ActionDelete:
		gate := argString(x.Args, "gate")
		if gate == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "gate required")
		}
		if err := deleteGateTx(ctx, x.Tx, gate); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("gate deleted", "gate", gate)
		return map[string]bool{"ok": true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// putGateTx upserts a gate's daily limit on tx (mirrors store.PutGate's SQL;
// ON CONFLICT keeps enabled, updating only the limit).
func putGateTx(ctx context.Context, tx *sql.Tx, gate string, limitPerDay int) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO onboarding_gates (gate, limit_per_day, enabled)
		 VALUES (?, ?, 1)
		 ON CONFLICT(gate) DO UPDATE SET limit_per_day = excluded.limit_per_day`,
		gate, limitPerDay)
	return err
}

// enableGateTx flips a gate's enabled flag on tx (mirrors store.EnableGate).
func enableGateTx(ctx context.Context, tx *sql.Tx, gate string, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE onboarding_gates SET enabled = ? WHERE gate = ?`, en, gate)
	return err
}

// deleteGateTx removes a gate on tx (mirrors store.DeleteGate).
func deleteGateTx(ctx context.Context, tx *sql.Tx, gate string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM onboarding_gates WHERE gate = ?`, gate)
	return err
}

// argString reads a string arg (path values + string body fields land as string).
func argString(args resreg.Args, key string) string {
	s, _ := args[key].(string)
	return s
}

// argInt reads an int arg (JSON numbers decode to float64 through resreg's Args).
func argInt(args resreg.Args, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// hasAnyScope reports whether held satisfies any of the "resource:verb" wants
// (the retired handleGate* any-of scope check).
func hasAnyScope(held []string, want ...string) bool {
	for _, w := range want {
		res, verb, ok := strings.Cut(w, ":")
		if ok && auth.HasScope(held, res, verb) {
			return true
		}
	}
	return false
}
