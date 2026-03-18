package grants

import (
	"strings"

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

// matchGlob: * matches word chars only ([a-zA-Z0-9_]).
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

// matchValueGlob: * matches any char except , and ).
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

// CheckAction returns true if the action+params are allowed by rules.
// Last match wins; no match = deny.
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

// MatchingRules returns the rules (unparsed) that match the given action name.
func MatchingRules(rules []string, action string) []string {
	var out []string
	for _, r := range rules {
		if matchGlob(ParseRule(r).Action, action) {
			out = append(out, r)
		}
	}
	return out
}

// NarrowRules merges parent+child. Child can only narrow, never widen.
// Deny rules are always kept; allow rules only if parent already allows them.
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

// DeriveRules returns default rules for folder+tier. DB overrides appended by caller.
func DeriveRules(s *store.Store, folder string, tier int, worldFolder string) []string {
	switch tier {
	case 0:
		return []string{"*"}
	case 1:
		var jids []string
		if s != nil {
			jids = s.RouteSourceJIDsInWorld(worldFolder)
		}
		return deriveTier1Rules(jids)
	case 2:
		var jids []string
		if s != nil {
			jids = s.RouteSourceJIDsInWorld(folder)
		}
		return deriveTier2Rules(jids)
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
	return append(platformRules(jids), tier1FixedActions...)
}

func deriveTier2Rules(jids []string) []string {
	return append([]string{"send_message", "send_reply"}, platformRules(jids)...)
}

func extractPlatforms(jids []string) []string {
	seen := map[string]bool{}
	for _, jid := range jids {
		if p := jidPlatform(jid); p != "" {
			seen[p] = true
		}
	}
	return sortedKeys(seen)
}

func jidPlatform(jid string) string {
	if i := strings.IndexByte(jid, ':'); i > 0 {
		return jid[:i]
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
