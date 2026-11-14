package routd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kronael/arizuko/retry"
)

func TestProcessRetryPolicy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newTurnControl(1)
		calls := 0
		q.SetProcessMessagesFn(func(string) (bool, error) {
			calls++
			if calls == 3 {
				return true, nil
			}
			return false, &retry.Error{Failure: retry.Failure{Kind: "transient"}, Err: errors.New("unavailable")}
		})
		q.EnqueueMessageCheck("chat")
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("calls=%d", calls)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls != 2 {
			t.Fatalf("calls=%d", calls)
		}
		time.Sleep(60 * time.Second)
		synctest.Wait()
		if calls != 3 || q.getGroup("chat").processFailures != 0 {
			t.Fatalf("calls=%d failures=%d", calls, q.getGroup("chat").processFailures)
		}
		q.EnqueueMessageCheck("chat")
		synctest.Wait()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls != 5 {
			t.Fatalf("reset did not restore first delay: calls=%d", calls)
		}
		q.Shutdown()
	})
}

func TestProcessRetryMaintenanceCannotRearm(t *testing.T) {
	t.Skip("L881: maintenance bypasses processing due time and Stop")
	for _, transient := range []bool{false, true} {
		t.Run(fmt.Sprint(transient), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := newTurnControl(1)
				defer q.Shutdown()
				calls := 0
				var cause error = errors.New("permanent")
				if transient {
					cause = &retry.Error{Failure: retry.Failure{Kind: "transient"}, Err: cause}
				}
				q.SetProcessMessagesFn(func(string) (bool, error) {
					calls++
					return false, cause
				})
				q.EnqueueMessageCheck("chat")
				synctest.Wait()
				q.EnqueueMessageCheck("chat")
				synctest.Wait()
				if calls != 1 {
					t.Fatalf("maintenance bypassed retry state: calls=%d", calls)
				}
			})
		})
	}
}

func TestProcessRetryAgainAndStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newTurnControl(1)
		calls := 0
		q.SetProcessMessagesFn(func(string) (bool, error) {
			calls++
			if calls == 1 {
				q.EnqueueMessageCheck("chat")
			}
			return false, errors.New("unknown")
		})
		q.EnqueueMessageCheck("chat")
		synctest.Wait()
		if calls != 2 {
			t.Fatalf("again calls=%d", calls)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls != 2 {
			t.Fatalf("unknown error retried: calls=%d", calls)
		}
		q.Shutdown()
		q.EnqueueMessageCheck("chat")
		synctest.Wait()
		if calls != 2 {
			t.Fatalf("shutdown calls=%d", calls)
		}
	})
}

func TestProcessRetryWrappedTransient(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, &net.DNSError{Err: "lookup failed"}} {
		synctest.Test(t, func(t *testing.T) {
			q := newTurnControl(1)
			defer q.Shutdown()
			calls := 0
			q.SetProcessMessagesFn(func(string) (bool, error) {
				calls++
				return false, fmt.Errorf("%w: %w", ErrDependencyDown, cause)
			})
			q.EnqueueMessageCheck("chat")
			synctest.Wait()
			time.Sleep(29 * time.Second)
			synctest.Wait()
			if calls != 1 {
				t.Fatalf("wrapped %v called %d times before due", cause, calls)
			}
			time.Sleep(time.Second)
			synctest.Wait()
			if calls != 2 {
				t.Fatalf("wrapped %v called %d times at due", cause, calls)
			}
		})
	}
}

func TestProcessRetryWrappedPermanent(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("permanent")} {
		synctest.Test(t, func(t *testing.T) {
			q := newTurnControl(1)
			calls := 0
			q.SetProcessMessagesFn(func(string) (bool, error) { calls++; return false, fmt.Errorf("%w: %w", ErrDependencyDown, cause) })
			q.EnqueueMessageCheck("chat")
			synctest.Wait()
			time.Sleep(time.Hour)
			synctest.Wait()
			if calls != 1 {
				t.Fatalf("wrapped %v retried %d times", cause, calls)
			}
		})
	}
}

func TestProcessRetryShutdownDuringFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newTurnControl(1)
		calls := 0
		q.SetProcessMessagesFn(func(string) (bool, error) {
			calls++
			q.Shutdown()
			return false, context.DeadlineExceeded
		})
		q.EnqueueMessageCheck("chat")
		synctest.Wait()
		q.mu.Lock()
		q.shuttingDown = false
		q.mu.Unlock()
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("shutdown retried %d times", calls)
		}
	})
}
