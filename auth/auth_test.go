package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	argon2id "golang.org/x/crypto/argon2"
)

func sha256sum(b []byte) [32]byte { return sha256.Sum256(b) }

func hmacSHA256(key, msg []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return hex.EncodeToString(h.Sum(nil))
}

func hashArgon2(password string, salt []byte) string {
	derived := argon2id.IDKey([]byte(password), salt, 3, 65536, 4, 32)
	s := base64.RawStdEncoding.EncodeToString(salt)
	h := base64.RawStdEncoding.EncodeToString(derived)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=4$%s$%s", s, h)
}

var testSecret = []byte("test-secret-key-for-testing-only")

func TestJWTRoundTrip(t *testing.T) {
	token := mintJWT(testSecret, "user1", "Test User", nil, time.Hour)
	claims, err := VerifyJWT(testSecret, token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "user1" {
		t.Fatalf("got sub=%q, want user1", claims.Sub)
	}
	if claims.Name != "Test User" {
		t.Fatalf("got name=%q, want Test User", claims.Name)
	}
}

func TestJWTExpired(t *testing.T) {
	token := mintJWT(testSecret, "user1", "Test", nil, -time.Hour)
	_, err := VerifyJWT(testSecret, token)
	if err != ErrExpiredToken {
		t.Fatalf("got err=%v, want ErrExpiredToken", err)
	}
}

func TestJWTBadSignature(t *testing.T) {
	token := mintJWT(testSecret, "user1", "Test", nil, time.Hour)
	_, err := VerifyJWT([]byte("wrong"), token)
	if err != ErrInvalidToken {
		t.Fatalf("got err=%v, want ErrInvalidToken", err)
	}
}

func newTestServer(s *store.Store) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", handleLoginPage(&core.Config{}))
	mux.HandleFunc("POST /auth/login", handleLogin(s, testSecret, false))
	mux.HandleFunc("POST /auth/refresh",
		handleRefresh(s, testSecret, false))
	mux.HandleFunc("POST /auth/logout", handleLogout(s, false))
	return httptest.NewServer(mux)
}

func seedUser(t *testing.T, s *store.Store) {
	t.Helper()
	h := hashArgon2("password123", []byte("testsalt"))
	if err := s.CreateAuthUser(
		"local:admin", "admin", h, "Admin",
	); err != nil {
		t.Fatal(err)
	}
}

func extractCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginCycle(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedUser(t, s)

	srv := newTestServer(s)
	defer srv.Close()

	// GET login page
	resp, err := http.Get(srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("login page: got %d", resp.StatusCode)
	}

	// POST login with wrong password
	resp, err = http.PostForm(srv.URL+"/auth/login", url.Values{
		"username": {"admin"}, "password": {"wrong"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login: got %d, want 401", resp.StatusCode)
	}
}

// issueSession folds a linked sub onto its canonical at JWT mint time
// so downstream sees the canonical identity regardless of which
// provider the user authenticated with.
func TestIssueSessionResolvesLinkedToCanonical(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateAuthUser("google:alice", "alice", "", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSubToCanonical("github:alice2", "Alice GH", "google:alice"); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/auth/login", nil)
	issueSession(w, r, s, testSecret, "github:alice2", "Alice GH", false)

	body := w.Body.String()
	// Extract JWT from the inline localStorage script.
	i := strings.Index(body, `'jwt',"`)
	if i < 0 {
		t.Fatalf("no jwt in body: %s", body)
	}
	rest := body[i+len(`'jwt',"`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("malformed jwt envelope: %s", body)
	}
	tok := rest[:j]
	claims, err := VerifyJWT(testSecret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Sub != "google:alice" {
		t.Fatalf("got sub=%q, want google:alice (canonical)", claims.Sub)
	}
}

func TestFullLoginRefreshLogout(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedUser(t, s)

	srv := newTestServer(s)
	defer srv.Close()

	// no-redirect client to inspect responses
	cl := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 1. login with correct password
	resp, err := cl.PostForm(srv.URL+"/auth/login", url.Values{
		"username": {"admin"}, "password": {"password123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("login: got %d, want 200", resp.StatusCode)
	}
	rc := extractCookie(resp, cookieName)
	if rc == nil {
		t.Fatal("login: no refresh cookie")
	}

	// 2. refresh with that cookie
	req, _ := http.NewRequest("POST", srv.URL+"/auth/refresh", nil)
	req.AddCookie(rc)
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("refresh: got %d, want 200", resp.StatusCode)
	}
	rc2 := extractCookie(resp, cookieName)
	if rc2 == nil {
		t.Fatal("refresh: no new cookie")
	}
	if rc2.Value == rc.Value {
		t.Fatal("refresh: cookie should rotate")
	}

	// old refresh token should be deleted (replay fails)
	req, _ = http.NewRequest("POST", srv.URL+"/auth/refresh", nil)
	req.AddCookie(rc) // old cookie
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay: got %d, want 401", resp.StatusCode)
	}

	// 3. logout with new cookie
	req, _ = http.NewRequest("POST", srv.URL+"/auth/logout", nil)
	req.AddCookie(rc2)
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: got %d, want 303", resp.StatusCode)
	}
	cleared := extractCookie(resp, cookieName)
	if cleared == nil || cleared.MaxAge != -1 {
		t.Fatal("logout: cookie not cleared")
	}

	// refresh with logged-out cookie should fail
	req, _ = http.NewRequest("POST", srv.URL+"/auth/refresh", nil)
	req.AddCookie(rc2)
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout refresh: got %d, want 401",
			resp.StatusCode)
	}
}

func TestOAuthStateExpired(t *testing.T) {
	// create state with timestamp 11 minutes in the past
	ts := fmt.Sprintf("%d", time.Now().Add(-11*time.Minute).Unix())
	mac := hmacSHA256(testSecret, []byte(ts))
	state := ts + "." + mac

	r := httptest.NewRequest(
		"GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if _, ok := verifyState(testSecret, r); ok {
		t.Fatal("expired state should not verify")
	}
}

func TestSplitArgon2EdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no dollars", "argon2id"},
		{"partial", "$argon2id$v=19$m=65536,t=3,p=4$salt"},
		{"wrong algo", "$bcrypt$v=19$m=65536,t=3,p=4$s$h"},
		{"no hash part", "$argon2id$v=19$m=65536,t=3,p=4$saltonly"},
	}
	for _, tc := range cases {
		if p := splitArgon2(tc.input); p != nil {
			t.Errorf("%s: expected nil, got %+v", tc.name, p)
		}
	}
}

func TestRefreshExpiredSession(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedUser(t, s)

	// create session with past expiry
	token := "expired-refresh-token"
	h := HashToken(token)
	past := time.Now().Add(-time.Hour)
	if err := s.CreateAuthSession(h, "local:admin", past); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(s)
	defer srv.Close()

	cl := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session: got %d, want 401",
			resp.StatusCode)
	}
}

func TestLogoutWithoutCookie(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	srv := newTestServer(s)
	defer srv.Close()

	cl := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/auth/logout", nil)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout no cookie: got %d, want 303",
			resp.StatusCode)
	}
	cleared := extractCookie(resp, cookieName)
	if cleared == nil || cleared.MaxAge != -1 {
		t.Fatal("logout: cookie not cleared")
	}
}

func TestVerifyArgon2(t *testing.T) {
	hash := hashArgon2("password123", []byte("somesalt8"))
	if !verifyArgon2(hash, "password123") {
		t.Fatal("expected valid password to verify")
	}
	if verifyArgon2(hash, "wrongpassword") {
		t.Fatal("expected wrong password to fail")
	}
}

func TestOAuthStateCookie(t *testing.T) {
	state := signState(testSecret)
	if !strings.Contains(state, ".") {
		t.Fatal("state should contain timestamp.signature")
	}
	// simulate verification
	r := httptest.NewRequest("GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if _, ok := verifyState(testSecret, r); !ok {
		t.Fatal("valid state should verify")
	}
}

func TestTelegramWidgetVerify(t *testing.T) {
	botToken := "123456:ABC-DEF"
	authDate := fmt.Sprintf("%d", time.Now().Unix())
	form := url.Values{
		"id":         {"12345"},
		"first_name": {"Test"},
		"auth_date":  {authDate},
	}
	// compute valid hash
	check := "auth_date=" + authDate + "\nfirst_name=Test\nid=12345"
	secret := sha256sum([]byte(botToken))
	h := hmacSHA256(secret[:], []byte(check))
	form.Set("hash", h)

	if !verifyTelegramWidget(form, botToken) {
		t.Fatal("valid telegram widget should verify")
	}
	// stale auth_date should fail
	staleForm := url.Values{
		"id":         {"12345"},
		"first_name": {"Test"},
		"auth_date":  {"1234567890"},
	}
	staleCheck := "auth_date=1234567890\nfirst_name=Test\nid=12345"
	staleH := hmacSHA256(secret[:], []byte(staleCheck))
	staleForm.Set("hash", staleH)
	if verifyTelegramWidget(staleForm, botToken) {
		t.Fatal("stale auth_date should fail")
	}
	form.Set("hash", "invalid")
	if verifyTelegramWidget(form, botToken) {
		t.Fatal("invalid hash should fail")
	}
}

