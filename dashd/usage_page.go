package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
)

// handleUsage renders GET /dash/usage/ — the instance-wide usage cockpit:
// aggregate message/token/cost totals, a per-group breakdown, and a 7-day
// daily message-volume table. Operator-only.
func (d *dash) handleUsage(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "usage")

	if d.dbRoutd == nil {
		fmt.Fprint(w, htmlBanner("err", "store unavailable"))
		pageClose(w, r)
		return
	}

	folders, err := d.usageFolders(r.Context())
	if err != nil {
		slog.Warn("usage page: folders", "err", err)
		fmt.Fprint(w, htmlBanner("err", "folder query error: "+err.Error()))
		pageClose(w, r)
		return
	}

	// The meter is asked about even with no folders to show: whether this
	// instance HAS one is a property of the instance, not of its group tree, and
	// an empty tree that reported "no meter" would blame the wrong thing.
	var summaries []groupUsage
	metered := false
	if d.adminDB() != nil {
		summaries, metered, err = d.groupUsageBulk(r.Context(), folders)
		if err != nil {
			slog.Warn("usage page: bulk", "err", err)
			fmt.Fprint(w, htmlBanner("err", "usage query error: "+err.Error()))
			pageClose(w, r)
			return
		}
	}

	totalMsgs, totalTokens, totalCents := 0, 0, 0
	for _, s := range summaries {
		totalMsgs += s.MsgCount
		totalTokens += s.Tokens7d
		totalCents += s.Cents7d
	}

	fmt.Fprintf(w, `<div class="cols-3">`+
		`<div class="card"><h3>%d</h3><p class="dim">total messages</p></div>`+
		`<div class="card"><h3>%s</h3><p class="dim">tokens / 7d</p></div>`+
		`<div class="card"><h3>%s</h3><p class="dim">cost / 7d</p></div>`+
		`</div>`,
		totalMsgs, meteredCell(metered, fmtTokens(totalTokens)),
		meteredCell(metered, fmtCents(totalCents)))
	if !metered {
		fmt.Fprint(w, htmlBanner("warn", "This instance has no model gateway, so nothing "+
			"meters what it spends. Token and cost columns stay empty until one is "+
			"configured; a spend cap needs a gateway to enforce it."))
	}

	fmt.Fprint(w, `<h2>Per-group</h2>`)
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Tokens7d > summaries[j].Tokens7d
	})
	var groupRows [][]string
	for _, s := range summaries {
		groupRows = append(groupRows, []string{
			fmt.Sprintf(`<a href="/dash/groups/%s">%s</a>`, folderPath(s.Folder), esc(s.Folder)),
			usageCount(s.MsgCount),
			meteredCell(metered, usageTokens(s.Tokens7d)),
			meteredCell(metered, usageCents(s.Cents7d)),
			lastActiveCell(s.LastActive),
		})
	}
	fmt.Fprint(w, htmlTable(
		[]string{"Group", "Msgs", "Tokens/7d", "$/7d", "Last active"}, groupRows))

	// 7-day daily message volume (human messages only).
	fmt.Fprint(w, `<h2>7-day volume</h2>`)
	fmt.Fprint(w, d.usageVolumeTable(r.Context()))

	pageClose(w, r)
}

// usageFolders lists every group folder through routd's /v1, alphabetically.
// routd owns `groups`; this page asks rather than reads its file (6/28).
func (d *dash) usageFolders(ctx context.Context) ([]string, error) {
	return d.foldersViaRoutd(ctx)
}

// groupUsage holds aggregated usage stats for a single folder.
type groupUsage struct {
	Folder     string
	Tokens7d   int    // tokens the gateway metered over the last 7 days
	Cents7d    int    // what the gateway charged over the last 7 days, in cents
	MsgCount   int    // all-time message count (routed_to = folder)
	LastActive string // RFC3339 timestamp of the latest message, or ""
}

// usageBulkBatch bounds how many folders go into one IN(...) list. SQLite
// caps bound parameters per statement (SQLITE_MAX_VARIABLE_NUMBER); an
// unbatched IN-list sized to the full folder count fails outright once an
// instance's group tree passes that ceiling. groupUsageBulk chunks instead.
const usageBulkBatch = 500

type usageCost struct{ tokens, cents int }
type usageMsg struct {
	cnt    int
	lastTS string
}

