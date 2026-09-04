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

	var summaries []groupUsage
	if len(folders) > 0 && d.adminDB() != nil {
		summaries, err = d.groupUsageBulk(r.Context(), folders)
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
		totalMsgs, fmtTokens(totalTokens), fmtCents(totalCents))

	fmt.Fprint(w, `<h2>Per-group</h2>`)
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Tokens7d > summaries[j].Tokens7d
	})
	var groupRows [][]string
	for _, s := range summaries {
		groupRows = append(groupRows, []string{
			fmt.Sprintf(`<a href="/dash/groups/%s">%s</a>`, folderPath(s.Folder), esc(s.Folder)),
			usageCount(s.MsgCount),
			usageTokens(s.Tokens7d),
			usageCents(s.Cents7d),
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
	Tokens7d   int    // input_tokens + output_tokens over the last 7 days
	Cents7d    int    // cost_cents over the last 7 days
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

// groupUsageBulk returns one groupUsage per folder, joining routd.db's cost_log
// (7d window) and messages (all-time count + last active) in Go. Both tables are
// routd-owned: cost_log is keyed (folder, turn_id, model) with recorded_at /
// input_tokens / output_tokens / cost_cents. folders may be the caller's scoped
// subset (Groups page) or the full instance list (Usage page); either way the
// IN-list is batched so the query stays valid regardless of folder count.
func (d *dash) groupUsageBulk(ctx context.Context, folders []string) ([]groupUsage, error) {
	if len(folders) == 0 {
		return nil, nil
	}
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)

	costMap := map[string]usageCost{}
	msgMap := map[string]usageMsg{}

	// Spend comes from routd's GET /v1/cost/summary in ONE call (spec 6/28
	// read-path; 6/30 named this endpoint as the /v1 work this page needed).
	// The batching below exists for SQLite's IN-list limit, which an HTTP call
	// does not have — so the cost read is hoisted out of the loop and only the
	// message aggregation is still batched.
	summary, err := d.costSummary(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, f := range folders {
		want[f] = true
	}
	for _, row := range summary {
		if want[row.Folder] {
			costMap[row.Folder] = usageCost{row.Tokens, row.CostCents}
		}
	}

	for start := 0; start < len(folders); start += usageBulkBatch {
		batch := folders[start:min(start+usageBulkBatch, len(folders))]
		if err := d.groupUsageBulkBatch(batch, costMap, msgMap); err != nil {
			return nil, err
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
	return out, nil
}

// groupUsageBulkBatch runs the cost_log + messages aggregation for one
// IN-list-sized batch of folders, merging results into the caller's maps.
func (d *dash) groupUsageBulkBatch(batch []string, costMap map[string]usageCost, msgMap map[string]usageMsg) error {
	ph := make([]string, len(batch))
	args := make([]any, len(batch))
	for i, f := range batch {
		ph[i] = "?"
		args[i] = f
	}
	in := strings.Join(ph, ",")

	// The message counts still read the DB: routd exposes no activity summary,
	// and cost_log and messages are different data with one reader each — not
	// two paths to one datum. See 6/29 for what closing them needs.

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

// lastActiveCell renders a folder's last-active timestamp as a relative <abbr>,
// or "—" when the folder has never been active.
func lastActiveCell(ts string) string {
	if ts == "" {
		return "—"
	}
	return abbrTS(ts)
}

// costSummaryRow mirrors routd's GET /v1/cost/summary reply.
type costSummaryRow struct {
	Folder    string `json:"folder"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
}

// costSummary reads per-folder spend since cutoff from routd's /v1.
//
// dashd presents a service bearer, whose folder claim is empty, so routd
// answers for every folder — the same reach the direct cost_log read had. The
// page's own visibility filter is applied by the caller, unchanged.
func (d *dash) costSummary(ctx context.Context, cutoff string) ([]costSummaryRow, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet,
		"/v1/cost/summary?since="+url.QueryEscape(cutoff), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/cost/summary: status %d", status)
	}
	var rows []costSummaryRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
