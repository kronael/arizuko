package routd

// dead_letters_resource.go publishes routd's dead-lettered outbound on both
// faces (BUGS L23). It adds NO state: 'failed' is the dead-letter state, the
// row stays in `messages`, and `delivery_attempts` separates the causes (0:
// refused; the cap: retries ran out). Replay flips the row back to 'pending',
// which PendingOutbound selects, so the EXISTING sweep sends it — no second
// delivery path. Containment is by `routed_to`, resolved in the handler
// because it bounds ROWS, not the call; on the agent socket a missing folder
// is refused outright, on REST the Gate already proved instance-wide authority.

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

// deadLettersMCPNames is single-sourced in resreg/resources.
var deadLettersMCPNames = resources.DeadLettersMCPNames

// deadLettersResource is the single renderer for both faces. Store is non-nil
// so resreg runs the Gate and opens the replay tx.
func (s *Server) deadLettersResource() resreg.Resource {
	r := resreg.Mount(resources.DeadLettersName)
	r.Handler = s.deadLettersHandler
	r.Store = s.db.store()
	return r
}

// mountDeadLetters wires the REST face onto the same handler the agent tools use.
func (s *Server) mountDeadLetters(mux *http.ServeMux) {
	res := s.deadLettersResource()
	res.Gate = s.deadLettersRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(nil))
}

// deadLettersRESTGate requires dead_letters:read to look and dead_letters:write
// to replay. Its OWN scope pair, not messages:write: that is "speak as a
// channel", denied to the operator UI (the service:dashd ceiling), and
// requeueing one composed row is a far smaller authority. Neither is reachable
// by a human bearer: a user token's scope list holds FOLDER GLOBS, and
// auth.scopeMatches rejects a held value without a colon.
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
// agent's OWN folder: replaying one re-sends its own message.
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
		// Project to the declared RowType: core.Message carries no json tags,
		// and the /openapi.json schema is reflected off DeadLetterRow.
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
		// Containment before mutation: the id alone would let one tenant
		// re-send another's message.
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
			// Replayed by someone else between the read and the write, or the
			// sweep moved it; "ok" would claim a queueing this call did not do.
			return nil, resreg.Errorf(http.StatusConflict,
				"message %s is no longer dead-lettered", id)
		}
		return map[string]any{"ok": true, "id": id, "status": "pending"}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}
