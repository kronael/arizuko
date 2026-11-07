package store

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestRouteKeyValidatesTarget(t *testing.T) {
	for _, target := range []string{"team?bad", "team#{unknown}"} {
		if _, err := routeKey(core.Route{Target: target, Match: "platform=slack"}); err == nil {
			t.Errorf("accepted %q", target)
		}
	}
}
