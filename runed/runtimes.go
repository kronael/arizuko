package runed

// runtimes.go holds the test seam: FakeRuntime backs unit tests of the run
// envelope without spawning anything. The production docker lifecycle lives
// in docker.go.

import (
	"context"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// FakeRuntime backs the contract test + standalone acceptance: it invokes a
// caller-supplied function with the RunSpec and returns its outcome. No
// docker, no socket (spec 5/P § acceptance: FakeRuntime backs unit tests of
// the envelope without spawning anything).
type FakeRuntime struct {
	Fn func(ctx context.Context, spec RunSpec) RunResult
}

// Run invokes the injected function.
func (f FakeRuntime) Run(ctx context.Context, spec RunSpec) RunResult {
	if f.Fn == nil {
		return RunResult{Outcome: runedv1.OutcomeSilent}
	}
	return f.Fn(ctx, spec)
}

// Kill is a no-op for the fake (no container to stop).
func (FakeRuntime) Kill(string) error { return nil }