// groupUsageBulk returns one groupUsage per folder, joining the gateway's spend
// (7d window, through routd) with routd.db's messages (all-time count + last
// active). It also reports whether a meter exists at all — false on an instance
// with no model gateway, where the spend figures are absent rather than zero.
//
// folders may be the caller's scoped subset (Groups page) or the full instance
// list (Usage page); either way the message IN-list is batched so the query
// stays valid regardless of folder count.
func (d *dash) groupUsageBulk(ctx context.Context, folders []string) ([]groupUsage, bool, error) {
	cutoff := core.TS(time.Now().Add(-7 * 24 * time.Hour))

	costMap := map[string]usageCost{}
	msgMap := map[string]usageMsg{}

	// Spend comes from routd's GET /v1/cost/summary in ONE call (spec 6/28
	// read-path; 6/30 named this endpoint as the /v1 work this page needed).
	// The batching below exists for SQLite's IN-list limit, which an HTTP call
	// does not have — so the cost read is hoisted out of the loop and only the
	// message aggregation is still batched.
	summary, err := d.costSummary(ctx, cutoff)
	if err != nil {
		return nil, false, err
	}
	want := map[string]bool{}
	for _, f := range folders {
		want[f] = true
	}
	for _, row := range summary.Folders {
		if want[row.Folder] {
			costMap[row.Folder] = usageCost{row.Tokens, row.CostCents}
		}
	}

	for start := 0; start < len(folders); start += usageBulkBatch {
		batch := folders[start:min(start+usageBulkBatch, len(folders))]
		if err := d.groupUsageBulkBatch(batch, costMap, msgMap); err != nil {
			return nil, false, err
		}
	}

	out := make([]groupUsage, len(folders))
	for i, f := range folders {
		c, m := costMap[f], msgMap[f]
		out[i] = groupUsage{
			Folder:     f,
			Tokens7d:   c.tokens,
			Cents7d:    c.cents,
			MsgCount:   m.cnt,
			LastActive: m.lastTS,
		}
	}
	return out, summary.Metered, nil
}

// groupUsageBulkBatch runs the messages aggregation for one IN-list-sized batch
// of folders, merging results into the caller's maps.
func (d *dash) groupUsageBulkBatch(batch []string, costMap map[string]usageCost, msgMap map[string]usageMsg) error {
	ph := make([]string, len(batch))
	args := make([]any, len(batch))
	for i, f := range batch {
		ph[i] = "?"
		args[i] = f
	}
	in := strings.Join(ph, ",")

	// The message counts still read the DB: routd exposes no activity summary.
	// Spend and messages are different data with one reader each — not two paths
	// to one datum. See 6/29 for what closing this one needs.

	msgRows, err := d.adminDB().Query(
		`SELECT routed_to, COUNT(*), COALESCE(MAX(timestamp),'')
		 FROM messages
		 WHERE routed_to IN (`+in+`)
		 GROUP BY routed_to`, args...)
	if err != nil {
		return err
	}
	defer msgRows.Close()
	for msgRows.Next() {
		var folder, lastTS string
		var cnt int
		if err := msgRows.Scan(&folder, &cnt, &lastTS); err != nil {
			return err
		}
		msgMap[folder] = usageMsg{cnt, lastTS}
	}
	return msgRows.Err()
}

// usageVolumeTable renders the 7-day daily human-message count from routd.db.
func (d *dash) usageVolumeTable(ctx context.Context) string {
	// routd owns `messages` and applies the is_bot_message filter itself, so
	// this page cannot widen the histogram to include the agent's own output
	// (6/28 read path, BUGS L142).
	days, err := d.messageVolumeViaRoutd(ctx)
	if err != nil {
		slog.Warn("usage page: volume via routd", "err", err)
		return htmlBanner("err", "could not reach routd for message volume")
	}
	var volRows [][]string
	for _, d := range days {
		volRows = append(volRows, []string{esc(d.Day), fmt.Sprintf("%d", d.Count)})
	}
	return htmlTable([]string{"Date", "Messages"}, volRows)
}

// fmtTokens renders a token count as "123k" for >=1000, else the raw number.
func fmtTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// fmtCents renders a cent count as a dollar amount ("$1.23").
func fmtCents(cents int) string {
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}

// usageCount / usageTokens / usageCents render a per-group cell, showing "—"
// when the value is zero.
func usageCount(n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func usageTokens(n int) string {
	if n == 0 {
		return "—"
	}
	return fmtTokens(n)
}

func usageCents(cents int) string {
	if cents == 0 {
		return "—"
	}
	return fmtCents(cents)
}

// meteredCell renders a spend figure, or "no meter" when the instance names no
// model gateway. An unmetered instance holds no spend at all, so showing 0 there
// would report a measurement nobody took (spec 6/39).
func meteredCell(metered bool, value string) string {
	if !metered {
		return "no meter"
	}
	return value
}

// lastActiveCell renders a folder's last-active timestamp as a relative <abbr>,
// or "—" when the folder has never been active.
func lastActiveCell(ts string) string {
	if ts == "" {
		return "—"
	}
	return abbrTS(ts)
}

// costSummaryRow is one folder's metered spend in routd's GET /v1/cost/summary
// reply.
type costSummaryRow struct {
	Folder    string `json:"folder"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
}

// costSummaryReply is routd's GET /v1/cost/summary reply. Metered false means
// the instance names no model gateway and so has no spend meter at all — the
// page says that rather than drawing a zero (spec 6/39).
type costSummaryReply struct {
	Metered bool             `json:"metered"`
	Folders []costSummaryRow `json:"folders"`
}

// costSummary reads per-folder spend since cutoff from routd's /v1, which
// proxies the model gateway — the one place every operator surface reaches the
// meter, so the dashboard cannot show a number the REST face does not.
//
// dashd presents a service bearer, whose folder claim is empty, so routd
// answers for every folder. The page's own visibility filter is applied by the
// caller, unchanged.
func (d *dash) costSummary(ctx context.Context, cutoff string) (costSummaryReply, error) {
	var out costSummaryReply
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet,
		"/v1/cost/summary?since="+url.QueryEscape(cutoff), nil)
	if err != nil {
		return out, err
	}
	if status < 200 || status >= 300 {
		return out, fmt.Errorf("routd /v1/cost/summary: status %d", status)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	return out, nil
}
