package runed

import (
	"context"
	"errors"
	"reflect"
	"testing"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

type pauseKillFailure struct{ FakeRuntime }

func (pauseKillFailure) Kill(string) error { return errors.New("guest did not stop") }

func TestModelPauseRecoveryCannotReportAnUnstoppedGuestAsStopped(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	rt := pauseKillFailure{FakeRuntime{Fn: func(context.Context, Turn) RunResult {
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}}
	_, mgr := newMgr(t, rt, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = mgr.Run(t.Context(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "a", TurnID: "probe", MessageBatch: "saved"})
	}()
	<-started
	defer func() { close(release); <-done }()
	if _, _, err := mgr.StopFolder(t.Context(), "a"); err == nil {
		t.Fatal("stop acknowledged an unstopped guest")
	}
	if n := mgr.ActiveCount(t.Context()); n != 1 {
		t.Fatalf("unresolved guest count=%d", n)
	}
}

func TestModelPauseKeepsRuntimePort(t *testing.T) {
	port := reflect.TypeFor[Runtime]()
	if port.NumMethod() != 2 || port.Method(0).Name != "Kill" || port.Method(1).Name != "Run" {
		t.Fatalf("runtime port changed: %v", port)
	}
	turn := reflect.TypeFor[Turn]()
	if turn.NumField() != 7 {
		t.Fatalf("runtime turn widened: %v", turn)
	}
}
