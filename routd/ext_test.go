package routd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

func TestLoadExtProviders_BuiltinCloudflare(t *testing.T) {
	tools, err := LoadExtProviders(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools {
		if tool.LocalName == "cloudflare_dns_list" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cloudflare_dns_list not found in %d tools", len(tools))
	}
	if len(tools) != 10 {
		t.Errorf("want 10 builtin tools, got %d", len(tools))
	}
}

func TestLoadExtProviders_InputSchema(t *testing.T) {
	tools, err := LoadExtProviders(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ipc.ExtTool{}
	for _, x := range tools {
		byName[x.LocalName] = x
	}
	parse := func(name string) (map[string]any, map[string]bool) {
		raw := byName[name].InputSchema
		if len(raw) == 0 {
			t.Fatalf("%s: no InputSchema", name)
		}
		var s struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		req := map[string]bool{}
		for _, r := range s.Required {
			req[r] = true
		}
		return s.Properties, req
	}
	// Path placeholders → required string props.
	props, req := parse("cloudflare_dns_delete")
	for _, want := range []string{"zone_id", "record_id"} {
		if _, ok := props[want]; !ok || !req[want] {
			t.Errorf("dns_delete: %s should be a required property", want)
		}
	}
	// Declared [[param]] fields merge with the path param.
	props, req = parse("cloudflare_dns_create")
	for _, want := range []string{"zone_id", "type", "name", "content"} {
		if _, ok := props[want]; !ok || !req[want] {
			t.Errorf("dns_create: %s should be a required property", want)
		}
	}
}

func TestCallExtTool_Bearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))

	tool := ipc.ExtTool{
		LocalName: "test_op",
		Method:    "GET",
		BaseURL:   srv.URL,
		Path:      "/test",
		Secrets:   []string{"MY_TOKEN"},
		Headers:   map[string]string{"Authorization": "Bearer {secret:MY_TOKEN}"},
	}
	secrets := map[string]string{"MY_TOKEN": "tok123"}
	result, err := ipc.CallExtTool(context.Background(), tool, nil, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if gotAuth != "Bearer tok123" {
		t.Errorf("got auth %q", gotAuth)
	}
	_ = result
}

func TestCallExtTool_PathParam(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tool := ipc.ExtTool{
		LocalName: "test_dns",
		Method:    "GET",
		BaseURL:   srv.URL,
		Path:      "/zones/{zone_id}/dns_records",
		Secrets:   []string{"TOK"},
		Headers:   map[string]string{"Authorization": "Bearer {secret:TOK}"},
	}
	args := map[string]any{"zone_id": "abc123"}
	secrets := map[string]string{"TOK": "x"}
	_, err := ipc.CallExtTool(context.Background(), tool, args, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/zones/abc123/dns_records" {
		t.Errorf("got path %q", gotPath)
	}
}

