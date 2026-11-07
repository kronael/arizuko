package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPassthrough(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.RequestURI() != "/v4/spreadsheets/id?fields=x" || r.Header.Get("Authorization") != "Bearer google" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("upstream request: %s %s %v", r.Method, r.URL.RequestURI(), r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"x":1}` {
			t.Errorf("body: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	b := &bot{cred: &credential{sheetsAPIBase: upstream.URL, sheetsHTTP: upstream.Client(), sheetsAPI: apiState{state: "on"}}}
	// Simulate the OAuth transport without contacting Google.
	b.cred.sheetsHTTP.Transport = tokenTransport{base: http.DefaultTransport}
	for _, tc := range []struct {
		token, auth, state string
		want               int
	}{
		{"", "Bearer ", "on", 401}, {"secret", "", "on", 401}, {"secret", "Bearer wrong", "on", 401},
		{"secret", "Bearer secret", "off", 404}, {"secret", "Bearer secret", "", 404}, {"secret", "Bearer secret", "on", 202},
	} {
		b.cred.sheetsAPI.state = tc.state
		req := httptest.NewRequest("POST", "/google/sheets/v4/spreadsheets/id?fields=x", strings.NewReader(`{"x":1}`))
		req.Header.Set("Authorization", tc.auth)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		b.passthrough(tc.token).ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("token=%q auth=%q state=%q: %d", tc.token, tc.auth, tc.state, w.Code)
		}
		if tc.want == 202 && (w.Body.String() != `{"ok":true}` || w.Header().Get("Content-Type") != "application/json") {
			t.Errorf("response: %v %s", w.Header(), w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("upstream calls: %d", calls)
	}
}

type tokenTransport struct{ base http.RoundTripper }

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer google")
	return t.base.RoundTrip(r)
}

func TestPassthroughConstantTime(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "passthrough.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "subtle" && sel.Sel.Name == "ConstantTimeCompare" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("token comparison must use crypto/subtle.ConstantTimeCompare")
	}
}

func TestSheetsHostDefault(t *testing.T) {
	t.Setenv("ROUTER_URL", "http://router")
	t.Setenv("SHEETS_API_URL", "")
	if got := loadConfig().SheetsAPIBase; got != "https://sheets.googleapis.com" {
		t.Fatalf("Sheets host: %q", got)
	}
}

func TestDrivePassthrough(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.EscapedPath() != "/drive/v3/files/a%2Fb" || r.Header.Get("Authorization") != "Bearer google" {
			t.Errorf("upstream: %s %v", r.URL.EscapedPath(), r.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	client := upstream.Client()
	client.Transport = tokenTransport{base: http.DefaultTransport}
	b := &bot{cred: &credential{apiBase: upstream.URL, http: client, driveAPI: apiState{state: "on"}}}
	for _, tc := range []struct {
		path, state string
		want        int
	}{
		{"/google/drive/drive/v3/files/a%2Fb", "on", http.StatusNoContent},
		{"/google/drive/drive/v3/files/a%2Fb", "off", http.StatusNotFound},
		{"/google/gmail/v1/messages", "on", http.StatusNotFound},
	} {
		b.cred.driveAPI.state = tc.state
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		b.passthrough("secret").ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.path, w.Code, tc.want)
		}
	}
	if calls != 1 {
		t.Fatalf("upstream calls: %d", calls)
	}
}
