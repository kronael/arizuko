package ipc

import (
	"testing"
)

// get_web_presence reports a folder's derived canonical host, the always-works
// /pub path, and the /priv base. Tier 0 may query any folder; tier 1+ only its
// own subtree. The renderer is WebPresenceFor (shared with routd's REST twin).
func TestServeMCP_WebPresence_Derived(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	gated := GatedFns{
		WebHost:       "krons.fiu.wtf",
		HostingDomain: "fiu.wtf",
	}
	// folder "atlas" → tier 0 (root): may query any folder.
	stop, err := ServeMCP(sock, gated, StoreFns{}, "atlas", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	res, errText := callTool(t, sock, "get_web_presence", map[string]any{})
	if errText != "" {
		t.Fatalf("get_web_presence: %s", errText)
	}
	want := map[string]string{
		"folder":           "atlas",
		"hosting_domain":   "fiu.wtf",
		"derived_host":     "atlas.fiu.wtf",
		"canonical_host":   "atlas.fiu.wtf",
		"public_base_url":  "https://atlas.fiu.wtf/",
		"private_base_url": "https://krons.fiu.wtf/priv/atlas/",
		"pub_path":         "https://krons.fiu.wtf/pub/atlas/",
	}
	for k, v := range want {
		if got, _ := res[k].(string); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if _, ok := res["alias_host"]; ok {
		t.Errorf("alias_host should be omitted when no alias; got %v", res["alias_host"])
	}
}

// An alias whose value == the folder overrides the derived host as canonical.
func TestServeMCP_WebPresence_Alias(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	gated := GatedFns{
		WebHost:       "krons.fiu.wtf",
		HostingDomain: "fiu.wtf",
		VhostAliases:  map[string]string{"fab.krons.cx": "atlas"},
	}
	stop, err := ServeMCP(sock, gated, StoreFns{}, "atlas", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	res, errText := callTool(t, sock, "get_web_presence", map[string]any{})
	if errText != "" {
		t.Fatalf("get_web_presence: %s", errText)
	}
	if got, _ := res["alias_host"].(string); got != "fab.krons.cx" {
		t.Errorf("alias_host = %q, want fab.krons.cx", got)
	}
	if got, _ := res["canonical_host"].(string); got != "fab.krons.cx" {
		t.Errorf("canonical_host = %q, want fab.krons.cx (alias wins)", got)
	}
	if got, _ := res["public_base_url"].(string); got != "https://fab.krons.cx/" {
		t.Errorf("public_base_url = %q, want https://fab.krons.cx/", got)
	}
	// pub_path stays path-based on WEB_HOST regardless of the alias.
	if got, _ := res["pub_path"].(string); got != "https://krons.fiu.wtf/pub/atlas/" {
		t.Errorf("pub_path = %q, want https://krons.fiu.wtf/pub/atlas/", got)
	}
}

// Tier 1 may inspect its own folder + descendants, never a sibling/other world.
func TestServeMCP_WebPresence_Tier1Containment(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	gated := GatedFns{WebHost: "krons.fiu.wtf", HostingDomain: "fiu.wtf"}
	// folder "world/a" → tier 1.
	stop, err := ServeMCP(sock, gated, StoreFns{}, "world/a", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	// Own folder: allowed.
	if _, errText := callTool(t, sock, "get_web_presence", map[string]any{"folder": "world/a"}); errText != "" {
		t.Fatalf("own folder should be allowed: %s", errText)
	}
	// Descendant: allowed.
	if _, errText := callTool(t, sock, "get_web_presence", map[string]any{"folder": "world/a/sub"}); errText != "" {
		t.Fatalf("descendant should be allowed: %s", errText)
	}
	// Other world: denied.
	if _, errText := callTool(t, sock, "get_web_presence", map[string]any{"folder": "other"}); errText == "" {
		t.Fatal("expected tier-1 cross-folder get_web_presence to be denied")
	}
}

// WebPresenceFor: with no HOSTING_DOMAIN and no alias, canonical falls back to
// the instance WEB_HOST and derived_host is empty.
func TestWebPresenceFor_NoDomain(t *testing.T) {
	got := WebPresenceFor("solo", "app.example.com", "", nil)
	if got.DerivedHost != "" {
		t.Errorf("derived_host = %q, want empty (no HOSTING_DOMAIN)", got.DerivedHost)
	}
	if got.CanonicalHost != "app.example.com" {
		t.Errorf("canonical_host = %q, want WEB_HOST fallback", got.CanonicalHost)
	}
	if got.PubPath != "https://app.example.com/pub/solo/" {
		t.Errorf("pub_path = %q", got.PubPath)
	}
}
