package routd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

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
}

func TestCallExtTool_Bearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tool := ipc.ExtTool{
		LocalName:  "test_op",
		Method:     "GET",
		BaseURL:    srv.URL,
		Path:       "/test",
		AuthMethod: "bearer",
		SecretKey:  "MY_TOKEN",
	}
	secrets := map[string]string{"MY_TOKEN": "tok123"}
	result, err := ipc.CallExtTool(context.Background(), tool, nil, secrets)
	if err != nil {
		t.Fatal(err)
	}
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
		LocalName:  "test_dns",
		Method:     "GET",
		BaseURL:    srv.URL,
		Path:       "/zones/{zone_id}/dns_records",
		AuthMethod: "bearer",
		SecretKey:  "TOK",
	}
	args := map[string]any{"zone_id": "abc123"}
	secrets := map[string]string{"TOK": "x"}
	_, err := ipc.CallExtTool(context.Background(), tool, args, secrets)
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
		LocalName:  "test_scrub",
		Method:     "GET",
		BaseURL:    srv.URL,
		Path:       "/test",
		AuthMethod: "bearer",
		SecretKey:  "MY_SECRET",
	}
	secrets := map[string]string{"MY_SECRET": "supersecret"}
	result, err := ipc.CallExtTool(context.Background(), tool, nil, secrets)
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
}
