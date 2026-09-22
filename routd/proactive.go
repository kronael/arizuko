package routd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kronael/arizuko/proactive"
)

// Proactive interjection: a silence-triggered scan the orchestration loop
// drives; on a pass it appends a synthetic inbound that fires a normal turn.
// Default off (PROACTIVE_ENABLED). Mode is per-group state in the group's
// CLAUDE.md frontmatter; tuning is instance env.

// ProactiveConfig is the instance-wide tuning, loaded once from PROACTIVE_* env.
type ProactiveConfig struct {
	Enabled           bool
	ScanInterval      time.Duration
	SilenceMin        time.Duration
	SilenceMax        time.Duration
	Cooldown          time.Duration
	BotQuiet          time.Duration
	RecentActivityMin int
}

// LoadProactiveConfig reads PROACTIVE_* from the environment with defaults;
// Enabled is the kill switch.
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

// modeCache caches per-group proactive modes parsed from CLAUDE.md; entries
// are invalidated by mtime change, never re-parsed per tick.
type modeCache struct {
	mu        sync.Mutex
	groupsDir string
	entries   map[string]modeCacheEntry
}

type modeCacheEntry struct {
	mode  proactive.Mode
	mtime time.Time
}

func newModeCache(groupsDir string) *modeCache {
	return &modeCache{groupsDir: groupsDir, entries: map[string]modeCacheEntry{}}
}

// get returns the group's proactive mode, parsing CLAUDE.md only on mtime change.
func (c *modeCache) get(folder string) proactive.Mode {
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
	m := proactive.Parse(path)
	c.entries[folder] = modeCacheEntry{mode: m, mtime: mtime}
	return m
}

// proactiveResult is one chat's scan verdict; reason is the renderer text.
type proactiveResult struct {
	fired  bool
	check  string // firing check on a fire; vetoing check on a skip
	reason string
}

// evalProactive runs the ordered checks for one chat. It does NOT mutate
// state — the caller fires only on fired=true. now is injected for tests.
func evalProactive(db *DB, cfg ProactiveConfig, mode proactive.Mode, jid string, now time.Time) proactiveResult {
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
	if mode.InQuietHours(now) {
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

// truncate cuts s to at most n bytes on a rune boundary, so a multibyte
// character at the cut never lands in a prompt as a broken byte.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// proactiveReasonBlock renders the per-turn ephemeral <proactive_reason> block.
func proactiveReasonBlock(check, reason string) string {
	return fmt.Sprintf("<proactive_reason check=%q>\n%s\n</proactive_reason>\n", check, reason)
}

// scanProactive runs one sweep over eligible chats: for each chat whose group
// mode is lurk, evaluate the checks and, on a pass, fire (the atomic tx) then
// enqueue. Misconfigured groups log a config error and fire nothing.
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
		if mode.Misconfigured {
			slog.Error("proactive config error", "folder", folder, "jid", jid, "err", mode.Err)
			continue
		}
		if !mode.Eligible() {
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
