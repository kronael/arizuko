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
		{"google:alice", "google:alice"},
		{"telegram:user/42", "telegram:user/42"},
	} {
		if got := turnCallerSub(c.trigger); got != c.want {
			t.Errorf("turnCallerSub(%q) = %q, want %q", c.trigger, got, c.want)
		}
	}
}
