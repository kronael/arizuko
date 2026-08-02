package routd

// Parity tests for the spec 5/16 route_tokens fold: the agent's
// issue_chat_link/issue_webhook/list_tokens/revoke_token now ride resreg's MCP
// mechanism through the ServeMCP postBuild seam instead of hand-rolled ipc bodies.
// Each test drives the REAL unix socket end-to-end so the seam + injected Gate +
// Visible predicate + the Gate's target containment + owner-scoped revoke are all
// exercised. 5/33: NONE of the four tools is in the role:member floor — minting a
// public unauthenticated endpoint AND the self-service list/revoke pair are all
// explicit delegation (or /root). Every test that expects a tool to work delegates
// it with grantMCPTools (scope <folder>/**).

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// serveRouteTokensMCP stands up the agent socket for folder + the route_tokens
// resreg seam, and returns the socket path. webHost seeds the URL prefix the issue
// tools echo (empty = no URL). Authz reads the folder's acl rows.
func serveRouteTokensMCP(t *testing.T, db *DB, folder, callerSub, webHost string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, webHost)
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.routeTokensPostBuild(folder, callerSub, srv.db.Authorize,
		agentVisibleFor(srv, callerSub, false))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, false, 0, callerSub, pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

// issueToken drives an issue tool and returns the parsed {token, jid, url}.
func issueToken(t *testing.T, sock, tool string, args map[string]any) (token, jid, url string) {
	t.Helper()
	text, e := callToolText(t, sock, tool, args)
	if e != "" {
		t.Fatalf("%s errored: %s", tool, e)
	}
	var out struct {
		Token string `json:"token"`
		JID   string `json:"jid"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s payload %q not JSON: %v", tool, text, err)
	}
	return out.Token, out.JID, out.URL
}

// TestRouteTokensMCP_IssueListRevoke: happy path for a delegated folder — issue a
// chat link + a webhook (both minted for the caller's own folder), the raw token
// resolves to the right JID/owner, list returns both, revoke removes the caller's
// own token and the raw token stops resolving.
func TestRouteTokensMCP_IssueListRevoke(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "issue_chat_link", "issue_webhook", "list_tokens", "revoke_token")
	sock := serveRouteTokensMCP(t, db, "hq", "folder:hq", "https://x.test")

	// issue_chat_link: default target = own folder → jid web:hq, url under /chat/.
	chatTok, chatJID, chatURL := issueToken(t, sock, "issue_chat_link", nil)
	if chatJID != "web:hq" {
		t.Fatalf("chat jid = %q, want web:hq", chatJID)
	}
	if chatURL != "https://x.test/chat/"+chatTok+"/" {
		t.Fatalf("chat url = %q, want https://x.test/chat/<tok>/", chatURL)
	}
	if j, owner, _, e := db.ResolveRouteToken(chatTok); e != nil || j != "web:hq" || owner != "hq" {
		t.Fatalf("resolve chat: jid=%q owner=%q err=%v", j, owner, e)
	}

	// issue_webhook: source_label github → jid hook:hq/github, url under /hook/.
	hookTok, hookJID, hookURL := issueToken(t, sock, "issue_webhook", map[string]any{"source_label": "github"})
	if hookJID != "hook:hq/github" {
		t.Fatalf("hook jid = %q, want hook:hq/github", hookJID)
	}
	if hookURL != "https://x.test/hook/"+hookTok {
		t.Fatalf("hook url = %q, want https://x.test/hook/<tok>", hookURL)
	}

	// list_tokens: both rows, owned by hq, raw token never returned.
	text, e := callToolText(t, sock, "list_tokens", nil)
	if e != "" {
		t.Fatalf("list_tokens errored: %s", e)
	}
	var listed struct {
		Tokens []struct {
			JID         string `json:"jid"`
			OwnerFolder string `json:"owner_folder"`
			Token       string `json:"token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(text), &listed); err != nil {
		t.Fatalf("list payload %q not JSON: %v", text, err)
	}
	if len(listed.Tokens) != 2 {
		t.Fatalf("list_tokens returned %d rows, want 2: %s", len(listed.Tokens), text)
	}
	for _, r := range listed.Tokens {
		if r.OwnerFolder != "hq" {
			t.Fatalf("listed token owner = %q, want hq", r.OwnerFolder)
		}
		if r.Token != "" {
			t.Fatalf("list_tokens leaked a raw token: %s", text)
		}
	}

	// revoke_token on own token → deleted, and it stops resolving.
	rtext, e := callToolText(t, sock, "revoke_token", map[string]any{"jid": "web:hq"})
	if e != "" {
		t.Fatalf("revoke_token errored: %s", e)
	}
	if rtext != `{"deleted":true}` {
		t.Fatalf("revoke result = %q, want {\"deleted\":true}", rtext)
	}
	if _, _, _, e := db.ResolveRouteToken(chatTok); e == nil {
		t.Fatal("chat token still resolves after revoke")
	}
}

