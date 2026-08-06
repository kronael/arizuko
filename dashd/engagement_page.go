package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// handleEngagement renders GET /dash/engagement/ — which conversations the
// agent is currently staying in without being addressed (spec 5/G item 6,
// BUGS F31).
//
// Read-only on purpose. A window ends by itself when its TTL runs out, and the
// two writers that end one early — the agent's `disengage` MCP tool and
// POST /v1/engagement — both keep the audit row inside routd's own transaction.
// A button here would be a third writer, so this ships as a view; the grant
// (`service:dashd`, authd/http.go) is read-only to match.
//
// It reads routd's /v1/engagement over HTTP rather than chat_reply_state out of
// dbRoutd. routd owns those columns and applies the folder containment, and the
// direct-DB read is the open defect this page declines to join. Operator-only,
// like /dash/proactive/.
func (d *dash) handleEngagement(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "engagement")

	fmt.Fprint(w, `<p class="dim">Normally the agent only answers when you say its name. `+
		`Once it has replied in a conversation it keeps listening there for a while, `+
		`so you can carry on talking without naming it every time. `+
		`Each row below is a conversation where that is switched on right now, and when it wears off.</p>`)
	fmt.Fprint(w, `<p class="dim">Nothing here needs clearing: a window ends on its own when the time runs out. `+
		`To end one sooner, ask the agent in that chat to disengage.</p>`)

	d.renderEngagementWindows(w, r)
	pageClose(w, r)
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
		})
	}
	fmt.Fprint(w, htmlTable([]string{"Chat", "Thread", "Group", "Wears off in"}, rows,
		"No conversation is engaged right now."))
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

// routdGet performs an authenticated GET against routd's /v1 face with the
// service:dashd bearer.
//
// It deliberately does NOT forward X-User-Sub/-Groups the way proxydCall does:
// proxyd authorizes the FORWARDED operator identity, routd authorizes the
// BEARER. Sending them would suggest routd narrows the answer per viewer, and
// it does not — the operator gate on the handler is the whole containment.
func (d *dash) routdGet(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.routdURL+path, nil)
	if err != nil {
		return nil, 0, err
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
