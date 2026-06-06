package routd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Proactive interjection. routd's orchestration loop drives a silence-triggered
// scan: after each loop iteration, when now ≥ next scan, one pass over eligible
// chats runs the ordered checks and, on a pass, appends a synthetic inbound that
// fires a normal turn. Default off (PROACTIVE_ENABLED unset → no scanner). Mode
// is per-group business state read from the group's CLAUDE.md frontmatter;
// tuning is instance env.

// ProactiveConfig is the instance-wide infra tuning. Loaded once from
// PROACTIVE_* env; identical across folders.
type ProactiveConfig struct {
	Enabled           bool
	ScanInterval      time.Duration
	SilenceMin        time.Duration
	SilenceMax        time.Duration
	Cooldown          time.Duration
	BotQuiet          time.Duration
	RecentActivityMin int
}

// LoadProactiveConfig reads PROACTIVE_* from the environment, applying the
// defaults. Enabled is the kill switch (unset/false → no scanner).
func LoadProactiveConfig(getenv func(string) string) ProactiveConfig {
	return ProactiveConfig{
		Enabled:           boolEnv(getenv("PROACTIVE_ENABLED")),
		ScanInterval:      durEnv(getenv("PROACTIVE_SCAN_INTERVAL"), 30*time.Second),
		SilenceMin:        durEnv(getenv("PROACTIVE_SILENCE_MIN"), 90*time.Second),
		SilenceMax:        durEnv(getenv("PROACTIVE_SILENCE_MAX"), 12*time.Hour),
		Cooldown:          durEnv(getenv("PROACTIVE_COOLDOWN"), 24*time.Hour),
		BotQuiet:          durEnv(getenv("PROACTIVE_BOT_QUIET"), 15*time.Minute),
		RecentActivityMin: intEnv(getenv("PROACTIVE_RECENT_ACTIVITY_MIN"), 3),
	}
}

func boolEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func durEnv(v string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
		return d
	}
	return def
}

func intEnv(v string, def int) int {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil && n > 0 {
		return n
	}
	return def
}

// proactiveMode is a group's parsed CLAUDE.md `proactive:` block.
// misconfigured marks a present-but-invalid block (logged config error,
// fires nothing — strict, not magical). absent block → mode "silent",
// misconfigured false.
type proactiveMode struct {
	mode          string // "silent" (default) | "lurk"
	quietHours    []quietWindow
	misconfigured bool
	err           string
}

func (m proactiveMode) eligible() bool { return m.mode == "lurk" && !m.misconfigured }

// quietWindow is one parsed `HH:MM-HH:MM <IANA tz>` entry; a window may
// cross midnight.
type quietWindow struct {
	startMin int // minutes since 00:00, local to loc
	endMin   int
	loc      *time.Location
}

func (q quietWindow) contains(t time.Time) bool {
	lt := t.In(q.loc)
	m := lt.Hour()*60 + lt.Minute()
	if q.startMin <= q.endMin {
		return m >= q.startMin && m < q.endMin
	}
	// crosses midnight: [start,24h) ∪ [0,end)
	return m >= q.startMin || m < q.endMin
}

var frontmatterRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)
var quietHourRE = regexp.MustCompile(`^(\d{2}):(\d{2})-(\d{2}):(\d{2})\s+(\S+)$`)

// parseProactiveMode parses a group's CLAUDE.md `proactive:` frontmatter.
// Absent file or absent block → silent (default off). A present-but-invalid
// block (unknown mode, unparseable quiet_hours, bad tz) → misconfigured
// (logged error, fires nothing) — never silently coerced to silent.
func parseProactiveMode(path string) proactiveMode {
	data, err := os.ReadFile(path)
	if err != nil {
		return proactiveMode{mode: "silent"}
	}
	fm := frontmatterRE.FindSubmatch(data)
	if fm == nil {
		return proactiveMode{mode: "silent"}
	}
	var meta struct {
		Proactive *struct {
			Mode       string   `yaml:"mode"`
			QuietHours []string `yaml:"quiet_hours"`
		} `yaml:"proactive"`
	}
	if err := yaml.Unmarshal(fm[1], &meta); err != nil {
		return proactiveMode{misconfigured: true, err: "frontmatter yaml: " + err.Error()}
	}
	if meta.Proactive == nil {
		return proactiveMode{mode: "silent"} // absent block → default off
	}
	mode := strings.TrimSpace(meta.Proactive.Mode)
	if mode == "" {
		mode = "silent"
	}
	if mode != "silent" && mode != "lurk" {
		return proactiveMode{misconfigured: true, err: "unknown mode " + mode}
	}
	var windows []quietWindow
	for _, raw := range meta.Proactive.QuietHours {
		w, perr := parseQuietWindow(raw)
		if perr != nil {
			return proactiveMode{misconfigured: true, err: "quiet_hours " + raw + ": " + perr.Error()}
		}
		windows = append(windows, w)
	}
	return proactiveMode{mode: mode, quietHours: windows}
}

func parseQuietWindow(raw string) (quietWindow, error) {
	m := quietHourRE.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return quietWindow{}, fmt.Errorf("want HH:MM-HH:MM <tz>")
	}
	sh, sm := atoiOr(m[1]), atoiOr(m[2])
	eh, em := atoiOr(m[3]), atoiOr(m[4])
	if sh > 23 || eh > 23 || sm > 59 || em > 59 {
		return quietWindow{}, fmt.Errorf("hour/minute out of range")
	}
	loc, err := time.LoadLocation(m[5])
	if err != nil {
		return quietWindow{}, fmt.Errorf("bad tz %q", m[5])
	}
	return quietWindow{startMin: sh*60 + sm, endMin: eh*60 + em, loc: loc}, nil
}

