package main

import (
	"fmt"

	"github.com/kronael/arizuko/chanlib"
)

// JID format: slack:<workspace>/<kind>/<id> where kind ∈ {channel, dm, group}.
// Parsing lives in chanlib so the container runner shares one parser.

func parseJID(jid string) (chanlib.SlackJID, error) {
	p, ok := chanlib.ParseSlackJID(jid)
	if !ok {
		return chanlib.SlackJID{}, fmt.Errorf("slakd: malformed slack jid: %q", jid)
	}
	return p, nil
}

func chanKind(isIM, isMpim bool) string {
	switch {
	case isIM:
		return "dm"
	case isMpim:
		return "group"
	default:
		return "channel"
	}
}
