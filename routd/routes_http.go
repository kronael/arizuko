package routd

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// buildMessageRow maps the ingress wire Message to a core.Message row.
func buildMessageRow(m apiv1.Message, ts time.Time, verb string) core.Message {
	// whapd sends a single attachment via the flat fields (its only shape); fold
	// it into the array so the stored `attachments` column is always valid JSON
	// that enrich.go can unmarshal. gated did this (api.go:271); the split dropped
	// it → the raw base64 landed in the column, json.Unmarshal failed, and 100% of
	// WhatsApp media was silently lost (never downloaded/transcribed).
	if m.Attachment != "" {
		m.Attachments = append([]apiv1.Attachment{{
			Mime: m.AttachmentMime, Filename: m.AttachmentName, Data: m.Attachment,
		}}, m.Attachments...)
	}
	atts := ""
	if len(m.Attachments) > 0 {
		if raw, err := json.Marshal(m.Attachments); err == nil {
			atts = string(raw)
		}
	}
	return core.Message{
		ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, Name: m.SenderName,
		Content: m.Content, Timestamp: ts, ReplyToID: m.ReplyTo,
		ReplyToText: m.ReplyToText, ReplyToSender: m.ReplyToSender,
		ForwardedFrom: m.ForwardedFrom,
		Topic:         m.Topic, Verb: verb, Attachments: atts, ChatName: m.ChatName,
		// Source = the originating adapter/account; LatestSource(jid) reads it so a
		// reply resolves back to the bot that received it (multi-account routing).
		Source:      m.Source,
		LinkContext: m.LinkContext,
		Status:      core.MessageStatusSent,
	}
}

func toWireRoute(r core.Route) apiv1.Route {
	return apiv1.Route{
		ID: r.ID, Seq: r.Seq, Match: r.Match, Target: r.Target,
		ObserveWindowMessages: r.ObserveWindowMessages, ObserveWindowChars: r.ObserveWindowChars,
	}
}

// mountRoutes wires the /v1/routes operator REST face onto the SAME routesResource
// handler the agent's four routing tools use (spec 5/44 fold): add/set/list/delete
// ride resreg.RegisterREST with a REST caller + gate injected; get-one stays a thin
// read (handleRouteGet — no agent twin, its own folder scoping).
func (s *Server) mountRoutes(mux *http.ServeMux) {
	// Operator/human REST face: per-target containment is ownsFolder on the caller's
	// JWT folder (own-or-descendant) — NOT the agent tier model. This is what confines
	// a top-level tenant (tier 0, for which the tier cap is a no-op) to its own subtree
	// and lets an operator manage its whole subtree (which the strict-descendant tier
	// cap wrongly denied). The route's own-folder / self-default invariants stay in the
	// handler.
	contain := func(c resreg.Caller, _ resreg.Action, target string) error {
		if routeTargetOwned(c.Folder, target) {
			return nil
		}
		return resreg.Errorf(http.StatusForbidden, "route target outside caller subtree: %s", target)
	}
	res := s.routesResource(contain)
	res.Gate = s.routesRESTGate
	resreg.RegisterREST(mux, res, s.routesRESTCaller)
	mux.HandleFunc("GET /v1/routes/{id}", s.handleRouteGet)
}

// routesRESTCaller builds the REST Caller for the shared routes handler. That
// handler reads x.Caller.Folder as the caller's OWN folder (tier source + set/list
// scoping); for the operator that IS the JWT folder. Held scopes ride in Claims for
// routesRESTGate. A nil Verifier is open (local-dev/tests): empty principal + empty
// folder (root — unrestricted, list spans all), mirroring s.authz.
func (s *Server) routesRESTCaller(r *http.Request) (resreg.Caller, error) {
	var sub, folder string
	var scope []string
	if s.verify != nil {
		var err error
		sub, scope, folder, err = s.verify.Verify(r)
		if err != nil {
			return resreg.Caller{}, err
		}
	}
	return resreg.Caller{
		Sub:    sub,
		Folder: folder,
		Claims: map[string]string{"scopes": strings.Join(scope, " ")},
	}, nil
}

// routesRESTGate is the operator/human REST twin of the agent socket's grant Gate:
// an any-of scope check (routes:read for GET, routes:write for mutations). Per-target
// ownsFolder containment now lives in the handler's injected contain seam (mountRoutes),
// so this gate carries only the scope decision. A nil Verifier is open.
func (s *Server) routesRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil
	}
	held := strings.Fields(x.Caller.Claims["scopes"])
	want := []string{"routes:read", "routes:read:own_group"}
	if x.Action.Mutates() {
		want = []string{"routes:write", "routes:write:own_group"}
	}
	if !hasAnyScope(held, want) {
		return resreg.Errorf(http.StatusForbidden, "missing scope %s", strings.Join(want, " or "))
	}
	return nil
}

// routeTargetOwned reports whether an operator whose JWT folder is jwtFolder may
// manage a route pointing at target. Empty jwtFolder (root / service token) owns
// everything; otherwise target's folder (a leading `folder:` prefix stripped) must
// be jwtFolder or a descendant — the retired REST's ownsFolder containment. A
// daemon:/builtin: target is owned only by root.
func routeTargetOwned(jwtFolder, target string) bool {
	return ownsFolder(jwtFolder, strings.TrimPrefix(target, "folder:"))
}

// handleRouteGet is the thin get-one REST read (GET /v1/routes/{id}); it has no
// agent twin, so it stays hand-rolled with its own routes:read scope + folder
// containment (a scoped caller can't read a route outside its subtree — 404, no
// existence leak).
func (s *Server) handleRouteGet(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, "routes:read", "routes:read:own_group")
	if !ok {
		return
	}
	id, ok := atoi64(r.PathValue("id"))
	if !ok {
		writeErr(w, 400, "bad_request", "non-numeric id")
		return
	}
	rt, err := s.db.GetRoute(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "route not found")
		return
	}
	if err != nil {
		slog.Error("route lookup failed", "id", id, "err", err)
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if !ownsFolder(folder, rt.Target) { // scoped caller can't GET a route outside its subtree
		writeErr(w, 404, "not_found", "route not found")
		return
	}
	writeJSON(w, 200, toWireRoute(rt))
}
