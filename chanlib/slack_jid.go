package chanlib

import "strings"

// SlackJID is a parsed Slack JID. Format: slack:<workspace>/<kind>/<id>
// where kind is one of channel, dm, group. Kept as a value type so
// callers can ignore fields they don't need.
type SlackJID struct {
	Workspace string
	Kind      string
	ID        string
}

// ParseSlackJID returns the parsed parts and ok=true on a well-formed JID.
// ok=false signals a parse failure; the caller decides whether that's an
// error (slakd) or just a "skip per-surface routing" hint (container).
// Single source of truth — no per-package reimplementations.
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
