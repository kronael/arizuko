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
func (d *dash) groupUsageBulk(folders []string) ([]groupUsage, error) {
	if len(folders) == 0 {
		return nil, nil
	}
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)

	costMap := map[string]usageCost{}
	msgMap := map[string]usageMsg{}
	for start := 0; start < len(folders); start += usageBulkBatch {
		batch := folders[start:min(start+usageBulkBatch, len(folders))]
		if err := d.groupUsageBulkBatch(batch, cutoff, costMap, msgMap); err != nil {
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
func (d *dash) groupUsageBulkBatch(batch []string, cutoff string, costMap map[string]usageCost, msgMap map[string]usageMsg) error {
	ph := make([]string, len(batch))
	args := make([]any, len(batch))
	for i, f := range batch {
		ph[i] = "?"
		args[i] = f
	}
	in := strings.Join(ph, ",")

	costRows, err := d.adminDB().Query(
		`SELECT folder,
		        COALESCE(SUM(input_tokens+output_tokens),0),
		        COALESCE(SUM(cost_cents),0)
		 FROM cost_log
		 WHERE folder IN (`+in+`) AND recorded_at >= ?
		 GROUP BY folder`,
		append(args, cutoff)...)
	if err != nil {
		return err
	}
	defer costRows.Close()
	for costRows.Next() {
		var folder string
		var tok, cents int
		if err := costRows.Scan(&folder, &tok, &cents); err != nil {
			return err
		}
		costMap[folder] = usageCost{tok, cents}
	}
	if err := costRows.Err(); err != nil {
		return err
	}

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
	return abbrTS(ts)
}
