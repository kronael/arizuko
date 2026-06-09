package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func sha256sum(b []byte) [32]byte { return sha256.Sum256(b) }

func hmacSHA256(key, msg []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return hex.EncodeToString(h.Sum(nil))
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

func TestOAuthStateExpired(t *testing.T) {
	// create state with timestamp 11 minutes in the past
	ts := fmt.Sprintf("%d", time.Now().Add(-11*time.Minute).Unix())
	mac := hmacSHA256(testSecret, []byte(ts))
	state := ts + "." + mac

	r := httptest.NewRequest(
		"GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if _, ok := VerifyState(testSecret, r); ok {
		t.Fatal("expired state should not verify")
	}
}

func TestOAuthStateCookie(t *testing.T) {
	state := SignState(testSecret, StateIntent{})
	if !strings.Contains(state, ".") {
		t.Fatal("state should contain timestamp.signature")
	}
	// simulate verification
	r := httptest.NewRequest("GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if _, ok := VerifyState(testSecret, r); !ok {
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

	if !VerifyTelegramWidget(form, botToken) {
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
	if VerifyTelegramWidget(staleForm, botToken) {
		t.Fatal("stale auth_date should fail")
	}
	form.Set("hash", "invalid")
	if VerifyTelegramWidget(form, botToken) {
		t.Fatal("invalid hash should fail")
	}
}

// --- Policy tests ---

func TestAuthorizeBasicTools(t *testing.T) {
	// list_tasks is unconditionally allowed.
	id := Resolve("world/parent/child")
	if err := AuthorizeStructural(id, "list_tasks", AuthzTarget{}); err != nil {
		t.Errorf("list_tasks should be allowed for all tiers: %v", err)
	}
}

func TestAuthorizeOutboundSubtree(t *testing.T) {
	// Subtree containment is the only rule. No tier bypass — every
	// agent is confined to JIDs that route to its folder or descendants.
	rhias := Resolve("rhias")
	if err := AuthorizeStructural(rhias, "send", AuthzTarget{TargetFolder: "rhias"}); err != nil {
		t.Errorf("rhias send to rhias should allow: %v", err)
	}
	if err := AuthorizeStructural(rhias, "send", AuthzTarget{TargetFolder: "rhias/content"}); err != nil {
		t.Errorf("rhias send to rhias/content should allow: %v", err)
	}
	// Cross-world deny.
	happy := Resolve("happy")
	if err := AuthorizeStructural(happy, "send", AuthzTarget{TargetFolder: "rhias/content"}); err == nil {
		t.Error("happy send to rhias/content must deny")
	}
	// Unrouted JID: denied for every caller (no one notionally owns it).
	if err := AuthorizeStructural(rhias, "send", AuthzTarget{TargetFolder: ""}); err == nil {
		t.Error("send to unrouted JID must deny")
	}
	// Even root cannot direct-send cross-world; delegate_group is the
	// inter-world mechanism.
	root := Resolve("root")
	if err := AuthorizeStructural(root, "send", AuthzTarget{TargetFolder: "happy"}); err == nil {
		t.Error("root direct-send cross-world must deny (use delegate_group)")
	}
	// Other outbound verbs follow same rule.
	for _, tool := range []string{"send_file", "reply", "post", "like", "dislike",
		"delete", "edit", "forward", "quote", "repost",
		"pin_message", "unpin_message", "unpin_all",
		"pane_set_prompts", "pane_set_title"} {
		if err := AuthorizeStructural(happy, tool, AuthzTarget{TargetFolder: "rhias"}); err == nil {
			t.Errorf("%s cross-world must deny", tool)
		}
		if err := AuthorizeStructural(rhias, tool, AuthzTarget{TargetFolder: "rhias/x"}); err != nil {
			t.Errorf("%s within subtree must allow: %v", tool, err)
		}
	}
}

func TestAuthorizeInspectTasks(t *testing.T) {
	id := Resolve("w/a")
	if err := AuthorizeStructural(id, "inspect_tasks", AuthzTarget{TaskOwner: "w/a"}); err != nil {
		t.Fatalf("inspect own task should allow: %v", err)
	}
	if err := AuthorizeStructural(id, "inspect_tasks", AuthzTarget{TaskOwner: "w/a/b"}); err != nil {
		t.Fatalf("inspect descendant task should allow: %v", err)
	}
	if err := AuthorizeStructural(id, "inspect_tasks", AuthzTarget{TaskOwner: "w/x"}); err == nil {
		t.Fatal("inspect non-descendant task should deny")
	}
}

func TestAuthorizeResetSession(t *testing.T) {
	id := Resolve("w/a")
	if err := AuthorizeStructural(id, "reset_session", AuthzTarget{TargetFolder: "w/a"}); err != nil {
		t.Fatalf("self reset should work: %v", err)
	}
	if err := AuthorizeStructural(id, "reset_session", AuthzTarget{TargetFolder: "w/a/b"}); err != nil {
		t.Fatalf("child reset should work: %v", err)
	}
	if err := AuthorizeStructural(id, "reset_session", AuthzTarget{TargetFolder: "w/x"}); err == nil {
		t.Fatal("non-descendant reset should fail")
	}
}

func TestAuthorizeInjectMessage(t *testing.T) {
	if err := AuthorizeStructural(Resolve("w"), "inject_message", AuthzTarget{}); err != nil {
		t.Fatal("tier 0 should inject")
	}
	if err := AuthorizeStructural(Resolve("w/a"), "inject_message", AuthzTarget{}); err != nil {
		t.Fatal("tier 1 should inject")
	}
	if err := AuthorizeStructural(Resolve("w/a/b"), "inject_message", AuthzTarget{}); err == nil {
		t.Fatal("tier 2 should not inject")
	}
}

func TestAuthorizeRegisterGroup(t *testing.T) {
	if err := AuthorizeStructural(Resolve("w"), "register_group", AuthzTarget{TargetFolder: "w"}); err == nil {
		t.Fatal("tier 0 should not register worlds")
	}
	if err := AuthorizeStructural(Resolve("w"), "register_group", AuthzTarget{TargetFolder: "w/child"}); err != nil {
		t.Fatalf("tier 0 should register children: %v", err)
	}
	if err := AuthorizeStructural(Resolve("w/a"), "register_group", AuthzTarget{TargetFolder: "w/a/child"}); err != nil {
		t.Fatalf("tier 1 should register direct children: %v", err)
	}
	if err := AuthorizeStructural(Resolve("w/a"), "register_group", AuthzTarget{TargetFolder: "w/b/child"}); err == nil {
		t.Fatal("tier 1 should not register outside own subtree")
	}
	if err := AuthorizeStructural(Resolve("w/a/b"), "register_group", AuthzTarget{}); err == nil {
		t.Fatal("tier 2 should not register groups")
	}
}

func TestAuthorizeEscalateGroup(t *testing.T) {
	if err := AuthorizeStructural(Resolve("w/a/b"), "escalate_group", AuthzTarget{}); err != nil {
		t.Fatal("tier 2 should escalate")
	}
	if err := AuthorizeStructural(Resolve("w/a"), "escalate_group", AuthzTarget{}); err == nil {
		t.Fatal("tier 1 should not escalate")
	}
}

func TestAuthorizeDelegateGroup(t *testing.T) {
	id := Resolve("w/a")
	if err := AuthorizeStructural(id, "delegate_group", AuthzTarget{TargetFolder: "w/a/child"}); err != nil {
		t.Fatalf("should delegate to child: %v", err)
	}
	if err := AuthorizeStructural(id, "delegate_group", AuthzTarget{TargetFolder: "w/b"}); err == nil {
		t.Fatal("should not delegate to non-child")
	}
	if err := AuthorizeStructural(Resolve("w/a/b/c"), "delegate_group", AuthzTarget{}); err == nil {
		t.Fatal("tier 3 should not delegate")
	}
}

func TestAuthorizeRouteTools(t *testing.T) {
	for _, tool := range []string{"list_routes", "set_routes", "add_route", "delete_route"} {
		if err := AuthorizeStructural(Resolve("w"), tool, AuthzTarget{}); err != nil {
			t.Errorf("%s should work at tier 0: %v", tool, err)
		}
		if err := AuthorizeStructural(Resolve("w/a/b"), tool, AuthzTarget{}); err == nil {
			t.Errorf("%s should fail at tier 2", tool)
		}
	}
}

func TestAuthorizeNetworkTools(t *testing.T) {
	for _, tool := range []string{"network_allow", "network_deny", "network_list"} {
		// Root (tier 0) may manage egress for any folder.
		if err := AuthorizeStructural(Resolve("w"), tool, AuthzTarget{TargetFolder: "w/a/b"}); err != nil {
			t.Errorf("%s: root should manage any folder: %v", tool, err)
		}
		// Tier 1 may manage its own folder and descendants.
		if err := AuthorizeStructural(Resolve("w/a"), tool, AuthzTarget{TargetFolder: "w/a"}); err != nil {
			t.Errorf("%s: tier 1 own folder should work: %v", tool, err)
		}
		if err := AuthorizeStructural(Resolve("w/a"), tool, AuthzTarget{TargetFolder: "w/a/b"}); err != nil {
			t.Errorf("%s: tier 1 descendant should work: %v", tool, err)
		}
		// Tier 1 may NOT manage a sibling/escaping folder.
		if err := AuthorizeStructural(Resolve("w/a"), tool, AuthzTarget{TargetFolder: "w/b"}); err == nil {
			t.Errorf("%s: tier 1 sibling folder should be denied", tool)
		}
		// Tier 2+ may not manage egress at all.
		if err := AuthorizeStructural(Resolve("w/a/b"), tool, AuthzTarget{TargetFolder: "w/a/b"}); err == nil {
			t.Errorf("%s: tier 2 should be denied", tool)
		}
	}
}

func TestAuthorizeScheduleTask(t *testing.T) {
	if err := AuthorizeStructural(Resolve("w"), "schedule_task", AuthzTarget{TaskOwner: "w/a"}); err != nil {
		t.Fatal("tier 0 should schedule any task")
	}
	if err := AuthorizeStructural(Resolve("w/a"), "schedule_task", AuthzTarget{TaskOwner: "w/a"}); err != nil {
		t.Fatal("tier 1 same world should schedule")
	}
	if err := AuthorizeStructural(Resolve("w/a"), "schedule_task", AuthzTarget{TaskOwner: "x/b"}); err == nil {
		t.Fatal("tier 1 different world should fail")
	}
	if err := AuthorizeStructural(Resolve("w/a/b"), "schedule_task", AuthzTarget{TaskOwner: "w/a/b"}); err != nil {
		t.Fatal("tier 2 own task should schedule")
	}
	if err := AuthorizeStructural(Resolve("w/a/b"), "schedule_task", AuthzTarget{TaskOwner: "w/a"}); err == nil {
		t.Fatal("tier 2 other's task should fail")
	}
	if err := AuthorizeStructural(Resolve("w/a/b/c"), "schedule_task", AuthzTarget{}); err == nil {
		t.Fatal("tier 3 should not schedule")
	}
}

func TestAuthorizeTaskOps(t *testing.T) {
	for _, tool := range []string{"pause_task", "resume_task", "cancel_task"} {
		if err := AuthorizeStructural(Resolve("w"), tool, AuthzTarget{TaskOwner: "w/a"}); err != nil {
			t.Errorf("%s tier 0 should work: %v", tool, err)
		}
		if err := AuthorizeStructural(Resolve("w/a/b/c"), tool, AuthzTarget{}); err == nil {
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