func atoiOr(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func (m proactiveMode) inQuietHours(t time.Time) bool {
	for _, w := range m.quietHours {
		if w.contains(t) {
			return true
		}
	}
	return false
}

// modeCache caches per-group proactive modes parsed from CLAUDE.md. routd's
// loop never re-parses per tick — entries are invalidated by mtime change.
type modeCache struct {
	mu        sync.Mutex
	groupsDir string
	entries   map[string]modeCacheEntry
}

type modeCacheEntry struct {
	mode  proactiveMode
	mtime time.Time
}

func newModeCache(groupsDir string) *modeCache {
	return &modeCache{groupsDir: groupsDir, entries: map[string]modeCacheEntry{}}
}

// get returns the group's proactive mode, parsing CLAUDE.md only when the
// file's mtime changed since the cached entry (or on first read).
func (c *modeCache) get(folder string) proactiveMode {
	path := filepath.Join(c.groupsDir, folder, "CLAUDE.md")
	var mtime time.Time
	if fi, err := os.Stat(path); err == nil {
		mtime = fi.ModTime()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[folder]; ok && e.mtime.Equal(mtime) {
		return e.mode
	}
	m := parseProactiveMode(path)
	c.entries[folder] = modeCacheEntry{mode: m, mtime: mtime}
	return m
}

// proactiveResult is one chat's scan verdict: the firing check (when fired)
// or the vetoing check (on a skip). reason is the renderer text.
type proactiveResult struct {
	fired  bool
	check  string // firing check on a fire; vetoing check on a skip
	reason string
}

// evalProactive runs the ordered checks for one chat against the loop's view of
// routd.db. It does NOT mutate state — the caller fires (the atomic tx) only on
// fired=true. now is injected for deterministic tests.
func evalProactive(db *DB, cfg ProactiveConfig, mode proactiveMode, jid string, now time.Time) proactiveResult {
	// A chat with a live turn is skipped (the per-folder queue serializes).
	if db.ChatHasRunningTurn(jid) {
		return proactiveResult{check: "RunningTurn"}
	}
	// 1. Silence debounce — gap from the last real inbound.
	last := db.LastInboundAt(jid)
	if last.IsZero() {
		return proactiveResult{check: "Silence"}
	}
	gap := now.Sub(last)
	if gap < cfg.SilenceMin || gap > cfg.SilenceMax {
		return proactiveResult{check: "Silence"}
	}
	// 2. Cooldown (MANDATORY).
	if fired := db.ProactiveLastFired(jid); !fired.IsZero() && now.Sub(fired) < cfg.Cooldown {
		return proactiveResult{check: "Cooldown"}
	}
	// 3. Hard vetoes, then ≥1 positive signal.
	if mode.inQuietHours(now) {
		return proactiveResult{check: "QuietHours"}
	}
	if db.BotSpokeSince(jid, now.Add(-cfg.BotQuiet)) {
		return proactiveResult{check: "BotQuiet"}
	}
	if db.InboundCountSince(jid, now.Add(-time.Hour)) < cfg.RecentActivityMin {
		return proactiveResult{check: "RecentActivity"}
	}
	// Positive signal: UnansweredQuestion.
	text, lastTS, ok := db.LastInbound(jid)
	if ok && strings.HasSuffix(strings.TrimSpace(text), "?") && !db.BotSpokeSince(jid, lastTS) {
		return proactiveResult{
			fired:  true,
			check:  "UnansweredQuestion",
			reason: fmt.Sprintf("Last inbound %q ends with a question and has no bot reply.", truncate(text, 200)),
		}
	}
	return proactiveResult{check: "NoSignal"}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// proactiveReasonBlock renders the per-turn ephemeral <proactive_reason> block.
// check is the only structured field; the body is freeform renderer text.
func proactiveReasonBlock(check, reason string) string {
	return fmt.Sprintf("<proactive_reason check=%q>\n%s\n</proactive_reason>\n", check, reason)
}

// scanProactive runs one proactive sweep over eligible chats. For each chat
// whose group mode is lurk, it evaluates the ordered checks and, on a pass, fires
// (the atomic tx) then enqueues the chat — the synthetic inbound drives a normal
// turn. Misconfigured groups log a config error and fire nothing.
func (l *Loop) scanProactive(now time.Time) {
	chats, err := l.db.ProactiveChats()
	if err != nil {
		slog.Warn("proactive scan: list chats", "err", err)
		return
	}
	for _, jid := range chats {
		msgs, _ := l.db.MessagesSince(jid, "")
		if len(msgs) == 0 {
			continue
		}
		folder, ok := l.resolveGroup(jid, msgs[len(msgs)-1])
		if !ok {
			continue
		}
		mode := l.modes.get(folder)
		if mode.misconfigured {
			slog.Error("proactive config error", "folder", folder, "jid", jid, "err", mode.err)
			continue
		}
		if !mode.eligible() {
			continue
		}
		res := evalProactive(l.db, l.proactive, mode, jid, now)
		if !res.fired {
			slog.Debug("proactive skip", "jid", jid, "folder", folder, "veto", res.check)
			continue
		}
		turnID, ferr := l.db.FireProactive(jid)
		if ferr != nil {
			slog.Warn("proactive fire", "jid", jid, "folder", folder, "err", ferr)
			continue
		}
		l.pendingReason.Store(turnID, proactiveResult{fired: true, check: res.check, reason: res.reason})
		slog.Info("proactive fire", "jid", jid, "folder", folder, "check", res.check, "turn_id", turnID)
		l.Enqueue(jid)
	}
}
