package main

// answering_section.go renders the Status page's answer to "are agents
// answering people?" — routd's GET /v1/routing/answering, which owns both
// halves (6/28: the daemon that owns the rows serves the read).
//
// OPERATOR-ONLY, like the held-set section it sits beside (routes_admin.go).
// The turn counts are one instance-wide fold and cannot be narrowed per viewer
// after the fact, and a held chat belongs to no folder, so no folder-scoped
// caller has a claim to either.

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// waitingPreview caps the table. An instance with a broken adapter can carry
// hundreds of waiting chats and the Status page is a summary.
const waitingPreview = 15

// verdictLabel turns a verdict into what the operator should DO about it.
//
// This is the whole value of the row. "Waiting" is the same measurement in all
// three cases and only one of them is a fault: a turn was due and no answer
// came. The other two are the operator's own configuration answering exactly as
// configured, and showing them as faults is what trains an operator to ignore
// the page.
func verdictLabel(verdict string) (text, class string) {
	switch verdict {
	case apiv1.VerdictRoute:
		return "no reply", "bad"
	case apiv1.VerdictObserve:
		return "observed", "dim"
	case apiv1.VerdictHeld:
		return "no route", "dim"
	}
	return verdict, "dim"
}

// waitedFor renders a wait in the coarsest unit that still reads honestly.
func waitedFor(seconds int) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// answeringSection renders the whole section, or a banner naming routd when it
// cannot be read.
//
// An unreachable owner is an ERROR and never empty data (6/29): "every turn
// finished" and "nobody is waiting" are both false statements an operator acts
// on, and this section exists because a false calm is exactly what could not be
// distinguished from a real one.
func (d *dash) answeringSection(ctx context.Context) string {
	out, err := d.answeringViaRoutd(ctx)
	if err != nil {
		return htmlBanner("err", "could not reach routd for answering health")
	}
	var b strings.Builder
	b.WriteString(`<h2>Answering</h2>`)
	b.WriteString(turnOutcomeBanner(out.Turns, out.Since))
	b.WriteString(waitingTable(out.Waiting))
	return b.String()
}

// turnOutcomeBanner is the dispatcher's half: what the turns in the window did.
// Expired is called out because a climbing expired count is turns stopping
// before they answer, which no surface reported before this one.
func turnOutcomeBanner(t apiv1.TurnOutcomes, since string) string {
	total := t.Done + t.Expired + t.Running + t.PendingRetry + t.Other
	if total == 0 {
		return htmlBannerRaw("warn", `No turns dispatched since `+esc(shortDate(since))+`.`)
	}
	// Each part is escaped on its own and the separator is joined in raw: escaping
	// the joined string would turn the entity into a literal "&middot;".
	parts := []string{esc(fmt.Sprintf("%d done", t.Done))}
	for _, p := range []struct {
		n     int
		label string
	}{
		{t.Expired, "expired"},
		{t.Running, "running"},
		{t.PendingRetry, "pending retry"},
		{t.Other, "other"},
	} {
		if p.n > 0 {
			parts = append(parts, esc(fmt.Sprintf("%d %s", p.n, p.label)))
		}
	}
	class := "ok"
	if t.Expired > 0 {
		class = "warn"
	}
	return htmlBannerRaw(class, fmt.Sprintf("%d turns since %s &middot; %s",
		total, esc(shortDate(since)), strings.Join(parts, " &middot; ")))
}

// waitingTable is the people's half: who wrote and got nothing back.
func waitingTable(chats []apiv1.WaitingChat) string {
	if len(chats) == 0 {
		return `<p class="ok">Every chat's newest message has an answer after it.</p>`
	}
	shown := chats
	more := ""
	if len(shown) > waitingPreview {
		more = fmt.Sprintf(`<p class="dim">%d more not shown.</p>`, len(shown)-waitingPreview)
		shown = shown[:waitingPreview]
	}
	rows := make([][]string, 0, len(shown))
	for _, c := range shown {
		label, class := verdictLabel(c.Verdict)
		name := c.ChatName
		if name == "" {
			name = c.ChatJID
		}
		folder := c.Folder
		if folder == "" {
			folder = "&mdash;"
		} else {
			folder = fmt.Sprintf(`<a href="/dash/groups/%s">%s</a>`,
				esc(folderPath(c.Folder)), esc(c.Folder))
		}
		rows = append(rows, []string{
			esc(name),
			esc(c.Platform),
			folder,
			fmt.Sprintf(`<span class="%s">%s</span>`, class, esc(label)),
			esc(waitedFor(c.WaitingSec)),
			esc(c.LastSender),
		})
	}
	return htmlTable([]string{"chat", "platform", "group", "state", "waiting", "from"}, rows) + more
}
