package routd

import "testing"

// B2: spawn-time secret resolution and the agent's per-tool-call connector
// resolution must render the turn's caller identically — they used to be two
// copies of the same rule, so a stray trigger shape could diverge them.
func TestTurnCallerSub(t *testing.T) {
	for _, c := range []struct{ trigger, want string }{
		{"", "service:routd"},
		{"timed-abc123", "service:routd"},
		{"system", "service:routd"},
		{"system:boot", "service:routd"},
		{"timed", "service:routd"},
		{"timed-isolated:task-1", "service:routd"},
		// Every sender with a person behind it passes through VERBATIM,
		// platform ones included: an allowlist of authd schemes between this
		// and cost_log emptied user_sub in all 7188 fleet rows (BUGS L69).
		{"google:alice", "google:alice"},
		{"github:bob", "github:bob"},
		{"local:admin", "local:admin"},
		{"anon:dead", "anon:dead"},
		{"telegram:user/42", "telegram:user/42"},
		{"slack:user/U123", "slack:user/U123"},
		{"discord:user/9", "discord:user/9"},
		{"email:address/a@b.c", "email:address/a@b.c"},
		{"delegate", "delegate"},
		{"operator", "operator"},
	} {
		if got := turnCallerSub(c.trigger); got != c.want {
			t.Errorf("turnCallerSub(%q) = %q, want %q", c.trigger, got, c.want)
		}
	}
}
