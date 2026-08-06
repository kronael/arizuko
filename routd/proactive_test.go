package routd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
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
	for i := range n {
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
	for i := range 3 {
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

// lurkGroup lays a lurk-mode CLAUDE.md on disk for folder and points the loop's
// mode cache at it, with the spec-default tuning.
func lurkGroup(t *testing.T, l *Loop, folder string) {
	t.Helper()
	root := t.TempDir()
	gdir := filepath.Join(root, folder)
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeClaudeMD(t, gdir, "---\nproactive:\n  mode: lurk\n---\n")
	l.modes = newModeCache(root)
	l.proactive = defaultProactiveCfg()
}

// consumeSeeded advances the chat's agent cursor past the seeded inbounds. The
// scan needs ≥90s of silence to fire at all, so by then those messages have long
// since been dispatched and the synthetic row is the only new one. Without this
// the same pass would also run a turn for the human sender, and THAT turn (not
// the proactive one) would bump engagement.
func consumeSeeded(t *testing.T, db *DB, jid string, now time.Time, gap time.Duration) {
	t.Helper()
	if err := db.SetAgentCursor(jid, now.Add(-gap).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

// proactiveSilentRunner is a runed whose agent considers the turn and says
// nothing: it records a turn result (deliberate silence still reports one) and
// returns outcome:"silent" without appending any reply.
type proactiveSilentRunner struct {
	srv  *Server
	runs int
}

func (r *proactiveSilentRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.runs++
	if first, _ := r.srv.db.RecordTurnResult(string(req.Folder), req.TurnID, "sess-silent", "success"); first {
		_ = r.srv.db.SetTurnState(req.TurnID, "done")
	}
	return runedv1.RunOutcome{RunID: "run-silent", Outcome: runedv1.OutcomeSilent, SessionID: "sess-silent"}, nil
}

// TestProactiveSilentOutcomeArmsCooldown is acceptance #3: the agent judges
// there is nothing to add, so no message is delivered and the run reports
// outcome:"silent" — and the cooldown is armed anyway, because a
// considered-but-empty turn counts.
//
// The cooldown is the ONLY thing standing between a silent turn and an
// immediate re-fire: a turn that said nothing leaves no bot row, so BotQuiet
// cannot veto, the synthetic row does not reset the silence clock, and the
// unanswered question is still unanswered. Without it the channel would be
// re-interjected every scan interval forever.
func TestProactiveSilentOutcomeArmsCooldown(t *testing.T) {
	runner := &proactiveSilentRunner{}
	db, srv := newTypingRoutd(t, runner, nil, 0) // the runner-parameterized fixture
	runner.srv = srv
	_ = db.PutGroup(core.Group{Folder: "demo"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	lurkGroup(t, srv.loop, "demo")

	const jid = "slack:T/C/U"
	now := time.Now().UTC()
	seedQuestion(t, db, jid, now, 5*time.Minute, 3)
	consumeSeeded(t, db, jid, now, 5*time.Minute)

	srv.loop.scanProactive(now)
	if n := countProactiveRows(t, db, jid); n != 1 {
		t.Fatalf("proactive rows after scan = %d, want 1", n)
	}
	had, err := srv.loop.processGroupMessages(jid)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if runner.runs != 1 {
		t.Fatalf("runs = %d, want 1 — the silent turn never reached runed", runner.runs)
	}

	// "no message": nothing was delivered or persisted for the user.
	if n := countBots(t, db, jid); n != 0 {
		t.Errorf("silent turn produced %d bot rows, want 0", n)
	}
	// `outcome:"silent"`: routd reports the turn as having produced no output.
	if had {
		t.Error("routd reported output for an outcome:silent run")
	}
	// The turn was still considered — the result is recorded.
	if !db.TurnResultRecorded("demo", proactiveTurnID(t, db, jid)) {
		t.Error("silent turn recorded no turn result")
	}

	// "cooldown still set".
	fired := db.ProactiveLastFired(jid)
	if fired.IsZero() {
		t.Fatal("cooldown not armed by a silent turn")
	}
	later := now.Add(time.Minute)
	if r := evalProactive(db, defaultProactiveCfg(), lurk, jid, later); r.fired || r.check != "Cooldown" {
		t.Fatalf("re-eval after a silent turn: want skip Cooldown, got %+v", r)
	}
	srv.loop.scanProactive(later)
	if n := countProactiveRows(t, db, jid); n != 1 {
		t.Errorf("second scan re-fired after a silent turn: %d proactive rows, want 1", n)
	}
	if runner.runs != 1 {
		t.Errorf("runs = %d after the second scan, want 1", runner.runs)
	}
}

// TestProactiveTurnDoesNotBumpEngagement is acceptance #6: a proactive turn
// must not open an engagement window, so the next user message routes exactly as
// it would have.
//
// Engagement OVERRIDES the route table (loop.resolve), which is what makes the
// claim observable: re-point the route afterwards and an engaged chat stays
// captured by its window while a chat that only had a proactive turn follows the
// new route. The human-triggered chat is the control — it runs the identical
// flow on the same server and DOES record engagement, so the proactive chat's
// assertion cannot pass merely because this fixture never engages anything.
func TestProactiveTurnDoesNotBumpEngagement(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.PutGroup(core.Group{Folder: "other"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	lurkGroup(t, srv.loop, "demo")

	const proJID = "slack:T/CP/U"
	const humanJID = "slack:T/CH/U"
	now := time.Now().UTC()

	// The proactive chat: scan fires the synthetic inbound, the turn runs, the
	// agent replies through the normal callback.
	seedQuestion(t, db, proJID, now, 5*time.Minute, 3)
	consumeSeeded(t, db, proJID, now, 5*time.Minute)
	srv.loop.scanProactive(now)
	if n := countProactiveRows(t, db, proJID); n != 1 {
		t.Fatalf("proactive rows after scan = %d, want 1", n)
	}
	if _, err := srv.loop.processGroupMessages(proJID); err != nil {
		t.Fatalf("process proactive: %v", err)
	}
	if countBots(t, db, proJID) == 0 {
		t.Fatal("the proactive turn delivered nothing — the engagement write site was never reached")
	}

	// The control: a human-triggered turn on another chat, same server, same
	// folder, same runner.
	if err := db.PutMessage(core.Message{ID: "h1", ChatJID: humanJID, Sender: "u1",
		Content: "hey", Timestamp: now, Verb: "message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.loop.processGroupMessages(humanJID); err != nil {
		t.Fatalf("process human: %v", err)
	}
	if _, ok := db.Engaged(humanJID, ""); !ok {
		t.Fatal("control: a human turn opened no engagement window — the assertion below would prove nothing")
	}

	if folder, ok := db.Engaged(proJID, ""); ok {
		t.Errorf("the proactive turn opened an engagement window on %s (folder %q)", proJID, folder)
	}

	// The routing consequence, with the route re-pointed under both chats.
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "other"}})
	next := core.Message{Sender: "u1", Content: "and now?", Timestamp: now.Add(time.Minute), Verb: "message"}
	next.ChatJID = proJID
	if f, ok := srv.loop.resolveGroup(proJID, next); !ok || f != "other" {
		t.Errorf("next message after a proactive turn resolved to (%q,%v), want (other,true)", f, ok)
	}
	next.ChatJID = humanJID
	if f, ok := srv.loop.resolveGroup(humanJID, next); !ok || f != "demo" {
		t.Errorf("control: next message on the engaged chat resolved to (%q,%v), want (demo,true)", f, ok)
	}
}

// proactiveTurnID returns the id of the chat's synthetic proactive row — the
// turn_id the fire path handed to dispatch.
func proactiveTurnID(t *testing.T, db *DB, jid string) string {
	t.Helper()
	var id string
	if err := db.SQL().QueryRow(
		`SELECT id FROM messages WHERE chat_jid=? AND sender='timed-proactive'`, jid).Scan(&id); err != nil {
		t.Fatalf("no synthetic proactive row: %v", err)
	}
	return id
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
