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
// commit together with the agent's answer (routd resolveHoldTx), and a
// direct-DB verdict from here would decide a call no agent is ever told about.
//
// The page is open to any signed-in caller and shows NOTHING by default: dashd
// forwards the proxyd-verified sub as `reviewer`, and routd returns only the
// rows that human holds `admin` on — the same question its verdict gate asks.
// A queue filtered by one rule and answered by another is how a reader gets a
// row they can only be refused on.
func (d *dash) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	pageTopFor(w, r, "approvals")

	_, _ = fmt.Fprint(w, `<p class="dim">A hold rule pauses a risky tool call until a human says go. `+
		`The agent is waiting on it right now. Review the arguments, then approve `+
		`&mdash; it runs immediately, with exactly these arguments &mdash; or reject, `+
		`and the agent is told so and never runs it.</p>`)

	writeFlash(w, r, map[string]flash{
		"approved": {"ok", "approved — the waiting agent runs the call now"},
		"rejected": {"ok", "rejected — the call will not run"},
		"always": {"ok", "approved, and this shape now skips approval — the rule is an acl row " +
			"on the group's grants page, and removing it is the only way back"},
	})

	d.renderApprovals(w, r)
	pageClose(w, r)
}

// pendingRow mirrors routd's PendingAction JSON — the fields this page shows.
//
// matcher/unmatchable/matcher_error are routd's, DERIVED there: they are the
// rule an `always` would write, and this page must display exactly what the
// writer will store. Rendering them here from `args` would be a second renderer
// of one fact, which is how a surface ends up showing one grant and writing
// another (spec 6/26 § "The predicate is part of the verdict").
type pendingRow struct {
	ID           string   `json:"id"`
	GroupFolder  string   `json:"group_folder"`
	Tool         string   `json:"tool"`
	Args         string   `json:"args"`
	Status       string   `json:"status"`
	ChatJID      string   `json:"chat_jid"`
	CreatedAt    string   `json:"created_at"`
	ReviewedBy   string   `json:"reviewed_by"`
	ReviewedAt   string   `json:"reviewed_at"`
	ReviewerNote string   `json:"reviewer_note"`
	Matcher      string   `json:"matcher"`
	Unmatchable  []string `json:"unmatchable"`
	MatcherError string   `json:"matcher_error"`
}

// renderApprovals writes the held-call queue and the recent verdicts off one
// GET /v1/pending_actions.
func (d *dash) renderApprovals(w http.ResponseWriter, r *http.Request) {
	body, status, err := d.routdGet(r.Context(),
		"/v1/pending_actions?reviewer="+url.QueryEscape(reviewerSub(r)))
	if err != nil {
		slog.Warn("approvals page: routd call", "err", err)
		_, _ = fmt.Fprint(w, htmlBanner("err", "could not reach routd: "+err.Error()))
		return
	}
	if status != http.StatusOK {
		slog.Warn("approvals page: routd status", "status", status)
		_, _ = fmt.Fprint(w, htmlBanner("err", upstreamErr("routd", status, body)))
		return
	}
	var out struct {
		Pending []pendingRow `json:"pending"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		slog.Warn("approvals page: decode", "err", err)
		_, _ = fmt.Fprint(w, htmlBanner("err", "could not read routd's answer: "+err.Error()))
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

	// ?id=<id> is the deep link routd puts in the held notice (spec 6/26-personal-mode §"Decision 6").
	// It names WHICH decision; the `reviewer` filter above names WHO. An id that
	// no longer waits — or that this reader may not answer — is reported, never
	// silently ignored: the operator followed a link and must learn why the row
	// is not there.
	focus := r.URL.Query().Get("id")
	if focus != "" {
		held = focusFirst(held, focus)
		if !hasPending(held, focus) {
			switch {
			case hasPending(resolved, focus):
				_, _ = fmt.Fprint(w, htmlBanner("ok", "call "+esc(focus)+" already has a verdict — it is in Recent verdicts below."))
			default:
				_, _ = fmt.Fprint(w, htmlBanner("err", "no held call with id "+esc(focus)+" — it may have expired."))
			}
		}
	}

	_, _ = fmt.Fprint(w, `<h2>Waiting for a verdict</h2>`)
	heldRows := make([][]string, 0, len(held))
	for _, p := range held {
		group := folderLink(p.GroupFolder)
		if p.ID == focus {
			group = `<span class="status-` + statusOK + `">&#9654;</span> ` + group
		}
		heldRows = append(heldRows, []string{
			group,
			`<code>` + esc(p.Tool) + `</code>`,
			approvalArgsCell(p.Args),
			d.chatJIDCell(p.ChatJID),
			abbrTS(p.CreatedAt),
			approvalVerdictCell(p),
		})
	}
	_, _ = fmt.Fprint(w, htmlTable([]string{"Group", "Tool", "Arguments", "Chat", "Held", ""}, heldRows,
		"Nothing is waiting. Held calls appear here the moment a hold rule fires."))

	if len(resolved) == 0 {
		return
	}
	// The list arrives newest-first; 20 verdicts of history is a review trail,
	// not an archive — /dash/audit/ has the rest.
	if len(resolved) > 20 {
		resolved = resolved[:20]
	}
	_, _ = fmt.Fprint(w, `<h2>Recent verdicts</h2>`)
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
	_, _ = fmt.Fprint(w, htmlTable([]string{"Group", "Tool", "Outcome", "By", "When", "Note"}, resolvedRows))
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

