package runed

import (
	"context"

	routdv1 "github.com/kronael/arizuko/routd/api/v1"
)

// Federator forwards the agent's message tools to routd's
// /v1/turns/{turn_id}/* — runed is a sink-router, never an appender (spec
// 5/P § MCP host + tool federation). The PINNED verb→path mapping lives
// here, identical to routd's verb→path exceptions (spec 5/E):
//   - most tools map name → /<name> (reply, send, like, edit, delete, …)
//   - send_file → /document
//   - dislike   → /like with reaction="👎" (no /dislike endpoint)
//   - pin_message → /pin, unpin_message → /unpin (strip _message)
//   - unpin_all → /unpin with all:true
//
// runed stamps {turn_id} (it owns the socket↔spawn binding) and forwards
// the agent's brokered token verbatim — never re-signs, never re-scopes.
type Federator struct {
	routd *routdv1.Client
}

// NewFederator builds the forward against routd's callback base URL.
func NewFederator(routdURL string) *Federator {
	return &Federator{routd: routdv1.NewClient(routdURL)}
}

// Forward routes one agent tool call to its routd endpoint. tool is the MCP
// tool name; turnID + token are stamped by runed. body is the tool's
// arguments already shaped to the routd request type. The PINNED exceptions
// are resolved here so neither side drifts.
func (f *Federator) Forward(ctx context.Context, tool, turnID, token, idemKey string, body map[string]any) (any, error) {
	switch tool {
	case "reply":
		return f.routd.Reply(ctx, turnID, token, idemKey, replyReq(body))
	case "send":
		return f.routd.Send(ctx, turnID, token, idemKey, replyReq(body))
	case "send_file": // → /document
		return f.routd.Document(ctx, turnID, token, idemKey, routdv1.DocumentRequest{
			JID: str(body, "jid"), Path: str(body, "path"), Name: str(body, "name"),
			Caption: str(body, "caption"), ReplyToID: str(body, "reply_to_id"),
		})
	case "like":
		return nil, f.routd.Like(ctx, turnID, token, idemKey, reactReq(body))
	case "dislike": // → /like reaction=👎 (no /dislike endpoint)
		r := reactReq(body)
		r.Reaction = "👎"
		return nil, f.routd.Like(ctx, turnID, token, idemKey, r)
	case "edit":
		return nil, f.routd.Verb(ctx, turnID, "edit", token, idemKey, body)
	case "delete":
		return nil, f.routd.Verb(ctx, turnID, "delete", token, idemKey, body)
	case "pin_message": // strip _message → /pin
		return nil, f.routd.Verb(ctx, turnID, "pin", token, idemKey, body)
	case "unpin_message": // strip _message → /unpin
		return nil, f.routd.Verb(ctx, turnID, "unpin", token, idemKey, body)
	case "unpin_all": // → /unpin all:true
		body["all"] = true
		return nil, f.routd.Verb(ctx, turnID, "unpin", token, idemKey, body)
	default:
		// get_history / get_thread and bare verbs fall through to the
		// direct name→path map.
		return nil, f.routd.Verb(ctx, turnID, tool, token, idemKey, body)
	}
}

// Result forwards the agent's submit_turn to routd /v1/turns/{id}/result.
func (f *Federator) Result(ctx context.Context, turnID, token, idemKey string, r routdv1.TurnResult) (routdv1.TurnResultAck, error) {
	return f.routd.Result(ctx, turnID, token, idemKey, r)
}

func replyReq(b map[string]any) routdv1.ReplyRequest {
	return routdv1.ReplyRequest{JID: str(b, "jid"), Text: str(b, "text"), ReplyToID: str(b, "reply_to_id")}
}

func reactReq(b map[string]any) routdv1.ReactionRequest {
	return routdv1.ReactionRequest{JID: str(b, "jid"), PlatformID: str(b, "platform_id"), Reaction: str(b, "reaction")}
}

func str(b map[string]any, k string) string {
	if v, ok := b[k].(string); ok {
		return v
	}
	return ""
}
