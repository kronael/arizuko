package grants

import (
	"strings"

	"github.com/onvos/arizuko/store"
)

// Rule is a parsed grant rule.
type Rule struct {
	Deny   bool
	Action string // may contain * wildcard
	Params map[string]ParamRule
}

// ParamRule is the constraint for a single parameter.
type ParamRule struct {
	Deny    bool   // true if !param (must not be present)
	Pattern string // glob pattern for value
}

// ParseRule parses a rule string into a Rule.
func ParseRule(r string) Rule {
	var rule Rule
	if strings.HasPrefix(r, "!") {
		rule.Deny = true
		r = r[1:]
	}

	// Split action from params
	paren := strings.IndexByte(r, '(')
	if paren < 0 {
		rule.Action = r
		return rule
	}
	rule.Action = r[:paren]
	rest := r[paren+1:]
	// strip trailing )
	rest = strings.TrimSuffix(rest, ")")
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
			// !param — param must not be present
			name := strings.TrimPrefix(part, "!")
			deny := strings.HasPrefix(part, "!")
			rule.Params[name] = ParamRule{Deny: deny}
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

// matchGlob matches s against a glob where * matches [a-zA-Z0-9_] only.
func matchGlob(pat, s string) bool {
	for {
		if pat == "" {
			return s == ""
		}
		if pat[0] == '*' {
			// * matches zero or more word chars
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

// matchValueGlob matches s against a glob where * matches any char except , and ).
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

// ruleMatchesAction returns true if the rule's action pattern matches the given action.
func ruleMatchesAction(rule Rule, action string) bool {
	return matchGlob(rule.Action, action)
}

// ruleMatchesParams returns true if the rule's param constraints match the given params.
// nil Params on rule = any params allowed.
func ruleMatchesParams(rule Rule, params map[string]string) bool {
	if rule.Params == nil {
		return true
	}
	for name, pr := range rule.Params {
		val, present := params[name]
		if pr.Deny {
			// param must NOT be present
			if present {
				return false
			}
		} else {
			// param must be present and match pattern
			if !present || !matchValueGlob(pr.Pattern, val) {
				return false
			}
		}
	}
	return true
}

// CheckAction returns true if the action+params are allowed by rules.
// Last match wins; no match = deny.
func CheckAction(rules []string, action string, params map[string]string) bool {
	result := false
	matched := false
	for _, r := range rules {
		rule := ParseRule(r)
		if ruleMatchesAction(rule, action) && ruleMatchesParams(rule, params) {
			result = !rule.Deny
			matched = true
		}
	}
	if !matched {
		return false
	}
	return result
}

// MatchingRules returns the rules (unparsed) that match the given action name.
func MatchingRules(rules []string, action string) []string {
	var out []string
	for _, r := range rules {
		rule := ParseRule(r)
		if ruleMatchesAction(rule, action) {
			out = append(out, r)
		}
	}
	return out
}

// NarrowRules merges parent+child rules. Child can only narrow, never widen.
// Allow rules in child are only kept if they are also allowed by parent.
// Deny rules in child are always kept.
func NarrowRules(parent, child []string) []string {
	var out []string
	out = append(out, parent...)
	for _, r := range child {
		rule := ParseRule(r)
		if rule.Deny {
			// deny rules always narrow — keep
			out = append(out, r)
		} else {
			// allow rule: only keep if parent allows this action with any params
			// use empty params to check broadest form — if parent allows at all
			if CheckAction(parent, rule.Action, map[string]string{}) {
				out = append(out, r)
			}
		}
	}
	return out
}

// platformSendActions are the per-platform actions granted at tier 1+.
var platformSendActions = []string{"send_message", "send_file", "send_reply"}

// tier1FixedActions are management actions always included at tier 1.
var tier1FixedActions = []string{
	"schedule_task", "register_group", "escalate_group", "delegate_group",
	"get_routes", "set_routes", "add_route", "delete_route",
	"list_tasks", "pause_task", "resume_task", "cancel_task",
}

// DeriveRules returns default rules for folder+tier, querying the store for
// route JIDs in the appropriate scope.
// worldFolder is the tier-1 ancestor (same as folder for tier-0/1 groups).
// DB override rows (from s.GetGrants) are appended by the caller.
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

func deriveTier1Rules(jids []string) []string {
	platforms := extractPlatforms(jids)
	var rules []string
	for _, p := range platforms {
		for _, a := range platformSendActions {
			rules = append(rules, a+"(jid="+p+":*)")
		}
	}
	rules = append(rules, tier1FixedActions...)
	return rules
}

func deriveTier2Rules(jids []string) []string {
	platforms := extractPlatforms(jids)
	var rules []string
	rules = append(rules, "send_message", "send_reply")
	for _, p := range platforms {
		for _, a := range platformSendActions {
			rules = append(rules, a+"(jid="+p+":*)")
		}
	}
	return rules
}

// extractPlatforms returns sorted unique platform prefixes from JIDs.
func extractPlatforms(jids []string) []string {
	seen := map[string]bool{}
	for _, jid := range jids {
		if p := jidPlatform(jid); p != "" {
			seen[p] = true
		}
	}
	return sortedKeys(seen)
}

// jidPlatform returns the platform prefix from a JID (e.g. "telegram:123" → "telegram").
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
	// simple insertion sort (small sets)
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
