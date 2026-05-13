package main

import (
	"fmt"
	"strings"
)

// JID format: slack:<workspace>/<kind>/<id> where kind ∈ {channel, dm, group}.

type jidParts struct {
	workspace string
	kind      string
	id        string
}

func parseJID(jid string) (jidParts, error) {
	rest := strings.TrimPrefix(jid, "slack:")
	if rest == jid {
		return jidParts{}, fmt.Errorf("slakd: jid missing slack: prefix")
	}
	ws, after, ok := strings.Cut(rest, "/")
	if !ok || ws == "" {
		return jidParts{}, fmt.Errorf("slakd: jid missing workspace segment: %q", jid)
	}
	kind, id, ok := strings.Cut(after, "/")
	if !ok || kind == "" || id == "" {
		return jidParts{}, fmt.Errorf("slakd: jid missing kind/id segment: %q", jid)
	}
	switch kind {
	case "channel", "dm", "group":
	default:
		return jidParts{}, fmt.Errorf("slakd: jid kind must be channel|dm|group, got %q", kind)
	}
	return jidParts{workspace: ws, kind: kind, id: id}, nil
}

func formatJID(workspace, kind, id string) string {
	return "slack:" + workspace + "/" + kind + "/" + id
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
