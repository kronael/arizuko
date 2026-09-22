package auth

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// Caller is the bearer of a request. Principal is the JWT subject as authd minted it:
// for a human that is the ACCOUNT's canonical provider sub, already resolved at mint
// (spec 5/32). There is no resolve step here — Authorize evaluates the principal it
// is handed, and `acl.principal` rows key on that same value.
type Caller struct {
	Principal string
	Claims    map[string]string
	// Extra principals to fold into the expansion set without a DB lookup,
	// e.g. the room JID for channel-bot inbound (spec 6/9 §Membership).
	Extra []string
	// Route is the ROUTE axis (spec 5/33 §"Two axes"): the folder the router
	// resolved this request TO. Empty on every single-axis caller — operator REST,
	// dashd, UserScopes, IsOperator — and the evaluator is then bit-for-bit what it
	// was. Non-empty folds `folder:<Route>` in as a principal of its own AND makes
	// Principal+Extra the IDENTITY axis, which Authorize confines; see there.
	Route string
}

// FolderPrincipal is the acl principal a folder holds its grants as. One spelling:
// store.SeedMemberRoleTx seeds `folder:<f>` into role:member, routd.turnPrincipal
// reads it back, and Caller.Route expands through it.
func FolderPrincipal(folder string) string { return "folder:" + folder }

// Authorize returns true iff caller is permitted to perform action on scope:
// row-based grants over the caller's expanded principal set, deny-wins. It is the
// SOLE runtime authorization evaluator (5/33): magnitude AND containment in one call
// — a delegated row scoped `acme/**` authorizes `mcp:<tool>` on any target under
// acme. Management callsites pass the ACTUAL target as scope; a no-match is a deny.
//
// TWO AXES (5/33 §"Two axes"). When Caller.Route is set the expanded set carries two
// kinds of principal, and they do NOT authorize alike:
//
//   - ROUTE — `folder:<Route>` and its role memberships. What reaching through this
//     destination confers. Unconfined: it is the authority the turn always had.
//   - IDENTITY — Principal + Extra and their memberships. What the attested sender
//     holds. Confined twice: the target must lie in Route's own subtree, and the
//     action must match without actionCovers' cross-namespace lattice.
//
// The confinement is the point. A union of grant rows can only WIDEN, so an
// unbounded fold would give a folder's agent the reach of everyone who talks to it
// and make a shared chat a place to borrow authority. With it the invariant holds:
// OUTSIDE Route's subtree the verdict is exactly what it was before this axis
// existed, whoever spoke.
//
// Deny is never confined, on either axis. A person's deny row refuses the turn they
// triggered wherever it reaches — the only NARROWING a union of grants can express.
func Authorize(
	s *store.Store,
	caller Caller,
	action, scope string,
	params map[string]string,
) bool {
	if s == nil || action == "" || (caller.Principal == "" && caller.Route == "") {
		return false
	}

	// 1. Expand principal set.
	expanded := expandPrincipals(s, caller)

	// 2+3. Exact-match rows plus wildcard rows (stored principal contains '*',
	// filtered against the expanded set with segment-wise glob).
	//
	// A read failure DENIES — but loudly. It used to be indistinguishable from
	// "this principal holds no grants", so a broken acl table degraded every caller
	// to no-access with nothing in the log to say why (BUGS A4).
	rows, err := policyRows(s, expanded)
	if err != nil {
		slog.Error("authorize: acl read failed; DENYING",
			"principal", caller.Principal, "route", caller.Route,
			"action", action, "scope", scope, "err", err)
		return false
	}
	route := routePrincipals(s, caller)

	// 4. Evaluate.
	allowed, denied := false, false
	for _, r := range rows {
		if !matchPattern(r.Scope, scope) {
			continue
		}
		if !predicateMatches(r.Predicate, caller.Claims) {
			continue
		}
		if !paramsMatch(r.Params, params) {
			continue
		}
		if r.Effect == "deny" {
			if actionCovers(r.Action, action) {
				denied = true
			}
			continue
		}
		if caller.Route == "" || anyPrincipalMatches(r.Principal, route) {
			// Single-axis caller, or a row the ROUTE itself reaches: unconfined.
			if actionCovers(r.Action, action) {
				allowed = true
			}
			continue
		}
		if withinSubtree(caller.Route, scope) && identityCovers(r.Action, action) {
			allowed = true
		}
	}
	if denied {
		return false
	}
	// A folder's magnitude is its role membership (role:member floor + delegated
	// grants) + operator grants. No fallback: a folder with no matching allow row is
	// denied, loud.
	return allowed
}

