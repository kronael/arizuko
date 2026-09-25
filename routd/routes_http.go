package routd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// replyExcerptChars bounds the parent text a reply carries: enough to identify
// the message the user answered, not a second copy of a long bot reply.
const replyExcerptChars = 200

func buildMessageRow(m apiv1.Message, ts time.Time, verb string) core.Message {
	// whapd sends a single attachment via the flat fields; fold it into the
	// array so the stored `attachments` column is always valid JSON — raw
	// base64 in the column silently lost 100% of WhatsApp media.
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
		// The originating adapter/account; LatestSource(jid) reads it so a reply
		// resolves back to the bot that received it (multi-account routing).
		Source:      m.Source,
		LinkContext: m.LinkContext,
	}
}

func toWireRoute(r core.Route) apiv1.Route {
	return apiv1.Route{
		ID: r.ID, Seq: r.Seq, Match: r.Match, Target: r.Target,
		ObserveWindowMessages: r.ObserveWindowMessages, ObserveWindowChars: r.ObserveWindowChars,
	}
}

// mountRoutes wires the /v1/routes operator REST face onto the SAME
// routesResource handler the agent's tools use; get-one stays a thin read
// (no agent twin).
func (s *Server) mountRoutes(mux *http.ServeMux) {
	// Per-target containment is ownsFolder on the JWT folder — its own rule,
	// not the agent face's grant-scope check; the self-default invariants stay
	// in the handler.
	contain := func(c resreg.Caller, _ resreg.Action, target string) error {
		if routeTargetOwned(c.Folder, target) {
			return nil
		}
		return resreg.Errorf(http.StatusForbidden, "route target outside caller subtree: %s", target)
	}
	res := s.routesResource(contain)
	res.Gate = s.routesRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(nil))
	mux.HandleFunc("GET /v1/routes/{id}", s.handleRouteGet)
}

// routesRESTGate is the REST twin of the agent socket's grant Gate: an any-of
// scope check only; containment lives in the contain seam. A nil Verifier is
// open.
func (s *Server) routesRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil
	}
	held := x.Caller.Scopes()
	want := []string{"routes:read", "routes:read:own_group"}
	if x.Action.Mutates() {
		want = []string{"routes:write", "routes:write:own_group"}
	}
	if !hasAnyScope(held, want) {
		return resreg.Errorf(http.StatusForbidden, "missing scope %s", strings.Join(want, " or "))
	}
	return nil
}

// routeTargetOwned reports whether an operator whose JWT folder is jwtFolder
// may manage a route at target: empty owns everything, else own-or-descendant.
// The `#observe`/`#announce` fragment comes off first; comparing the raw
// string hid a folder's own `<folder>#observe` rows from it (BUGS L82).
func routeTargetOwned(jwtFolder, target string) bool {
	return ownsFolder(jwtFolder, core.ParseRouteTarget(target).Folder)
}

// handleRouteGet is the thin get-one read (GET /v1/routes/{id}); a scoped
// caller cannot read a route outside its subtree — 404, no existence leak.
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
	if errors.Is(err, ErrNoRoute) {
		writeErr(w, 404, "not_found", "route not found")
		return
	}
	if errors.Is(err, store.ErrRouteIDAmbiguous) {
		writeErr(w, 409, "route_id_ambiguous", err.Error())
		return
	}
	if err != nil {
		slog.Error("route lookup failed", "id", id, "err", err)
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	if !ownsFolder(folder, rt.Target) {
		writeErr(w, 404, "not_found", "route not found")
		return
	}
	writeJSON(w, 200, toWireRoute(rt))
}
