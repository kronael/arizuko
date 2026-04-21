package grants

import (
	"testing"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

// --- ParseRule ---

func TestParseRule_Allow(t *testing.T) {
	r := ParseRule("send_message")
	if r.Deny {
		t.Fatal("expected allow")
	}
	if r.Action != "send_message" {
		t.Fatalf("action = %q", r.Action)
	}
	if r.Params != nil {
		t.Fatal("expected nil params")
	}
}

func TestParseRule_Deny(t *testing.T) {
	r := ParseRule("!send_message")
	if !r.Deny {
		t.Fatal("expected deny")
	}
	if r.Action != "send_message" {
		t.Fatalf("action = %q", r.Action)
	}
}

func TestParseRule_ParamAllow(t *testing.T) {
	r := ParseRule("send_message(jid=telegram:*)")
	if r.Deny {
		t.Fatal("expected allow")
	}
	if r.Action != "send_message" {
		t.Fatalf("action = %q", r.Action)
	}
	pr, ok := r.Params["jid"]
	if !ok {
		t.Fatal("expected jid param")
	}
	if pr.Deny {
		t.Fatal("param should not be deny")
	}
	if pr.Pattern != "telegram:*" {
		t.Fatalf("pattern = %q", pr.Pattern)
	}
}

func TestParseRule_EmptyParens(t *testing.T) {
	r := ParseRule("send_message()")
	if r.Action != "send_message" {
		t.Fatalf("action = %q", r.Action)
	}
	// empty parens means no param constraints
	if len(r.Params) != 0 {
		t.Fatalf("expected 0 params, got %d", len(r.Params))
	}
}

func TestParseRule_Wildcard(t *testing.T) {
	r := ParseRule("*")
	if r.Action != "*" {
		t.Fatalf("action = %q", r.Action)
	}
}

// --- matchGlob ---

func TestMatchGlob_Exact(t *testing.T) {
	if !matchGlob("send_message", "send_message", notWordChar) {
		t.Fatal("exact match failed")
	}
	if matchGlob("send_message", "send_reply", notWordChar) {
		t.Fatal("should not match different name")
	}
}

func TestMatchGlob_Star(t *testing.T) {
	if !matchGlob("*", "send_message", notWordChar) {
		t.Fatal("* should match send_message")
	}
	if !matchGlob("send_*", "send_message", notWordChar) {
		t.Fatal("send_* should match send_message")
	}
	// * should NOT match ':' (non-word char)
	if matchGlob("*", "send:message", notWordChar) {
		t.Fatal("* should not match colon")
	}
}

// --- CheckAction ---

func TestCheckAction_NoRules(t *testing.T) {
	if CheckAction(nil, "send_message", nil) {
		t.Fatal("no rules = deny")
	}
}

func TestCheckAction_EmptyRules(t *testing.T) {
	if CheckAction([]string{}, "send_message", nil) {
		t.Fatal("empty rules = deny")
	}
}

func TestCheckAction_AllowAll(t *testing.T) {
	if !CheckAction([]string{"*"}, "send_message", nil) {
		t.Fatal("* should allow everything")
	}
}

func TestCheckAction_DenyAfterAllow(t *testing.T) {
	rules := []string{"*", "!send_message"}
	if CheckAction(rules, "send_message", nil) {
		t.Fatal("last rule is deny, should be denied")
	}
	// other actions still allowed
	if !CheckAction(rules, "send_reply", nil) {
		t.Fatal("send_reply should still be allowed")
	}
}

func TestCheckAction_AllowAfterDeny(t *testing.T) {
	rules := []string{"!send_message", "send_message"}
	if !CheckAction(rules, "send_message", nil) {
		t.Fatal("last rule is allow, should be allowed")
	}
}

func TestCheckAction_ParamMatch(t *testing.T) {
	rules := []string{"send_message(jid=telegram:*)"}
	params := map[string]string{"jid": "telegram:123456"}
	if !CheckAction(rules, "send_message", params) {
		t.Fatal("should be allowed for telegram jid")
	}
}

func TestCheckAction_ParamMismatch(t *testing.T) {
	rules := []string{"send_message(jid=telegram:*)"}
	params := map[string]string{"jid": "discord:789"}
	if CheckAction(rules, "send_message", params) {
		t.Fatal("should be denied for discord jid")
	}
}

func TestCheckAction_NilParamsWithParamConstraint(t *testing.T) {
	// Rule requires jid param; nil input params must not panic or silently match.
	rules := []string{"send_message(jid=telegram:*)"}
	if CheckAction(rules, "send_message", nil) {
		t.Fatal("nil params must not silently match a param-constrained rule")
	}
}

func TestCheckAction_NoMatch_Deny(t *testing.T) {
	rules := []string{"send_reply"}
	if CheckAction(rules, "send_message", nil) {
		t.Fatal("unmatched action should be denied")
	}
}

func TestCheckAction_MultipleParams(t *testing.T) {
	rules := []string{"send_message(jid=telegram:*, type=text)"}
	params := map[string]string{"jid": "telegram:123", "type": "text"}
	if !CheckAction(rules, "send_message", params) {
		t.Fatal("should allow when all params match")
	}
	params2 := map[string]string{"jid": "telegram:123", "type": "image"}
	if CheckAction(rules, "send_message", params2) {
		t.Fatal("should deny when type doesn't match")
	}
}

// --- MatchingRules ---

func TestMatchingRules(t *testing.T) {
	rules := []string{"send_message", "!send_message", "send_reply", "send_message(jid=tg:*)"}
	got := MatchingRules(rules, "send_message")
	if len(got) != 3 {
		t.Fatalf("expected 3 matching, got %d: %v", len(got), got)
	}
}

func TestMatchingRules_Wildcard(t *testing.T) {
	rules := []string{"*", "send_reply"}
	got := MatchingRules(rules, "send_message")
	if len(got) != 1 || got[0] != "*" {
		t.Fatalf("expected [*], got %v", got)
	}
}

// --- DeriveRules ---

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addRoute(t *testing.T, s *store.Store, jid, target string) {
	t.Helper()
	match := "platform=" + core.JidPlatform(jid) + " room=" + core.JidRoom(jid)
	_, err := s.AddRoute(core.Route{
		Match:  match,
		Target: target,
	})
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
}

// addRouteRoomOnly inserts a route whose match is `room=X` only, mirroring
// what the /add_route MCP tool and SetupGroup write in production.
func addRouteRoomOnly(t *testing.T, s *store.Store, roomOnly, target string) {
	t.Helper()
	_, err := s.AddRoute(core.Route{
		Match:  "room=" + roomOnly,
		Target: target,
	})
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
}

func TestDeriveRules_Tier1_RoomOnlyRoute(t *testing.T) {
	s := openTestStore(t)
	addRouteRoomOnly(t, s, "-1003805633088", "world/sub")

	rules := DeriveRules(s, "world/sub", 1, "world")
	var hasMsg, hasFile, hasReply bool
	for _, r := range rules {
		switch r {
		case "send_message":
			hasMsg = true
		case "send_file":
			hasFile = true
		case "send_reply":
			hasReply = true
		}
	}
	if !hasMsg {
		t.Error("tier-1 room-only route: missing send_message")
	}
	if !hasFile {
		t.Error("tier-1 room-only route: missing send_file")
	}
	if !hasReply {
		t.Error("tier-1 room-only route: missing send_reply")
	}
}

func TestDeriveRules_Tier2_RoomOnlyRoute(t *testing.T) {
	s := openTestStore(t)
	addRouteRoomOnly(t, s, "-1003805633088", "world/sub")

	rules := DeriveRules(s, "world/sub", 2, "world")
	var hasMsg, hasFile, hasReply bool
	for _, r := range rules {
		switch r {
		case "send_message":
			hasMsg = true
		case "send_file":
			hasFile = true
		case "send_reply":
			hasReply = true
		}
	}
	if !hasMsg {
		t.Error("tier-2 room-only route: missing send_message")
	}
	if !hasFile {
		t.Error("tier-2 room-only route: missing send_file")
	}
	if !hasReply {
		t.Error("tier-2 room-only route: missing send_reply")
	}
}

func TestDeriveRules_Tier0(t *testing.T) {
	rules := DeriveRules(nil, "root", 0, "root")
	if len(rules) != 1 || rules[0] != "*" {
		t.Fatalf("tier 0 = %v, want [*]", rules)
	}
}

func TestDeriveRules_Tier3Plus(t *testing.T) {
	rules := DeriveRules(nil, "leaf", 3, "leaf")
	if len(rules) != 1 || rules[0] != "send_reply" {
		t.Fatalf("tier 3+ = %v, want [send_reply]", rules)
	}
}

func TestDeriveRules_Tier1(t *testing.T) {
	s := openTestStore(t)
	addRoute(t, s, "telegram:100", "main")
	addRoute(t, s, "discord:200", "main")
	addRoute(t, s, "telegram:101", "other")

	rules := DeriveRules(s, "main", 1, "main")
	hasDiscord := false
	hasTelegram := false
	hasMgmt := false
	hasGetRoutes := false
	hasListTasks := false
	hasFile := false
	for _, r := range rules {
		if r == "send_message(jid=discord:*)" {
			hasDiscord = true
		}
		if r == "send_message(jid=telegram:*)" {
			hasTelegram = true
		}
		if r == "schedule_task" {
			hasMgmt = true
		}
		if r == "get_routes" {
			hasGetRoutes = true
		}
		if r == "list_tasks" {
			hasListTasks = true
		}
		if r == "send_file" {
			hasFile = true
		}
	}
	if !hasFile {
		t.Error("missing hardcoded send_file rule for tier 1")
	}
	if !hasDiscord {
		t.Error("missing discord rule")
	}
	if !hasTelegram {
		t.Error("missing telegram rule")
	}
	if !hasMgmt {
		t.Error("missing management rules (schedule_task)")
	}
	if !hasGetRoutes {
		t.Error("missing get_routes rule")
	}
	if !hasListTasks {
		t.Error("missing list_tasks rule")
	}
}

func TestDeriveRules_Tier1_WorldScope(t *testing.T) {
	s := openTestStore(t)
	// routes targeting worldFolder itself and subfolders
	addRoute(t, s, "telegram:100", "main")
	addRoute(t, s, "discord:200", "main/child")
	addRoute(t, s, "slack:300", "other") // different world — should not appear

	rules := DeriveRules(s, "main", 1, "main")
	hasDiscord := false
	hasSlack := false
	for _, r := range rules {
		if r == "send_message(jid=discord:*)" {
			hasDiscord = true
		}
		if r == "send_message(jid=slack:*)" {
			hasSlack = true
		}
	}
	if !hasDiscord {
		t.Error("tier-1 should include routes to subfolders in world")
	}
	if hasSlack {
		t.Error("tier-1 should not include routes from other worlds")
	}
}

func TestDeriveRules_Tier2(t *testing.T) {
	s := openTestStore(t)
	addRoute(t, s, "telegram:100", "main")
	addRoute(t, s, "discord:200", "main/child")
	addRoute(t, s, "slack:300", "other") // should not appear

	rules := DeriveRules(s, "main", 2, "main")
	hasTelegram := false
	hasDiscord := false
	hasSlack := false
	hasBasic := false
	for _, r := range rules {
		if r == "send_message(jid=telegram:*)" {
			hasTelegram = true
		}
		if r == "send_message(jid=discord:*)" {
			hasDiscord = true
		}
		if r == "send_message(jid=slack:*)" {
			hasSlack = true
		}
		if r == "send_message" || r == "send_reply" {
			hasBasic = true
		}
	}
	hasFile := false
	for _, r := range rules {
		if r == "send_file" {
			hasFile = true
		}
	}
	if !hasFile {
		t.Error("missing hardcoded send_file rule for tier 2")
	}
	if !hasTelegram {
		t.Error("missing telegram rule for own route")
	}
	if !hasDiscord {
		t.Error("missing discord rule for child route")
	}
	if hasSlack {
		t.Error("should not have slack rule (different target)")
	}
	if !hasBasic {
		t.Error("missing basic send_message/send_reply rules")
	}
}

func TestParseRule_UnterminatedParens(t *testing.T) {
	// Malformed rule must not silently match: Action empty so no action matches.
	r := ParseRule("foo(a=1")
	if r.Action != "" {
		t.Fatalf("malformed rule produced action %q; want empty", r.Action)
	}
}

// --- share_mount grant ---

func TestShareMount_Tier0(t *testing.T) {
	rules := DeriveRules(nil, "root", 0, "root")
	if !CheckAction(rules, "share_mount", map[string]string{"readonly": "false"}) {
		t.Error("tier 0 should allow share_mount RW via wildcard")
	}
	if !CheckAction(rules, "share_mount", map[string]string{"readonly": "true"}) {
		t.Error("tier 0 should allow share_mount RO via wildcard")
	}
}

func TestShareMount_Tier1(t *testing.T) {
	s := openTestStore(t)
	addRoute(t, s, "telegram:100", "main")
	rules := DeriveRules(s, "main", 1, "main")
	if !CheckAction(rules, "share_mount", map[string]string{"readonly": "false"}) {
		t.Error("tier 1 should allow share_mount RW")
	}
	if CheckAction(rules, "share_mount", map[string]string{"readonly": "true"}) {
		t.Error("tier 1 should NOT match share_mount RO (only RW rule)")
	}
}

func TestShareMount_Tier2(t *testing.T) {
	s := openTestStore(t)
	addRoute(t, s, "telegram:100", "main/child")
	rules := DeriveRules(s, "main/child", 2, "main")
	if !CheckAction(rules, "share_mount", map[string]string{"readonly": "true"}) {
		t.Error("tier 2 should allow share_mount RO")
	}
	if CheckAction(rules, "share_mount", map[string]string{"readonly": "false"}) {
		t.Error("tier 2 should NOT allow share_mount RW")
	}
}

func TestShareMount_Tier3(t *testing.T) {
	rules := DeriveRules(nil, "deep/group/leaf", 3, "deep")
	if CheckAction(rules, "share_mount", map[string]string{"readonly": "true"}) {
		t.Error("tier 3 should NOT have share_mount")
	}
	if CheckAction(rules, "share_mount", map[string]string{"readonly": "false"}) {
		t.Error("tier 3 should NOT have share_mount")
	}
}

func TestShareMount_DenyOverride(t *testing.T) {
	s := openTestStore(t)
	addRoute(t, s, "telegram:100", "main")
	rules := DeriveRules(s, "main", 1, "main")
	allRules := append(rules, "!share_mount")
	if CheckAction(allRules, "share_mount", map[string]string{"readonly": "false"}) {
		t.Error("!share_mount override should block share_mount RW")
	}
}
