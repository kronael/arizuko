package routd

// issue_pairing_link (spec 5/31): the agent-MCP-only mint on the route_tokens
// resource. Every test drives the REAL agent socket so the injected Gate, the
// route-ownership containment and the kind='pair' write are all exercised.
// Like the other token verbs, issue_pairing_link is NOT in the role:member floor
// — each test delegates it explicitly with grantMCPTools.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// pairingFixture registers folder, routes `match` to it, and delegates the mint.
func pairingFixture(t *testing.T, folder, match string) (*DB, string) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	if match != "" {
		if _, err := db.AddRoute(core.Route{Match: match, Target: folder}); err != nil {
			t.Fatal(err)
		}
	}
	grantMCPTools(t, db, folder, "issue_pairing_link")
	return db, serveRouteTokensMCP(t, db, folder, "folder:"+folder, "https://x.test")
}

// issuePairing drives the tool and returns the parsed {url, jid}.
func issuePairing(t *testing.T, sock, jid string) (url, gotJID string) {
	t.Helper()
	text, e := callToolText(t, sock, "issue_pairing_link", map[string]any{"jid": jid})
	if e != "" {
		t.Fatalf("issue_pairing_link(%s) errored: %s", jid, e)
	}
	var out struct {
		URL string `json:"url"`
		JID string `json:"jid"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("payload %q not JSON: %v", text, err)
	}
	return out.URL, out.JID
}

// The happy path: a routed JID mints a /pair/ URL whose token is a kind='pair'
// row owned by the folder the JID routes to, and which is NOT a delivery token.
func TestPairingMint_RoutedJID(t *testing.T) {
	db, sock := pairingFixture(t, "hq", "room=user/42")

	url, jid := issuePairing(t, sock, "telegram:user/42")
	if jid != "telegram:user/42" {
		t.Fatalf("jid = %q", jid)
	}
	raw, ok := strings.CutPrefix(url, "https://x.test/pair/")
	if !ok || raw == "" {
		t.Fatalf("url = %q, want https://x.test/pair/<token>", url)
	}

	st := store.New(db.SQL())
	got, err := st.PeekPairing(raw)
	if err != nil || got != "telegram:user/42" {
		t.Fatalf("PeekPairing = (%q, %v)", got, err)
	}
	if _, _, _, err := db.ResolveRouteToken(raw); err == nil {
		t.Error("a pairing token resolved as a delivery route token")
	}

	var owner, kind string
	if err := db.SQL().QueryRow(
		`SELECT owner_folder, kind FROM route_tokens WHERE jid = ?`, "telegram:user/42",
	).Scan(&owner, &kind); err != nil {
		t.Fatal(err)
	}
	if owner != "hq" || kind != store.RouteTokenKindPair {
		t.Errorf("row = (owner %q, kind %q), want (hq, pair)", owner, kind)
	}
}

// Minting for a JID that does not route to the caller's folder is refused: the
// Gate authorizes the caller against the folder the JID routes to.
func TestPairingMint_ForeignJIDRefused(t *testing.T) {
	db, sock := pairingFixture(t, "hq", "room=user/42")
	// A second folder handles a different chat; hq holds no grant over it.
	if err := db.PutGroup(core.Group{Folder: "rival"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddRoute(core.Route{Match: "room=user/99", Target: "rival"}); err != nil {
		t.Fatal(err)
	}

	_, e := callToolText(t, sock, "issue_pairing_link", map[string]any{"jid": "telegram:user/99"})
	if e == "" {
		t.Fatal("minting for another folder's chat was allowed")
	}
	if !strings.Contains(e, "not permitted") {
		t.Errorf("error = %q, want a permission refusal", e)
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM route_tokens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("refused mint still wrote %d token rows", n)
	}
}

// A JID with no route at all is refused — there is no folder to bind it to.
func TestPairingMint_UnroutedJIDRefused(t *testing.T) {
	_, sock := pairingFixture(t, "hq", "room=user/42")

	_, e := callToolText(t, sock, "issue_pairing_link", map[string]any{"jid": "telegram:user/777"})
	if e == "" {
		t.Fatal("minting for an unrouted JID was allowed")
	}
	if !strings.Contains(e, "no route") && !strings.Contains(e, "not permitted") {
		t.Errorf("error = %q, want a route/permission refusal", e)
	}
}

// Ingress surfaces are not identities: a web:/hook:/anon: JID is refused even
// when it routes (spec 5/31 § Not in scope).
func TestPairingMint_IngressSurfacesRefused(t *testing.T) {
	db, sock := pairingFixture(t, "hq", "room=hq")

	for _, jid := range []string{"web:hq", "hook:hq/github", "anon:deadbeef"} {
		if _, e := callToolText(t, sock, "issue_pairing_link", map[string]any{"jid": jid}); e == "" {
			t.Errorf("issue_pairing_link(%q) was allowed", jid)
		}
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM route_tokens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("refused mints wrote %d token rows", n)
	}
}

// Without the delegation the tool is neither callable nor visible — the mint is
// default-deny, like the other token verbs.
func TestPairingMint_DefaultDeny(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "hq"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddRoute(core.Route{Match: "room=user/42", Target: "hq"}); err != nil {
		t.Fatal(err)
	}
	sock := serveRouteTokensMCP(t, db, "hq", "folder:hq", "https://x.test")

	if listToolNames(t, sock)["issue_pairing_link"] {
		t.Fatal("issue_pairing_link is visible to an undelegated folder")
	}

	// The delivery mints are a SEPARATE grant — holding them does not surface
	// the pairing mint.
	grantMCPTools(t, db, "hq", "issue_chat_link", "issue_webhook")
	sock2 := serveRouteTokensMCP(t, db, "hq", "folder:hq", "https://x.test")
	tools := listToolNames(t, sock2)
	if !tools["issue_chat_link"] {
		t.Fatal("issue_chat_link missing after delegation; fixture is wrong")
	}
	if tools["issue_pairing_link"] {
		t.Fatal("delegating the delivery mints surfaced issue_pairing_link")
	}
}
