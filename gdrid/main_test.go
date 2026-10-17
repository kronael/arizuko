package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// capRecorder stands in for the routd registration.
type capRecorder struct {
	mu    sync.Mutex
	calls int
	caps  map[string]bool
}

func (c *capRecorder) SetCaps(caps map[string]bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.caps = caps
	return nil
}

func (c *capRecorder) read() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.caps["send_text"]
}

var testPrivateKey = sync.OnceValue(func() string {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		panic(err)
	}
	return pemKey(der)
})

// pemKey wraps DER in a PKCS#8 PEM block. The banner is produced by
// encoding/pem rather than written out, so the pre-commit private-key
// detector does not fire on a key this file generates for itself.
func pemKey(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func keyJSON(t *testing.T, tokenURI string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"client_email":   "rhias@example.iam.gserviceaccount.com",
		"private_key":    testPrivateKey(),
		"private_key_id": "kid-1",
		"token_uri":      tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// fakeGoogle answers the token exchange and about.get. tokenStatus and
// aboutStatus are read per request so a test can repair the fake mid-run.
type fakeGoogle struct {
	srv         *httptest.Server
	tokenStatus atomic.Int64
	aboutStatus atomic.Int64
	tokenCalls  atomic.Int64
	aboutCalls  atomic.Int64

	mu         sync.Mutex
	assertion  map[string]any
	aboutToken string
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{}
	g.tokenStatus.Store(http.StatusOK)
	g.aboutStatus.Store(http.StatusOK)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		g.tokenCalls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		g.mu.Lock()
		g.assertion = jwtClaims(t, r.PostForm.Get("assertion"))
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if st := int(g.tokenStatus.Load()); st != http.StatusOK {
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"key disabled"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"ya29.fake","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("GET /drive/v3/about", func(w http.ResponseWriter, r *http.Request) {
		g.aboutCalls.Add(1)
		g.mu.Lock()
		g.aboutToken = r.Header.Get("Authorization")
		g.mu.Unlock()
		if got := r.URL.Query().Get("fields"); got != "user" {
			t.Errorf("about fields = %q, want user", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if st := int(g.aboutStatus.Load()); st != http.StatusOK {
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"SERVICE_DISABLED"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"user":{"emailAddress":"rhias@example.iam.gserviceaccount.com","permissionId":"123","me":true}}`))
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGoogle) config(t *testing.T) config {
	return config{KeyJSON: keyJSON(t, g.srv.URL+"/token"), APIBase: g.srv.URL}
}

func jwtClaims(t *testing.T, assertion string) map[string]any {
	t.Helper()
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Errorf("assertion %q is not a JWT", assertion)
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Errorf("assertion payload: %v", err)
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Errorf("assertion payload: %v", err)
	}
	return claims
}

func captureErrorLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// assertDegraded is the shape every refused credential must take: gdrid stays
// up, withdraws send_text from the registration that already ran, logs at
// ERROR, and says why on the dashboard.
func assertDegraded(t *testing.T, b *bot, rc *capRecorder, logs *bytes.Buffer, dashHas string) {
	t.Helper()
	if !b.isConnected() {
		t.Error("gdrid stopped serving over a credential that gates only itself")
	}
	calls, sendText := rc.read()
	if calls != 1 {
		t.Fatalf("SetCaps calls = %d, want 1: routd still routes send_text to gdrid", calls)
	}
	if sendText {
		t.Error("send_text was left advertised on a dead credential")
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("no ERROR logged for a dead credential: %q", logs.String())
	}
	var dash string
	for _, sec := range b.DashSections() {
		for _, row := range sec.Rows {
			dash += row[0] + "=" + row[1] + "\n"
		}
	}
	if !strings.Contains(dash, dashHas) {
		t.Errorf("dashboard %q lacks %q", dash, dashHas)
	}
}

// With no key gdrid can neither read nor reply. Returning an error from Start
// exits chanlib.Run, and a daemon that exits at boot cycles the whole compose
// project (BUGS L406, L419), so gdrid stays up and withdraws send_text.
func TestCredential_MissingKeyDegradesToIngress(t *testing.T) {
	logs := captureErrorLogs(t)
	b := newBot(t.Context(), config{})
	rc := &capRecorder{}

	verifyCredential(t.Context(), b, rc, time.Millisecond)

	assertDegraded(t, b, rc, logs, "GDRIVE_SA_KEY_JSON is not set")
}

func TestCredential_MalformedKeyDegradesToIngress(t *testing.T) {
	g := newFakeGoogle(t)
	cases := map[string]string{
		"not json":        "{not json",
		"wrong type":      `{"type":"authorized_user","client_email":"a@b","token_uri":"https://x/token","private_key":"x"}`,
		"no client_email": `{"type":"service_account","token_uri":"https://x/token","private_key":"x"}`,
		"no token_uri":    `{"type":"service_account","client_email":"a@b","private_key":"x"}`,
		"private_key not PEM": `{"type":"service_account","client_email":"a@b",` +
			`"token_uri":"https://x/token","private_key":"not a pem"}`,
		"private_key not a key": `{"type":"service_account","client_email":"a@b","token_uri":"https://x/token",` +
			`"private_key":` + strconv.Quote(pemKey([]byte("AAAA"))) + `}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			logs := captureErrorLogs(t)
			b := newBot(t.Context(), config{KeyJSON: raw, APIBase: g.srv.URL})
			rc := &capRecorder{}

			verifyCredential(t.Context(), b, rc, time.Millisecond)

			assertDegraded(t, b, rc, logs, "key will not parse")
			time.Sleep(20 * time.Millisecond)
			if calls, _ := rc.read(); calls != 1 {
				t.Errorf("SetCaps calls = %d, want 1: a key that will not parse never heals", calls)
			}
			if n := g.tokenCalls.Load() + g.aboutCalls.Load(); n != 0 {
				t.Errorf("Google was asked %d times about a key that never parsed", n)
			}
		})
	}
}

// Google accepting the exchange and refusing the Drive call is the measured
// state of a project whose Drive API is disabled (SERVICE_DISABLED), and it is
// as dead as a refused key.
func TestCredential_GoogleRefusalDegradesToIngress(t *testing.T) {
	for name, set := range map[string]func(*fakeGoogle){
		"token endpoint 401": func(g *fakeGoogle) { g.tokenStatus.Store(http.StatusUnauthorized) },
		"drive api disabled": func(g *fakeGoogle) { g.aboutStatus.Store(http.StatusForbidden) },
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGoogle(t)
			set(g)
			logs := captureErrorLogs(t)
			b := newBot(t.Context(), g.config(t))
			rc := &capRecorder{}

			verifyCredential(t.Context(), b, rc, time.Hour)

			assertDegraded(t, b, rc, logs, "google refused the key")
			if !b.cred.isRetryable() {
				t.Error("a refusal Google may revoke must stay retryable")
			}
		})
	}
}

