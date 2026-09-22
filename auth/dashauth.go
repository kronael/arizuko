package auth

// dashauth.go is the ONE operator gate every per-daemon dashboard applies
// (spec 6/28 §Auth). It was three copies before this file existed — timed's
// requireOperator, onbod's requireOperator and the `**` shortcut inside dashd's
// callerScope — and they had already drifted: timed rendered a themed 403 page,
// onbod rendered the bare string "forbidden". A dashboard gate that answers
// differently per daemon is the same class of defect as a second containment
// predicate (BUGS L101), so there is one decision here and one refusal page.
//
// This gate is the SECOND of the two layers 6/28 names. The first is proxyd's
// transit proof (auth.ProxydTransit), which is what makes the X-User-* headers
// trustworthy at all; a daemon MUST verify that before calling anything here.
// This file decides only "is the verified caller an operator", never "is the
// caller who they say they are".
//
// dashd's own requireAdmin is deliberately NOT folded in: it answers a
// different question (does this caller hold `admin` on THIS folder scope), for
// the cross-cutting pages 6/29 keeps central. Collapsing the two would widen or
// narrow one of them silently.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"

	"github.com/kronael/arizuko/theme"
)

// OperatorGroup is the scope that makes a caller an operator. Operator is not a
// role: it is emergent from holding everything (spec 5/32-acl-unified).
const OperatorGroup = "**"

// CallerGroups parses the proxyd-stamped X-User-Groups header — the JSON array
// of folder scopes the caller holds an allow-grant on, as auth.UserScopes
// produced them at sign time. A missing or malformed header is no scopes, never
// all scopes.
func CallerGroups(r *http.Request) []string {
	hdr := r.Header.Get("X-User-Groups")
	if hdr == "" {
		return nil
	}
	var groups []string
	if err := json.Unmarshal([]byte(hdr), &groups); err != nil {
		return nil
	}
	return groups
}

// IsOperator reports whether the stamped scopes carry `**`.
func IsOperator(r *http.Request) bool {
	return slices.Contains(CallerGroups(r), OperatorGroup)
}

// RequireOperator gates a dashboard page to an operator and writes the refusal
// itself, returning false when the caller may not proceed.
//
// daemon names the dashboard for the refusal page's title. A nil ks is the
// local-dev path (AUTHD_URL unset, and tests): there is no authd to verify a
// transit proof against, so the gate falls open exactly as every /v1 gate in
// the tree already does — it must not be the one surface that hard-fails
// without authd.
func RequireOperator(w http.ResponseWriter, r *http.Request, ks *KeySet, daemon string) bool {
	if ks == nil {
		return true
	}
	// X-User-* is trustworthy ONLY behind proxyd's service bearer: proxyd
	// strips the client's copy and stamps its own. Reading the header without
	// verifying that proof lets anyone who can reach the port send
	// `X-User-Groups: ["**"]` and receive the operator page.
	if ProxydTransit(r, ks) && IsOperator(r) {
		return true
	}
	Forbidden(w, daemon, "Operator access required.")
	return false
}

// RequireWrite is RequireOperator plus the same-origin check every mutating
// dashboard request needs. A dashboard POST is a cookie-authenticated write, so
// without this a third-party page can drive an operator's browser into one.
// Ordered operator-first so a non-operator never learns whether the origin
// check would have passed.
func RequireWrite(w http.ResponseWriter, r *http.Request, ks *KeySet, daemon string) bool {
	if !RequireOperator(w, r, ks, daemon) {
		return false
	}
	if ks == nil {
		return true
	}
	if !sameOrigin(r) {
		Forbidden(w, daemon, "Cross-origin write refused.")
		return false
	}
	return true
}

// sameOrigin reports whether the request's Origin (or Referer, which older
// browsers send instead) matches the Host it was sent to. A request carrying
// NEITHER header is refused rather than admitted: a browser sends one on every
// cross-origin POST, so their joint absence is not the ordinary case.
func sameOrigin(r *http.Request) bool {
	host := r.Host
	if host == "" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == host
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && u.Host == host
	}
	return false
}

// Forbidden renders the themed refusal page. 6/28 §Auth requires the theme
// error banner rather than plain text, because the hub reads a deep-page 403 as
// `warn` (daemon up, caller lacks scope) and an operator who lands on one
// should still see where they are.
func Forbidden(w http.ResponseWriter, daemon, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprint(w, theme.Page(daemon, template.HTML(
		`<div class="banner-err">`+template.HTMLEscapeString(msg)+`</div>`)))
}
