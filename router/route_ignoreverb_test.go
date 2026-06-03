package router

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// A verb-scoped route must match a verb="message" message when the verb
// predicate is ignored (send-authz path), but stay strict under RouteMatches.
func TestRouteMatchesIgnoreVerb(t *testing.T) {
	r := core.Route{Match: "chat_jid=slack:T/channel/C0B4 verb=mention"}
	msg := core.Message{ChatJID: "slack:T/channel/C0B4", Verb: "message"}

	if RouteMatches(r, msg) {
		t.Fatal("RouteMatches: verb=mention route must not match a verb=message msg")
	}
	if !RouteMatchesIgnoreVerb(r, msg) {
		t.Fatal("RouteMatchesIgnoreVerb: verb=mention route must match when verb is ignored")
	}
	// Non-verb predicates still filter — a different chat_jid must not match.
	other := core.Message{ChatJID: "slack:T/channel/OTHER", Verb: "message"}
	if RouteMatchesIgnoreVerb(r, other) {
		t.Fatal("RouteMatchesIgnoreVerb: chat_jid predicate must still apply")
	}
}
