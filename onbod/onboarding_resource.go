package main

// onboarding_resource.go folds onbod's /v1/onboarding REST face onto the shared
// resreg handler (spec 5/16, 5/17), retiring the five hand-rolled handlers
// (handleOnboarding{Insert,List,Approve,Deny,Reprompt}). resreg owns the
// plumbing (handler dispatch + one tx wrapping the mutation AND its audit_log
// row); onbod owns the auth POLICY via the injected REST Caller + Gate — the
// same split invites_resource.go and gates_resource.go already use.
//
// The Endpoints mount at the REAL served paths from resources.OnboardingEndpoints
// — the single source the /openapi.json doc also reads, so served routes and doc
// cannot drift. No credential appears in any projection here: OnboardingRow
// omitted token_ref outright (Z3), and onbod 0006 dropped the column with its
// expiry twin once the 5/31 fold left nothing writing them (BUGS F40).

import (
	"context"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// mountOnboarding wires the /v1/onboarding REST surface onto the shared resreg
// handler with onbod's bearer-scope Caller + Gate injected.
func (a *admin) mountOnboarding(mux *http.ServeMux) {
	resreg.RegisterREST(mux, a.onboardingResource(), a.gatesRESTCaller)
}

// onboardingResource is the serving resreg.Resource for onboarding. Store is a
// store.Store over onbod.db so resreg.invoke opens the mutation+audit tx there.
// Authz is a no-op; all policy lives in the injected Gate.
func (a *admin) onboardingResource() resreg.Resource {
	return resreg.Resource{
		Name:      resources.OnboardingName,
		Endpoints: resources.OnboardingEndpoints,
		MCPDoc:    resources.OnboardingMCPDoc,
		MCPArgs:   resources.OnboardingMCPArgs,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: a.onboardingHandler,
		Gate:    a.onboardingRESTGate,
		Store:   store.New(a.db),
	}
}

// onboardingRESTGate reproduces the retired handlers' bearer-scope check
// VERBATIM: invites:write for every verb, read included (the queue page is
// operator-only, and the row set is instance-global — there is no folder to
// contain against). nil KeySet → open, mirroring admin.authed.
func (a *admin) onboardingRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if a.ks == nil {
		return nil
	}
	if !hasAnyScope(strings.Fields(x.Caller.Claims["scopes"]), "invites:write") {
		return resreg.Errorf(http.StatusForbidden, "missing scope invites:write")
	}
	return nil
}

// onboardingHandler runs the admission verbs against onbod.db. Unlike
// invites/gates these call the store's audited writers rather than raw tx SQL:
// ApproveOnboarding/DenyOnboarding already emit their own audit_log rows with
// the right action names, and duplicating that SQL here to save a tx would be
// the second path the repo forbids. InsertOnboarding and RepromptOnboarding are
// unaudited in the store today; that asymmetry is pre-existing, not introduced
// here.
func (a *admin) onboardingHandler(_ context.Context, x resreg.Execution) (any, error) {
	s := store.New(a.db)
	switch x.Action {
	case resreg.ActionList:
		rows, err := s.ListOnboarding(argString(x.Args, "status"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"onboarding": rows}, nil

	case resreg.ActionCreate:
		jid := argString(x.Args, "jid")
		if jid == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "jid required")
		}
		if err := s.InsertOnboarding(jid); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]bool{"ok": true}, nil

	case resreg.Action("approve"):
		jid := argString(x.Args, "jid")
		if jid == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "jid required")
		}
		// ApproveOnboarding returns an error for an unknown jid — surface it as
		// 404 rather than letting a typo look like a successful admission.
		if err := s.ApproveOnboarding(jid); err != nil {
			return nil, resreg.Errorf(http.StatusNotFound, "%v", err)
		}
		return map[string]bool{"ok": true}, nil

	case resreg.Action("reprompt"):
		jid := argString(x.Args, "jid")
		if jid == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "jid required")
		}
		if err := s.RepromptOnboarding(jid); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]bool{"ok": true}, nil

	case resreg.ActionDelete:
		jid := argString(x.Args, "jid")
		if jid == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "jid required")
		}
		if err := s.DenyOnboarding(jid); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]bool{"ok": true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}
