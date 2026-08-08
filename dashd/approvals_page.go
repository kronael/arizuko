package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// handleApprovals renders GET /dash/approvals/ — spec 5/19's review queue:
// every tool call a hold rule has suspended, with the approve/reject verdicts.
// Until this page, a held call was visible only in the chat notice; an operator
// who missed it had no queue to come back to.
//
// It reads AND resolves over routd's /v1/pending_actions with the service:dashd
// bearer, never in SQL: routd owns the resolution lifecycle — the verdict must
// commit together with the resolution message that makes the agent re-issue
// the call (routd resolveHoldTx), and a direct-DB verdict from here would
// approve a row no agent is ever told about. Operator-only, and load-bearing:
// routd authorizes the BEARER, whose empty folder claim reads as list-all.
func (d *dash) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "approvals")

	fmt.Fprint(w, `<p class="dim">A hold rule pauses a risky tool call until a human says go. `+
		`Review the arguments, then approve to let this exact call run once &mdash; `+
		`the agent re-issues it with the arguments shown &mdash; or reject so it never runs. `+
		`Changing even one argument puts the call back on hold.</p>`)

	writeFlash(w, r, map[string]flash{
		"approved": {"ok", "approved — the agent will re-issue the call in its next turn"},
		"rejected": {"ok", "rejected — the call will not run"},
	})

	d.renderApprovals(w, r)
	pageClose(w, r)
}

// pendingRow mirrors routd's PendingAction JSON — the fields this page shows.
type pendingRow struct {
	ID           string `json:"id"`
	GroupFolder  string `json:"group_folder"`
	Tool         string `json:"tool"`
	Args         string `json:"args"`
	Status       string `json:"status"`
	ChatJID      string `json:"chat_jid"`
	CreatedAt    string `json:"created_at"`
	ReviewedBy   string `json:"reviewed_by"`
	ReviewedAt   string `json:"reviewed_at"`
	ReviewerNote string `json:"reviewer_note"`
}