// routePrincipals is the ROUTE axis's own expansion: the destination folder's
// principal plus its transitive role memberships. A row reachable from this set is
// one the DESTINATION holds, so it authorizes unconfined. Empty when there is no
// second axis, which is what leaves every existing caller unchanged.
func routePrincipals(s *store.Store, caller Caller) []string {
	if caller.Route == "" {
		return nil
	}
	p := FolderPrincipal(caller.Route)
	return append([]string{p}, s.Ancestors(p)...)
}

// withinSubtree reports whether the CONCRETE target `scope` is `folder` itself or
// lies beneath it. Exact string work, not the SQL LIKE form: `%` and `_` are
// literals here and the compare is case-sensitive, neither of which LIKE gives —
// that difference widened six read paths (BUGS L122).
//
// A scope carrying a glob is never within any subtree, and cannot be: `**` names
// targets outside every folder, and routd's installedPackagesScope is exactly `**`,
// so arguing containment from a pattern would confer package management — the widest
// grant in the tree — on the identity axis.
func withinSubtree(folder, scope string) bool {
	if folder == "" || scope == "" || strings.ContainsAny(scope, "*?[") {
		return false
	}
	return scope == folder || strings.HasPrefix(scope, folder+"/")
}

// identityCovers is the action test for an allow row reached ONLY through the
// identity axis: the action matched EXACTLY, or its own namespace wildcard (`mcp:*`
// for an `mcp:<tool>` request).
//
// It refuses actionCovers' cross-namespace lattice for the reason namespacedEffect
// already refuses it (spec 5/19 Hazard 2): an operator's (`*`, `**`) row covers
// every action, so reading the lattice here would make every message an operator
// sends silently elevate the turn that answered it. Elevation by identity already
// HAS a mechanism — `/root`, gated on IsOperator and one-shot (routd.cmdRoot) — and
// it is explicit on purpose. One mechanism, not a second implicit one.
func identityCovers(rowAction, requested string) bool {
	if rowAction == requested {
		return true
	}
	ns, _, ok := strings.Cut(requested, ":")
	return ok && rowAction == ns+":*"
}

// EffectiveActions returns a predicate reporting whether caller holds an allow row
// for an mcp action at ANY scope — the tool-VISIBILITY view, orthogonal to the
// per-target authz Authorize does. A lattice grant makes every mcp tool visible; an
// explicit `mcp:<tool>` allow makes that one visible. Deny rows are scope-specific
// and do not hide a tool from the list; the per-call Authorize still enforces them.
//
// It reads Caller.Route on the same terms Authorize does, so a tool the SENDER holds
// is listed for the turn they triggered — without it the identity axis would be
// inert, since an unlisted tool is never registered and its gate never runs. The
// lattice half is refused on the identity axis for identityCovers' reason. Being
// scope-free, this can list a tool the sender holds only OUTSIDE the turn's subtree;
// the per-call Authorize then denies it, which is the same order of events as a deny
// row and not a new one.
func EffectiveActions(s *store.Store, caller Caller) func(action string) bool {
	if s == nil || (caller.Principal == "" && caller.Route == "") {
		return func(string) bool { return false }
	}
	expanded := expandPrincipals(s, caller)
	rows, err := policyRows(s, expanded)
	if err != nil {
		slog.Error("effective actions: acl read failed; advertising NO tools",
			"principal", caller.Principal, "route", caller.Route, "err", err)
		return func(string) bool { return false }
	}
	route := routePrincipals(s, caller)
	lattice := false
	held := map[string]bool{}
	for _, r := range rows {
		if r.Effect == "deny" {
			continue
		}
		if caller.Route != "" && !anyPrincipalMatches(r.Principal, route) {
			// Identity-only row: `mcp:*` is the deliberate all-tools delegation,
			// but `*`/`admin` reach across namespaces and confer nothing here.
			switch r.Action {
			case "*", "admin":
			case "mcp:*":
				lattice = true
			default:
				held[r.Action] = true
			}
			continue
		}
		switch r.Action {
		case "*", "mcp:*", "admin":
			lattice = true
		default:
			held[r.Action] = true
		}
	}
	return func(action string) bool {
		if lattice {
			return strings.HasPrefix(action, "mcp:")
		}
		return held[action]
	}
}

