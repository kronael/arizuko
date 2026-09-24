package runed

import (
	"context"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// FakeRuntime backs the contract test + standalone acceptance: it invokes a
// caller-supplied function with the rendered Turn and returns its outcome.
type FakeRuntime struct {
	Fn func(ctx context.Context, t Turn) RunResult
}

func (f FakeRuntime) Run(ctx context.Context, t Turn) RunResult {
	if f.Fn == nil {
		return RunResult{Outcome: runedv1.OutcomeSilent}
	}
	return f.Fn(ctx, t)
}

// Kill is a no-op for the fake (nothing runs).
func (FakeRuntime) Kill(string) error { return nil }
