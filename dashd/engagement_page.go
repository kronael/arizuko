package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// handleEngagement renders GET /dash/engagement/ — which conversations the
// agent is currently staying in without being addressed, and the control that
// ends one early (spec 5/G item 6, BUGS F31).
//
// It reads AND writes routd's /v1/engagement over HTTP rather than
// chat_reply_state out of dbRoutd. routd owns those columns, applies the folder
// containment, and writes the audit row inside the mutation's own transaction —
// none of which a direct-DB write from here would do, which is why the
// direct-DB read is the open defect this page declines to join. Operator-only,
// like /dash/proactive/.
func (d *dash) handleEngagement(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "engagement")

	fmt.Fprint(w, `<p class="dim">Normally the agent only answers when you say its name. `+
		`Once it has replied in a conversation it keeps listening there for a while, `+
		`so you can carry on talking without naming it every time. `+
		`Each row below is a conversation where that is switched on right now, and when it wears off.</p>`)
	fmt.Fprint(w, `<p class="dim">Most of the time nothing here needs clearing &mdash; a window ends on its own `+
		`when the time runs out. Use <strong>disengage</strong> when the agent is replying in a chat `+
		`it should stay out of: it stops there straight away and needs to be named again before it speaks.</p>`)

	writeFlash(w, r, map[string]flash{
		"disengaged": {"ok", "disengaged — the agent has stopped listening in that conversation"},
	})

	d.renderEngagementWindows(w, r)
	pageClose(w, r)
}

// handleEngagementDisengage handles POST /dash/engagement/disengage — end one
// window now, through routd's POST /v1/engagement with ttl_seconds=0.
//
// Operator-only and same-origin, like every other dashd write. It sends the
// window's OWNING folder back, not an empty one, so the cleared row keeps
// naming who held it; routd re-checks that folder against the caller and writes
// the audit row, so nothing here is trusted as the containment.
func (d *dash) handleEngagementDisengage(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	jid := strings.TrimSpace(r.FormValue("jid"))
	if jid == "" {
		http.Error(w, "jid required", http.StatusBadRequest)
		return
	}
	if d.routdURL == "" {
		engagementRedirect(w, r, "", "ROUTER_URL not configured")
		return
	}
	// ttl_seconds 0 IS the disengage path (routd handleEngagementSet): a
	// non-positive TTL writes a past deadline, which is what "not live" means.
	body, status, err := d.routdCall(r.Context(), http.MethodPost, "/v1/engagement",
		apiv1.EngagementRequest{
			JID:        jid,
			Topic:      r.FormValue("topic"),
			Folder:     r.FormValue("folder"),
			TTLSeconds: 0,
		})
	if err != nil {
		slog.Warn("engagement disengage: routd call", "jid", jid, "err", err)
		engagementRedirect(w, r, "", "could not reach routd: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		slog.Warn("engagement disengage: routd status", "jid", jid, "status", status)
		engagementRedirect(w, r, "", upstreamErr("routd", status, body))
		return
	}
	slog.Info("engagement disengaged", "jid", jid, "topic", r.FormValue("topic"))
	engagementRedirect(w, r, "disengaged", "")
}

func engagementRedirect(w http.ResponseWriter, r *http.Request, msg, errMsg string) {
	dest := "/dash/engagement/?msg=" + msg
	if errMsg != "" {
		dest = "/dash/engagement/?err=" + url.QueryEscape(errMsg)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// renderEngagementWindows writes the live-window table from routd's
// GET /v1/engagement. dashd's service token carries an empty folder claim, which
// routd reads as list-all — correct here, and safe only because the page is
// operator-gated. A folder-scoped viewer must NOT be given this page: routd
// authorizes dashd's bearer, not the X-User-* headers dashd forwards, so the
// list would not narrow to the viewer.
func (d *dash) renderEngagementWindows(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `<h2>Engaged conversations</h2>`)

	if d.routdURL == "" {
		fmt.Fprint(w, htmlBanner("err", "ROUTER_URL not configured"))
		return
	}
	body, status, err := d.routdGet(r.Context(), "/v1/engagement")
	if err != nil {
		slog.Warn("engagement page: routd call", "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not reach routd: "+err.Error()))
		return
	}
	if status != http.StatusOK {
		slog.Warn("engagement page: routd status", "status", status)
		fmt.Fprint(w, htmlBanner("err", upstreamErr("routd", status, body)))
		return
	}
	var out apiv1.EngagementListResponse
	if err := json.Unmarshal(body, &out); err != nil {
		slog.Warn("engagement page: decode", "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not read routd's answer: "+err.Error()))
		return
	}

	rows := make([][]string, 0, len(out.Engaged))
	for _, e := range out.Engaged {
		rows = append(rows, []string{
			`<code>` + esc(e.JID) + `</code>`,
			engagementTopicCell(e.Topic),
			folderLink(e.Folder),
			fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(e.EngagedUntil), esc(remainingTS(e.EngagedUntil))),
			engagementDisengageCell(e),
		})
	}
	fmt.Fprint(w, htmlTable([]string{"Chat", "Thread", "Group", "Wears off in", ""}, rows,
		"No conversation is engaged right now."))
}

