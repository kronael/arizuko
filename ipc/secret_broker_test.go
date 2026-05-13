package ipc

import (
	"context"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestResolveSecret_UserWins(t *testing.T) {
	store := map[[3]string]string{
		{"user", "github:alice", "GITHUB_TOKEN"}:  "ghp_user",
		{"folder", "atlas/eng", "GITHUB_TOKEN"}:   "ghp_folder",
		{"folder", "root", "GITHUB_TOKEN"}:        "ghp_root",
	}
	db := StoreFns{LookupSecret: lookupFn(store)}

	v, scope := resolveSecret(db, "github:alice", "atlas/eng", "GITHUB_TOKEN")
	if v != "ghp_user" || scope != "user" {
		t.Errorf("got (%q,%q), want (ghp_user,user)", v, scope)
	}
}

func TestResolveSecret_FolderFallback_DeepestFirst(t *testing.T) {
	store := map[[3]string]string{
		{"folder", "atlas", "JIRA_TOKEN"}:     "jira_atlas",
		{"folder", "atlas/eng", "JIRA_TOKEN"}: "jira_eng",
		{"folder", "root", "JIRA_TOKEN"}:      "jira_root",
	}
	db := StoreFns{LookupSecret: lookupFn(store)}

	v, scope := resolveSecret(db, "", "atlas/eng", "JIRA_TOKEN")
	if v != "jira_eng" || scope != "folder" {
		t.Errorf("deepest: got (%q,%q), want (jira_eng,folder)", v, scope)
	}
	v, scope = resolveSecret(db, "", "atlas/eng/sre", "JIRA_TOKEN")
	if v != "jira_eng" || scope != "folder" {
		t.Errorf("walks-up: got (%q,%q), want (jira_eng,folder)", v, scope)
	}
}

func TestResolveSecret_RootCatchAll(t *testing.T) {
	store := map[[3]string]string{
		{"folder", "root", "BASE"}: "root_val",
	}
	db := StoreFns{LookupSecret: lookupFn(store)}

	v, scope := resolveSecret(db, "", "atlas/eng", "BASE")
	if v != "root_val" || scope != "folder" {
		t.Errorf("got (%q,%q), want (root_val,folder)", v, scope)
	}
}

func TestResolveSecret_Missing(t *testing.T) {
	db := StoreFns{LookupSecret: lookupFn(map[[3]string]string{})}
	v, scope := resolveSecret(db, "github:alice", "atlas", "NOPE")
	if v != "" || scope != "missing" {
		t.Errorf("got (%q,%q), want (\"\",missing)", v, scope)
	}
}

func TestResolveSecret_NoLookupFn(t *testing.T) {
	v, scope := resolveSecret(StoreFns{}, "x", "y", "K")
	if v != "" || scope != "missing" {
		t.Errorf("got (%q,%q), want (\"\",missing)", v, scope)
	}
}

func lookupFn(store map[[3]string]string) func(scope, scopeID, key string) (string, bool) {
	return func(scope, scopeID, key string) (string, bool) {
		v, ok := store[[3]string{scope, scopeID, key}]
		return v, ok
	}
}

// TestEchoSecret_BrokerEndToEnd exercises the broker chain end-to-end
// through the MCP unix socket: ARIZUKO_DEV=1 registers `echo_secret`,
// which declares requires=[ECHO_SECRET]. Seeding a folder row makes
// found=true; removing it makes found=false. Spec 9/11 M1.
func TestEchoSecret_BrokerEndToEnd(t *testing.T) {
	t.Setenv("ARIZUKO_DEV", "1")
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var mu sync.Mutex
	store := map[[3]string]string{}
	var auditRows []SecretUseRow
	db := StoreFns{
		LookupSecret: func(scope, scopeID, key string) (string, bool) {
			mu.Lock()
			defer mu.Unlock()
			v, ok := store[[3]string{scope, scopeID, key}]
			return v, ok
		},
		LogSecretUse: func(row SecretUseRow) error {
			mu.Lock()
			defer mu.Unlock()
			auditRows = append(auditRows, row)
			return nil
		},
	}

	stop, err := ServeMCP(sock, GatedFns{}, db, "atlas/eng", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	// Phase 1: no secret seeded → broker returns "", found=false.
	got, errText := callTool(t, sock, "echo_secret", map[string]any{})
	if errText != "" {
		t.Fatalf("echo_secret unexpected isError: %s", errText)
	}
	if got["found"] != false || got["len"].(float64) != 0 {
		t.Errorf("phase 1: got %v, want {found:false, len:0}", got)
	}

	// Phase 2: seed folder row → resolver finds it, len=N, no value leak.
	const seeded = "ECHO_VALUE_TOPSECRET"
	mu.Lock()
	store[[3]string{"folder", "atlas/eng", "ECHO_SECRET"}] = seeded
	mu.Unlock()

	got, errText = callTool(t, sock, "echo_secret", map[string]any{})
	if errText != "" {
		t.Fatalf("echo_secret phase 2 isError: %s", errText)
	}
	if got["found"] != true || int(got["len"].(float64)) != len(seeded) {
		t.Errorf("phase 2: got %v, want {found:true, len:%d}", got, len(seeded))
	}

	// Audit: one row per call × one declared key = at least 2 rows total.
	mu.Lock()
	defer mu.Unlock()
	if len(auditRows) < 2 {
		t.Fatalf("audit rows = %d, want ≥ 2", len(auditRows))
	}
	last := auditRows[len(auditRows)-1]
	if last.Tool != "echo_secret" || last.Key != "ECHO_SECRET" {
		t.Errorf("audit tool/key: %+v", last)
	}
	if last.Scope != "folder" {
		t.Errorf("audit scope = %q, want folder", last.Scope)
	}

	// Audit rows record status only, never the value itself.
	for _, row := range auditRows {
		if row.Status != "ok" {
			t.Errorf("audit row status = %q, want ok", row.Status)
		}
	}
}


// End-to-end: the adapter resolves secrets, calls the handler with the
// resolved map, and emits one audit row per required key with correct scope.
func TestInjectSecretsAdapter_HandlerReceivesMapAndAuditEmitted(t *testing.T) {
	store := map[[3]string]string{
		{"folder", "atlas", "JIRA_TOKEN"}: "jira_atlas",
	}
	var audit []SecretUseRow
	db := StoreFns{
		LookupSecret: lookupFn(store),
		LogSecretUse: func(r SecretUseRow) error { audit = append(audit, r); return nil },
	}

	var got map[string]string
	h := injectSecretsAdapter(db, "atlas/eng", "create_jira_issue",
		[]string{"GITHUB_TOKEN", "JIRA_TOKEN"},
		func(_ context.Context, _ mcp.CallToolRequest, s map[string]string) (*mcp.CallToolResult, error) {
			got = s
			return mcp.NewToolResultText("ok"), nil
		})

	if _, err := h(context.Background(), mcp.CallToolRequest{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got["GITHUB_TOKEN"] != "" {
		t.Errorf("missing key should be empty, got %q", got["GITHUB_TOKEN"])
	}
	if got["JIRA_TOKEN"] != "jira_atlas" {
		t.Errorf("folder walk: got %q, want jira_atlas", got["JIRA_TOKEN"])
	}

	if len(audit) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(audit))
	}
	want := map[string]string{"GITHUB_TOKEN": "missing", "JIRA_TOKEN": "folder"}
	for _, row := range audit {
		if want[row.Key] != row.Scope {
			t.Errorf("audit %s: scope=%q want %q", row.Key, row.Scope, want[row.Key])
		}
		if row.Folder != "atlas/eng" || row.Tool != "create_jira_issue" || row.Status != "ok" {
			t.Errorf("audit row mismatch: %+v", row)
		}
	}
}