func TestCallExtTool_Scrub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"supersecret","data":"ok"}`))
	}))
	defer srv.Close()

	tool := ipc.ExtTool{
		LocalName: "test_scrub",
		Method:    "GET",
		BaseURL:   srv.URL,
		Path:      "/test",
		Secrets:   []string{"MY_SECRET"},
		Headers:   map[string]string{"Authorization": "Bearer {secret:MY_SECRET}"},
	}
	secrets := map[string]string{"MY_SECRET": "supersecret"}
	result, err := ipc.CallExtTool(context.Background(), tool, nil, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	tc, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		t.Fatal("content[0] is not TextContent")
	}
	if strings.Contains(tc.Text, "supersecret") {
		t.Errorf("secret not scrubbed from response: %s", tc.Text)
	}
	if !strings.Contains(tc.Text, "«redacted»") {
		t.Error("expected «redacted» marker in scrubbed output")
	}
}

// TestCallExtTool_ScrubsTransportError guards the leak where an apikey-query
// secret rides in the request URL: on a dial/DNS/TLS failure, *url.Error
// embeds the full URL (incl. RawQuery), so an unscrubbed transport-error
// result would expose the key.
func TestCallExtTool_ScrubsTransportError(t *testing.T) {
	// A started-then-closed server yields a definitely-dead address, forcing
	// http.DefaultClient.Do to return a *url.Error over the request URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	tool := ipc.ExtTool{
		LocalName: "test_qkey",
		Method:    "GET",
		BaseURL:   deadURL,
		Path:      "/dns",
		Secrets:   []string{"NC_KEY"},
		Query:     map[string]string{"ApiKey": "{secret:NC_KEY}"},
	}
	secrets := map[string]string{"NC_KEY": "supersecretkey"}
	result, err := ipc.CallExtTool(context.Background(), tool, nil, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	tc, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		t.Fatal("content[0] is not TextContent")
	}
	if strings.Contains(tc.Text, "supersecretkey") {
		t.Errorf("secret leaked in transport-error result: %s", tc.Text)
	}
}

func TestCallExtTool_JsonBody(t *testing.T) {
	var gotKey, gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]string
		json.Unmarshal(body, &m)
		gotKey = m["apikey"]
		gotSecret = m["secretapikey"]
		w.Write([]byte(`{"status":"SUCCESS"}`))
	}))

	tool := ipc.ExtTool{
		LocalName: "test_jsonbody",
		Method:    "POST",
		BaseURL:   srv.URL,
		Path:      "/test",
		Secrets:   []string{"PB_API_KEY", "PB_SECRET"},
		Body: map[string]string{
			"apikey":       "{secret:PB_API_KEY}",
			"secretapikey": "{secret:PB_SECRET}",
		},
	}
	secrets := map[string]string{"PB_API_KEY": "mykey", "PB_SECRET": "mysecret"}
	_, err := ipc.CallExtTool(context.Background(), tool, nil, secrets, nil)
	srv.Close()
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "mykey" {
		t.Errorf("apikey in body: got %q", gotKey)
	}
	if gotSecret != "mysecret" {
		t.Errorf("secretapikey in body: got %q", gotSecret)
	}
}

func TestCallExtTool_MissingSecret(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))

	tool := ipc.ExtTool{
		LocalName: "test_missing",
		Method:    "GET",
		BaseURL:   srv.URL,
		Path:      "/test",
		Secrets:   []string{"CF_API_TOKEN"},
		Headers:   map[string]string{"Authorization": "Bearer {secret:CF_API_TOKEN}"},
	}
	secrets := map[string]string{}
	_, err := ipc.CallExtTool(context.Background(), tool, nil, secrets, nil)
	srv.Close()
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header when secret missing, got %q", gotAuth)
	}
}

// extResultText returns the text of an ext-tool result and whether it is an
// error result.
func extResultText(t *testing.T, r *mcp.CallToolResult) (string, bool) {
	t.Helper()
	tc, ok := mcp.AsTextContent(r.Content[0])
	if !ok {
		t.Fatal("content[0] is not TextContent")
	}
	return tc.Text, r.IsError
}

// F69 / spec 5/15 § "Refresh at call time": on a 401 the call refreshes the
// credential and retries ONCE. Without it, an access token that dies mid-turn
// (a provider with an optimistic expires_in, so the proactive near-expiry
// refresh never fires) fails the agent's call outright.
func TestCallExtTool_RefreshesAndRetriesOnce401(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seen = append(seen, auth)
		if auth != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"token expired"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tool := ipc.ExtTool{
		LocalName: "test_op", Method: "GET", BaseURL: srv.URL, Path: "/test",
		Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"},
	}
	refreshes := 0
	refresh := func(context.Context) (map[string]string, error) {
		refreshes++
		return map[string]string{"TOK": "fresh"}, nil
	}
	result, err := ipc.CallExtTool(context.Background(), tool, nil, map[string]string{"TOK": "stale"}, refresh)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := extResultText(t, result)
	if isErr {
		t.Fatalf("call failed after refresh: %s", text)
	}
	if refreshes != 1 {
		t.Fatalf("refresh called %d times, want exactly 1", refreshes)
	}
	if len(seen) != 2 || seen[0] != "Bearer stale" || seen[1] != "Bearer fresh" {
		t.Fatalf("attempts = %v, want the stale token then the refreshed one", seen)
	}
}

// The retry is ONCE, and only on 401. A 401 that survives the refresh is not
// transient — it surfaces to the agent instead of looping.
func TestCallExtTool_NoSecondRetryWhen401Persists(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"nope"}`))
	}))
	defer srv.Close()

	tool := ipc.ExtTool{
		LocalName: "test_op", Method: "GET", BaseURL: srv.URL, Path: "/test",
		Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"},
	}
	refresh := func(context.Context) (map[string]string, error) {
		return map[string]string{"TOK": "fresh"}, nil
	}
	result, err := ipc.CallExtTool(context.Background(), tool, nil, map[string]string{"TOK": "stale"}, refresh)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := extResultText(t, result)
	if !isErr || !strings.Contains(text, "HTTP 401") {
		t.Fatalf("persistent 401 must surface, got isErr=%v text=%q", isErr, text)
	}
	if hits != 2 {
		t.Fatalf("upstream hits = %d, want 2 (one attempt + exactly one retry)", hits)
	}
}

