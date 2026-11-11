package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// TestGrantActionReachesEgress pins the gap migration 0023 opened. It removed the
// depth-derived tiers and documented that operators re-delegate `egress` and
// `web:publish` explicitly — but runGrant hardcoded Action:"admin", and the lattice
// (auth.actionCovers) makes admin cover only `interact` and `mcp:*`. So the recovery
// path the migration prescribes could not be typed. On all three live instances this
// showed up as agents spawning with no network and no web mount.
func TestGrantActionReachesEgress(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	// The default grant is unchanged, and deliberately does NOT reach egress.
	if err := runGrant(s, core.ACLRow{Principal: "folder:demo", Scope: "demo"}, &out); err != nil {
		t.Fatalf("admin grant: %v", err)
	}
	if auth.Authorize(s, auth.Caller{Principal: "folder:demo"}, "egress", "demo", nil) {
		t.Fatal("an admin grant must not confer egress — the lattice stops at mcp:*")
	}

	// An explicit action does.
	if err := runGrant(s, core.ACLRow{Principal: "folder:demo", Scope: "demo", Action: "egress"}, &out); err != nil {
		t.Fatalf("egress grant: %v", err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: "folder:demo"}, "egress", "demo", nil) {
		t.Fatal("explicit egress grant did not authorize egress")
	}
	if !strings.Contains(out.String(), "egress") {
		t.Errorf("output does not name the action granted: %q", out.String())
	}
}

// TestGrantRejectsUnknownAction: a typo must fail loud, not write an inert row that
// reads as granted in `arizuko group grants`.
func TestGrantRejectsUnknownAction(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	err := runGrant(s, core.ACLRow{Principal: "folder:demo", Scope: "demo", Action: "egres"}, &out)
	if err == nil {
		t.Fatal("typo'd action was accepted; an inert acl row now reads as a grant")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error does not name the problem: %v", err)
	}
	if rows, err := s.ListACL("folder:demo"); err != nil || len(rows) != 0 {
		t.Error("a rejected grant still wrote a row")
	}
}

// TestUngrantRemovesAStoredRowOutsideTheVocabulary: the action vocabulary
// guards what a grant may write, not what a revoke may remove. On sloth eleven
// `folder:<f> codex <f> allow` rows sat in the store, and `ungrant` refused
// them with "unknown action", so only code could delete them (BUGS L814).
// The row is seeded raw because no door writes that action today.
func TestUngrantRemovesAStoredRowOutsideTheVocabulary(t *testing.T) {
	s := newMem(t)
	row := core.ACLRow{Principal: "folder:coach", Scope: "coach", Action: "codex"}
	sort := strings.Join([]string{row.Principal, row.Action, row.Scope, "", "", store.EffectAllow}, store.KeySep)
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{
		Op: objstore.Merge, Entity: store.ACL,
		Key:  objstore.Key{Partition: store.OwnerRoutd, Sort: sort},
		Init: map[string]string{"grant_option": "0"},
	}); err != nil {
		t.Fatalf("seed the legacy row: %v", err)
	}
	if rows, err := s.ListACL(row.Principal); err != nil || len(rows) != 1 {
		t.Fatalf("seeded row not readable: %+v, %v", rows, err)
	}

	var out bytes.Buffer
	if err := runGrant(s, row, &out); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("a grant of an action outside the vocabulary must still be refused: %v", err)
	}
	if err := runUngrant(s, row, &out); err != nil {
		t.Fatalf("ungrant of a stored row refused: %v", err)
	}
	if rows, err := s.ListACL(row.Principal); err != nil || len(rows) != 0 {
		t.Fatalf("the stored row survived its ungrant: %+v, %v", rows, err)
	}
	if err := runUngrant(s, row, &out); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("an ungrant of an unknown action with no such row must still be refused: %v", err)
	}
}