// --- Policy tests ---

func TestAuthorizeBasicTools(t *testing.T) {
	// list_tasks is unconditionally allowed.
	id := Resolve("world/parent/child")
	if err := Authorize(id, "list_tasks", AuthzTarget{}); err != nil {
		t.Errorf("list_tasks should be allowed for all tiers: %v", err)
	}
}

func TestAuthorizeOutboundSubtree(t *testing.T) {
	// Subtree containment is the only rule. No tier bypass — every
	// agent is confined to JIDs that route to its folder or descendants.
	rhias := Resolve("rhias")
	if err := Authorize(rhias, "send", AuthzTarget{TargetFolder: "rhias"}); err != nil {
		t.Errorf("rhias send to rhias should allow: %v", err)
	}
	if err := Authorize(rhias, "send", AuthzTarget{TargetFolder: "rhias/content"}); err != nil {
		t.Errorf("rhias send to rhias/content should allow: %v", err)
	}
	// Cross-world deny.
	happy := Resolve("happy")
	if err := Authorize(happy, "send", AuthzTarget{TargetFolder: "rhias/content"}); err == nil {
		t.Error("happy send to rhias/content must deny")
	}
	// Unrouted JID: denied for every caller (no one notionally owns it).
	if err := Authorize(rhias, "send", AuthzTarget{TargetFolder: ""}); err == nil {
		t.Error("send to unrouted JID must deny")
	}
	// Even root cannot direct-send cross-world; delegate_group is the
	// inter-world mechanism.
	root := Resolve("root")
	if err := Authorize(root, "send", AuthzTarget{TargetFolder: "happy"}); err == nil {
		t.Error("root direct-send cross-world must deny (use delegate_group)")
	}
	// Other outbound verbs follow same rule.
	for _, tool := range []string{"send_file", "reply", "post", "like", "dislike",
		"delete", "edit", "forward", "quote", "repost"} {
		if err := Authorize(happy, tool, AuthzTarget{TargetFolder: "rhias"}); err == nil {
			t.Errorf("%s cross-world must deny", tool)
		}
		if err := Authorize(rhias, tool, AuthzTarget{TargetFolder: "rhias/x"}); err != nil {
			t.Errorf("%s within subtree must allow: %v", tool, err)
		}
	}
}

func TestAuthorizeResetSession(t *testing.T) {
	id := Resolve("w/a")
	if err := Authorize(id, "reset_session", AuthzTarget{TargetFolder: "w/a"}); err != nil {
		t.Fatalf("self reset should work: %v", err)
	}
	if err := Authorize(id, "reset_session", AuthzTarget{TargetFolder: "w/a/b"}); err != nil {
		t.Fatalf("child reset should work: %v", err)
	}
	if err := Authorize(id, "reset_session", AuthzTarget{TargetFolder: "w/x"}); err == nil {
		t.Fatal("non-descendant reset should fail")
	}
}

func TestAuthorizeInjectMessage(t *testing.T) {
	if err := Authorize(Resolve("w"), "inject_message", AuthzTarget{}); err != nil {
		t.Fatal("tier 0 should inject")
	}
	if err := Authorize(Resolve("w/a"), "inject_message", AuthzTarget{}); err != nil {
		t.Fatal("tier 1 should inject")
	}
	if err := Authorize(Resolve("w/a/b"), "inject_message", AuthzTarget{}); err == nil {
		t.Fatal("tier 2 should not inject")
	}
}

func TestAuthorizeRegisterGroup(t *testing.T) {
	if err := Authorize(Resolve("w"), "register_group", AuthzTarget{TargetFolder: "w"}); err == nil {
		t.Fatal("tier 0 should not register worlds")
	}
	if err := Authorize(Resolve("w"), "register_group", AuthzTarget{TargetFolder: "w/child"}); err != nil {
		t.Fatalf("tier 0 should register children: %v", err)
	}
	if err := Authorize(Resolve("w/a"), "register_group", AuthzTarget{TargetFolder: "w/a/child"}); err != nil {
		t.Fatalf("tier 1 should register direct children: %v", err)
	}
	if err := Authorize(Resolve("w/a"), "register_group", AuthzTarget{TargetFolder: "w/b/child"}); err == nil {
		t.Fatal("tier 1 should not register outside own subtree")
	}
	if err := Authorize(Resolve("w/a/b"), "register_group", AuthzTarget{}); err == nil {
		t.Fatal("tier 2 should not register groups")
	}
}

