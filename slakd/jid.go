package main

import (
	"fmt"

	"github.com/kronael/arizuko/chanlib"
)

// JID format: slack:<workspace>/<kind>/<id> where kind ∈ {channel, dm, group}.
// Parsing lives in chanlib so the container runner shares one parser.

type jidParts struct {
	workspace string
	kind      string
	id        string
}

func parseJID(jid string) (jidParts, error) {
	p, ok := chanlib.ParseSlackJID(jid)
	if !ok {
		return jidParts{}, fmt.Errorf("slakd: malformed slack jid: %q", jid)
	}
	return jidParts{workspace: p.Workspace, kind: p.Kind, id: p.ID}, nil
}

func formatJID(workspace, kind, id string) string {
	return chanlib.FormatSlackJID(workspace, kind, id)
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
