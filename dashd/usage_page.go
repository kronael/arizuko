package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/kronael/arizuko/core"
)

func (d *dash) handleUsage(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "usage")

	if d.routdStore() == nil {
		_, _ = fmt.Fprint(w, htmlBanner("err", "store unavailable"))
		pageClose(w, r)
		return
	}

	folders, err := d.foldersViaRoutd(r.Context())
	if err != nil {
		slog.Warn("usage page: folders", "err", err)
		_, _ = fmt.Fprint(w, htmlBanner("err", "folder query error: "+err.Error()))
		pageClose(w, r)
		return
	}

	summaries, metered, err := d.groupUsageBulk(r.Context(), folders)
	if err != nil {
		slog.Warn("usage page: bulk", "err", err)
		_, _ = fmt.Fprint(w, htmlBanner("err", "usage query error: "+err.Error()))
		pageClose(w, r)
		return
	}

	totalMsgs, totalTokens, totalCents := 0, 0, 0
	for _, s := range summaries {
		totalMsgs += s.MsgCount
		totalTokens += s.Tokens7d
		totalCents += s.Cents7d
	}

	_, _ = fmt.Fprintf(w, `<div class="cols-3">`+
		`<div class="card"><h3>%d</h3><p class="dim">total messages</p></div>`+
		`<div class="card"><h3>%s</h3><p class="dim">tokens / 7d</p></div>`+
		`<div class="card"><h3>%s</h3><p class="dim">cost / 7d</p></div>`+
		`</div>`,
		totalMsgs, meteredCell(metered, fmtTokens(totalTokens)),
		meteredCell(metered, fmtCents(totalCents)))
	if !metered {
		_, _ = fmt.Fprint(w, htmlBanner("warn", "No model gateway is set up on this instance, "+
			"so nothing is counting what it spends. Every token and cost figure below "+
			"reads \"no meter\" — that means unknown, not zero. A daily spend cap needs "+
			"a gateway to enforce it."))
	}

	_, _ = fmt.Fprint(w, `<h2>Per-group</h2>`)
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
	_, _ = fmt.Fprint(w, htmlTable(
		[]string{"Group", "Msgs", "Tokens/7d", "$/7d", "Last active"}, groupRows))

	_, _ = fmt.Fprint(w, `<h2>7-day volume</h2>`)
	_, _ = fmt.Fprint(w, d.usageVolumeTable(r.Context()))

	pageClose(w, r)
}

type groupUsage struct {
	Folder     string
	Tokens7d   int
	Cents7d    int
	MsgCount   int
	LastActive string
}

type usageCost struct{ tokens, cents int }

func (d *dash) groupUsageBulk(ctx context.Context, folders []string) ([]groupUsage, bool, error) {
	cutoff := core.TS(time.Now().Add(-7 * 24 * time.Hour))

	costMap := map[string]usageCost{}

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

	msgMap, err := d.ds().MessageActivityByFolder(folders)
	if err != nil {
		return nil, false, err
	}

	out := make([]groupUsage, len(folders))
	for i, f := range folders {
		c, m := costMap[f], msgMap[f]
		out[i] = groupUsage{
			Folder:     f,
			Tokens7d:   c.tokens,
			Cents7d:    c.cents,
			MsgCount:   m.Count,
			LastActive: m.LastAt,
		}
	}
	return out, summary.Metered, nil
}

func (d *dash) usageVolumeTable(ctx context.Context) string {
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

func fmtTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

func fmtCents(cents int) string {
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}

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

func meteredCell(metered bool, value string) string {
	if !metered {
		return "no meter"
	}
	return value
}

func lastActiveCell(ts string) string {
	if ts == "" {
		return "—"
	}
	return abbrTS(ts)
}

type costSummaryRow struct {
	Folder    string `json:"folder"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
}

type costSummaryReply struct {
	Metered bool             `json:"metered"`
	Folders []costSummaryRow `json:"folders"`
}

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
