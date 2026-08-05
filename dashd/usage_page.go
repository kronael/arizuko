package main

import (
	"fmt"
	"log/slog"
	"net/http"
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "usage")

	if d.dbRoutd == nil {
		fmt.Fprint(w, htmlBanner("err", "store unavailable"))
		pageClose(w, r)
		return
	}

	folders, err := d.usageFolders()
	if err != nil {
		slog.Warn("usage page: folders", "err", err)
		fmt.Fprint(w, htmlBanner("err", "folder query error: "+err.Error()))
		pageClose(w, r)
		return
	}

	var summaries []groupUsage
	if len(folders) > 0 && d.adminDB() != nil {
		summaries, err = d.groupUsageBulk(folders)
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

	// Summary cards.
	fmt.Fprintf(w, `<div class="cols-3">`+
		`<div class="card"><h3>%d</h3><p class="dim">total messages</p></div>`+
		`<div class="card"><h3>%s</h3><p class="dim">tokens / 7d</p></div>`+
		`<div class="card"><h3>%s</h3><p class="dim">cost / 7d</p></div>`+
		`</div>`,
		totalMsgs, fmtTokens(totalTokens), fmtCents(totalCents))

	// Per-group breakdown, sorted by 7-day token use.
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
	fmt.Fprint(w, d.usageVolumeTable())

	pageClose(w, r)
}

// usageFolders lists every group folder from routd.db, alphabetically.
func (d *dash) usageFolders() ([]string, error) {
	rows, err := d.dbRoutd.Query(`SELECT folder FROM groups ORDER BY folder`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var folders []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

// groupUsage holds aggregated usage stats for a single folder.
type groupUsage struct {
	Folder     string
	Tokens7d   int    // input_tokens + output_tokens over the last 7 days
	Cents7d    int    // cost_cents over the last 7 days
	MsgCount   int    // all-time message count (routed_to = folder)
	LastActive string // RFC3339 timestamp of the latest message, or ""
}

// groupUsageBulk returns one groupUsage per folder, joining routd.db's cost_log
// (7d window) and messages (all-time count + last active) in Go. Both tables are
// routd-owned: cost_log is keyed (folder, turn_id, model) with recorded_at /
// input_tokens / output_tokens / cost_cents — the pre-split store schema used
// ts / input_tok / cents, which is why this could never be a handle swap.
func (d *dash) groupUsageBulk(folders []string) ([]groupUsage, error) {
	if len(folders) == 0 {
		return nil, nil
	}
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	ph := make([]string, len(folders))
	args := make([]any, len(folders))
	for i, f := range folders {
		ph[i] = "?"
		args[i] = f
	}
	in := strings.Join(ph, ",")

	type cost struct{ tokens, cents int }
	costMap := map[string]cost{}
	costRows, err := d.adminDB().Query(
		`SELECT folder,
		        COALESCE(SUM(input_tokens+output_tokens),0),
		        COALESCE(SUM(cost_cents),0)
		 FROM cost_log
		 WHERE folder IN (`+in+`) AND recorded_at >= ?
		 GROUP BY folder`,
		append(args, cutoff)...)
	if err != nil {
		return nil, err
	}
	defer costRows.Close()
	for costRows.Next() {
		var folder string
		var tok, cents int
		if err := costRows.Scan(&folder, &tok, &cents); err != nil {
			return nil, err
		}
		costMap[folder] = cost{tok, cents}
	}
	if err := costRows.Err(); err != nil {
		return nil, err
	}

	type msg struct {
		cnt    int
		lastTS string
	}
	msgMap := map[string]msg{}
	msgRows, err := d.adminDB().Query(
		`SELECT routed_to, COUNT(*), COALESCE(MAX(timestamp),'')
		 FROM messages
		 WHERE routed_to IN (`+in+`)
		 GROUP BY routed_to`, args...)
	if err != nil {
		return nil, err
	}
	defer msgRows.Close()
	for msgRows.Next() {
		var folder, lastTS string
		var cnt int
		if err := msgRows.Scan(&folder, &cnt, &lastTS); err != nil {
			return nil, err
		}
		msgMap[folder] = msg{cnt, lastTS}
	}
	if err := msgRows.Err(); err != nil {
		return nil, err
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

// usageVolumeTable renders the 7-day daily human-message count from routd.db.
func (d *dash) usageVolumeTable() string {
	if d.adminDB() == nil {
		return htmlBanner("err", "messages store unavailable")
	}
	rows, err := d.adminDB().Query(
		`SELECT DATE(timestamp) AS day, COUNT(*) AS cnt
		 FROM messages
		 WHERE is_bot_message=0 AND timestamp >= datetime('now', '-7 days')
		 GROUP BY day ORDER BY day DESC`)
	if err != nil {
		slog.Warn("usage page: volume", "err", err)
		return htmlBanner("err", "volume query error: "+err.Error())
	}
	defer rows.Close()
	var volRows [][]string
	for rows.Next() {
		var day string
		var cnt int
		if err := rows.Scan(&day, &cnt); err != nil {
			slog.Warn("usage page: volume scan", "err", err)
			continue
		}
		volRows = append(volRows, []string{esc(day), fmt.Sprintf("%d", cnt)})
	}
	if err := rows.Err(); err != nil {
		slog.Warn("usage page: volume rows", "err", err)
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
	return fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(ts), esc(relativeTS(ts)))
}
