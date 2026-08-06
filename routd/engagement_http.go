package routd

// engagement_http.go is routd's REST face for the spec 5/G engagement window —
// the operator twin of the agent's hand-authored engage/disengage MCP tools.
//
// Engagement is HOT-TIER conversational state, not a cold-tier management
// entity, so it deliberately does NOT become a resreg resource (spec 5/17
// §"The model: two tiers"): engage/disengage already exist as hand-authored
// tools carrying a three-arm authorization (ipc/ipc.go), and promoting the
// table would put a second path into the same seam. This pair is extended
// instead — BUGS F12a shape (a).
//
// GET /v1/engagement?jid=&topic= reads one pair; GET /v1/engagement with no jid
// LISTS the live windows the caller may see. POST engages, and ttl_seconds<=0
// disengages.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kronael/arizuko/audit"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func (s *Server) handleEngagementGet(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, scopeRoutesRead...)
	if !ok {
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		s.listEngagement(w, folder)
		return
	}
	if !s.ownsJID(folder, jid) {
		denyCrossFolder(w, jid)
		return
	}
	topic := r.URL.Query().Get("topic")
	// last_reply_id is read separately and unconditionally: it is the thread
	// anchor, which outlives the engagement window, so an idle pair still
	// reports one. engaged_until/folder are empty when the window is not live.
	out := apiv1.EngagementResponse{LastReplyID: s.db.LastReplyID(jid, topic)}
	if e, live := s.db.Engagement(jid, topic); live {
		out.Folder = e.Folder
		out.EngagedUntil = e.Until.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(w, 200, out)
}

// listEngagement answers GET /v1/engagement with no jid: every LIVE window the
// caller may see, so an operator can find engaged chats instead of only looking
// up a jid already known.
//
// The list-all key is an EMPTY folder claim — a root/service token — and NOTHING
// else. A top-level tenant carries a non-empty folder however shallow it is, so
// keying widening on depth would hand it every other tenant's chats; that is the
// exact leak the 5/16 REST folds had to close. Per-row containment then runs in
// DB.ListEngaged.
func (s *Server) listEngagement(w http.ResponseWriter, folder string) {
	live, err := s.db.ListEngaged(folder, folder == "")
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := apiv1.EngagementListResponse{Engaged: make([]apiv1.EngagedChat, len(live))}
	for i, e := range live {
		out.Engaged[i] = apiv1.EngagedChat{
			JID:          e.JID,
			Topic:        e.Topic,
			Folder:       e.Folder,
			EngagedUntil: e.Until.UTC().Format(time.RFC3339Nano),
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleEngagementSet(w http.ResponseWriter, r *http.Request) {
	sub, folder, ok := s.authz(w, r, scopeRoutesWrite...)
	if !ok {
		return
	}
	var req apiv1.EngagementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if req.JID == "" {
		writeErr(w, 400, "bad_request", "jid required")
		return
	}
	if !s.ownsJID(folder, req.JID) {
		denyCrossFolder(w, req.JID)
		return
	}
	if !ownsFolder(folder, req.Folder) {
		denyCrossFolder(w, req.Folder)
		return
	}
	// A LIVE window belongs to the folder that CLAIMED it, and that owner is what
	// the write is contained on — the same predicate the list read applies per
	// row, so the write face can never reach a window the read face would not
	// show. The two checks above are not that: ownsJID resolves the jid's ROUTE
	// TARGET, so a folder that is merely a route target for the chat could
	// otherwise clear a window claimed by a sibling it cannot see, and ownsFolder
	// only bounds the folder the caller is asking to write.
	if cur, live := s.db.Engagement(req.JID, req.Topic); live && !ownsFolder(folder, cur.Folder) {
		denyCrossFolder(w, req.JID)
		return
	}
	// TTLSeconds<=0 is the disengage path: SetEngagement with a zero/past
	// deadline clears the live window (Engaged checks until > now).
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = -time.Second
	}
	if err := s.db.SetEngagementAudited(r.Context(), req.JID, req.Topic, req.Folder, ttl,
		engagementEvent(sub, req)); err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// engagementEvent renders the audit_log row for one /v1/engagement write.
//
// Two actions, not one with a flag: an operator reading the trail is looking for
// who ENDED a conversation the bot was in, and `engagement.clear` is the term
// they grep for. Category `mutation` — this is an operator-originated change to
// a routd table, the "what changed" trail, not the per-turn `agent` band the
// engage/disengage tools write to.
func engagementEvent(sub string, req apiv1.EngagementRequest) audit.Event {
	action := "engagement.set"
	if req.TTLSeconds <= 0 {
		action = "engagement.clear"
	}
	resource := "engagement/" + req.JID
	if req.Topic != "" {
		resource += "/" + req.Topic
	}
	actor := sub
	if actor == "" {
		actor = "system" // unverified local-dev: authz returns an empty sub
	}
	return audit.Event{
		Category: audit.CategoryMutation,
		Action:   action,
		Actor:    actor,
		ActorSub: sub,
		Surface:  audit.SurfaceREST,
		Resource: resource,
		Folder:   req.Folder,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"jid":         req.JID,
			"topic":       req.Topic,
			"ttl_seconds": req.TTLSeconds,
		},
	}
}
