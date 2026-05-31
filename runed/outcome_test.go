package runed

import (
	"testing"

	"github.com/kronael/arizuko/container"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// TestOutcomeFor pins the classifier against every container.Output exit path the
// runner produces (container/runner.go), guarding the breaker-storm regression:
// a clean run (Status=success, no output visible to runed) MUST be OK, not Silent.
func TestOutcomeFor(t *testing.T) {
	cases := []struct {
		name string
		out  container.Output
		want string
	}{
		{"clean success (no output — went via socket)", container.Output{Status: "success", ExitCode: 0}, runedv1.OutcomeOK},
		{"crash exit!=0", container.Output{Status: "error", Error: "exited code 1", ExitCode: 1}, runedv1.OutcomeError},
		{"timeout (Error set, no Status)", container.Output{Error: "Container timed out after 20m", ExitCode: -1}, runedv1.OutcomeError},
		{"spawn failure (Error set)", container.Output{Error: "start: no such image"}, runedv1.OutcomeError},
		{"egress register fail", container.Output{Status: "error", Error: "egress register: x"}, runedv1.OutcomeError},
	}
	for _, c := range cases {
		if got := outcomeFor(c.out); got != c.want {
			t.Errorf("%s: outcomeFor=%q want %q", c.name, got, c.want)
		}
	}
}
