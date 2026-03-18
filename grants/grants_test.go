package grants

import (
	"testing"

	"github.com/onvos/arizuko/core"
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
	if !matchGlob("send_message", "send_message") {
		t.Fatal("exact match failed")
	}
	if matchGlob("send_message", "send_reply") {
		t.Fatal("should not match different name")
	}
}

func TestMatchGlob_Star(t *testing.T) {
	if !matchGlob("*", "send_message") {
		t.Fatal("* should match send_message")
	}
	if !matchGlob("send_*", "send_message") {
		t.Fatal("send_* should match send_message")
	}
	// * should NOT match ':' (non-word char)
	if matchGlob("*", "send:message") {
		t.Fatal("* should not match colon")
	}
}

// --- CheckAction ---

func TestCheckAction_NoRules(t *testing.T) {
	if CheckAction(nil, "send_message", nil) {
		t.Fatal("no rules = deny")
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

func TestDeriveRules_Tier0(t *testing.T) {
	rules := DeriveRules(nil, "root", 0)
	if len(rules) != 1 || rules[0] != "*" {
		t.Fatalf("tier 0 = %v, want [*]", rules)
	}
}

func TestDeriveRules_Tier3Plus(t *testing.T) {
	rules := DeriveRules(nil, "leaf", 3)
	if len(rules) != 1 || rules[0] != "send_reply" {
		t.Fatalf("tier 3+ = %v, want [send_reply]", rules)
	}
}

func TestDeriveRules_Tier1(t *testing.T) {
	routes := []core.Route{
		{JID: "telegram:100", Target: "main"},
		{JID: "discord:200", Target: "main"},
		{JID: "telegram:101", Target: "other"},
	}
	rules := DeriveRules(routes, "root", 1)
	// should include rules for discord and telegram, plus management actions
	hasDiscord := false
	hasTelegram := false
	hasMgmt := false
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
	}
	if !hasDiscord {
		t.Error("missing discord rule")
	}
	if !hasTelegram {
		t.Error("missing telegram rule")
	}
	if !hasMgmt {
		t.Error("missing management rules")
	}
}

func TestDeriveRules_Tier2(t *testing.T) {
	routes := []core.Route{
		{JID: "telegram:100", Target: "main"},
		{JID: "discord:200", Target: "main/child"},
		{JID: "slack:300", Target: "other"}, // should not appear
	}
	rules := DeriveRules(routes, "main", 2)
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

// --- NarrowRules ---

func TestNarrowRules_DenyAlwaysKept(t *testing.T) {
	parent := []string{"*"}
	child := []string{"!send_file"}
	out := NarrowRules(parent, child)
	hasDeny := false
	for _, r := range out {
		if r == "!send_file" {
			hasDeny = true
		}
	}
	if !hasDeny {
		t.Fatal("deny rule from child should be kept")
	}
}

func TestNarrowRules_AllowNotWidened(t *testing.T) {
	parent := []string{"send_reply"}
	child := []string{"send_message"} // parent doesn't allow this
	out := NarrowRules(parent, child)
	for _, r := range out {
		if r == "send_message" {
			t.Fatal("send_message should not be added (parent doesn't allow it)")
		}
	}
}

func TestNarrowRules_AllowKeptIfParentAllows(t *testing.T) {
	parent := []string{"*"}
	child := []string{"send_reply"}
	out := NarrowRules(parent, child)
	found := false
	for _, r := range out {
		if r == "send_reply" {
			found = true
		}
	}
	if !found {
		t.Fatal("send_reply should be kept (parent allows it)")
	}
}
