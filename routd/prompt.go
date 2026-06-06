package routd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
	"gopkg.in/yaml.v3"
)

// buildAgentPrompt assembles the prompt for a single agent turn — routd's
// port of gated buildAgentPrompt (spec 5/E). Order: sysMsgs + autocalls +
// persona + <observed> rule + envelope(<topic>+paneHints) + the trigger feed
// and observed block via router.FormatMessages.
//
// Per-topic observed cursor (spec 6/F): each (folder, topic) tracks its own
// watermark over is_observed=1 messages, so two topics in the same folder
// both see ambient observed context without one consuming it for the other.
// At-least-once — the cursor advance is not transactional with the turn; on
// crash recovery a topic may re-see an observed message, which is benign
// because the <rule> below tells the agent not to act on observed context.
func (l *Loop) buildAgentPrompt(folder, topic string, trigger []core.Message) string {
	sysMsgs := l.db.FlushSysMsgs(folder)
	maxN, maxC := l.observeWindow(folder)

	var cursor string
	if line, ok := l.db.TopicLineage(folder, topic); ok {
		cursor = line.ObservedCursor
	}
	observed := l.db.ObservedSince(folder, cursor, maxN, maxC)
	if len(observed) > 0 {
		newest := observed[len(observed)-1].Timestamp.UTC().Format(time.RFC3339Nano)
		_ = l.db.UpdateObservedCursor(folder, topic, newest)
	}

	rules := ""
	if len(observed) > 0 {
		rules += "<rule>Observed messages are context, not requests. Do not reply to them; reply to the explicit message.</rule>\n"
	}
	envelope := `<topic name="` + topic + `" />` + "\n" + l.paneHints(trigger)
	return sysMsgs +
		l.autocallsBlock(folder, topic) +
		l.personaBlock(folder) +
		rules +
		envelope +
		router.FormatMessages(trigger, observed)
}

// paneHints emits <surface>slack-pane</surface> and an optional
// <pane-context jid="..."/> when the trigger originated in an open Slack
// assistant pane. Hash-keyed on the chat_jid's DM channel id (slack: prefix
// only); other surfaces yield empty. Spec 6/D. Port of gateway paneHints —
// pane_sessions is routd's OWN table (routd.db, migration 0010), read via
// PaneContextJID (sibling_db.go); slakd writes it via POST /v1/pane.
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

var personaFrontmatterRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)

// personaBlock reads <groupsDir>/<folder>/PERSONA.md, parses YAML frontmatter,
// and returns a <persona> XML block carrying the `summary` field. Strict: a
// missing file, no frontmatter, or no `summary` returns "" — no fallback to
// body text. Port of gateway personaBlock.
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

// autocallCtx carries the facts an autocall resolves at prompt-build time.
// Resolved synchronously — no I/O, no locks.
type autocallCtx struct {
	Instance  string
	Folder    string
	SessionID string
	Tier      int
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
	{"tier", func(c autocallCtx) string { return strconv.Itoa(c.Tier) }},
	{"session", func(c autocallCtx) string {
		id := c.SessionID
		if len(id) > 8 {
			id = id[:8]
		}
		return id
	}},
}

// autocallsBlock renders the <autocalls> block (instance/folder/tier/session/
// now) for (folder, topic). Port of gateway autocallsBlock.
func (l *Loop) autocallsBlock(folder, topic string) string {
	return renderAutocalls(autocallCtx{
		Instance:  l.instanceName,
		Folder:    folder,
		SessionID: l.db.SessionID(folder, topic),
		Tier:      auth.Resolve(folder).Tier,
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

// observeWindow resolves the observed-context window for folder: cfg defaults,
// overridden by the group's stored window, then by the first route targeting
// the folder that carries an override (spec 6/F). Port of gateway observeWindow.
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
		break // first route with an override wins (spec 6/F)
	}
	return maxN, maxC
}

// emitSystemEvents enqueues new_day / new_session system messages for
// (folder, chatJID) before the prompt is built; FlushSysMsgs renders them
// into the next turn's envelope. Port of gateway emitSystemEvents (called per
// dispatch in runTurn, the routd twin of gated's processSenderBatch). new_day
// fires when the chat's cursor crossed midnight; new_session fires when no
// live session_id is recorded and carries the prior session's tail. Run
// BEFORE buildAgentPrompt so the flush picks the rows up the same turn.
func (l *Loop) emitSystemEvents(folder, chatJID string) {
	today := time.Now().Format("2006-01-02")

	if cur := l.db.GetAgentCursor(chatJID); cur != "" {
		if t, err := time.Parse(time.RFC3339Nano, cur); err == nil &&
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
// <previous_session> tag for the new_session continuity hint. Empty input →
// "" (no prior run on file). Port of gateway previousSessionXML — exact tag
// shape, the agent parses it.
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