// engagementDisengageCell renders the force-disengage control for one window.
//
// Behind a confirm, like every other dashd danger-zone action: this is visible
// to whoever is in that chat — the agent goes quiet mid-conversation — so it is
// not a bare button. The confirm names the chat, because the rows differ only by
// a jid an operator is not going to recognise from muscle memory.
func engagementDisengageCell(e apiv1.EngagedChat) string {
	return fmt.Sprintf(
		`<form method="post" action="/dash/engagement/disengage" class="form-inline"`+
			` onsubmit="return confirm('Disengage %s? The agent stops listening there right away `+
			`and will not reply again until someone names it.')">`+
			`<input type="hidden" name="jid" value="%s">`+
			`<input type="hidden" name="topic" value="%s">`+
			`<input type="hidden" name="folder" value="%s">`+
			`<button class="btn-danger btn-sm" type="submit">disengage</button></form>`,
		esc(e.JID), esc(e.JID), esc(e.Topic), esc(e.Folder))
}

// engagementTopicCell names the scope a window covers. An empty topic is the
// main conversation rather than a missing value, so it says so — a blank cell
// would read as data dashd failed to load.
func engagementTopicCell(topic string) string {
	if topic == "" {
		return `<span class="dim">main conversation</span>`
	}
	return `<code>` + esc(topic) + `</code>`
}

// routdCall performs an authenticated call against routd's /v1 face with the
// service:dashd bearer. body nil → no request body.
func (d *dash) routdCall(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	return d.bearerCall(ctx, d.routdURL, method, path, body)
}

// bearerCall performs an authenticated call against a sibling daemon's /v1 face
// with the service:dashd bearer. base is the daemon's URL; body nil → no
// request body.
//
// It deliberately does NOT forward X-User-Sub/-Groups the way proxydCall does:
// proxyd authorizes the FORWARDED operator identity, routd and authd authorize
// the BEARER. Sending them would suggest the backend narrows the answer per
// viewer, and it does not — the operator gate on the handler is the whole
// containment.
//
// One helper for both callers rather than a per-daemon copy: the credential and
// the header discipline are the thing that must not drift, and a second body
// would be a second place to forget the X-User-* rule.
func (d *dash) bearerCall(ctx context.Context, base, method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if d.svc != nil {
		tok, terr := d.svc(ctx)
		if terr != nil {
			return nil, 0, terr
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return buf, resp.StatusCode, err
}

func (d *dash) routdGet(ctx context.Context, path string) ([]byte, int, error) {
	return d.routdCall(ctx, http.MethodGet, path, nil)
}
