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
	// pane_sessions not in routd.db; Slack-pane hints deferred (bugs.md).
	// previous-session XML omitted: session_log lives in runed; continuity hint deferred.
	envelope := `<topic name="` + topic + `" />` + "\n"
	return sysMsgs +
		l.autocallsBlock(folder, topic) +
		l.personaBlock(folder) +
		rules +
		envelope +
		router.FormatMessages(trigger, observed)
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
