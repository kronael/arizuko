package chanlib

import "strings"

// SlackJID is a parsed Slack JID: slack:<workspace>/<kind>/<id>, kind one of
// channel, dm, group.
type SlackJID struct {
	Workspace string
	Kind      string
	ID        string
}

// ParseSlackJID returns the parsed parts and ok=true on a well-formed JID.
// The single parser — no per-package reimplementations.
func ParseSlackJID(jid string) (parts SlackJID, ok bool) {
	rest := strings.TrimPrefix(jid, "slack:")
	if rest == jid {
		return SlackJID{}, false
	}
	ws, after, ok := strings.Cut(rest, "/")
	if !ok || ws == "" {
		return SlackJID{}, false
	}
	kind, id, ok := strings.Cut(after, "/")
	if !ok || kind == "" || id == "" {
		return SlackJID{}, false
	}
	switch kind {
	case "channel", "dm", "group":
	default:
		return SlackJID{}, false
	}
	return SlackJID{Workspace: ws, Kind: kind, ID: id}, true
}

// FormatSlackJID is the inverse of ParseSlackJID.
func FormatSlackJID(workspace, kind, id string) string {
	return "slack:" + workspace + "/" + kind + "/" + id
}