// UserScopes lists the distinct allow-scope patterns `sub` holds — the source of the
// X-User-Groups header proxyd stamps and of authd's login-time scope snapshot.
//
// It reads the SAME rows Authorize evaluates, rather than its own SQL, so it cannot
// drift from the gate about WHICH rows exist. It previously lived in store/ as a raw
// `principal IN (...)` query, which silently missed every wildcard-principal grant.
//
// It is a LISTING, and it can never be the decision. A []string carries no action and
// cannot represent a deny row, so anything gating access MUST call Authorize; a
// caller that treats this result as a verdict has built a second, weaker evaluator.
func UserScopes(s *store.Store, sub string) ([]string, error) {
	if s == nil || sub == "" {
		return nil, nil
	}
	expanded := expandPrincipals(s, Caller{Principal: sub})
	rows, err := policyRows(s, expanded)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.Effect == "deny" || r.Scope == "" || seen[r.Scope] {
			continue
		}
		seen[r.Scope] = true
		out = append(out, r.Scope)
	}
	slices.Sort(out)
	return out, nil
}

// policyRows is the single read every evaluator here goes through: exact-match
// rows for the expanded principal set, plus wildcard-principal rows that glob
// onto it. One reader means one failure mode — its callers cannot drift on what
// "could not read the policy" means, and none of them can accidentally evaluate
// half of it. Counting them here is what rots; the guarantee is that they share
// this one.
func policyRows(s *store.Store, expanded []string) ([]core.ACLRow, error) {
	rows, err := s.ACLRowsFor(expanded)
	if err != nil {
		return nil, err
	}
	wild, err := s.ACLWildcardRows()
	if err != nil {
		return nil, err
	}
	for _, r := range wild {
		if anyPrincipalMatches(r.Principal, expanded) {
			rows = append(rows, r)
		}
	}
	return rows, nil
}

