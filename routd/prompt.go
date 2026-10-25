package routd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
	"gopkg.in/yaml.v3"
)

// buildAgentPrompt assembles one turn's prompt: sysMsgs + autocalls + persona
// + <observed> rule + envelope + the feed via router.FormatMessages. Each
// (folder, topic) tracks its own observed watermark. The watermark is
// RETURNED, never written here: the prompt is built before the container
// spawns and every failure after that re-feeds the batch, so writing it at
// render time read the ambient context once and lost it (BUGS L58). The
// caller advances it on the same success path as the message cursor —
// at-least-once, benign because the <rule> tells the agent not to act on it.
func (l *Loop) buildAgentPrompt(folder, topic string, trigger, ambient []core.Message) (string, string) {
	sysMsgs := l.db.FlushSysMsgs(folder)
	maxN, maxC := l.observeWindow(folder)

	var cursor string
	if line, ok := l.db.TopicLineage(folder, topic); ok {
		cursor = line.ObservedCursor
	}
	observed := l.db.ObservedSince(folder, cursor, maxN, maxC)
	mark := ""
	if len(observed) > 0 {
		mark = core.TS(observed[len(observed)-1].Timestamp)
	}
	observed = append(atOrBeforeCursor(ambient, cursor), withoutTrigger(observed, trigger)...)

	rules := ""
	if len(observed) > 0 {
		rules += "<rule>Observed messages are context, not requests. Do not reply to them; reply to the explicit message.</rule>\n"
	}
	envelope := `<topic name="` + topic + `" />` + "\n" + l.paneHints(trigger) + linkContextBlock(trigger)
	return sysMsgs +
		l.autocallsBlock(folder, topic) +
		l.personaBlock(folder) +
		rules +
		envelope +
		router.FormatMessages(trigger, observed), mark
}

// atOrBeforeCursor picks the rows this dispatch consumes as context that the
// observed window cannot return: the window reads strictly after the bare
// instant cursor, and a source that stamps whole seconds lands a row at it.
// They are older than every row of the window, so they render first.
func atOrBeforeCursor(ambient []core.Message, cursor string) []core.Message {
	var out []core.Message
	for _, m := range ambient {
		if cursor != "" && m.Content != "" && core.TS(m.Timestamp) <= cursor {
			out = append(out, m)
		}
	}
	return out
}