func TestGrantToolActionNamespace(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	row := core.ACLRow{Principal: "folder:demo", Scope: "demo", Action: "send"}
	if err := runGrant(s, row, &out); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("bare send must be refused: %v", err)
	}
	if err := runUngrant(s, row, &out); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("bare send revocation must be refused: %v", err)
	}
	if rows, err := s.ListACL(row.Principal); err != nil || len(rows) != 0 {
		t.Fatalf("refused action persisted: %+v, %v", rows, err)
	}
	row.Action = "mcp:send"
	if err := runGrant(s, row, &out); err != nil {
		t.Fatal(err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: row.Principal}, row.Action, row.Scope, nil) {
		t.Error("mcp:send grant did not authorize send")
	}
	if err := runUngrant(s, row, &out); err != nil {
		t.Fatal(err)
	}
}

// TestGrantActionReachesShellEnv is the same gap one tier down. A shell
// credential is delivered by a `shell:env:<KEY>` acl row, so an action the CLI
// refuses to type is a capability the operator cannot hand out at all — the
// feature would ship with no door. The default `admin` grant must NOT reach it:
// widening the shell is always an explicit act.
func TestGrantActionReachesShellEnv(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runGrant(s, core.ACLRow{Principal: "folder:atlas", Scope: "atlas"}, &out); err != nil {
		t.Fatalf("admin grant: %v", err)
	}
	if got := auth.ShellEnvKeys(s, auth.Caller{Principal: "folder:atlas"}, "atlas",
		[]string{"GITHUB_TOKEN"}); len(got) != 0 {
		t.Fatalf("an admin grant must not confer a shell credential, got %v", got)
	}

	if err := runGrant(s, core.ACLRow{Principal: "folder:atlas", Scope: "atlas", Action: auth.ShellEnvPrefix + "GITHUB_TOKEN"}, &out); err != nil {
		t.Fatalf("shell:env grant: %v", err)
	}
	if got := auth.ShellEnvKeys(s, auth.Caller{Principal: "folder:atlas"}, "atlas",
		[]string{"GITHUB_TOKEN"}); len(got) != 1 {
		t.Fatalf("explicit shell:env grant did not confer the key, got %v", got)
	}
}

// TestGrantActionReachesAnExtTool is the same gap a third time. An external
// connector's tools authorize on the `scope` its TOML declares
// (`ext:cloudflare:dns:read`), which the agent socket passes to Authorize
// verbatim — not the tool's `mcp:` name. So an operator who cannot type that
// action cannot open ONE ext tool, and the only rows that reach it are a
// blanket `*` and an operator elevation, both of which confer everything.
// The default `admin` grant must not reach it: the lattice stops at `mcp:*`.
func TestGrantActionReachesAnExtTool(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	const scope = auth.ExtScopePrefix + "cloudflare:dns:read"

	if err := runGrant(s, core.ACLRow{Principal: "folder:atlas", Scope: "atlas"}, &out); err != nil {
		t.Fatalf("admin grant: %v", err)
	}
	if auth.Authorize(s, auth.Caller{Principal: "folder:atlas"}, scope, "atlas", nil) {
		t.Fatal("an admin grant must not confer an ext tool — the lattice stops at mcp:*")
	}

	if err := runGrant(s, core.ACLRow{Principal: "folder:atlas", Scope: "atlas", Action: scope}, &out); err != nil {
		t.Fatalf("ext scope grant: %v", err)
	}
	if !auth.Authorize(s, auth.Caller{Principal: "folder:atlas"}, scope, "atlas", nil) {
		t.Fatal("explicit ext grant did not authorize the tool the socket asks about")
	}
}

func TestOperatorShortcutRequiresOmittedAction(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	if err := runGrant(s, core.ACLRow{Principal: "operator", Scope: "**"}, &out); err != nil {
		t.Fatal(err)
	}
	ancestors, err := s.Ancestors("operator")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ancestors, store.RoleOperator) {
		t.Fatalf("operator edge missing: %v", ancestors)
	}
	if err := runGrant(s, core.ACLRow{Principal: "ordinary", Scope: "**", Action: "admin"}, &out); err != nil {
		t.Fatal(err)
	}
	ancestors, err = s.Ancestors("ordinary")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ancestors, store.RoleOperator) {
		t.Fatalf("explicit admin made operator: %v", ancestors)
	}
	rows, err := s.ListACL("ordinary")
	if err != nil || len(rows) != 1 || rows[0].Action != "admin" {
		t.Fatalf("admin row: %v, %v", rows, err)
	}
}