// expandPrincipals: caller.Principal + caller.Extra + caller.Route's folder
// principal, plus the transitive ancestors of each via acl_membership.
// Deduplicated. It deliberately loses which axis a principal came from — the row
// read is one query over the union; routePrincipals re-derives the route half for
// the confinement, so the two never disagree about who is in the set.
func expandPrincipals(s *store.Store, caller Caller) []string {
	// Start a frontier; expand each new principal's ancestors. Ancestors
	// is itself transitive but is keyed off a single child — fold them.
	frontier := append([]string{caller.Principal}, caller.Extra...)
	if caller.Route != "" {
		frontier = append(frontier, FolderPrincipal(caller.Route))
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range frontier {
		add(p)
	}
	for _, p := range frontier {
		for _, anc := range s.Ancestors(p) {
			add(anc)
		}
	}
	return out
}

// anyPrincipalMatches: row.Principal contains a glob; does it match any
// element of expanded? Uses segment-wise globbing on ':' AND '/'.
func anyPrincipalMatches(pattern string, expanded []string) bool {
	for _, p := range expanded {
		if matchPrincipal(pattern, p) {
			return true
		}
	}
	return false
}

// matchPrincipal: segment-wise glob on both ':' and '/'. `**` crosses
// segments; `*` does not. Implemented by chunking on ':' first, then on
// '/' within each chunk.
func matchPrincipal(pattern, p string) bool {
	if pattern == p {
		return true
	}
	if pattern == "**" {
		return true
	}
	// Split on ':' once: namespace : rest.
	patNs, patRest := splitNs(pattern)
	pNs, pRest := splitNs(p)
	if !segGlob(patNs, pNs) {
		return false
	}
	// rest is '/' separated; reuse matchPattern.
	if patRest == "" && pRest == "" {
		return true
	}
	return matchPattern(patRest, pRest)
}

func splitNs(s string) (ns, rest string) {
	if before, after, ok := strings.Cut(s, ":"); ok {
		return before, after
	}
	return s, ""
}

// segGlob: glob across a single segment (no '/' or ':' crossing). `*`
// matches any chars, `**` matches like `*` here (no boundary to cross).
func segGlob(pat, s string) bool {
	if pat == "*" || pat == "**" {
		return true
	}
	if pat == s {
		return true
	}
	// Fall through to path.Match-like with no separators in segment.
	for {
		if pat == "" {
			return s == ""
		}
		if pat[0] == '*' {
			pat = strings.TrimLeft(pat, "*")
			for i := 0; i <= len(s); i++ {
				if segGlob(pat, s[i:]) {
					return true
				}
			}
			return false
		}
		if s == "" || pat[0] != s[0] {
			return false
		}
		pat = pat[1:]
		s = s[1:]
	}
}

// actionCovers: does a stored row action cover the requested action?
// Lattice: `*` ⊃ admin ⊃ interact; `*` ⊃ mcp:<tool>; admin ⊃ mcp:<tool>;
// `mcp:*` ⊃ mcp:<tool>.
func actionCovers(rowAction, requested string) bool {
	if rowAction == "*" {
		return true
	}
	if rowAction == requested {
		return true
	}
	switch rowAction {
	case "admin":
		return requested == "interact" || strings.HasPrefix(requested, "mcp:")
	case "mcp:*":
		return strings.HasPrefix(requested, "mcp:")
	}
	return false
}

// predicateMatches: empty predicate = no claim required. Non-empty has
// the form `key=glob`; the claim at `key` must glob-match `glob`. Only
// one conjunction supported (spec open question 1).
func predicateMatches(predicate string, claims map[string]string) bool {
	if predicate == "" {
		return true
	}
	for part := range strings.SplitSeq(predicate, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		before, after, hasEq := strings.Cut(part, "=")
		if !hasEq {
			// Bare key: claim must be present and non-empty.
			if claims[part] == "" {
				return false
			}
			continue
		}
		k := strings.TrimSpace(before)
		v := strings.TrimSpace(after)
		got, ok := claims[k]
		if !ok {
			return false
		}
		if !segGlob(v, got) {
			return false
		}
	}
	return true
}

// paramsMatch: empty params = no constraint. Non-empty has the form
// `key=glob[,key=glob]`; every key must be present in passed params and
// glob-match.
func paramsMatch(paramSpec string, params map[string]string) bool {
	if paramSpec == "" {
		return true
	}
	for part := range strings.SplitSeq(paramSpec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		before, after, hasEq := strings.Cut(part, "=")
		if !hasEq {
			if _, ok := params[part]; !ok {
				return false
			}
			continue
		}
		k := strings.TrimSpace(before)
		v := strings.TrimSpace(after)
		got, ok := params[k]
		if !ok {
			return false
		}
		if !segGlob(v, got) {
			return false
		}
	}
	return true
}

// ValidateParams reports whether a `params` spec a caller asked to STORE means
// at read time exactly what it says at write time. paramsMatch trims and splits
// leniently, which is right for evaluation and wrong for a writer: the stored
// string is also the revoke key, so `a=1` and `a = 1` evaluate identically and
// delete differently, and a duplicated key silently makes a row that can never
// match. An empty spec is valid — it is the unnarrowed row.
//
// The grammar lives here, next to the evaluator that defines it, so the writer
// cannot accept a spec paramsMatch reads as something else.
func ValidateParams(spec string) error {
	if spec == "" {
		return nil
	}
	seen := map[string]bool{}
	for part := range strings.SplitSeq(spec, ",") {
		// Testing the halves also tests the whole: an empty clause has an empty
		// name, and any space around `part` is a space around its name or value.
		k, v, hasEq := strings.Cut(part, "=")
		if k != strings.TrimSpace(k) || (hasEq && v != strings.TrimSpace(v)) {
			return fmt.Errorf("params %q: %q has surrounding spaces the evaluator strips", spec, part)
		}
		if k == "" {
			return fmt.Errorf("params %q: empty parameter name", spec)
		}
		if seen[k] {
			return fmt.Errorf("params %q: parameter %q named twice; the clauses are ANDed, so the row can never match", spec, k)
		}
		seen[k] = true
	}
	return nil
}

// ParamsSpec renders the matcher for a set of CONCRETE argument values — the
// exact-value rule spec 6/26's `always` verdict writes. It refuses any value it
// cannot state exactly rather than writing a rule that means something wider:
//
//   - `,` separates clauses, so a value carrying one would split into two rules;
//   - `*` is a glob in segGlob, so a value carrying one would match beyond itself;
//   - leading/trailing spaces are stripped at read time (see ValidateParams).
//
// A refusal is the honest outcome: a surface must not offer a standing rule it
// cannot state. The rendered spec is checked back through paramsMatch, so this
// builder cannot drift from the evaluator that has to read it.
func ParamsSpec(values map[string]string) (string, error) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := values[k]
		if strings.Contains(k, "=") {
			return "", fmt.Errorf("argument name %q carries an =, which separates a name from its value", k)
		}
		for what, s := range map[string]string{"name": k, "value": v} {
			if strings.ContainsAny(s, ",*") || s != strings.TrimSpace(s) {
				return "", fmt.Errorf("argument %q: its %s cannot be written as an exact rule "+
					"(a comma splits the rule, a star widens it, spaces are stripped)", k, what)
			}
		}
		parts = append(parts, k+"="+v)
	}
	spec := strings.Join(parts, ",")
	if err := ValidateParams(spec); err != nil {
		return "", err
	}
	if !paramsMatch(spec, values) {
		return "", fmt.Errorf("rendered rule %q does not match the arguments it was built from", spec)
	}
	return spec, nil
}

