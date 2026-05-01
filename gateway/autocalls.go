package gateway

import (
	"strconv"
	"strings"
	"time"

	"github.com/onvos/arizuko/auth"
)

// AutocallCtx carries the facts an autocall may resolve at prompt-build
// time. Must resolve synchronously in microseconds — no I/O, no locks.
type AutocallCtx struct {
	Instance  string
	Folder    string
	SessionID string
	Tier      int
	Now       time.Time
}

type autocall struct {
	name string
	eval func(AutocallCtx) string
}

var autocalls = []autocall{
	{"now", func(c AutocallCtx) string { return c.Now.UTC().Format(time.RFC3339) }},
	{"instance", func(c AutocallCtx) string { return c.Instance }},
	{"folder", func(c AutocallCtx) string { return c.Folder }},
	{"tier", func(c AutocallCtx) string { return strconv.Itoa(c.Tier) }},
	{"session", func(c AutocallCtx) string {
		id := c.SessionID
		if len(id) > 8 {
			id = id[:8]
		}
		return id
	}},
}

// autocallsBlock builds the autocalls XML for a (folder, topic).
// Reads Tier from auth and session id from the store.
func (g *Gateway) autocallsBlock(folder, topic string) string {
	sessionID, _ := g.store.GetSession(folder, topic)
	return renderAutocalls(AutocallCtx{
		Instance:  g.cfg.Name,
		Folder:    folder,
		SessionID: sessionID,
		Tier:      auth.Resolve(folder).Tier,
		Now:       time.Now(),
	})
}

// renderAutocalls produces the <autocalls>...</autocalls> block. Empty
// eval outputs skip their line. Always terminates with a trailing newline.
func renderAutocalls(ctx AutocallCtx) string {
	var b strings.Builder
	b.WriteString("<autocalls>\n")
	for _, a := range autocalls {
		v := a.eval(ctx)
		if v == "" {
			continue
		}
		b.WriteString(a.name)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteByte('\n')
	}
	b.WriteString("</autocalls>\n")
	return b.String()
}
