package obs

import (
	"context"
	"errors"
	"testing"
)

func TestStartSpan_TracesOff_Noop(t *testing.T) {
	// tracesOn defaults false (SetupTraces never called); StartSpan must return
	// ctx unchanged and a usable no-op end.
	ctx := context.Background()
	got, end := StartSpan(ctx, "turn", "folder", "f")
	if got != ctx {
		t.Error("StartSpan changed ctx while traces off")
	}
	end(nil) // no panic
	end(errors.New("x"))
}

func TestOutcomeOf(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "success"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("boom"), "error"},
	}
	for _, c := range cases {
		if got := outcomeOf(c.err); got != c.want {
			t.Errorf("outcomeOf(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestPairs(t *testing.T) {
	got := pairs([]string{"a", "1", "b", "2", "odd"})
	if len(got) != 2 {
		t.Fatalf("pairs dropped/added wrong count: %d", len(got))
	}
	if got[0].Key != "a" || got[0].Value.AsString() != "1" {
		t.Errorf("pairs[0] = %v", got[0])
	}
}

func TestSetupTraces_NoEnv_Noop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	shutdown := SetupTraces("test", "inst")
	if shutdown == nil {
		t.Fatal("SetupTraces returned nil shutdown")
	}
	shutdown()
	if tracesOn {
		t.Error("traces enabled despite unset endpoint")
	}
}