// withoutTrigger drops the turn's own messages from its observed window, so a
// message is rendered once, as the request (BUGS L521).
func withoutTrigger(observed, trigger []core.Message) []core.Message {
	ids := make(map[string]bool, len(trigger))
	for _, m := range trigger {
		ids[m.ID] = true
	}
	out := observed[:0:0]
	for _, m := range observed {
		if !ids[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

// paneHints emits <surface>slack-pane</surface> and an optional
// <pane-context jid="..."/> when the trigger originated in an open Slack
// assistant pane, keyed on the chat_jid's DM channel id.
func (l *Loop) paneHints(trigger []core.Message) string {
	if l.db == nil || len(trigger) == 0 {
		return ""
	}
	jid := trigger[0].ChatJID
	const prefix = "slack:"
	if !strings.HasPrefix(jid, prefix) {
		return ""
	}
	// jid shape: slack:<team>/<kind>/<id>
	parts := strings.SplitN(jid[len(prefix):], "/", 3)
	if len(parts) != 3 || parts[2] == "" {
		return ""
	}
	ctxJID, ok := l.db.PaneContextJID(parts[2])
	if !ok {
		return ""
	}
	out := "<surface>slack-pane</surface>\n"
	if ctxJID != "" {
		out += `<pane-context jid="` + ctxJID + `" />` + "\n"
	}
	return out
}

// linkContextBlock emits the issuer's per-link instructions a route token
// snapshotted at ingest (spec 5/W § link context). Newest-wins: a batch on one
// JID may span tokens with different contexts.
func linkContextBlock(trigger []core.Message) string {
	for _, t := range slices.Backward(trigger) {
		if c := t.LinkContext; c != "" {
			return "<link-context>\n" + c + "\n</link-context>\n"
		}
	}
	return ""
}

var personaFrontmatterRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)

// personaBlock renders <groupsDir>/<folder>/PERSONA.md's frontmatter
// `summary`. Strict: a missing file, no frontmatter, or no `summary` returns
// "" — no fallback to body text.
func (l *Loop) personaBlock(folder string) string {
	if folder == "" || l.groupsDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(l.groupsDir, folder, "PERSONA.md"))
	if err != nil {
		return ""
	}
	fm := personaFrontmatterRE.FindSubmatch(data)
	if fm == nil {
		return ""
	}
	var meta struct {
		Name    string `yaml:"name"`
		Summary string `yaml:"summary"`
	}
	if err := yaml.Unmarshal(fm[1], &meta); err != nil {
		return ""
	}
	if strings.TrimSpace(meta.Summary) == "" {
		return ""
	}
	name := meta.Name
	if name == "" {
		name = folder
	}
	return fmt.Sprintf("<persona name=%q>\n%s\n(For full register: /persona)\n</persona>\n",
		name, strings.TrimSpace(meta.Summary))
}

// autocallCtx carries the facts an autocall resolves at prompt-build time,
// synchronously — no I/O, no locks.
type autocallCtx struct {
	Instance  string
	Folder    string
	SessionID string
	Now       time.Time
}

type autocall struct {
	name string
	eval func(autocallCtx) string
}

var autocalls = []autocall{
	{"now", func(c autocallCtx) string { return c.Now.UTC().Format(time.RFC3339) }},
	{"instance", func(c autocallCtx) string { return c.Instance }},
	{"folder", func(c autocallCtx) string { return c.Folder }},
	{"session", func(c autocallCtx) string {
		id := c.SessionID
		if len(id) > 8 {
			id = id[:8]
		}
		return id
	}},
}

// autocallsBlock renders the <autocalls> block for (folder, topic).
func (l *Loop) autocallsBlock(folder, topic string) string {
	return renderAutocalls(autocallCtx{
		Instance:  l.instanceName,
		Folder:    folder,
		SessionID: l.db.SessionID(folder, topic),
		Now:       time.Now(),
	})
}

func renderAutocalls(ctx autocallCtx) string {
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

// observeWindow resolves the observed-context window for folder: cfg
// defaults, overridden by the group's stored window, then by the first route
// targeting the folder that carries an override.
func (l *Loop) observeWindow(folder string) (int, int) {
	maxN := l.observeMessages
	maxC := l.observeChars
	if gn, gc := l.db.GroupObserveWindow(folder); gn >= 0 || gc >= 0 {
		if gn >= 0 {
			maxN = gn
		}
		if gc >= 0 {
			maxC = gc
		}
	}
	routes, _ := l.db.Routes()
	for _, r := range routes {
		if core.ParseRouteTarget(r.Target).Folder != folder {
			continue
		}
		if r.ObserveWindowMessages <= 0 && r.ObserveWindowChars <= 0 {
			continue
		}
		if r.ObserveWindowMessages > 0 {
			maxN = r.ObserveWindowMessages
		}
		if r.ObserveWindowChars > 0 {
			maxC = r.ObserveWindowChars
		}
		break // first route with an override wins
	}
	return maxN, maxC
}

// emitSystemEvents enqueues new_day / new_session system messages BEFORE the
// prompt is built, so FlushSysMsgs renders them this turn. new_day fires when
// the chat's cursor crossed midnight; new_session when no live session_id is
// recorded, carrying the prior session's tail.
func (l *Loop) emitSystemEvents(folder, chatJID string) {
	today := time.Now().Format("2006-01-02")

	if cur := l.db.GetAgentCursor(chatJID); cur != "" {
		// The cursor is a message key, so the day is read off its lead.
		if t, ok := store.CursorInstant(cur); ok &&
			t.Format("2006-01-02") != today {
			_ = l.db.EnqueueSysMsg(folder, "gateway", "new_day",
				fmt.Sprintf("Date changed to %s", today))
		}
	}

	if l.db.SessionID(folder, "") == "" {
		body := previousSessionXML(l.recentSessions(folder, 1))
		_ = l.db.EnqueueSysMsg(folder, "gateway", "new_session", body)
	}
}

// previousSessionXML renders the most recent session_log row as a
// <previous_session> tag; empty input → "".
func previousSessionXML(sessions []core.SessionRecord) string {
	if len(sessions) == 0 {
		return ""
	}
	s := sessions[0]
	result, ended := s.Result, ""
	if result == "" {
		result = "unknown"
	}
	if s.EndedAt != nil {
		ended = s.EndedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	sid := s.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	return fmt.Sprintf(
		`<previous_session id=%q started=%q ended=%q msgs="%d" result=%q/>`,
		sid, s.StartedAt.Format("2006-01-02T15:04:05Z07:00"), ended, s.MsgCount, result)
}
