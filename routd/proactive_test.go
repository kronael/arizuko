package routd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// defaultProactiveCfg is the spec-default tuning with the kill switch on,
// used to drive the check chain deterministically in tests.
func defaultProactiveCfg() ProactiveConfig {
	return ProactiveConfig{
		Enabled:           true,
		ScanInterval:      30 * time.Second,
		SilenceMin:        90 * time.Second,
		SilenceMax:        12 * time.Hour,
		Cooldown:          24 * time.Hour,
		BotQuiet:          15 * time.Minute,
		RecentActivityMin: 3,
	}
}

// lurk is a non-misconfigured eligible group with no quiet hours.
var lurk = proactiveMode{mode: "lurk"}

// seedQuestion lays down `n` inbound messages on a chat ending in a
// question, the newest at now-gap. The bot has not spoken since. Ids are
// unique per (jid, last-message timestamp) so repeated calls on one chat at
// different times all land (PutMessage is INSERT OR IGNORE on id).
func seedQuestion(t *testing.T, db *DB, jid string, now time.Time, gap time.Duration, n int) {
	t.Helper()
	last := now.Add(-gap)
	base := jid + "-" + strconv.FormatInt(last.UnixNano(), 10) + "-"
	for i := 0; i < n; i++ {
		ts := last.Add(-time.Duration(n-1-i) * time.Minute)
		content := "chatter"
		if i == n-1 {
			content = "where did we land on the lambda issue?"
		}
		if err := db.PutMessage(core.Message{ID: base + strconv.Itoa(i), ChatJID: jid, Sender: "u1",
			Content: content, Timestamp: ts, Verb: "message"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// TestProactiveFiresOnUnansweredQuestion is acceptance #2 (the positive
// path): lurk, ≥3 inbound in the last hour, last ends with ?, no bot reply.
func TestProactiveFiresOnUnansweredQuestion(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)

	res := evalProactive(db, defaultProactiveCfg(), lurk, "slack:T/C/U", now)
	if !res.fired || res.check != "UnansweredQuestion" {
		t.Fatalf("want fire UnansweredQuestion, got %+v", res)
	}
}

// TestProactiveSilenceDebounce covers veto #1 both ways: too-recent (live
// conversation) and too-old (dormant) both skip.
func TestProactiveSilenceDebounce(t *testing.T) {
	cfg := defaultProactiveCfg()
	now := time.Now().UTC()

	// gap < SilenceMin → live, skip.
	db1, _ := OpenMem()
	defer db1.Close()
	seedQuestion(t, db1, "slack:T/C/U", now, 10*time.Second, 3)
	if r := evalProactive(db1, cfg, lurk, "slack:T/C/U", now); r.fired || r.check != "Silence" {
		t.Fatalf("too-recent: want skip Silence, got %+v", r)
	}

	// gap > SilenceMax → dormant, skip.
	db2, _ := OpenMem()
	defer db2.Close()
	seedQuestion(t, db2, "slack:T/C/U", now, 13*time.Hour, 3)
	if r := evalProactive(db2, cfg, lurk, "slack:T/C/U", now); r.fired || r.check != "Silence" {
		t.Fatalf("too-old: want skip Silence, got %+v", r)
	}
}

// TestProactiveBotQuietVeto covers veto: the bot spoke within BotQuiet.
func TestProactiveBotQuietVeto(t *testing.T) {
	db, _ := OpenMem()
	defer db.Close()
	now := time.Now().UTC()
	// last inbound 5m ago (passes Silence) ending in a question, so only
	// BotQuiet stands between the chat and a fire.
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)
	// a bot row 2m ago (< 15m BotQuiet) — note it does NOT answer the
	// question for the signal (the signal isn't reached; BotQuiet vetoes).
	_ = db.PutMessage(core.Message{ID: "bot1", ChatJID: "slack:T/C/U", Sender: "bot",
		Content: "unrelated", Timestamp: now.Add(-2 * time.Minute), BotMsg: true})

	if r := evalProactive(db, defaultProactiveCfg(), lurk, "slack:T/C/U", now); r.fired || r.check != "BotQuiet" {
		t.Fatalf("want skip BotQuiet, got %+v", r)
	}
}

// TestProactiveRecentActivityVeto covers veto: too few inbound in the last
// hour.
func TestProactiveRecentActivityVeto(t *testing.T) {
	db, _ := OpenMem()
	defer db.Close()
	now := time.Now().UTC()
	// only 2 inbound in the last hour (< RecentActivityMin=3).
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 2)
	if r := evalProactive(db, defaultProactiveCfg(), lurk, "slack:T/C/U", now); r.fired || r.check != "RecentActivity" {
		t.Fatalf("want skip RecentActivity, got %+v", r)
	}
}

// TestProactiveQuietHoursVeto is acceptance #4: a quiet window vetoes.
func TestProactiveQuietHoursVeto(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Prague")
	// 23:00 Prague — inside 22:00-08:00.
	now := time.Date(2026, 5, 30, 23, 0, 0, 0, loc).UTC()
	db, _ := OpenMem()
	defer db.Close()
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)
	mode := proactiveMode{mode: "lurk", quietHours: []quietWindow{
		{startMin: 22 * 60, endMin: 8 * 60, loc: loc},
	}}
	if r := evalProactive(db, defaultProactiveCfg(), mode, "slack:T/C/U", now); r.fired || r.check != "QuietHours" {
		t.Fatalf("want skip QuietHours, got %+v", r)
	}
}

