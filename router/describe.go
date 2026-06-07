package router

import (
	"strings"

	"github.com/kronael/arizuko/core"
)

// RouteView is a core.Route plus derived, self-documenting fields. An agent
// reading the routing table sees intent — does this rule fire a turn, and on
// what? — instead of having to know that a bare target triggers, a #observe
// fragment is silent, and a verb=mention predicate gates on mentions.
type RouteView struct {
	core.Route
	Mode       string `json:"mode"`                  // "trigger" | "observe"
	FiresTurn  bool   `json:"fires_turn"`            // observe rows never fire a turn
	TriggersOn string `json:"triggers_on,omitempty"` // "every message" | "verb=mention" | ...
	Explain    string `json:"explain"`               // one-line plain description
	ShadowedBy int64  `json:"shadowed_by,omitempty"` // id of an earlier rule that intercepts this one (first-match-wins)
}

// Describe annotates routes (assumed in seq order, as store.ListRoutes returns)
// with derived fields. Shadow detection simulates each rule's representative
// message against the earlier rules: if an earlier rule already matches it,
// this rule is unreachable for that traffic and would never fire — verify
// before deleting, since a glob rule's representative under-approximates.
func Describe(routes []core.Route) []RouteView {
	out := make([]RouteView, len(routes))
	for i, r := range routes {
		rt := core.ParseRouteTarget(r.Target)
		v := RouteView{Route: r}
		if rt.Mode == "observe" {
			v.Mode = "observe"
			v.Explain = "observes silently (no turn); visible as <observed> context → " + rt.Folder
		} else {
			v.Mode = "trigger"
			v.FiresTurn = true
			switch verb := matchVerb(r.Match); verb {
			case "":
				v.TriggersOn = "every message"
				v.Explain = "fires a turn on EVERY message → " + rt.Folder
			case "mention":
				v.TriggersOn = "verb=mention"
				v.Explain = "fires a turn on @mention → " + rt.Folder
			default:
				v.TriggersOn = "verb=" + verb
				v.Explain = "fires a turn on verb=" + verb + " → " + rt.Folder
			}
		}
		if rt.Topic != "" {
			v.Explain += " (pins topic #" + rt.Topic + ")"
		}
		synth := synthMessage(r)
		for j := 0; j < i; j++ {
			if RouteMatches(routes[j], synth) && expandTarget(routes[j].Target, synth) != "" {
				v.ShadowedBy = routes[j].ID
				break
			}
		}
		out[i] = v
	}
	return out
}

func matchVerb(match string) string {
	for _, f := range strings.Fields(match) {
		if k, val, ok := strings.Cut(f, "="); ok && k == "verb" {
			return val
		}
	}
	return ""
}

// synthMessage builds a representative message that route r matches, so it can
// be tested against earlier routes for shadowing. Exact predicates reproduce
// verbatim; glob metachars collapse to a filler token, so the result
// under-approximates a glob rule's true match set (never over-claims a shadow).
func synthMessage(r core.Route) core.Message {
	pred := map[string]string{}
	for _, f := range strings.Fields(r.Match) {
		if k, val, ok := strings.Cut(f, "="); ok && k != "" {
			pred[k] = val
		}
	}
	var msg core.Message
	if cj, ok := pred["chat_jid"]; ok {
		msg.ChatJID = fillGlob(cj)
	} else {
		plat := "x"
		if p := pred["platform"]; p != "" {
			plat = fillGlob(p)
		}
		msg.ChatJID = plat + ":" + fillGlob(pred["room"])
	}
	msg.Sender = fillGlob(pred["sender"])
	msg.Verb = fillGlob(pred["verb"])
	return msg
}

func fillGlob(pat string) string {
	if pat == "" {
		return ""
	}
	return strings.NewReplacer("*", "x", "?", "x").Replace(pat)
}