// A refusal may heal (the Drive API gets enabled, a key is re-enabled), and
// nothing else in the process would ever ask again.
func TestCredential_RefusalRetriesAndRestoresSendText(t *testing.T) {
	g := newFakeGoogle(t)
	g.aboutStatus.Store(http.StatusForbidden)
	logs := captureErrorLogs(t)
	b := newBot(t.Context(), g.config(t))
	rc := &capRecorder{}

	verifyCredential(t.Context(), b, rc, time.Millisecond)

	assertDegraded(t, b, rc, logs, "google refused the key")
	g.aboutStatus.Store(http.StatusOK)
	deadline := time.Now().Add(10 * time.Second)
	for {
		calls, sendText := rc.read()
		if sendText {
			if calls != 2 {
				t.Errorf("SetCaps calls = %d, want 2: one withdrawal and one restore", calls)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("send_text never came back: SetCaps calls = %d", calls)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := b.cred.account(); got != "rhias@example.iam.gserviceaccount.com" {
		t.Errorf("account = %q", got)
	}
}

func TestCredential_UnreachableGoogleRetries(t *testing.T) {
	g := newFakeGoogle(t)
	cfg := g.config(t)
	g.srv.Close()
	logs := captureErrorLogs(t)
	b := newBot(t.Context(), cfg)
	rc := &capRecorder{}

	verifyCredential(t.Context(), b, rc, time.Hour)

	assertDegraded(t, b, rc, logs, "google token endpoint did not answer")
	if !b.cred.isRetryable() {
		t.Error("an unanswered Google is not a verdict on the key")
	}
}

func TestCredential_GoodKeyKeepsRegistration(t *testing.T) {
	g := newFakeGoogle(t)
	logs := captureErrorLogs(t)
	b := newBot(t.Context(), g.config(t))
	rc := &capRecorder{}

	verifyCredential(t.Context(), b, rc, time.Millisecond)

	if calls, _ := rc.read(); calls != 0 {
		t.Errorf("SetCaps calls = %d on a working key, want 0", calls)
	}
	if logs.Len() != 0 {
		t.Errorf("a working key logged at ERROR: %q", logs)
	}
	if b.cred.account() != "rhias@example.iam.gserviceaccount.com" {
		t.Errorf("account = %q", b.cred.account())
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.aboutToken != "Bearer ya29.fake" {
		t.Errorf("about.get Authorization = %q", g.aboutToken)
	}
	if g.assertion["iss"] != "rhias@example.iam.gserviceaccount.com" ||
		g.assertion["scope"] != driveScope {
		t.Errorf("assertion claims = %v", g.assertion)
	}
	if _, ok := g.assertion["sub"]; ok {
		t.Errorf("assertion carries sub %v without GDRIVE_IMPERSONATE", g.assertion["sub"])
	}
}

func TestCredential_ImpersonateSetsSubject(t *testing.T) {
	g := newFakeGoogle(t)
	cfg := g.config(t)
	cfg.Subject = "agent@example.org"
	b := newBot(t.Context(), cfg)

	if err := b.start(t.Context()); err != nil {
		t.Fatal(err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.assertion["sub"] != "agent@example.org" {
		t.Errorf("assertion sub = %v, want the delegation subject", g.assertion["sub"])
	}
}

// The downloaded key is JSON with \n escapes in private_key. godotenv expands
// \n inside a double-quoted value (parser.go), which turns those escapes into
// raw newlines and breaks the JSON string, so the .env line is single-quoted.
func TestCredential_KeySurvivesDotenvWhenSingleQuoted(t *testing.T) {
	raw := keyJSON(t, "https://oauth2.googleapis.com/token")
	if !strings.Contains(raw, `\n`) {
		t.Fatal("fixture key has no \\n escape; the test proves nothing")
	}
	dir := t.TempDir()
	read := func(line string) string {
		path := filepath.Join(dir, ".env")
		if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		env, err := godotenv.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		return env["GDRIVE_SA_KEY_JSON"]
	}

	single := read("GDRIVE_SA_KEY_JSON='" + raw + "'")
	if single != raw {
		t.Fatalf("single-quoted value changed: %q", single)
	}
	if _, err := parseKey(single, ""); err != nil {
		t.Errorf("single-quoted key does not parse: %v", err)
	}

	double := read(`GDRIVE_SA_KEY_JSON="` + strings.ReplaceAll(raw, `"`, `\"`) + `"`)
	if _, err := parseKey(double, ""); err == nil {
		t.Error("double-quoted key parsed; the landmine this test guards is gone, update INSTALL")
	}
}

// chanlib.MustEnv calls os.Exit(1) (BUGS L406, L419), which cycles the whole
// compose project. ROUTER_URL is the one variable gdrid cannot start without.
func TestConfig_NeverCallsMustEnv(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	mustEnv := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			lit, _ := call.Args[0].(*ast.BasicLit)
			name := ""
			if lit != nil {
				name = lit.Value
			}
			switch sel.Sel.Name {
			case "MustEnv":
				mustEnv[name] = true
			case "Exit", "Fatal", "Fatalf":
				t.Errorf("%s: %s exits the process; gdrid degrades instead", fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if len(mustEnv) != 1 || !mustEnv[`"ROUTER_URL"`] {
		t.Errorf("MustEnv is called for %v; only ROUTER_URL may exit the process", mustEnv)
	}
}