// TestProactiveNoSignal covers the no-positive-signal skip: an active chat
// past all vetoes, but the last inbound is not a question.
func TestProactiveNoSignal(t *testing.T) {
	db, _ := OpenMem()
	defer db.Close()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		_ = db.PutMessage(core.Message{ID: "n" + strconv.Itoa(i), ChatJID: "slack:T/C/U",
			Sender: "u1", Content: "no question here", Timestamp: now.Add(-time.Duration(5-i) * time.Minute), Verb: "message"})
	}
	if r := evalProactive(db, defaultProactiveCfg(), lurk, "slack:T/C/U", now); r.fired || r.check != "NoSignal" {
		t.Fatalf("want skip NoSignal, got %+v", r)
	}
}

// TestProactiveAnsweredQuestion: the question has a later bot reply → the
// signal does not hold.
func TestProactiveAnsweredQuestion(t *testing.T) {
	db, _ := OpenMem()
	defer db.Close()
	now := time.Now().UTC()
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)
	// bot replied AFTER the question (1m ago).
	_ = db.PutMessage(core.Message{ID: "botreply", ChatJID: "slack:T/C/U", Sender: "bot",
		Content: "we shipped it", Timestamp: now.Add(-3 * time.Minute), BotMsg: true})
	if r := evalProactive(db, defaultProactiveCfg(), lurk, "slack:T/C/U", now); r.fired {
		t.Fatalf("answered question should not fire, got %+v", r)
	}
}

// TestProactiveCooldownBlocksSecond is acceptance #2's tail: the 24h
// cooldown blocks a second fire even when the checks pass again.
func TestProactiveCooldownBlocksSecond(t *testing.T) {
	db, _ := OpenMem()
	defer db.Close()
	cfg := defaultProactiveCfg()
	now := time.Now().UTC()
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)

	// first fire: the atomic tx appends the synthetic row + sets cooldown.
	if r := evalProactive(db, cfg, lurk, "slack:T/C/U", now); !r.fired {
		t.Fatalf("first eval should fire, got %+v", r)
	}
	turnID, err := db.FireProactive("slack:T/C/U")
	if err != nil {
		t.Fatalf("fire: %v", err)
	}
	if turnID == "" {
		t.Fatal("fire returned empty turn id")
	}
	// the synthetic inbound row exists, empty content, timed-proactive sender.
	var sender, content string
	db.db.QueryRow("SELECT sender, content FROM messages WHERE id=?", turnID).Scan(&sender, &content)
	if sender != "timed-proactive" || content != "" {
		t.Fatalf("synthetic row sender=%q content=%q", sender, content)
	}
	// cooldown set.
	if db.ProactiveLastFired("slack:T/C/U").IsZero() {
		t.Fatal("cooldown not set after fire")
	}

	// a fresh question 1h later, all checks pass again — but within 24h.
	later := now.Add(1 * time.Hour)
	seedQuestion(t, db, "slack:T/C/U", later, 5*time.Minute, 3)
	if r := evalProactive(db, cfg, lurk, "slack:T/C/U", later); r.fired || r.check != "Cooldown" {
		t.Fatalf("second eval within 24h: want skip Cooldown, got %+v", r)
	}

	// past 24h the cooldown lifts.
	wayLater := now.Add(25 * time.Hour)
	seedQuestion(t, db, "slack:T/C/U", wayLater, 5*time.Minute, 3)
	if r := evalProactive(db, cfg, lurk, "slack:T/C/U", wayLater); !r.fired {
		t.Fatalf("eval after 24h should fire again, got %+v", r)
	}
}