// HoldPrefix namespaces hold rules. A hold is NEVER an `effect` on a plain
// `mcp:<tool>` row: Authorize treats every non-deny effect as ALLOW, so such a
// row would silently GRANT the tool it meant to gate (spec 5/19 Hazard 1).
//
// store declares it, because the row is store's and the seed floor writes these
// actions. One declaration, two readers.
const HoldPrefix = store.HoldPrefix

// EffectSkipApproval is the deny effect on a namespaced rule row — on a `hold:`
// row it means "do not hold this shape", on a `shell:env:` row "do not grant
// this key". It is spelled `deny` because Authorize reads every OTHER value as
// ALLOW (spec 5/19 Hazard 1), so a third effect name would be the same trap
// wearing a friendlier word. The constant exists so the wire spelling and the
// operator-facing wording ("skip approval") cannot drift apart.
//
// The acl ROW is store's, so store declares the two spellings and this reads
// one: a value the writer accepts and the evaluator does not is a row that
// means one thing in the table and another in the gate (BUGS L272).
const EffectSkipApproval = store.EffectDeny

// EffectHoldBlock is a hold rule's allow spelling, and the ONLY thing a hold
// rule can say: the tool call WAITS inside the agent's turn until a human
// answers. A call worth gating is a call worth not proceeding without, so there
// is no spelling for "return now and tell the agent later" — one let an agent
// report an action it had not taken (spec 5/19, 2026-09-02).
const EffectHoldBlock = store.EffectAllow