// Non-401 failures never retry — the repo rule is that only transient errors do,
// and a 500 or a 403 is not the expired-token case 5/15 describes. A refresh
// that renews nothing (a pasted PAT) also does not re-send.
func TestCallExtTool_NoRetryOnNon401OrNoOpRefresh(t *testing.T) {
	for _, c := range []struct {
		name       string
		status     int
		refreshed  map[string]string
		refreshErr error
	}{
		{"500 is not an auth failure", http.StatusInternalServerError, map[string]string{"TOK": "fresh"}, nil},
		{"403 is not an expired token", http.StatusForbidden, map[string]string{"TOK": "fresh"}, nil},
		{"refresh renewed nothing", http.StatusUnauthorized, map[string]string{"TOK": "stale"}, nil},
		{"refresh failed", http.StatusUnauthorized, nil, io.ErrUnexpectedEOF},
	} {
		t.Run(c.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.WriteHeader(c.status)
				w.Write([]byte(`{"error":"x"}`))
			}))
			defer srv.Close()

			tool := ipc.ExtTool{
				LocalName: "test_op", Method: "GET", BaseURL: srv.URL, Path: "/test",
				Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"},
			}
			refresh := func(context.Context) (map[string]string, error) {
				return c.refreshed, c.refreshErr
			}
			if _, err := ipc.CallExtTool(context.Background(), tool, nil,
				map[string]string{"TOK": "stale"}, refresh); err != nil {
				t.Fatal(err)
			}
			if hits != 1 {
				t.Fatalf("upstream hits = %d, want 1 (no retry)", hits)
			}
		})
	}
}

