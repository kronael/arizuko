package main

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/resreg"
)

// POST /v1/onboarding says created only for the call that made the row, so
// routd sends one notice per request, not one per message.
func TestOnboardingCreateReportsCreated(t *testing.T) {
	a := &admin{st: testDB(t)}
	post := func() bool {
		out, err := a.onboardingHandler(context.Background(), resreg.Execution{
			Action: resreg.ActionCreate,
			Args:   map[string]any{"jid": "telegram:user/7", "sender": "telegram:user/7"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]bool)["created"]
	}
	if !post() {
		t.Fatal("first request not reported created")
	}
	if post() {
		t.Fatal("second message of the same request reported created")
	}
}
