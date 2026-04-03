package grants

import (
	"sort"
	"strings"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

type Rule struct {
	Deny   bool
	Action string
	Params map[string]ParamRule
}

type ParamRule struct {
	Deny    bool
	Pattern string
}

func ParseRule(r string) Rule {
	var rule Rule
	if strings.HasPrefix(r, "!") {
		rule.Deny = true
		r = r[1:]
	}

	paren := strings.IndexByte(r, '(')
	if paren < 0 {
		rule.Action = r
		return rule
	}
	rule.Action = r[:paren]
	rest := strings.TrimSuffix(r[paren+1:], ")")
	if rest == "" {
		return rule
	}

	rule.Params = make(map[string]ParamRule)
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			deny := strings.HasPrefix(part, "!")
			rule.Params[strings.TrimPrefix(part, "!")] = ParamRule{Deny: deny}
			continue
		}
		name := strings.TrimSpace(part[:eq])
		val := strings.TrimSpace(part[eq+1:])
		deny := false
		if strings.HasPrefix(name, "!") {
			deny = true
			name = name[1:]
		}
		rule.Params[name] = ParamRule{Deny: deny, Pattern: val}
	}
	return rule
}

func matchGlob(pat, s string) bool {
	for {
		if pat == "" {
			return s == ""
		}
		if pat[0] == '*' {
			pat = pat[1:]
			for i := 0; i <= len(s); i++ {
				if matchGlob(pat, s[i:]) {
					return true
				}
				if i < len(s) && !isWordChar(s[i]) {
					break
				}
			}
			return false
		}
		if s == "" || pat[0] != s[0] {
			return false
		}
		pat = pat[1:]
		s = s[1:]
	}
}

func matchValueGlob(pat, s string) bool {
	for {
		if pat == "" {
			return s == ""
		}
		if pat[0] == '*' {
			pat = pat[1:]
			for i := 0; i <= len(s); i++ {
				if matchValueGlob(pat, s[i:]) {
					return true
				}
				if i < len(s) && (s[i] == ',' || s[i] == ')') {
					break
				}
			}
			return false
		}
		if s == "" || pat[0] != s[0] {
			return false
		}
		pat = pat[1:]
		s = s[1:]
	}
}

func isWordChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

func ruleMatchesParams(rule Rule, params map[string]string) bool {
	if rule.Params == nil {
		return true
	}
	for name, pr := range rule.Params {
		val, present := params[name]
		if pr.Deny {
			if present {
				return false
			}
		} else if !present || !matchValueGlob(pr.Pattern, val) {
			return false
		}
	}
	return true
}

func CheckAction(rules []string, action string, params map[string]string) bool {
	result, matched := false, false
	for _, r := range rules {
		rule := ParseRule(r)
		if matchGlob(rule.Action, action) && ruleMatchesParams(rule, params) {
			result = !rule.Deny
			matched = true
		}
	}
	return matched && result
}

func MatchingRules(rules []string, action string) []string {
	var out []string
	for _, r := range rules {
		if matchGlob(ParseRule(r).Action, action) {
			out = append(out, r)
		}
	}
	return out
}

func NarrowRules(parent, child []string) []string {
	out := append([]string(nil), parent...)
	for _, r := range child {
		rule := ParseRule(r)
		if rule.Deny || CheckAction(parent, rule.Action, map[string]string{}) {
			out = append(out, r)
		}
	}
	return out
}

var platformSendActions = []string{"send_message", "send_file", "send_reply"}

var tier1FixedActions = []string{
	"schedule_task", "register_group", "escalate_group", "delegate_group",
	"get_routes", "set_routes", "add_route", "delete_route",
	"list_tasks", "pause_task", "resume_task", "cancel_task",
}

func DeriveRules(s *store.Store, folder string, tier int, worldFolder string) []string {
	jidsIn := func(scope string) []string {
		if s == nil {
			return nil
		}
		return s.RouteSourceJIDsInWorld(scope)
	}
	switch tier {
	case 0:
		return []string{"*"}
	case 1:
		return deriveTier1Rules(jidsIn(worldFolder))
	case 2:
		return deriveTier2Rules(jidsIn(folder))
	default:
		return []string{"send_reply"}
	}
}

func platformRules(jids []string) []string {
	var rules []string
	for _, p := range extractPlatforms(jids) {
		for _, a := range platformSendActions {
			rules = append(rules, a+"(jid="+p+":*)")
		}
	}
	return rules
}

func deriveTier1Rules(jids []string) []string {
	r := append(platformRules(jids), tier1FixedActions...)
	r = append(r, "share_mount(readonly=false)")
	return r
}

func deriveTier2Rules(jids []string) []string {
	r := append([]string{"send_message", "send_reply"}, platformRules(jids)...)
	r = append(r, "share_mount(readonly=true)")
	return r
}

func extractPlatforms(jids []string) []string {
	seen := map[string]bool{}
	for _, jid := range jids {
		if p := core.JidPlatform(jid); p != "" {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