func TestAuthorizeEscalateGroup(t *testing.T) {
	if err := Authorize(Resolve("w/a/b"), "escalate_group", AuthzTarget{}); err != nil {
		t.Fatal("tier 2 should escalate")
	}
	if err := Authorize(Resolve("w/a"), "escalate_group", AuthzTarget{}); err == nil {
		t.Fatal("tier 1 should not escalate")
	}
}

func TestAuthorizeDelegateGroup(t *testing.T) {
	id := Resolve("w/a")
	if err := Authorize(id, "delegate_group", AuthzTarget{TargetFolder: "w/a/child"}); err != nil {
		t.Fatalf("should delegate to child: %v", err)
	}
	if err := Authorize(id, "delegate_group", AuthzTarget{TargetFolder: "w/b"}); err == nil {
		t.Fatal("should not delegate to non-child")
	}
	if err := Authorize(Resolve("w/a/b/c"), "delegate_group", AuthzTarget{}); err == nil {
		t.Fatal("tier 3 should not delegate")
	}
}

func TestAuthorizeRouteTools(t *testing.T) {
	for _, tool := range []string{"get_routes", "set_routes", "add_route", "delete_route"} {
		if err := Authorize(Resolve("w"), tool, AuthzTarget{}); err != nil {
			t.Errorf("%s should work at tier 0: %v", tool, err)
		}
		if err := Authorize(Resolve("w/a/b"), tool, AuthzTarget{}); err == nil {
			t.Errorf("%s should fail at tier 2", tool)
		}
	}
}

func TestAuthorizeScheduleTask(t *testing.T) {
	if err := Authorize(Resolve("w"), "schedule_task", AuthzTarget{TaskOwner: "w/a"}); err != nil {
		t.Fatal("tier 0 should schedule any task")
	}
	if err := Authorize(Resolve("w/a"), "schedule_task", AuthzTarget{TaskOwner: "w/a"}); err != nil {
		t.Fatal("tier 1 same world should schedule")
	}
	if err := Authorize(Resolve("w/a"), "schedule_task", AuthzTarget{TaskOwner: "x/b"}); err == nil {
		t.Fatal("tier 1 different world should fail")
	}
	if err := Authorize(Resolve("w/a/b"), "schedule_task", AuthzTarget{TaskOwner: "w/a/b"}); err != nil {
		t.Fatal("tier 2 own task should schedule")
	}
	if err := Authorize(Resolve("w/a/b"), "schedule_task", AuthzTarget{TaskOwner: "w/a"}); err == nil {
		t.Fatal("tier 2 other's task should fail")
	}
	if err := Authorize(Resolve("w/a/b/c"), "schedule_task", AuthzTarget{}); err == nil {
		t.Fatal("tier 3 should not schedule")
	}
}

func TestAuthorizeTaskOps(t *testing.T) {
	for _, tool := range []string{"pause_task", "resume_task", "cancel_task"} {
		if err := Authorize(Resolve("w"), tool, AuthzTarget{TaskOwner: "w/a"}); err != nil {
			t.Errorf("%s tier 0 should work: %v", tool, err)
		}
		if err := Authorize(Resolve("w/a/b/c"), tool, AuthzTarget{}); err == nil {
			t.Errorf("%s tier 3 should fail", tool)
		}
	}
}

func TestIdentityResolve(t *testing.T) {
	tests := []struct {
		folder string
		tier   int
		world  string
	}{
		{"main", 0, "main"},
		{"world/parent", 1, "world"},
		{"world/parent/child", 2, "world"},
		{"world/a/b/c", 3, "world"},
		{"world/a/b/c/d", 3, "world"},
	}
	for _, tc := range tests {
		id := Resolve(tc.folder)
		if id.Tier != tc.tier {
			t.Errorf("%s: tier got %d, want %d", tc.folder, id.Tier, tc.tier)
		}
		if id.World != tc.world {
			t.Errorf("%s: world got %q, want %q", tc.folder, id.World, tc.world)
		}
	}
}
