package routd

import (
	"context"
	"net/http"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func (s *Server) chatsResource() resreg.Resource {
	r := resreg.Mount(resources.ChatsName)
	r.Store = s.db.store()
	r.Handler = s.chatsHandler
	r.Authz = func(_ resreg.Caller, _ resreg.Action, args resreg.Args) (string, map[string]string, error) {
		jid := args.Str("jid")
		if jid == "" {
			return "", nil, resreg.Errorf(http.StatusBadRequest, "jid required")
		}
		folder := s.db.DefaultFolderForJID(jid)
		if folder == "" {
			return "", nil, resreg.Errorf(http.StatusNotFound, "no route for chat %s", jid)
		}
		return folder, map[string]string{"jid": jid}, nil
	}
	return r
}

func (s *Server) mountChats(mux *http.ServeMux) {
	resreg.RegisterREST(mux, s.chatsResource(), s.restCaller(nil))
}

func (s *Server) chatsPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	r := s.chatsResource()
	r.Gate = func(x resreg.Execution, target string, params map[string]string) error {
		if !authorize(target, "mcp:"+resources.ChatsMCPNames[x.Action], params) {
			return resreg.Errorf(http.StatusForbidden, "forbidden")
		}
		return nil
	}
	return mountAgentResource(r, callerSub, folder, visible)
}

func (s *Server) chatsHandler(_ context.Context, x resreg.Execution) (any, error) {
	jid := x.Args.Str("jid")
	var on bool
	var err error
	switch x.Action {
	case resreg.ActionGet:
		on, err = s.db.store().ChatThreadReplies(jid)
	case resreg.ActionUpdate:
		var pref *bool
		if value, exists := x.Args["thread_replies"]; exists && value != nil {
			v, ok := value.(bool)
			if !ok {
				return nil, resreg.Errorf(http.StatusBadRequest, "thread_replies must be a boolean or null")
			}
			pref = &v
			on = v
		} else {
			on, err = s.db.store().ChatIsGroup(jid)
		}
		if err == nil {
			err = s.db.store().SetChatThreadRepliesInto(x.Writes, jid, pref)
		}
	default:
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return resources.ChatSettingsResponse{JID: jid, ThreadReplies: on}, nil
}
