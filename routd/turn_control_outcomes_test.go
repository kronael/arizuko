package routd

import (
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
)

func TestRepeatedOutcomesReleaseAdmission(t *testing.T) {
	for _, failure := range []error{nil, errors.New("local failure"), fmt.Errorf("runed: %w", ErrDependencyDown)} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := newTurnControl(1)
				q.SetFolderForJidFn(func(string) string { return "same-folder" })
				calls := 0
				q.SetProcessMessagesFn(func(string) (bool, error) {
					calls++
					return false, failure
				})
				for i := range 6 {
					jid := "first"
					if i == 5 {
						jid = "next"
					}
					q.EnqueueMessageCheck(jid)
					synctest.Wait()
					if calls != i+1 || q.ActiveCount() != 0 {
						t.Fatalf("attempt %d: calls=%d active=%d", i, calls, q.ActiveCount())
					}
				}
			})
		})
	}
}
