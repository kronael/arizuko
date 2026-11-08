package routd

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

func TestGitHubConnectorTrustedRepository(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	t.Setenv("GITHUB_APP_ID", "123")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", base64.StdEncoding.EncodeToString(pemKey))
	var installationRequests, discoveries, calls atomic.Int32
	var refuseInstallation atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		installationRequests.Add(1)
		switch r.URL.Path {
		case "/repos/acme/project/installation":
			if refuseInstallation.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"id":42}`)
		case "/app/installations/42/access_tokens":
			var body struct {
				Repositories []string `json:"repositories"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(body.Repositories, []string{"project"}) {
				t.Errorf("token repositories = %v", body.Repositories)
			}
			fmt.Fprintf(w, `{"token":"installation-token","expires_at":%q}`,
				time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		default:
			t.Errorf("untrusted repository request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	t.Setenv("GITHUB_API_URL", api.URL)
	remote := server.NewMCPServer("github", "1")
	remote.AddTool(mcp.NewTool("echo_auth",
		mcp.WithString("owner"), mcp.WithString("repo"), mcp.WithString("chat_jid")),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			if req.GetString("repo", "") != "other" {
				t.Errorf("agent arguments changed: %v", req.GetArguments())
			}
			return mcp.NewToolResultText("installation-token"), nil
		})
	transport := server.NewStreamableHTTPServer(remote)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			discoveries.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer installation-token" {
				t.Errorf("connector credential = %q", got)
				http.Error(w, "wrong credential", http.StatusUnauthorized)
				return
			}
		}
		transport.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	spec := ipc.ConnectorSpec{
		Name: "github", URL: endpoint.URL,
		Secrets:     []string{ipc.GitHubInstallationToken},
		Headers:     map[string]string{"Authorization": "Bearer {secret:GITHUB_INSTALLATION_TOKEN}"},
		CallTimeout: 5 * time.Second,
	}
	for _, tc := range []struct {
		name, jid, source  string
		refuse, invalidKey bool
		want               bool
	}{
		{name: "pr", jid: "github:acme/project/pr/12", want: true},
		{name: "issue", jid: "github:acme/project/issue/7", want: true},
		{name: "repository", jid: "github:acme/project", want: true},
		{name: "other channel", jid: "telegram:123"},
		{name: "account", jid: "github:acme"},
		{name: "repo source wins", jid: "github:acme/project", source: "repo/acme/project"},
		{name: "org source wins", jid: "github:acme/project", source: "org/acme"},
		{name: "missing source", jid: "github:acme/project", source: "none"},
		{name: "missing installation", jid: "github:acme/project", refuse: true},
		{name: "invalid credentials", jid: "github:acme/project", invalidKey: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			st := seedSecretsStore(t, db, "github-connector-test-key")
			for _, scope := range []store.SecretScope{store.ScopeFolder, store.ScopeUser} {
				id := "eng"
				if scope == store.ScopeUser {
					id = "github:alice"
				}
				for _, name := range []string{ipc.GitHubInstallationToken, "GITHUB_TOKEN"} {
					if err := st.PutSecretRow(scope, id, name, "user-pat"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.source != "none" {
				for _, source := range []string{"app/bot", tc.source} {
					if source == "" {
						continue
					}
					_, err := st.AddGitHubSource(store.GitHubSource{
						Source: source, OwnerFolder: "eng",
					}, "eng")
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.invalidKey {
				t.Setenv("GITHUB_APP_PRIVATE_KEY", "invalid-base64")
			}
			refuseInstallation.Store(tc.refuse)
			before := discoveries.Load()
			beforeCalls := calls.Load()
			beforeInstallations := installationRequests.Load()
			srv := NewServer(db, nil, nil, nil, 0, "")
			srv.SetConnectors([]ipc.ConnectorSpec{spec})
			turn := turnMCP{folder: "eng", chatJID: tc.jid, trigger: "github:alice", elevated: true}
			fns := srv.buildStoreFns(turn)
			for _, resolve := range []func(string, string, []string) (map[string]string, error){
				fns.ResolveConnectorSecrets, fns.RefreshConnectorSecrets,
			} {
				secrets, err := resolve("eng", "github_echo_auth", spec.Secrets)
				if (err == nil) != tc.want {
					t.Fatalf("credential error = %v, want success %v", err, tc.want)
				}
				if tc.want && secrets[ipc.GitHubInstallationToken] != "installation-token" {
					t.Fatalf("derived credential = %v", secrets)
				}
				if !tc.want && secrets[ipc.GitHubInstallationToken] != "" {
					t.Fatal("refused turn received a credential")
				}
			}
			pat, err := fns.ResolveConnectorSecrets("eng", "github_pat", []string{"GITHUB_TOKEN"})
			if err != nil || pat["GITHUB_TOKEN"] != "user-pat" {
				t.Fatalf("PAT meaning changed: %v, %v", pat, err)
			}
			ipcDir := t.TempDir()
			stop, err := srv.ServeTurnMCP(turn, ipcDir)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if !tc.want {
				if listToolNames(t, groupfolder.IpcSocket(ipcDir))["github_echo_auth"] {
					t.Fatal("refused connector tool was advertised")
				}
				_, err := ipc.CallConnectorTool(context.Background(), ipc.ConnectorTool{
					Connector: &spec, RemoteName: "echo_auth",
				}, nil, nil)
				if err == nil || discoveries.Load() != before {
					t.Fatal("refused turn reached connector")
				}
				return
			}
			text, callErr := callToolText(t, groupfolder.IpcSocket(ipcDir), "github_echo_auth", map[string]any{
				"owner": "foreign", "repo": "other", "chat_jid": "github:foreign/other/pr/1",
			})
			if callErr != "" || strings.Contains(text, "installation-token") || !strings.Contains(text, "«redacted»") {
				t.Fatalf("call result = %q, %q", text, callErr)
			}
			if calls.Load() != beforeCalls+1 || installationRequests.Load() <= beforeInstallations {
				t.Fatal("call did not use the installation credential")
			}
			if tc.name == "pr" {
				before := discoveries.Load()
				refuseInstallation.Store(true)
				_, callErr := callToolText(t, groupfolder.IpcSocket(ipcDir), "github_echo_auth", nil)
				if callErr == "" || calls.Load() != beforeCalls+1 || discoveries.Load() != before {
					t.Fatal("cached token allowed a connector call after installation removal")
				}
				refuseInstallation.Store(false)
				if _, err := fns.ResolveConnectorSecrets("eng", "github_echo_auth", spec.Secrets); err != nil {
					t.Fatal(err)
				}
				rec := doJSON(t, srv.Handler(), "DELETE", "/v1/github_sources/app/bot?owner_folder=eng", "", nil)
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":true`) {
					t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
				}
				_, callErr = callToolText(t, groupfolder.IpcSocket(ipcDir), "github_echo_auth", nil)
				if callErr == "" || calls.Load() != beforeCalls+1 || discoveries.Load() != before {
					t.Fatal("deleted source allowed an existing connector call")
				}
				for _, resolve := range []func(string, string, []string) (map[string]string, error){
					fns.ResolveConnectorSecrets, fns.RefreshConnectorSecrets,
				} {
					if _, err := resolve("eng", "github_echo_auth", spec.Secrets); err == nil {
						t.Fatal("deleted source supplied connector credentials")
					}
				}
				nextDir := t.TempDir()
				nextStop, err := srv.ServeTurnMCP(turn, nextDir)
				if err != nil {
					t.Fatal(err)
				}
				defer nextStop()
				if listToolNames(t, groupfolder.IpcSocket(nextDir))["github_echo_auth"] || discoveries.Load() != before {
					t.Fatal("deleted source allowed connector discovery")
				}
				if _, err := st.AddGitHubSource(store.GitHubSource{Source: "app/bot", OwnerFolder: "eng"}, "eng"); err != nil {
					t.Fatal(err)
				}
				beforeInstallations := installationRequests.Load()
				if _, err := fns.ResolveConnectorSecrets("eng", "github_echo_auth", spec.Secrets); err != nil {
					t.Fatal(err)
				}
				if installationRequests.Load() != beforeInstallations+2 {
					t.Fatal("source revocation retained cached connector token")
				}
			}
		})
	}
}
