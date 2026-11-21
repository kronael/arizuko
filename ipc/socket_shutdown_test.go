package ipc

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
)

func TestIdleSocketStopsOnCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		left, right := net.Pipe()
		defer left.Close()
		defer right.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			serveConn(ctx, left, nil, RoutdFns{}, StoreFns{}, "alpha")
			close(done)
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("idle connection remains blocked after cancellation")
		}
	})
}