func writeOperatorConnectors(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connectors.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The rest loader reads the same operator file and refuses the same unknown
// table, so a wrong block name cannot slip past one loader by being skipped
// in the other.
func TestLoadExtProviders_RejectsUnknownTable(t *testing.T) {
	dir := writeOperatorConnectors(t, "[[ext]]\nname = \"x\"\nurl = \"https://example.com\"\n")
	if _, err := LoadExtProviders(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "ext") {
		t.Fatalf("an [[ext]] block must fail the load by name, got %v", err)
	}
}

// A bare block is mcp (ipc.ConnectorSpec): the rest loader must pass over it
// rather than register a rest provider with no tools.
func TestLoadExtProviders_BareBlockIsMCP(t *testing.T) {
	dir := writeOperatorConnectors(t, "[[connector]]\nname = \"fake\"\nurl = \"https://example.com/mcp\"\n")
	tools, err := LoadExtProviders(context.Background(), dir)
	if err != nil {
		t.Fatalf("bare block is mcp and must not be loaded as rest: %v", err)
	}
	if len(tools) != 10 {
		t.Errorf("want the 10 builtin tools only, got %d", len(tools))
	}
}

// A rest block that declares no tool configures nothing — refused at load.
func TestLoadExtProviders_RestWithoutToolsIsLoud(t *testing.T) {
	dir := writeOperatorConnectors(t, "[[connector]]\nname = \"empty\"\ntransport = \"rest\"\nurl = \"https://example.com\"\n")
	if _, err := LoadExtProviders(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("a rest connector without tools must fail the load by name, got %v", err)
	}
}

// The operator file is the later source, so a connector it names after a
// built-in replaces that built-in's tool under the same LocalName rather than
// registering a second one — and says so at WARN, naming both sources (6/19).
func TestLoadExtProviders_OperatorShadowsBuiltin(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := writeOperatorConnectors(t, `[[connector]]
name      = "cloudflare"
transport = "rest"
url       = "https://operator.example.com/v4"

  [[connector.tool]]
  name   = "dns_list"
  method = "GET"
  path   = "/zones/{zone_id}/dns_records"
`)
	tools, err := LoadExtProviders(context.Background(), dir)
	if err != nil {
		t.Fatalf("an operator override is a shadow, not a failure: %v", err)
	}
	var hits []ipc.ExtTool
	for _, tool := range tools {
		if tool.LocalName == "cloudflare_dns_list" {
			hits = append(hits, tool)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("cloudflare_dns_list registered %d times, want 1", len(hits))
	}
	if hits[0].BaseURL != "https://operator.example.com/v4" {
		t.Errorf("BaseURL = %q, want the operator's", hits[0].BaseURL)
	}
	if len(tools) != 10 {
		t.Errorf("want 10 tools (the shadow replaces, never adds), got %d", len(tools))
	}
	logged := buf.String()
	if !strings.Contains(logged, "ext tool shadowed") ||
		!strings.Contains(logged, "cloudflare_dns_list") ||
		!strings.Contains(logged, "embedded extproviders") ||
		!strings.Contains(logged, "connectors.toml") {
		t.Errorf("a shadow must log loud, naming the tool and both sources; got %q", logged)
	}
}

// Two blocks in the operator file claiming one tool name is not an ordering
// question — nothing picks between them, so the load fails.
func TestLoadExtProviders_DuplicateInOneSourceIsError(t *testing.T) {
	dir := writeOperatorConnectors(t, `[[connector]]
name      = "acme"
transport = "rest"
url       = "https://one.example.com"

  [[connector.tool]]
  name   = "zones"
  method = "GET"
  path   = "/zones"

[[connector]]
name      = "acme"
transport = "rest"
url       = "https://two.example.com"

  [[connector.tool]]
  name   = "zones"
  method = "GET"
  path   = "/zones"
`)
	_, err := LoadExtProviders(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "acme_zones") {
		t.Fatalf("one name declared twice in one source must fail the load by name, got %v", err)
	}
}

// The same refusal inside a single connector: two [[connector.tool]] blocks
// under one name collide on the same LocalName.
func TestLoadExtProviders_DuplicateToolInOneConnectorIsError(t *testing.T) {
	dir := writeOperatorConnectors(t, `[[connector]]
name      = "acme"
transport = "rest"
url       = "https://one.example.com"

  [[connector.tool]]
  name   = "zones"
  method = "GET"
  path   = "/zones"

  [[connector.tool]]
  name   = "zones"
  method = "POST"
  path   = "/zones"
`)
	_, err := LoadExtProviders(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "acme_zones") {
		t.Fatalf("one name declared twice in one connector must fail the load by name, got %v", err)
	}
}

// Granting one of the embedded REST providers is what puts its tool into
// tools/list, and every one of them carries a schema extInputSchema builds from
// its path placeholders. The list marshals as ONE document, so the granted tool
// and every unrelated tool in it stand or fall together (BUGS L222).
//
// An ext tool needs two rows: the announce filter reads the tool's own scope,
// and the tools/list visibility predicate reads mcp:<name>.
func TestExtTool_Granted_IsListedBesideTheOtherTools(t *testing.T) {
	tools, err := LoadExtProviders(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("LoadExtProviders: %v", err)
	}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	grantMCPTools(t, db, "main", "cloudflare_dns_list")
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:main", Action: "ext:cloudflare:dns:read",
		Scope: "main/**", Effect: "allow",
	}); err != nil {
		t.Fatalf("grant ext scope: %v", err)
	}

	srv := NewServer(db, nil, nil, nil, 0, "")
	srv.SetExtTools(tools)
	sock := groupfolder.IpcSocket(t.TempDir())
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(turnMCP{folder: "main"}),
		srv.buildStoreFns(turnMCP{folder: "main"}), "main", false, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	names := listToolNames(t, sock)
	if !names["cloudflare_dns_list"] {
		t.Errorf("tools/list did not announce the granted ext tool; got %v", names)
	}
	// inspect_identity registers at a different site and carries a structured
	// schema, so its absence means the whole document was lost, not one tool.
	if !names["inspect_identity"] {
		t.Errorf("an ext tool took the unrelated tools down with it; got %v", names)
	}
}
