package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func isHtmx(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// htmlTable renders a <table> with thead+tbody. Empty rows emits a <p class="empty">.
// Pass an optional emptyMsg to override the default "no entries" text.
func htmlTable(headers []string, rows [][]string, emptyMsg ...string) string {
	if len(rows) == 0 {
		msg := "no entries"
		if len(emptyMsg) > 0 && emptyMsg[0] != "" {
			msg = emptyMsg[0]
		}
		return `<p class="empty">` + esc(msg) + `</p>`
	}
	var b strings.Builder
	b.WriteString(`<table><thead><tr>`)
	for _, h := range headers {
		fmt.Fprintf(&b, `<th>%s</th>`, esc(h))
	}
	b.WriteString(`</tr></thead><tbody>`)
	for _, row := range rows {
		b.WriteString(`<tr>`)
		for _, cell := range row {
			fmt.Fprintf(&b, `<td>%s</td>`, cell) // callers escape their own data
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

func htmlBanner(class, msg string) string {
	return fmt.Sprintf(`<div class="banner-%s">%s</div>`, class, esc(msg))
}

// htmlBannerRaw is htmlBanner for pre-escaped/trusted HTML content.
func htmlBannerRaw(class, html string) string {
	return fmt.Sprintf(`<div class="banner-%s">%s</div>`, class, html)
}

// flash is one ?msg= banner: class + operator text.
type flash struct{ Class, Text string }

// writeFlash renders the post-redirect notices a mutating page round-trips
// through the URL: ?msg= mapped through msgs, then ?err= (the upstream's own
// reason) as an err banner. One renderer for every page that used to hand-roll
// the same switch.
func writeFlash(w http.ResponseWriter, r *http.Request, msgs map[string]flash) {
	if m, ok := msgs[strings.TrimSpace(r.URL.Query().Get("msg"))]; ok {
		fmt.Fprint(w, htmlBanner(m.Class, m.Text))
	}
	if e := strings.TrimSpace(r.URL.Query().Get("err")); e != "" {
		fmt.Fprint(w, htmlBanner("err", e))
	}
}

func htmlSection(title, body string) string {
	return fmt.Sprintf(`<h2>%s</h2>%s`, esc(title), body)
}

func htmlFormRow(label, inputHTML string) string {
	return fmt.Sprintf(`<p class="form-row"><label>%s %s</label></p>`, esc(label), inputHTML)
}

// htmlDetail renders a <tr> for use inside a detail <table>.
// key is escaped; val is passed raw (caller escapes as needed).
func htmlDetail(key, val string) string {
	return fmt.Sprintf(`<tr><th>%s</th><td>%s</td></tr>`, esc(key), val)
}

// chatJIDCell renders a chat_jid as a group link when its folder is resolvable
// (web:/hook: prefixes carry the folder; platform JIDs resolve via the routes
// table), else as a plain <code> JID. It lived in routd_page.go until that page
// moved to routd itself (spec 6/28); approvals and proactive still render JIDs.
func (d *dash) chatJIDCell(jid string) string {
	if folder := d.jidFolder(jid); folder != "" {
		return fmt.Sprintf(`<a href="/me/chats?folder=%s"><code>%s</code></a>`,
			url.QueryEscape(folder), esc(jid))
	}
	return `<code>` + esc(jid) + `</code>`
}