// renderApprovals writes the held-call queue and the recent verdicts off one
// GET /v1/pending_actions.
func (d *dash) renderApprovals(w http.ResponseWriter, r *http.Request) {
	body, status, err := d.routdGet(r.Context(), "/v1/pending_actions")
	if err != nil {
		slog.Warn("approvals page: routd call", "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not reach routd: "+err.Error()))
		return
	}
	if status != http.StatusOK {
		slog.Warn("approvals page: routd status", "status", status)
		fmt.Fprint(w, htmlBanner("err", upstreamErr("routd", status, body)))
		return
	}
	var out struct {
		Pending []pendingRow `json:"pending"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		slog.Warn("approvals page: decode", "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not read routd's answer: "+err.Error()))
		return
	}

	var held, resolved []pendingRow
	for _, p := range out.Pending {
		if p.Status == "held" {
			held = append(held, p)
		} else {
			resolved = append(resolved, p)
		}
	}

	fmt.Fprint(w, `<h2>Waiting for a verdict</h2>`)
	heldRows := make([][]string, 0, len(held))
	for _, p := range held {
		heldRows = append(heldRows, []string{
			folderLink(p.GroupFolder),
			`<code>` + esc(p.Tool) + `</code>`,
			approvalArgsCell(p.Args),
			d.chatJIDCell(p.ChatJID),
			abbrTS(p.CreatedAt),
			approvalVerdictCell(p.ID),
		})
	}
	fmt.Fprint(w, htmlTable([]string{"Group", "Tool", "Arguments", "Chat", "Held", ""}, heldRows,
		"Nothing is waiting. Held calls appear here the moment a hold rule fires."))

	if len(resolved) == 0 {
		return
	}
	// The list arrives newest-first; 20 verdicts of history is a review trail,
	// not an archive — /dash/audit/ has the rest.
	if len(resolved) > 20 {
		resolved = resolved[:20]
	}
	fmt.Fprint(w, `<h2>Recent verdicts</h2>`)
	resolvedRows := make([][]string, 0, len(resolved))
	for _, p := range resolved {
		when := p.ReviewedAt
		if when == "" {
			when = p.CreatedAt
		}
		resolvedRows = append(resolvedRows, []string{
			folderLink(p.GroupFolder),
			`<code>` + esc(p.Tool) + `</code>`,
			fmt.Sprintf(`<span class="status-%s">%s</span>`, approvalStatusClass(p.Status), esc(p.Status)),
			esc(p.ReviewedBy),
			abbrTS(when),
			esc(p.ReviewerNote),
		})
	}
	fmt.Fprint(w, htmlTable([]string{"Group", "Tool", "Outcome", "By", "When", "Note"}, resolvedRows))
}

// approvalArgsCell shows the call's arguments — the material under review, so
// they are never hidden behind a hover: full JSON inside a collapsed details.
func approvalArgsCell(args string) string {
	if args == "" {
		return `<span class="dim">none</span>`
	}
	short := args
	if len(short) > 60 {
		short = short[:60] + "…"
	}
	return fmt.Sprintf(`<details><summary><code>%s</code></summary><pre>%s</pre></details>`,
		esc(short), esc(args))
}

// approvalVerdictCell is one form, two submit buttons: the optional note rides
// whichever verdict is pressed, matching chat's "/approve <id> [note]".
func approvalVerdictCell(id string) string {
	return fmt.Sprintf(
		`<form method="post" action="/dash/approvals/%s/resolve" class="form-inline">`+
			`<input type="text" name="note" placeholder="note (optional)" size="14">`+
			` <button class="btn btn-sm" type="submit" name="verdict" value="approve">approve</button>`+
			` <button class="btn-danger btn-sm" type="submit" name="verdict" value="reject">reject</button>`+
			`</form>`,
		esc(url.PathEscape(id)))
}

// approvalStatusClass maps a resolved status to its color: released ran (ok),
// approved is about to run (ok), rejected was stopped (err), expired lapsed
// unanswered (unknown).
func approvalStatusClass(status string) string {
	switch status {
	case "approved", "released":
		return statusOK
	case "rejected":
		return statusErr
	default:
		return statusUnknown
	}
}

// handleApprovalResolve handles POST /dash/approvals/{id}/resolve — forward the
// verdict to routd's POST /v1/pending_actions/{id}/approve|reject. The
// proxyd-verified operator sub travels as `reviewer` so reviewed_by names the
// human, not dashd's service principal.
func (d *dash) handleApprovalResolve(w http.ResponseWriter, r *http.Request) {
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
	id := strings.TrimSpace(r.PathValue("id"))
	verdict := r.FormValue("verdict")
	if id == "" || (verdict != "approve" && verdict != "reject") {
		http.Error(w, "verdict must be approve or reject", http.StatusBadRequest)
		return
	}
	body, status, err := d.routdCall(r.Context(), http.MethodPost,
		"/v1/pending_actions/"+url.PathEscape(id)+"/"+verdict,
		map[string]string{
			"note":     strings.TrimSpace(r.FormValue("note")),
			"reviewer": strings.TrimSpace(r.Header.Get("X-User-Sub")),
		})
	if err != nil {
		slog.Warn("approval resolve: routd call", "id", id, "verdict", verdict, "err", err)
		approvalsRedirect(w, r, "", "could not reach routd: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		slog.Warn("approval resolve: routd status", "id", id, "verdict", verdict, "status", status)
		approvalsRedirect(w, r, "", upstreamErr("routd", status, body))
		return
	}
	slog.Info("approval resolved", "id", id, "verdict", verdict)
	msg := "approved"
	if verdict == "reject" {
		msg = "rejected"
	}
	approvalsRedirect(w, r, msg, "")
}

func approvalsRedirect(w http.ResponseWriter, r *http.Request, msg, errMsg string) {
	dest := "/dash/approvals/?msg=" + msg
	if errMsg != "" {
		dest = "/dash/approvals/?err=" + url.QueryEscape(errMsg)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// countHeldApprovals counts calls still waiting for a verdict — the portal
// banner's number. A direct routd.db read like the portal's other counts; the
// predicate mirrors routd's lazy expiry (applyExpiry: a held row past
// expires_at is expired, not waiting; both timestamps are RFC3339 UTC, so
// string order is time order). 0 on any failure — the banner is a nudge, and
// a deploy whose routd has not migrated the table yet must not break the
// portal.
func (d *dash) countHeldApprovals() int {
	if d.adminDB() == nil {
		return 0
	}
	var n int
	if err := d.adminDB().QueryRow(
		`SELECT COUNT(*) FROM pending_actions
		  WHERE status='held' AND (expires_at IS NULL OR expires_at='' OR expires_at > ?)`,
		time.Now().UTC().Format(time.RFC3339)).Scan(&n); err != nil {
		slog.Warn("scope: held-approvals count", "err", err)
		return 0
	}
	return n
}