// TestProactiveRunningTurnSkips: a chat with a live turn is skipped.
func TestProactiveRunningTurnSkips(t *testing.T) {
	db, _ := OpenMem()
	defer db.Close()
	now := time.Now().UTC()
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)
	db.PutTurnContext("live", "demo", "", "slack:T/C/U", "u1", "")
	if r := evalProactive(db, defaultProactiveCfg(), lurk, "slack:T/C/U", now); r.fired || r.check != "RunningTurn" {
		t.Fatalf("want skip RunningTurn, got %+v", r)
	}
}

// TestParseProactiveModeAbsent: no `proactive:` block → silent, not
// misconfigured (acceptance #1 default).
func TestParseProactiveModeAbsent(t *testing.T) {
	dir := t.TempDir()
	writeClaudeMD(t, dir, "---\nsummary: hi\n---\n# body\n")
	m := parseProactiveMode(filepath.Join(dir, "CLAUDE.md"))
	if m.mode != "silent" || m.misconfigured {
		t.Fatalf("absent block: want silent !misconfigured, got %+v", m)
	}
	if m.eligible() {
		t.Fatal("silent must not be eligible")
	}
}

// TestParseProactiveModeNoFrontmatter / missing file → silent default.
func TestParseProactiveModeNoFile(t *testing.T) {
	m := parseProactiveMode(filepath.Join(t.TempDir(), "absent.md"))
	if m.mode != "silent" || m.misconfigured {
		t.Fatalf("missing file: want silent, got %+v", m)
	}
}

// TestParseProactiveModeLurk: a valid lurk block with quiet hours parses.
func TestParseProactiveModeLurk(t *testing.T) {
	dir := t.TempDir()
	writeClaudeMD(t, dir, "---\nproactive:\n  mode: lurk\n  quiet_hours: ['22:00-08:00 Europe/Prague']\n---\n# body\n")
	m := parseProactiveMode(filepath.Join(dir, "CLAUDE.md"))
	if !m.eligible() {
		t.Fatalf("want eligible lurk, got %+v", m)
	}
	if len(m.quietHours) != 1 {
		t.Fatalf("want 1 quiet window, got %d", len(m.quietHours))
	}
}

// TestParseProactiveModeMalformed is acceptance #7: a present-but-invalid
// block is a logged config error (misconfigured), NOT silently silent.
func TestParseProactiveModeMalformed(t *testing.T) {
	cases := map[string]string{
		"unknown mode":      "---\nproactive:\n  mode: shout\n---\n",
		"bad quiet format":  "---\nproactive:\n  mode: lurk\n  quiet_hours: ['always']\n---\n",
		"bad tz":            "---\nproactive:\n  mode: lurk\n  quiet_hours: ['22:00-08:00 Mars/Olympus']\n---\n",
		"hour out of range": "---\nproactive:\n  mode: lurk\n  quiet_hours: ['28:00-08:00 UTC']\n---\n",
	}
	for name, body := range cases {
		dir := t.TempDir()
		writeClaudeMD(t, dir, body)
		m := parseProactiveMode(filepath.Join(dir, "CLAUDE.md"))
		if !m.misconfigured {
			t.Fatalf("%s: want misconfigured, got %+v", name, m)
		}
		if m.eligible() {
			t.Fatalf("%s: misconfigured must not be eligible", name)
		}
	}
}

