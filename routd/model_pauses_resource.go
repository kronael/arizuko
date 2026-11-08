package routd

import (
	"context"
	"net/http"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func (s *Server) modelPausesResource() resreg.Resource {
	r := resreg.Mount(resources.ModelPausesName)
	r.Store, r.Handler = s.db.store(), s.modelPausesHandler
	return r
}

func (s *Server) mountModelPauses(mux *http.ServeMux) {
	r := s.modelPausesResource()
	r.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		verb := "read"
		if x.Action.Mutates() {
			verb = "write"
		}
		if s.verify == nil || (x.Caller.Folder == "" &&
			(s.db.IsOperator(x.Caller.Sub) || (x.Caller.Sub == "service:dashd" &&
				auth.HasScope(x.Caller.Scopes(), resources.ModelPausesName, verb)))) {
			return nil
		}
		return resreg.Errorf(http.StatusForbidden, "model pauses require an operator")
	}
	resreg.RegisterREST(mux, r, s.restCaller(nil))
}

func (s *Server) modelPausesPostBuild(folder, caller string, authorize authorizeFn,
	visible func(string) bool) func(*mcpserver.MCPServer) {
	r := s.modelPausesResource()
	r.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		if authorize("**", "admin", nil) &&
			authorize("**", "mcp:"+resources.ModelPausesMCPNames[x.Action], nil) {
			return nil
		}
		return resreg.Errorf(http.StatusForbidden, "model pauses require an operator")
	}
	return mountAgentResource(r, caller, folder, visible)
}

func (s *Server) modelPausesHandler(ctx context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		rows := []resources.ModelPauseRow{}
		pauses, err := s.db.store().ListModelPauses(ctx)
		if err != nil {
			return nil, err
		}
		jids, err := s.db.ChatJIDs()
		if err != nil {
			return nil, err
		}
		loop := s.loop
		if loop == nil {
			loop = &Loop{db: s.db}
		}
		for _, p := range pauses {
			n := 0
			for _, jid := range jids {
				msgs, err := s.db.NewMessagesForChat(jid, s.db.GetAgentCursor(jid), 0)
				if err != nil {
					return nil, err
				}
				if newestUserIdx(msgs) < 0 {
					continue
				}
				batch := loop.resolveBatch(jid, msgs)
				for i, m := range msgs {
					if !m.BotMsg && batch.rows[i].ok && batch.rows[i].Folder == p.Folder {
						n++
					}
				}
			}
			row := resources.ModelPauseRow{Folder: p.Folder, Reason: p.Reason, OpenedAt: p.OpenedAt,
				Attempts: p.Attempts, Evidence: p.Evidence, ProbeTurnID: p.ProbeTurnID,
				ProbeAttemptID: p.ProbeAttemptID, QueuedCount: n}
			if p.NextAttemptAt != "" {
				row.NextAttemptAt = &p.NextAttemptAt
			}
			rows = append(rows, row)
		}
		return rows, nil
	case resreg.Action("clear"):
		p, err := s.db.store().ReadModelPause(ctx, x.Args.Str("folder"))
		if err != nil {
			return nil, err
		}
		if p == nil || p.OpenedAt != x.Args.Str("opened_at") ||
			p.ProbeAttemptID != x.Args.Str("probe_attempt_id") {
			return nil, resreg.Errorf(http.StatusConflict, "model pause changed; list it again")
		}
		err = s.db.store().ClearModelPauseInto(x.Writes, *p,
			resreg.Errorf(http.StatusConflict, "model pause changed; list it again"))
		return map[string]bool{"ok": true}, err
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown model pause action")
}
