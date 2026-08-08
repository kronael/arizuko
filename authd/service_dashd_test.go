package main

import (
	"slices"
	"testing"
)

// Every daemon that presents AUTHD_SERVICE_KEY needs a serviceGrants entry;
// without one it is minted with EMPTY scope and its calls 403 silently. That has
// now shipped five times — the channel adapters (split A1), runed's broker
// ceiling, webd's form submissions, and dashd's kill button (BUGS F15a).
//
// This test pins the invariant for the whole map rather than for dashd alone, so
// the sixth occurrence fails here instead of in production: a principal that is
// declared must carry at least one scope.
func TestEveryServiceGrantIsNonEmpty(t *testing.T) {
	if len(serviceGrants) == 0 {
		t.Fatal("serviceGrants is empty — no principal could authenticate")
	}
	for principal, scopes := range serviceGrants {
		if len(scopes) == 0 {
			t.Errorf("%s has an empty grant — its calls will 403 with no error at the call site", principal)
		}
	}
}

// dashd proxies the operator UI's actions rather than writing other daemons'
// tables, and reads what it renders. Nine scopes, each pinned as NECESSARY:
//
//   - runs:kill — POST /v1/runs/stop (runed/server.go), the /dash/runed/ kill
//     button. The whapd pair endpoints are not scope-gated and proxyd authorizes
//     the FORWARDED operator identity, not dashd's scope, so neither adds one.
//   - routes:read — GET /v1/engagement (routd/reads_http.go scopeRoutesRead), the
//     /dash/engagement/ view (spec 5/G item 6, BUGS F31).
//   - routes:write — POST /v1/engagement (scopeRoutesWrite), the same page's
//     force-disengage control. It was pinned as TOO WIDE until that control
//     existed, on two stated conditions: the mutation must land an audit row
//     inside routd's own transaction, and the write path must contain on the
//     window's claiming folder rather than only on the jid's route target. Both
//     shipped (routd DB.SetEngagementAudited; handleEngagementSet's ownsFolder
//     check on the live window's owner), so the scope is earned rather than
//     merely convenient.
//   - audit:read — GET /v1/audit on routd, runed AND authd, the three sources
//     /dash/audit/ federates (spec 5/I, BUGS F29). Unlike the routes:* pair
//     this is NOT a subset of reach dashd already holds: dashd is FS-mounted on
//     routd.db but on neither runed.db nor auth.db, so it is genuinely new
//     authority — on the token authority, no less. What earns it is that the
//     column it reaches was audited rather than assumed. authd's params_summary
//     had exactly one writer (daemon.start's {dsn, serving_keys, service_subs});
//     the counts are len() values, the DSN is redacted at the writer
//     (audit.redactRE) and scrubbed from history (authd migration 0007), and
//     audit.Query names audit_log and no other table — signing_keys and
//     refresh_tokens are not reachable through it.
//   - signing_keys:read — GET /v1/signing_keys, the key table on /dash/authd/
//     (spec 5/1 DoD item 6). What it reaches is SigningKeysRow, and the
//     omission is the bound: priv_pem and pub_pem are in no SELECT list, no Row
//     struct and no db: tag, and selectSigningKeys is a CONSTANT statement with
//     no argument that could add a column. The scope reads the rotation, never
//     the key.
//   - sessions:read — GET /v1/sessions, the login table on the same page. There
//     is no credential to withhold: authd persists only a sha256 of a refresh
//     token (insertRefresh) and token_hash is unnamed in both the projection
//     and the query, so the strongest fact this scope yields is that a login
//     exists.
//   - sessions:write — DELETE /v1/sessions/{family_id}, that page's sign-out
//     control. This is the sharp one: a KILL VERB ON THE TOKEN AUTHORITY. Spec
//     5/1 withheld it on a stated ground — "granting the token authority's kill
//     verb to a daemon with no caller for it is authority without a user" — so
//     what earns it is that the caller now exists, not that the ground moved.
//     The two conditions routes:write's sign-off established both hold: the
//     audit row is written inside the mutation's OWN transaction (resreg.invoke
//     opens it, revokeSession revokes in it, a failed audit write rolls the
//     revoke back — TestSessionsRevokeKillsTheFamilyAndAudits), and the row is
//     READABLE, since 5/I federated authd's audit_log into /dash/audit/ —
//     which is exactly the objection BUGS F15a raised and left open.
//   - pending_actions:read — GET /v1/pending_actions on routd, the
//     /dash/approvals/ review queue (spec 5/19). What it reaches is
//     PendingActionsRow: the tool name and the arguments the AGENT chose to
//     send — precisely the material an operator must see to review a held
//     call. The table has no secret, token or key column to withhold.
//   - pending_actions:write — POST /v1/pending_actions/{id}/approve|reject,
//     that page's verdict controls. One guarded UPDATE on a `held` row: it
//     cannot create a hold, cannot delete the record of a decision, and routd
//     commits the verdict, the resolution message and the audit row in ONE
//     transaction (resolveHoldTx inside resreg's tx). The chat path's
//     IsOperator gate is matched here by dashd's requireOperator page gate,
//     the same pairing audit:read rides.
//
// The kill verb's blast radius is bounded by what the endpoint can do, not by
// intent: it sets revoked_at and nothing else. It cannot mint, cannot rotate a
// key, and cannot delete a row — the tombstone IS the reuse-detection evidence.
// A leaked dashd key therefore buys the ability to END sessions, never to make
// or extend one. The fleet-wide lever (retiring the active key) has no wire
// face at all, deliberately, so no scope reaches it.
//
// All four colon-scopes are also unreachable by any human bearer, so none can
// widen a USER session even if one were somehow minted with it: a user token's
// scope list holds folder globs, and auth.scopeMatches rejects a held value
// with no colon. signing_keys and sessions additionally REFUSE a folder-claimed
// caller (instanceWideGate), neither table having a folder column to narrow by.
//
// The upper bound is the half that matters, so it is asserted TWICE and neither
// half was weakened to let the three new ones in.
//
//   - By NAME, for the message: each listed scope names a distinct way the
//     ceiling could stop being "proxies and reads" — originating work
//     (runs:run), speaking as a channel (messages:write), reading credentials
//     (grants:read, secrets:read), or holding everything (*). secrets:read is
//     the one READ that would turn this into a leak; routes:read cannot, its
//     widest read (ListRouteTokens) never selects the token value.
//   - By COUNT, which is the stronger bound and closes the gap that letting
//     routes:write off the named list would otherwise open: the grant is exactly
//     these nine, so a TENTH scope of ANY name — including one nobody thought
//     to blacklist — fails here before it ships. This bound has now caught seven
//     additions (routes:write, audit:read, the three /dash/authd/ scopes and the
//     two pending_actions scopes) and forced each to state its case.
//
// tokens:mint is blacklisted by name from here on. It is the one scope that
// would turn "can end a session" into "can BE anyone" — IssuerMint mints a user
// token bounded by the TARGET's grants, not the caller's — and no dashd page
// has ever needed it. It is on the named list precisely because the count bound
// alone would have let it in as a swap for one of the seven.
func TestServiceDashdIsScopedToWhatItProxiesAndReads(t *testing.T) {
	g := serviceGrants["service:dashd"]
	if len(g) == 0 {
		t.Fatal("service:dashd has no grant — /dash/runed/'s kill button 403s")
	}
	want := []string{
		"runs:kill", "routes:read", "routes:write", "audit:read",
		"signing_keys:read", "sessions:read", "sessions:write",
		"pending_actions:read", "pending_actions:write",
	}
	for _, needed := range want {
		if !slices.Contains(g, needed) {
			t.Errorf("service:dashd grant missing %q, got %v", needed, g)
		}
	}
	if len(g) != len(want) {
		t.Errorf("service:dashd holds %d scopes %v, want exactly %v — the operator UI "+
			"proxies and reads; every scope beyond these needs the sign-off routes:write got",
			len(g), g, want)
	}
	for _, tooWide := range []string{
		"*", "runs:run", "messages:write", "grants:read", "secrets:read", "tokens:mint",
	} {
		if slices.Contains(g, tooWide) {
			t.Errorf("service:dashd holds %q — the operator UI proxies and reads, "+
				"it does not originate work", tooWide)
		}
	}
}