// TestModeCacheReparsesOnMtime: the cache parses once and re-parses only
// after the file changes (loop never re-parses per tick).
func TestModeCacheReparsesOnMtime(t *testing.T) {
	root := t.TempDir()
	gdir := filepath.Join(root, "demo")
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(gdir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte("---\nproactive:\n  mode: silent\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newModeCache(root)
	if c.get("demo").mode != "silent" {
		t.Fatal("want silent first read")
	}
	// rewrite to lurk with a bumped mtime.
	if err := os.WriteFile(path, []byte("---\nproactive:\n  mode: lurk\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)
	if !c.get("demo").eligible() {
		t.Fatal("want lurk after mtime change")
	}
}

// TestScanProactiveFiresAndDispatches is the end-to-end loop path: a lurk
// group with an unanswered question fires one synthetic turn through the
// normal dispatch (stubRunner), and the rendered batch carries
// <proactive_reason>. A second scan within 24h fires nothing.
func TestScanProactiveFiresAndDispatches(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})

	// a lurk group config on disk.
	root := t.TempDir()
	gdir := filepath.Join(root, "demo")
	_ = os.MkdirAll(gdir, 0o755)
	_ = os.WriteFile(filepath.Join(gdir, "CLAUDE.md"), []byte("---\nproactive:\n  mode: lurk\n---\n"), 0o644)
	srv.loop.modes = newModeCache(root)
	srv.loop.proactive = defaultProactiveCfg()

	now := time.Now().UTC()
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)

	srv.loop.scanProactive(now)
	// the synthetic inbound was appended; drive the queued turn directly.
	if _, err := srv.loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}
	// runed saw the proactive turn with the <proactive_reason> block.
	if runner.gotBatch == "" {
		t.Fatal("no run dispatched")
	}
	if !strings.Contains(runner.gotBatch, `<proactive_reason check="UnansweredQuestion">`) {
		t.Fatalf("rendered batch missing proactive_reason:\n%s", runner.gotBatch)
	}
	// cooldown set → a second scan fires nothing new.
	before := countProactiveRows(t, db, "slack:T/C/U")
	srv.loop.scanProactive(now.Add(time.Minute))
	if after := countProactiveRows(t, db, "slack:T/C/U"); after != before {
		t.Fatalf("second scan fired again: %d → %d", before, after)
	}
}

// TestScanProactiveSilentGroupSkips: acceptance #1 — a silent (default)
// group never fires even with an unanswered question.
func TestScanProactiveSilentGroupSkips(t *testing.T) {
	db, srv, runner := newTestRoutd(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	root := t.TempDir()
	gdir := filepath.Join(root, "demo")
	_ = os.MkdirAll(gdir, 0o755)
	_ = os.WriteFile(filepath.Join(gdir, "CLAUDE.md"), []byte("# no frontmatter\n"), 0o644)
	srv.loop.modes = newModeCache(root)
	srv.loop.proactive = defaultProactiveCfg()

	now := time.Now().UTC()
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)
	srv.loop.scanProactive(now)
	if countProactiveRows(t, db, "slack:T/C/U") != 0 {
		t.Fatal("silent group fired a proactive turn")
	}
	if runner.gotBatch != "" {
		t.Fatal("silent group dispatched a run")
	}
}

// TestMaybeScanProactiveDisabled is acceptance #5: PROACTIVE_ENABLED unset →
// no scan runs (global no-op).
func TestMaybeScanProactiveDisabled(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	// Proactive.Enabled is false (zero value); modes is nil.
	srv.loop.proactive = ProactiveConfig{Enabled: false}
	now := time.Now().UTC()
	seedQuestion(t, db, "slack:T/C/U", now, 5*time.Minute, 3)
	// maybeScanProactive must be a no-op; it must NOT panic on nil modes.
	srv.loop.maybeScanProactive(now)
	if countProactiveRows(t, db, "slack:T/C/U") != 0 {
		t.Fatal("disabled proactive fired")
	}
}

func writeClaudeMD(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func countProactiveRows(t *testing.T, db *DB, jid string) int {
	t.Helper()
	var n int
	db.db.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid=? AND sender='timed-proactive'", jid).Scan(&n)
	return n
}