// TestRouteTokensMCP_IssueWithContext: the optional context arg (spec 5/W § link
// context) lands on the row and surfaces via resolve + list_tokens; omitted →
// empty, exactly the pre-context behavior.
func TestRouteTokensMCP_IssueWithContext(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "issue_chat_link", "issue_webhook", "list_tokens")
	sock := serveRouteTokensMCP(t, db, "hq", "folder:hq", "")

	chatTok, _, _ := issueToken(t, sock, "issue_chat_link",
		map[string]any{"context": "bug reports from the acme site; triage, don't chat"})
	if _, _, linkCtx, e := db.ResolveRouteToken(chatTok); e != nil ||
		linkCtx != "bug reports from the acme site; triage, don't chat" {
		t.Fatalf("resolve context = (%q,%v)", linkCtx, e)
	}
	hookTok, _, _ := issueToken(t, sock, "issue_webhook", map[string]any{"source_label": "github"})
	if _, _, linkCtx, e := db.ResolveRouteToken(hookTok); e != nil || linkCtx != "" {
		t.Fatalf("context-less mint got context (%q,%v)", linkCtx, e)
	}

	text, e := callToolText(t, sock, "list_tokens", nil)
	if e != "" {
		t.Fatalf("list_tokens errored: %s", e)
	}
	var listed struct {
		Tokens []struct {
			JID     string `json:"jid"`
			Context string `json:"context"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(text), &listed); err != nil {
		t.Fatalf("list payload %q not JSON: %v", text, err)
	}
	byJID := map[string]string{}
	for _, r := range listed.Tokens {
		byJID[r.JID] = r.Context
	}
	if byJID["web:hq"] != "bug reports from the acme site; triage, don't chat" ||
		byJID["hook:hq/github"] != "" {
		t.Fatalf("list contexts = %+v", byJID)
	}
}

// TestRouteTokensMCP_RevokeCrossFolderDenied is the crux: a folder may revoke only
// tokens IT minted. Folder "other" cannot revoke folder "acme"'s token — the
// DELETE is scoped to owner_folder=other, so acme's row (owner=acme) matches zero
// rows: deleted=false and the token still resolves. Dropping the owner_folder
// predicate in revokeRouteTokenTx would delete acme's token and flip this to
// deleted=true — so this test fails-on-broken, guarding the ownership containment.
func TestRouteTokensMCP_RevokeCrossFolderDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "acme"})
	_ = db.PutGroup(core.Group{Folder: "other"})
	// acme mints its own token out-of-band.
	acmeTok, _, err := db.IssueRouteToken("web:acme", "acme", "")
	if err != nil {
		t.Fatalf("seed acme token: %v", err)
	}

	// "other" (granted revoke_token) tries to revoke acme's token.
	grantMCPTools(t, db, "other", "revoke_token")
	sock := serveRouteTokensMCP(t, db, "other", "folder:other", "")
	rtext, e := callToolText(t, sock, "revoke_token", map[string]any{"jid": "web:acme"})
	if e != "" {
		t.Fatalf("revoke_token errored: %s", e)
	}
	if rtext != `{"deleted":false}` {
		t.Fatalf("cross-folder revoke result = %q, want {\"deleted\":false}", rtext)
	}
	if j, owner, _, e := db.ResolveRouteToken(acmeTok); e != nil || j != "web:acme" || owner != "acme" {
		t.Fatalf("acme token revoked cross-folder: jid=%q owner=%q err=%v", j, owner, e)
	}
}

// TestRouteTokensMCP_MintTargetContainment: the Gate binds the minted token's
// target_folder to the caller's grant scope. "acme", delegated the mint tools over
// acme/**, may NOT point a token at a sibling top-level folder "other", but MAY
// point at a descendant "acme/sub".
func TestRouteTokensMCP_MintTargetContainment(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "acme"})
	grantMCPTools(t, db, "acme", "issue_chat_link", "issue_webhook", "list_tokens", "revoke_token")
	sock := serveRouteTokensMCP(t, db, "acme", "folder:acme", "")

	// sibling top-level target: outside the acme/** grant scope → denied.
	if _, e := callToolText(t, sock, "issue_chat_link", map[string]any{"target_folder": "other"}); e == "" {
		t.Fatal("minting for a sibling top-level folder should be denied")
	}
	if rows, _ := db.ListRouteTokens("acme"); len(rows) != 0 {
		t.Fatalf("denied mint still wrote a token: %+v", rows)
	}
	// descendant target: allowed (owner_folder stays acme, jid targets the child).
	_, jid, _ := issueToken(t, sock, "issue_chat_link", map[string]any{"target_folder": "acme/sub"})
	if jid != "web:acme/sub" {
		t.Fatalf("descendant mint jid = %q, want web:acme/sub", jid)
	}
}

// TestRouteTokensMCP_Visibility: the Visible predicate (auth.EffectiveActions)
// preserves tools/list gating — a folder granted all four tools sees all four; an
// ungranted folder sees NONE. 5/33 demoted the former tier-1 self-service default:
// list_tokens/revoke_token are not in the role:member floor either, so a folder that
// was never delegated them sees no token tool at all.
func TestRouteTokensMCP_Visibility(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	grantMCPTools(t, db, "hq", "issue_chat_link", "issue_webhook", "list_tokens", "revoke_token")

	granted := listToolNames(t, serveRouteTokensMCP(t, db, "hq", "folder:hq", ""))
	for _, name := range []string{"issue_chat_link", "issue_webhook", "list_tokens", "revoke_token"} {
		if !granted[name] {
			t.Fatalf("%s not visible to a folder granted it", name)
		}
	}
	ungranted := listToolNames(t, serveRouteTokensMCP(t, db, "world/a", "folder:world/a", ""))
	for _, name := range []string{"issue_chat_link", "issue_webhook", "list_tokens", "revoke_token"} {
		if ungranted[name] {
			t.Fatalf("%s visible to a folder never delegated it", name)
		}
	}
}

// TestServeTurnMCP_ElevatedMintsChatLink drives the REAL per-turn socket the way
// dispatch wires an operator /root turn (turnMCP.elevated): a folder with NO
// route-token grants mints a chat link in ONE native MCP call. Guards the
// 2026-07-16 marinade regression: elevation widened tools/list but left the row-ACL
// half unelevated, so issue_chat_link 403'd even under /root and the agent fell back
// to the broken mcpc path.
func TestServeTurnMCP_ElevatedMintsChatLink(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	srv := NewServer(db, nil, nil, nil, 0, "https://x.test")
	ipcDir := t.TempDir()
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "hq", turnID: "t1", elevated: true}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	t.Cleanup(stop)

	sock := groupfolder.IpcSocket(ipcDir)
	tok, jid, url := issueToken(t, sock, "issue_chat_link", nil)
	if jid != "web:hq" || url != "https://x.test/chat/"+tok+"/" {
		t.Fatalf("elevated mint: jid=%q url=%q", jid, url)
	}
	if j, owner, _, e := db.ResolveRouteToken(tok); e != nil || j != "web:hq" || owner != "hq" {
		t.Fatalf("resolve minted token: jid=%q owner=%q err=%v", j, owner, e)
	}
}

// TestServeTurnMCP_UnelevatedHidesMint is the control for the elevated test: the
// same ungranted folder WITHOUT /root sees NO route-token tool. 5/33: the role:member
// floor is the 12 messaging verbs, so neither the mint pair nor the former
// tier-1 self-service pair is advertised without an explicit delegation. `reply`
// (a floor verb) is asserted visible so this is a real gate, not a dead socket.
func TestServeTurnMCP_UnelevatedHidesMint(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "hq", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	t.Cleanup(stop)

	names := listToolNames(t, groupfolder.IpcSocket(ipcDir))
	for _, name := range []string{"issue_chat_link", "issue_webhook", "list_tokens", "revoke_token"} {
		if names[name] {
			t.Fatalf("%s visible without elevation or a delegated grant: %v", name, names)
		}
	}
	if !names["reply"] {
		t.Fatalf("role:member floor verb `reply` missing — the socket advertises nothing: %v", names)
	}
}

// TestRouteTokensMCP_GateDenies: a tool that is VISIBLE (an allow row grants it) but
// overridden by a DENY row is rejected at call time by the injected Gate
// (auth.Authorize is deny-wins), before the mint runs.
func TestRouteTokensMCP_GateDenies(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "issue_chat_link")
	// A deny row on the same (tool, scope) wins over the allow — visible, uncallable.
	if err := db.AddACLRow(core.ACLRow{
		Principal: "folder:hq", Action: "mcp:issue_chat_link", Scope: "hq/**", Effect: "deny",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	sock := serveRouteTokensMCP(t, db, "hq", "folder:hq", "")

	if !listToolNames(t, sock)["issue_chat_link"] {
		t.Fatal("issue_chat_link should be visible (an allow row grants it)")
	}
	if _, e := callToolText(t, sock, "issue_chat_link", nil); e == "" {
		t.Fatal("issue_chat_link should be denied by the deny row")
	}
	if rows, _ := db.ListRouteTokens("hq"); len(rows) != 0 {
		t.Fatalf("denied issue still wrote a token: %+v", rows)
	}
}

// TestRouteTokensMCP_AuditRowLands: an agent mint writes one audit_log row in
// routd.db via resreg's tx-bound EmitInTx (was emitSys/LogIPCAudit). Confirms the
// audit ROW still lands; note the shape change (action route_tokens:issue_chat,
// resource route_tokens, surface mcp) vs the old flat "issue_chat_link" tool name.
func TestRouteTokensMCP_AuditRowLands(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "issue_chat_link", "issue_webhook", "list_tokens", "revoke_token")
	sock := serveRouteTokensMCP(t, db, "hq", "folder:hq", "")

	if _, e := callToolText(t, sock, "issue_chat_link", nil); e != "" {
		t.Fatalf("issue_chat_link errored: %s", e)
	}
	var n int
	err = db.SQL().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action='route_tokens:issue_chat' AND outcome='ok' AND folder='hq' AND surface='mcp'`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log has %d route_tokens:issue_chat rows, want 1", n)
	}
}