// CheckHold answers "must this tool call wait for a human?" — a DIRECT scoped read of
// `hold:mcp:`-prefixed rows, deliberately NOT routed through Authorize, whose action
// lattice would hold EVERY tool for the operator (spec 5/19 Hazard 2). Matching is
// exact on the action with `hold:mcp:*` as the only wildcard; scope, predicate and
// params reuse Authorize's own matchers, so there is one predicate language.
//
// The answer is a yes or a no, because a hold has one meaning: the call waits.
//
// Deny-wins IS modelled, and a matching `effect='deny'` row means SKIP APPROVAL. It
// denies the `hold:` action, not the tool, and it is the only way an exception can be
// expressed: the obvious alternative (an allow row on plain `mcp:<tool>`) is Hazard 1
// — Authorize reads every non-deny effect as ALLOW, so it would grant the tool while
// the hold still fired. Every surface calls this "skip approval"; only the wire says
// `deny` (spec 6/26 § Decision 5).
//
// Any matching deny beats every hold rule regardless of specificity, so revocation is
// removing the skip-approval row.
func CheckHold(s *store.Store, caller Caller, tool, scope string, params map[string]string) bool {
	if s == nil || caller.Principal == "" || tool == "" {
		return false
	}
	rows, err := policyRows(s, expandPrincipals(s, caller))
	if err != nil {
		// This is the OPPOSITE direction to Authorize's deny. Authorize's false
		// REFUSES the call; this false only says "no special treatment", so the
		// call proceeds unapproved. It is survivable because every write tool
		// runs Authorize after this gate and Authorize denies on the same
		// unreadable acl — the reads that skip Authorize are never held anyway.
		// Whether an unreadable acl should HOLD instead is an open decision,
		// not an oversight (BUGS L92).
		slog.Error("check hold: acl read failed; holding NOTHING",
			"principal", caller.Principal, "tool", tool, "err", err)
		return false
	}
	return namespacedAllows(rows, HoldPrefix, tool, scope, caller.Claims, params)
}

// namespacedAllows answers one namespaced rule: does the caller hold an allow row
// for `prefix+name` (or the namespace's `prefix+"*"`) covering scope, with no deny
// row? Deny wins, and an absent row is no.
//
// The action is matched EXACTLY, never through actionCovers. That is the whole
// reason this body exists apart from Authorize: an operator's (`*`, `**`) row
// covers every action under the lattice, so a lattice read here would hold every
// tool the operator calls (spec 5/19 Hazard 2) and hand every folder secret to
// every operator turn. Both namespaces need the same refusal, so they share one
// body rather than two loops that drift.
func namespacedAllows(
	rows []core.ACLRow,
	prefix, name, scope string,
	claims, params map[string]string,
) bool {
	var allowed, denied bool
	want := prefix + name
	for _, r := range rows {
		if r.Action != want && r.Action != prefix+"*" {
			continue
		}
		if !matchPattern(r.Scope, scope) {
			continue
		}
		if !predicateMatches(r.Predicate, claims) {
			continue
		}
		if !paramsMatch(r.Params, params) {
			continue
		}
		if r.Effect == EffectSkipApproval {
			denied = true
		} else {
			allowed = true
		}
	}
	return allowed && !denied
}

const ShellEnvPrefix = store.ShellEnvPrefix

// ExtScopePrefix opens the namespace an external connector's tools authorize
// on. `routd.LoadExtProviders` carries each tool's `scope` from its TOML
// (`ext:cloudflare:dns:read`) and the agent socket authorizes THAT string, not
// the tool's `mcp:` name — so an action in this namespace has to be grantable
// or the only rows that reach an ext tool are a blanket `*` and an operator
// elevation, both of which confer everything (BUGS L230).
const ExtScopePrefix = "ext:"

// ShellEnvKeys returns the subset of keys the caller may read from the shell env
// in scope, sorted. Matched EXACTLY, like CheckHold and for the same reason: an
// operator's (`*`, `**`) row covers every action, so routing this through
// Authorize would put every folder secret into every operator turn's env — the
// grant would widen hardest exactly where it should not. `shell:env:*` is the
// deliberate all-keys row; a deny row wins, as everywhere else.
func ShellEnvKeys(s *store.Store, caller Caller, scope string, keys []string) []string {
	if s == nil || caller.Principal == "" || len(keys) == 0 {
		return nil
	}
	rows, err := policyRows(s, expandPrincipals(s, caller))
	if err != nil {
		// Same direction as Authorize's deny, said out loud: an unreadable acl
		// table grants no shell credential rather than falling back to the set.
		slog.Error("shell env: acl read failed; GRANTING NOTHING",
			"principal", caller.Principal, "scope", scope, "err", err)
		return nil
	}
	var out []string
	for _, k := range keys {
		if namespacedAllows(rows, ShellEnvPrefix, k, scope, caller.Claims, nil) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
