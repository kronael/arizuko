package routd

// dead_letters_resource.go publishes routd's dead-lettered outbound on both
// faces: the operator REST face at GET /v1/dead_letters plus
// POST /v1/dead_letters/{id}/replay, and the agent socket's
// `list_dead_letters` / `replay_dead_letter`.
//
// Why a resource at all: dashd's routd page rendered a `dead-lettered` COUNT
// and nothing else, so an operator could see THAT delivery failed and not what
// was lost, to whom, or with what error — and had no way to send it again
// (BUGS L23). A count is a symptom readout, not a recovery path.
//
// It adds NO state. 'failed' is already the dead-letter state (core/types.go):
// the row stays in `messages` with its content, and `delivery_attempts`
// separates the two causes — 0 means the platform refused it permanently, the
// cap means the retries ran out. Replay flips the row back to 'pending' with a
// fresh budget, which is precisely what PendingOutbound selects, so routd's
// EXISTING outbound sweep does the sending. There is no second delivery path.
//
// Containment is by `routed_to`, the folder the message was routed to, and it
// is resolved in the handler rather than either Gate because it bounds ROWS,
// not the call: a folder-claimed caller is pinned to its own subtree and the
// `folder` argument cannot widen it. On the agent socket a missing folder is
// refused outright — the socket folder is supposed to be the agent's group, and
// an empty one means containment is unknown, which is the last state in which
// to hand over other tenants' undelivered messages. REST keeps the opposite
// default because its Gate already proved instance-wide authority through a
// scope no folder-bound principal can hold, the same reading the audit face
// uses.

import (
	"context"
	"net/http"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

const actionReplay = resreg.Action("replay")

// deadLettersMCPNames is the action→flat-tool-name map, single-sourced in
// resreg/resources. Aliased here for the Gate's action→policy-name lookup.
var deadLettersMCPNames = resources.DeadLettersMCPNames

// deadLettersResource is the single renderer for both faces. Store is non-nil
// so resreg runs the Gate and opens the replay tx.
func (s *Server) deadLettersResource() resreg.Resource {
	r := resreg.Mount(resources.DeadLettersName)
	r.Handler = s.deadLettersHandler
	r.Store = s.db.store()
	return r
}

// mountDeadLetters wires the REST face onto the same handler the agent tools
// use.
func (s *Server) mountDeadLetters(mux *http.ServeMux) {
	res := s.deadLettersResource()
	res.Gate = s.deadLettersRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(nil))
}

// deadLettersRESTGate requires dead_letters:read to look and
// dead_letters:write to replay.
//
// Its OWN scope pair, not messages:read/messages:write. The rows live in
// `messages`, but messages:write is "speak as a channel" — it authorizes
// posting arbitrary inbound at POST /v1/messages — and the operator UI that
// needs this surface is explicitly denied that (authd serviceGrants, the
// service:dashd ceiling). Requeueing one already-composed row is a far smaller
// authority than composing any row, and the scope has to say so. Same shape as
// pending_actions:read/write.
//
// Like those, neither is reachable by a human bearer: a user token's scope list
// holds FOLDER GLOBS and auth.scopeMatches rejects a held value without a
// colon, so `acme/**` and an operator's own `**` both fail.
func (s *Server) deadLettersRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil // local-dev open mode, mirrors s.authz
	}
	verb := "read"
	if x.Action.Mutates() {
		verb = "write"
	}
	if !auth.HasScope(x.Caller.Scopes(), "dead_letters", verb) {
		return resreg.Errorf(http.StatusForbidden, "missing scope dead_letters:%s", verb)
	}
	return nil
}

// deadLettersPostBuild mounts the agent tools. The authorization target is the
// agent's OWN folder: an agent asking which of its replies never landed is
// asking about its own folder, and replaying one re-sends its own message.
func (s *Server) deadLettersPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.deadLettersResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := deadLettersMCPNames[x.Action]
		if !authorize(folder, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden,
				"%s acts on this folder's undelivered messages: not permitted", name)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

func (s *Server) deadLettersHandler(_ context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		folder := x.Args.Str("folder")
		if x.Surface == audit.SurfaceMCP && x.Caller.Folder == "" {
			return nil, resreg.Errorf(http.StatusForbidden,
				"list_dead_letters needs a folder-bound socket")
		}
		if x.Caller.Folder != "" {
			folder = x.Caller.Folder
		}
		msgs, err := s.db.DeadLetters(folder, int(x.Args.Int("limit")))
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		// Project to the declared RowType. core.Message carries no json tags,
		// so returning it directly ships Go field names in a body whose
		// /openapi.json schema is reflected off DeadLetterRow — the two
		// declarations would disagree on every field name.
		rows := make([]resources.DeadLetterRow, len(msgs))
		for i, m := range msgs {
			rows[i] = resources.DeadLetterRow{
				ID: m.ID, ChatJID: m.ChatJID, Content: m.Content,
				Timestamp: m.Timestamp.UTC().Format(time.RFC3339Nano),
				Topic:     m.Topic, RoutedTo: m.RoutedTo, Source: m.Source,
				TurnID: m.TurnID, DeliveryAttempts: m.Attempts,
			}
		}
		return rows, nil
	case actionReplay:
		id := x.Args.Str("id")
		if id == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "id required")
		}
		// Containment before mutation: a caller bound to a folder may only
		// replay a message routed INTO that subtree. Without this the id alone
		// would let one tenant re-send another's message.
		msg, ok := s.db.DeadLetterByID(id)
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "no dead-lettered message %s", id)
		}
		if x.Caller.Folder != "" && !ownsFolder(x.Caller.Folder, msg.RoutedTo) {
			return nil, resreg.Errorf(http.StatusForbidden,
				"message %s is routed outside the caller subtree", id)
		}
		moved, err := s.db.ReplayDeadLetter(msg)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if !moved {
			// Someone replayed it between the read and the write, or the sweep
			// already moved it. Saying "ok" would claim a queueing that this
			// call did not do.
			return nil, resreg.Errorf(http.StatusConflict,
				"message %s is no longer dead-lettered", id)
		}
		return map[string]any{"ok": true, "id": id, "status": "pending"}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}