// approvalVerdictCell is one form, three submit buttons: the optional note rides
// whichever verdict is pressed, matching chat's "/approve <id> [note]".
//
// "always" is approve PLUS the standing rule, so the rule it will write is
// printed beside the button — never a button whose effect the operator has to
// guess. routd refuses a rule it cannot state exactly; when it says so, the
// button is replaced by the reason. There is no "never": undoing one needs a
// provenance acl rows do not carry (spec 6/26).
func approvalVerdictCell(p pendingRow) string {
	id := esc(url.PathEscape(p.ID))
	out := fmt.Sprintf(
		`<form method="post" action="/dash/approvals/%s/resolve" class="form-inline">`+
			`<input type="text" name="note" placeholder="note (optional)" size="14">`+
			` <button class="btn btn-sm" type="submit" name="verdict" value="approve">approve</button>`+
			` <button class="btn-danger btn-sm" type="submit" name="verdict" value="reject">reject</button>`,
		id)
	if p.MatcherError != "" {
		return out + `</form><div class="dim">no standing answer for this call: ` +
			esc(p.MatcherError) + `</div>`
	}
	return out +
		` <button class="btn btn-sm" type="submit" name="verdict" value="always">always</button>` +
		`</form><div class="dim">always &rarr; skip approval for <code>` +
		esc(alwaysRuleText(p)) + `</code> in <code>` + esc(p.GroupFolder) + `</code>. ` +
		esc(alwaysCaveat(p)) +
		` It skips approval only; the grant still has to allow the call. ` +
		`Undo it by removing that acl row.</div>`
}

// alwaysRuleText spells the rule routd will write, from routd's own matcher.
func alwaysRuleText(p pendingRow) string {
	if p.Matcher == "" {
		return p.Tool
	}
	return p.Tool + "(" + p.Matcher + ")"
}

// alwaysCaveat states what the rule text cannot show: which arguments it leaves
// free. Both widen it past what an operator reading the rule assumes.
func alwaysCaveat(p pendingRow) string {
	s := "Arguments it does not name are unconstrained."
	if p.Matcher == "" {
		s = "No argument constrains it: EVERY call to this tool will run."
	}
	if len(p.Unmatchable) > 0 {
		s += " No rule can constrain " + strings.Join(p.Unmatchable, ", ") + "."
	}
	return s
}

// approvalStatusClass maps a resolved status to its color: approved ran (ok),
// rejected was stopped (err), expired lapsed unanswered (unknown).
func approvalStatusClass(status string) string {
	switch status {
	case "approved":
		return statusOK
	case "rejected":
		return statusErr
	default:
		return statusUnknown
	}
}

// handleApprovalResolve handles POST /dash/approvals/{id}/resolve — forward the
// verdict to routd's POST /v1/pending_actions/{id}/approve|reject. The
// proxyd-verified sub travels as `reviewer` so reviewed_by names the human, not
// dashd's service principal, and so routd can authorize the verdict against the
// held call's folder. routd refuses with 403 when the human holds no admin
// there, and that refusal reaches the operator through approvalsRedirect.
func (d *dash) handleApprovalResolve(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
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
	if id == "" || (verdict != "approve" && verdict != "reject" && verdict != "always") {
		http.Error(w, "verdict must be approve, reject or always", http.StatusBadRequest)
		return
	}
	// `always` is approve plus the standing rule — routd's `standing` argument,
	// not a fourth endpoint, because pending_actions has no third status.
	action, payload := verdict, map[string]string{
		"note":     strings.TrimSpace(r.FormValue("note")),
		"reviewer": reviewerSub(r),
	}
	if verdict == "always" {
		action, payload["standing"] = "approve", "true"
	}
	body, status, err := d.routdCall(r.Context(), http.MethodPost,
		"/v1/pending_actions/"+url.PathEscape(id)+"/"+action, payload)
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
	switch verdict {
	case "reject":
		msg = "rejected"
	case "always":
		msg = "always"
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

// countHeldApprovals counts the calls this caller may answer — the portal
// banner's number, so it must agree with the page the banner links to. A direct
// routd.db read like the portal's other counts; the expiry predicate is
// dashstore's (a held row past expires_at is expired, not waiting), and the
// folder predicate is auth.Authorize with action `admin`, the same one routd
// applies to the verdict itself. 0 on any failure — the banner is a nudge, and
// a deploy whose routd has not migrated the table yet must not break the
// portal.
func (d *dash) countHeldApprovals(r *http.Request) int {
	st := d.ds()
	if st == nil {
		return 0
	}
	folders, err := st.HeldApprovalFolders(time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		slog.Warn("scope: held-approvals count", "err", err)
		return 0
	}
	n := 0
	for _, f := range folders {
		if d.adminOn(r, f) {
			n++
		}
	}
	return n
}

// focusFirst moves the row the deep link names to the top of the queue, so the
// operator sees it without reading a table. Order is otherwise untouched.
func focusFirst(rows []pendingRow, id string) []pendingRow {
	for i, p := range rows {
		if p.ID == id {
			out := append([]pendingRow{p}, rows[:i]...)
			return append(out, rows[i+1:]...)
		}
	}
	return rows
}

// hasPending reports whether id is in rows.
func hasPending(rows []pendingRow, id string) bool {
	for _, p := range rows {
		if p.ID == id {
			return true
		}
	}
	return false
}
