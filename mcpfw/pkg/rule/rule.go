// Package rule is the ruleset model: a flat list over opaque tool-name globs.
//
// Deny-wins, default-deny. Every rule is evaluated; one matching deny refuses
// the call whatever else allows it, and a tool matched by no allow rule is
// refused too. This is the opposite of routd's first-match route table, and
// deliberately so: routing is positive selection, gating is negative authority,
// and the safe failure for a gate is "refuse".
package rule

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Action is what a matching rule contributes to the decision.
type Action string

const (
	Allow Action = "allow"
	Deny  Action = "deny"
	// Log records the call and contributes nothing to allow/deny.
	Log Action = "log"
)

// Rule matches a tool name by glob. The shape stays flat and string-typed on
// purpose: nothing here may become folder- or grant-shaped, or the component
// stops standing alone (spec 6/12-mcp-firewall "What it does NOT own").
type Rule struct {
	Tool   string `json:"tool"`
	Action Action `json:"action"`
}

// Set is an evaluated ruleset.
type Set struct {
	Rules []Rule `json:"rules"`
}

// Decision is the outcome for one tool name.
type Decision struct {
	Allowed bool
	// Logged reports whether a log rule matched, so the caller records the
	// call whether or not it went through.
	Logged bool
}

// Load reads a ruleset as JSON. An unknown action is an error rather than a
// silent skip: a typo'd "allwo" that parsed as "no rule" would open a tool by
// default-deny's own logic inverted — the rule simply vanishes.
func Load(r io.Reader) (Set, error) {
	var s Set
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return Set{}, fmt.Errorf("mcpfw: decode ruleset: %w", err)
	}
	for i, rl := range s.Rules {
		switch rl.Action {
		case Allow, Deny, Log:
		default:
			return Set{}, fmt.Errorf("mcpfw: rule %d: unknown action %q", i, rl.Action)
		}
		if rl.Tool == "" {
			return Set{}, fmt.Errorf("mcpfw: rule %d: empty tool pattern", i)
		}
	}
	return s, nil
}

// Match reports whether pattern matches tool. `*` spans any run of characters
// including separators, so `read_*` covers `read_file` and `send_*` covers
// `send_message/attachment`. path.Match stops at `/`, which a tool name may
// contain, so the check is done here rather than borrowed.
func Match(pattern, tool string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == tool
	}
	parts := strings.Split(pattern, "*")
	rest := tool
	if p := parts[0]; p != "" {
		if !strings.HasPrefix(rest, p) {
			return false
		}
		rest = rest[len(p):]
	}
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	if p := parts[len(parts)-1]; p != "" {
		return strings.HasSuffix(rest, p)
	}
	return true
}

// Decide evaluates every rule. Deny dominates; absence of an allow denies.
func (s Set) Decide(tool string) Decision {
	var allowed, denied, logged bool
	for _, r := range s.Rules {
		if !Match(r.Tool, tool) {
			continue
		}
		switch r.Action {
		case Deny:
			denied = true
		case Allow:
			allowed = true
		case Log:
			logged = true
		}
	}
	return Decision{Allowed: allowed && !denied, Logged: logged}
}
