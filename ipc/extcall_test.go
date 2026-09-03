package ipc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// CallExtTool is how a connector's REST descriptor becomes an agent tool. It
// had no test: neither the five auth wirings, nor the 401 refresh retry, nor
// scrubSecrets -- the one thing standing between a live API key and the agent's
// transcript.

type extRequest struct {
	method  string
	path    string
	query   string
	headers http.Header
	body    map[string]any
}

// extServer records one request and answers with the caller's status + body.
func extServer(t *testing.T, got *extRequest, status *atomic.Int32, reply string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.RawQuery
		got.headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		got.body = map[string]any{}
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(int(status.Load()))
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(s.Close)
	return s
}

func status200() *atomic.Int32 {
	var s atomic.Int32
	s.Store(http.StatusOK)
	return &s
}

func resultText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if r == nil {
		t.Fatal("nil CallToolResult")
	}
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Each auth method must put the credential in exactly one place. A credential
// that also lands somewhere else is a second copy in a second log.
func TestCallExtToolRendersCredentialsIntoTheDeclaredSinks(t *testing.T) {
	const secret = "sk-live-abcdef"
	cases := []struct {
		name    string
		tool    ExtTool
		secrets map[string]string // nil → the single "TOK" default
		check   func(*testing.T, *extRequest)
	}{
		{
			name: "a bearer is a literal prefix, not an enum value",
			tool: ExtTool{Method: "GET", Path: "/v1/me", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}},
			check: func(t *testing.T, r *extRequest) {
				if got := r.headers.Get("Authorization"); got != "Bearer "+secret {
					t.Errorf("Authorization = %q", got)
				}
				if strings.Contains(r.query, secret) {
					t.Errorf("the bearer also reached the query string: %q", r.query)
				}
			},
		},
		{
			name: "credential in a named header",
			tool: ExtTool{Method: "GET", Path: "/v1/me", Secrets: []string{"TOK"}, Headers: map[string]string{"X-Api-Key": "{secret:TOK}"}},
			check: func(t *testing.T, r *extRequest) {
				if got := r.headers.Get("X-Api-Key"); got != secret {
					t.Errorf("X-Api-Key = %q", got)
				}
				if r.headers.Get("Authorization") != "" {
					t.Error("a named-header credential also set Authorization")
				}
			},
		},
		{
			name: "credential in a query param",
			tool: ExtTool{Method: "GET", Path: "/v1/me", Secrets: []string{"TOK"}, Query: map[string]string{"api_key": "{secret:TOK}"}},
			check: func(t *testing.T, r *extRequest) {
				if !strings.Contains(r.query, "api_key="+secret) {
					t.Errorf("query = %q, want api_key=%s", r.query, secret)
				}
				if r.headers.Get("Authorization") != "" {
					t.Error("a query credential also set Authorization")
				}
			},
		},
		{
			name: "any literal prefix, no enum needed",
			tool: ExtTool{Method: "GET", Path: "/v1/me", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Basic {secret:TOK}"}},
			check: func(t *testing.T, r *extRequest) {
				want := "Basic " + secret
				if got := r.headers.Get("Authorization"); got != want {
					t.Errorf("Authorization = %q, want %q", got, want)
				}
				if strings.Contains(r.query, secret) {
					t.Errorf("the basic credential reached the query string: %q", r.query)
				}
			},
		},
		{
			name: "credential in the JSON body",
			tool: ExtTool{Method: "POST", Path: "/v1/me",
				Secrets: []string{"TOK"}, Body: map[string]string{"api_key": "{secret:TOK}"}},
			check: func(t *testing.T, r *extRequest) {
				if r.body["api_key"] != secret {
					t.Errorf("body = %#v, want api_key in it", r.body)
				}
				if r.headers.Get("Authorization") != "" || strings.Contains(r.query, secret) {
					t.Error("a body credential also set a header or a query param")
				}
			},
		},
		{
			// The ceiling that killed the old shape: `[ext.auth]` had exactly
			// secret/secret2 and header/header2, so porkbun's two credentials
			// were the most it could ever express. A map has no such number.
			name: "more credentials than the old two-slot ceiling allowed",
			tool: ExtTool{Method: "POST", Path: "/v1/me",
				Secrets: []string{"TOK", "TOK2", "TOK3"},
				Body: map[string]string{
					"a": "{secret:TOK}", "b": "{secret:TOK2}", "c": "{secret:TOK3}",
				}},
			secrets: map[string]string{"TOK": secret, "TOK2": "two", "TOK3": "three"},
			check: func(t *testing.T, r *extRequest) {
				for k, want := range map[string]string{"a": secret, "b": "two", "c": "three"} {
					if r.body[k] != want {
						t.Errorf("body[%q] = %v, want %q", k, r.body[k], want)
					}
				}
			},
		},
		{
			// A declared key the broker resolved to nothing must not travel as
			// the literal placeholder — that would send "{secret:MISSING}"
			// upstream as if it were a credential.
			name: "an unresolved placeholder is dropped, never sent literally",
			tool: ExtTool{Method: "GET", Path: "/v1/me",
				Secrets: []string{"MISSING"},
				Headers: map[string]string{"X-Key": "{secret:MISSING}"},
				Query:   map[string]string{"k": "{secret:MISSING}"}},
			secrets: map[string]string{},
			check: func(t *testing.T, r *extRequest) {
				if got := r.headers.Get("X-Key"); got != "" {
					t.Errorf("X-Key = %q, want the header dropped", got)
				}
				if strings.Contains(r.query, "secret:") {
					t.Errorf("query carries a raw placeholder: %q", r.query)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got extRequest
			srv := extServer(t, &got, status200(), `{"ok":true}`)
			c.tool.BaseURL = srv.URL

			sec := c.secrets
			if sec == nil {
				sec = map[string]string{"TOK": secret}
			}
			res, err := CallExtTool(context.Background(), c.tool, nil, sec, nil)
			if err != nil {
				t.Fatalf("CallExtTool: %v", err)
			}
			if res.IsError {
				t.Fatalf("call failed: %s", resultText(t, res))
			}
			c.check(t, &got)
		})
	}
}

// A path parameter is consumed by the path. Leaving it in `remaining` would
// also send it as a query param or a body field — the same id twice, and for a
// mutating call a field the API did not ask for.
func TestCallExtToolConsumesPathParams(t *testing.T) {
	var got extRequest
	srv := extServer(t, &got, status200(), `{}`)

	tool := ExtTool{
		BaseURL: srv.URL, Method: "GET", Path: "/repos/{owner}/{repo}/issues",
		Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"},
	}
	_, err := CallExtTool(context.Background(), tool,
		map[string]any{"owner": "kronael", "repo": "arizuko", "state": "open"},
		map[string]string{"TOK": "t"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/repos/kronael/arizuko/issues" {
		t.Errorf("path = %q, want the params rendered into it", got.path)
	}
	if strings.Contains(got.query, "owner=") || strings.Contains(got.query, "repo=") {
		t.Errorf("a consumed path param was also sent as a query param: %q", got.query)
	}
	if !strings.Contains(got.query, "state=open") {
		t.Errorf("query = %q, want the unconsumed arg", got.query)
	}
}

// An unfilled placeholder must stay literal rather than collapse the path. A
// silently shortened path can hit a different, broader endpoint.
func TestCallExtToolLeavesAnUnfilledPathParamLiteral(t *testing.T) {
	var got extRequest
	srv := extServer(t, &got, status200(), `{}`)
	tool := ExtTool{BaseURL: srv.URL, Method: "GET", Path: "/repos/{owner}/issues", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}

	if _, err := CallExtTool(context.Background(), tool, nil, map[string]string{"TOK": "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if got.path != "/repos/{owner}/issues" {
		t.Errorf("path = %q, want the placeholder left in place", got.path)
	}
}

func TestCallExtToolPutsArgsInTheQueryOrTheBodyByMethod(t *testing.T) {
	for _, method := range []string{"GET", "DELETE", "HEAD"} {
		var got extRequest
		srv := extServer(t, &got, status200(), `{}`)
		tool := ExtTool{BaseURL: srv.URL, Method: method, Path: "/x", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}
		if _, err := CallExtTool(context.Background(), tool,
			map[string]any{"q": "hello world"}, map[string]string{"TOK": "t"}, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.query, "q=hello+world") {
			t.Errorf("%s: query = %q, want the arg url-encoded", method, got.query)
		}
		if len(got.body) != 0 {
			t.Errorf("%s: sent a body: %#v", method, got.body)
		}
	}

	for _, method := range []string{"POST", "PUT", "PATCH"} {
		var got extRequest
		srv := extServer(t, &got, status200(), `{}`)
		tool := ExtTool{BaseURL: srv.URL, Method: method, Path: "/x", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}
		if _, err := CallExtTool(context.Background(), tool,
			map[string]any{"title": "hi", "n": 3}, map[string]string{"TOK": "t"}, nil); err != nil {
			t.Fatal(err)
		}
		if got.body["title"] != "hi" || got.body["n"] != float64(3) {
			t.Errorf("%s: body = %#v, want the args as JSON", method, got.body)
		}
		if got.headers.Get("Content-Type") != "application/json" {
			t.Errorf("%s: Content-Type = %q", method, got.headers.Get("Content-Type"))
		}
		if got.query != "" {
			t.Errorf("%s: args also went to the query: %q", method, got.query)
		}
	}
}

// Secret scrubbing.

// The response body goes straight into the agent's transcript, and a provider
// that echoes the credential in an error is common. Every secret value must be
// replaced before the text is returned.
func TestCallExtToolScrubsSecretsFromTheResponse(t *testing.T) {
	const primary, secondary = "sk-live-PRIMARY", "sk-live-SECOND"
	var got extRequest
	st := status200()
	srv := extServer(t, &got, st,
		`{"error":"bad key sk-live-PRIMARY","hint":"you sent sk-live-SECOND too"}`)

	tool := ExtTool{BaseURL: srv.URL, Method: "GET", Path: "/x", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}
	secrets := map[string]string{"TOK": primary, "TOK2": secondary, "EMPTY": ""}

	res, err := CallExtTool(context.Background(), tool, nil, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	for _, s := range []string{primary, secondary} {
		if strings.Contains(text, s) {
			t.Errorf("a secret survived into the agent's result: %s", text)
		}
	}
	if !strings.Contains(text, "«redacted»") {
		t.Errorf("nothing was redacted: %s", text)
	}

	// And on the error path, where the body is wrapped in "HTTP %d: %s".
	st.Store(http.StatusBadRequest)
	res, err = CallExtTool(context.Background(), tool, nil, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("a 400 did not produce an error result")
	}
	if strings.Contains(resultText(t, res), primary) {
		t.Errorf("a secret survived into the error result: %s", resultText(t, res))
	}
}

// An empty secret must not turn every empty string in the response into
// «redacted» — strings.ReplaceAll with "" would rewrite the whole body.
func TestScrubSecretsIgnoresEmptyValues(t *testing.T) {
	got := scrubSecrets("hello world", map[string]string{"A": "", "B": "world"})
	if got != "hello «redacted»" {
		t.Errorf("scrubSecrets = %q, want only the non-empty secret replaced", got)
	}
	if scrubSecrets("hello", map[string]string{"A": ""}) != "hello" {
		t.Error("an empty secret rewrote the text")
	}
}

// A transport error's message embeds the full request URL, and for
// apikey-query that URL carries the credential in its query string.
func TestCallExtToolScrubsTheSecretFromATransportError(t *testing.T) {
	const secret = "sk-live-INURL"
	// A server that is closed before the call: the dial fails and the error
	// text carries the URL.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	tool := ExtTool{BaseURL: url, Method: "GET", Path: "/x",
		Secrets: []string{"TOK"}, Query: map[string]string{"api_key": "{secret:TOK}"}}

	res, err := CallExtTool(context.Background(), tool, nil, map[string]string{"TOK": secret}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a dial failure did not produce an error result")
	}
	if strings.Contains(resultText(t, res), secret) {
		t.Errorf("the api key leaked through the transport error: %s", resultText(t, res))
	}
}

// The 401 refresh retry (spec 5/15).

func TestCallExtToolRetriesOnceWithRefreshedCredentials(t *testing.T) {
	const stale, fresh = "stale-token", "fresh-token"
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, tok)
		if tok != fresh {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"expired"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	var refreshes atomic.Int32
	tool := ExtTool{BaseURL: srv.URL, Method: "GET", Path: "/x", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}
	res, err := CallExtTool(context.Background(), tool, nil, map[string]string{"TOK": stale},
		func(context.Context) (map[string]string, error) {
			refreshes.Add(1)
			return map[string]string{"TOK": fresh}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("the retry did not succeed: %s", resultText(t, res))
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("refresh called %d times, want exactly 1", n)
	}
	if len(seen) != 2 || seen[0] != stale || seen[1] != fresh {
		t.Errorf("requests carried %v, want [stale fresh]", seen)
	}
}

// A pasted PAT has nothing to refresh, so the provider hands back what it was
// given. Re-sending it would spend a second 401 for nothing.
func TestCallExtToolSkipsTheRetryWhenNothingWasRenewed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad token"}`)
	}))
	t.Cleanup(srv.Close)

	secrets := map[string]string{"TOK": "pat-unchanged"}
	tool := ExtTool{BaseURL: srv.URL, Method: "GET", Path: "/x", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}

	res, err := CallExtTool(context.Background(), tool, nil, secrets,
		func(context.Context) (map[string]string, error) {
			return map[string]string{"TOK": "pat-unchanged"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("a surviving 401 must reach the agent as an error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the provider was called %d times, want 1 — nothing was renewed", n)
	}
}

// A 401 that survives the refresh is not transient. It must surface, not loop.
func TestCallExtToolStopsAfterOneRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"still bad"}`)
	}))
	t.Cleanup(srv.Close)

	var refreshes atomic.Int32
	tool := ExtTool{BaseURL: srv.URL, Method: "GET", Path: "/x", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}
	res, err := CallExtTool(context.Background(), tool, nil, map[string]string{"TOK": "a"},
		func(context.Context) (map[string]string, error) {
			n := refreshes.Add(1)
			return map[string]string{"TOK": "b" + strings.Repeat("!", int(n))}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "401") {
		t.Errorf("want a surfaced 401, got %q (isError=%v)", resultText(t, res), res.IsError)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("the provider was called %d times, want exactly 2 (attempt + one retry)", n)
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("refresh ran %d times, want exactly 1", n)
	}
}

// Most connectors carry a static credential and pass no refresh at all.
func TestCallExtToolSurfaces401WithNoRefresher(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	tool := ExtTool{BaseURL: srv.URL, Method: "GET", Path: "/x", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}
	res, err := CallExtTool(context.Background(), tool, nil, map[string]string{"TOK": "a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("a 401 with no refresher must be an error result")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the provider was called %d times, want 1", n)
	}
}

// A refresh that itself fails leaves the original 401 standing rather than
// replacing it with a confusing credential-store error.
func TestCallExtToolKeepsTheOriginal401WhenRefreshFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"expired"}`)
	}))
	t.Cleanup(srv.Close)

	tool := ExtTool{BaseURL: srv.URL, Method: "GET", Path: "/x", Secrets: []string{"TOK"}, Headers: map[string]string{"Authorization": "Bearer {secret:TOK}"}}
	res, err := CallExtTool(context.Background(), tool, nil, map[string]string{"TOK": "a"},
		func(context.Context) (map[string]string, error) {
			return nil, io.ErrUnexpectedEOF
		})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "401") {
		t.Errorf("want the provider's 401 surfaced, got %q", resultText(t, res))
	}
}
