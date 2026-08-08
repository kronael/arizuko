package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/kronael/arizuko/proactive"
)

// handleProactive renders GET /dash/proactive/ — which groups are allowed to
// speak unprompted, and which chats already have (spec 5/6, BUGS F24).
//
// Read-only on purpose. `mode:` is group business state in the group's
// CLAUDE.md frontmatter, so the file is the single source and a dashboard
// writer would be a second one; the cooldown is mandatory by spec, so there is
// no operator override to offer. What an operator could not do before this
// page is SEE either — mode meant opening every CLAUDE.md by hand and the
// cooldown meant a sqlite3 prompt. Operator-only.
func (d *dash) handleProactive(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "proactive")

	if d.dbRoutd == nil {
		fmt.Fprint(w, htmlBanner("err", "routd store unavailable"))
		pageClose(w, r)
		return
	}

	// dashd cannot read routd's environment, so this states the rule rather
	// than a live value — a green "enabled" dot dashd cannot verify would be
	// worse than no dot.
	fmt.Fprint(w, `<p class="dim">Normally the agent only answers when spoken to. `+
		`A group set to <code>lurk</code> may also speak up on its own &mdash; once per chat per cooldown, `+
		`and only when a question has gone unanswered in a busy room.</p>`)
	fmt.Fprint(w, htmlBannerRaw("warn",
		`Proactive interjection is off unless routd runs with <code>PROACTIVE_ENABLED</code> set. `+
			`No shipped template sets it, so it is off unless you opted in. `+
			`This page shows what would happen once you do &mdash; it cannot read routd&rsquo;s environment to confirm the switch.`))

	d.renderProactiveModes(w)
	d.renderProactiveCooldowns(w)
	pageClose(w, r)
}

// renderProactiveModes writes the per-group mode table, parsing each group's
// CLAUDE.md through the same parser routd's scanner gates on.
func (d *dash) renderProactiveModes(w http.ResponseWriter) {
	fmt.Fprint(w, `<h2>Group settings</h2>`)
	fmt.Fprint(w, `<p class="dim">silent &mdash; never speaks first (the default). `+
		`lurk &mdash; may speak first. `+
		`broken &mdash; the settings have a mistake, so this group says nothing at all until you fix it.</p>`)

	rows, err := d.dbRoutd.Query(`SELECT folder FROM groups ORDER BY folder LIMIT 500`)
	if err != nil {
		slog.Warn("proactive page: groups query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "group query error: "+err.Error()))
		return
	}
	defer rows.Close()

	var tableRows [][]string
	for rows.Next() {
		var folder string
		if err := rows.Scan(&folder); err != nil {
			slog.Warn("proactive page: groups scan", "err", err)
			continue
		}
		m := d.groupProactiveMode(folder)
		tableRows = append(tableRows, []string{
			folderLink(folder),
			proactiveModeCell(m),
			proactiveQuietCell(m),
			proactiveProblemCell(m),
		})
	}
	if err := rows.Err(); err != nil {
		slog.Warn("proactive page: groups rows", "err", err)
	}
	fmt.Fprint(w, htmlTable([]string{"Group", "Mode", "Quiet hours", "Problem"}, tableRows,
		"No groups yet."))
}

// groupProactiveMode parses one group's CLAUDE.md. The path is confined to
// groupsDir the same way the memory browser confines it — folder comes from
// the groups table, but a traversal-shaped row must not escape the tree.
func (d *dash) groupProactiveMode(folder string) proactive.Mode {
	if d.groupsDir == "" {
		return proactive.Mode{Name: "silent"}
	}
	groupDir := filepath.Join(d.groupsDir, filepath.Clean(folder))
	if groupDir != d.groupsDir && !strings.HasPrefix(groupDir, d.groupsDir+string(filepath.Separator)) {
		slog.Warn("proactive page: folder escapes groups dir", "folder", folder)
		return proactive.Mode{Name: "silent"}
	}
	return proactive.Parse(filepath.Join(groupDir, "CLAUDE.md"))
}

// proactiveModeCell renders the mode with its status colour. A broken block is
// an error, not a quieter kind of silent — the spec refuses to coerce it and
// so does this cell, or a typo would look like a deliberate setting.
func proactiveModeCell(m proactive.Mode) string {
	if m.Misconfigured {
		return fmt.Sprintf(`<span class="status-%s">broken</span>`, statusErr)
	}
	if m.Eligible() {
		return fmt.Sprintf(`<span class="status-%s">lurk</span>`, statusOK)
	}
	return `<span class="dim">silent</span>`
}

// proactiveQuietCell lists the group's quiet windows as the operator typed
// them. Empty for a group with none — "&mdash;" rather than a blank cell so
// the column reads as answered rather than missing.
func proactiveQuietCell(m proactive.Mode) string {
	if len(m.QuietHours) == 0 {
		return `<span class="dim">&mdash;</span>`
	}
	var raw []string
	for _, q := range m.QuietHours {
		raw = append(raw, `<code>`+esc(q.Raw)+`</code>`)
	}
	return strings.Join(raw, " ")
}

// proactiveProblemCell shows the parse error verbatim; that string is the
// whole reason a silent group is silent, and it is otherwise only in the log.
func proactiveProblemCell(m proactive.Mode) string {
	if !m.Misconfigured {
		return ""
	}
	return fmt.Sprintf(`<span class="status-%s">%s</span>`, statusErr, esc(m.Err))
}

// renderProactiveCooldowns writes the per-chat cooldown table from
// chat_proactive — routd.db's record of when a chat last spoke unprompted.
func (d *dash) renderProactiveCooldowns(w http.ResponseWriter) {
	fmt.Fprint(w, `<h2>Chats that spoke first</h2>`)
	fmt.Fprint(w, `<p class="dim">After speaking unprompted, a chat is barred from doing it again `+
		`until the cooldown passes &mdash; 24 hours unless routd runs with a different `+
		`<code>PROACTIVE_COOLDOWN</code>. This is the usual answer to &ldquo;why didn&rsquo;t it say anything?&rdquo;</p>`)

	rows, err := d.dbRoutd.Query(
		`SELECT jid, COALESCE(proactive_last_fired_at,'')
		 FROM chat_proactive
		 WHERE proactive_last_fired_at IS NOT NULL
		 ORDER BY proactive_last_fired_at DESC LIMIT 50`)
	if err != nil {
		slog.Warn("proactive page: cooldown query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "cooldown query error: "+err.Error()))
		return
	}
	defer rows.Close()

	var tableRows [][]string
	for rows.Next() {
		var jid, firedAt string
		if err := rows.Scan(&jid, &firedAt); err != nil {
			slog.Warn("proactive page: cooldown scan", "err", err)
			continue
		}
		tableRows = append(tableRows, []string{
			d.chatJIDCell(jid),
			abbrTS(firedAt),
		})
	}
	if err := rows.Err(); err != nil {
		slog.Warn("proactive page: cooldown rows", "err", err)
	}
	fmt.Fprint(w, htmlTable([]string{"Chat", "Last spoke first"}, tableRows,
		"No chat has spoken unprompted yet."))
}
