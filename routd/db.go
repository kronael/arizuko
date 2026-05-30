package routd

import (
	"database/sql"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "routd"

// DB is routd's owner of routd.db: the message/event store + routing rules
// + turn lifecycle + route tokens. routd is the SOLE appender of messages
// (spec 5/E). Times are RFC3339Nano UTC, computed in Go.
type DB struct {
	db *sql.DB
}

// Open opens routd.db at dir/routd.db (WAL, FK on) and runs the routd
// migration sequence.
func Open(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "routd.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	return open(dsn)
}

// OpenMem opens a fresh isolated in-memory routd.db for tests. The DB name
// is unique per call so concurrent / sequential tests don't share state via
// the shared cache.
func OpenMem() (*DB, error) {
	name := "routd_mem_" + randHex(8)
	return open("file:" + name + "?mode=memory&cache=shared&_pragma=foreign_keys(on)")
}

func open(dsn string) (*DB, error) {
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := sqldb.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqldb.Close()
		return nil, err
	}
	if err := db_utils.Migrate(sqldb, migrationFS, "migrations", serviceName); err != nil {
		sqldb.Close()
		return nil, err
	}
	return &DB{db: sqldb}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// SQL returns the raw handle for callers that need it (tests).
func (d *DB) SQL() *sql.DB { return d.db }

func nowTS() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// --- groups ---

// PutGroup upserts a group identity row.
func (d *DB) PutGroup(g core.Group) error {
	_, err := d.db.Exec(`INSERT INTO groups(folder, added_at, product, model)
		VALUES(?,?,?,?)
		ON CONFLICT(folder) DO UPDATE SET product=excluded.product, model=excluded.model, updated_at=?`,
		g.Folder, nowTS(), g.Product, g.Model, nowTS())
	return err
}

// GroupExists reports whether folder is a registered group.
func (d *DB) GroupExists(folder string) bool {
	var n int
	d.db.QueryRow("SELECT 1 FROM groups WHERE folder=?", folder).Scan(&n)
	return n == 1
}

// --- messages (sole appender) ---

// PutMessage appends one messages row. routd is the only writer. A
// duplicate id is a no-op (first-written row authoritative, append-only
// log).
func (d *DB) PutMessage(m core.Message) error {
	_, err := d.db.Exec(`INSERT OR IGNORE INTO messages
		(id, chat_jid, sender, sender_name, content, timestamp, is_from_me,
		 is_bot_message, forwarded_from, reply_to_id, reply_to_text, reply_to_sender,
		 topic, routed_to, verb, attachments, source, is_observed, turn_id, status,
		 platform_id, chat_name)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.ChatJID, m.Sender, m.Name, m.Content, m.Timestamp.UTC().Format(time.RFC3339Nano),
		b2i(m.FromMe), b2i(m.BotMsg), m.ForwardedFrom, m.ReplyToID, m.ReplyToText, m.ReplyToSender,
		m.Topic, m.RoutedTo, defaultVerb(m.Verb), m.Attachments, m.Source, b2i(false),
		nullStr(m.TurnID), defaultStatus(m.Status), nullStr(m.PlatformID), m.ChatName)
	return err
}

// MessageExists reports whether a message id is stored.
func (d *DB) MessageExists(id string) bool {
	var n int
	d.db.QueryRow("SELECT 1 FROM messages WHERE id=?", id).Scan(&n)
	return n == 1
}

// NewMessages returns rows with timestamp > since across all chats, plus
// the new high-water mark. Mirrors store.NewMessages (the pollOnce feed).
func (d *DB) NewMessages(since string) ([]core.Message, string, error) {
	rows, err := d.db.Query(`SELECT id, chat_jid, sender, sender_name, content, timestamp,
		is_from_me, is_bot_message, reply_to_id, topic, routed_to, verb, source, turn_id,
		status, platform_id, chat_name
		FROM messages WHERE timestamp > ? ORDER BY timestamp ASC`, since)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()
	msgs, hi := scanMessages(rows, since)
	return msgs, hi, rows.Err()
}

// MessagesSince returns one chat's rows with timestamp > since.
func (d *DB) MessagesSince(chatJID, since string) ([]core.Message, error) {
	rows, err := d.db.Query(`SELECT id, chat_jid, sender, sender_name, content, timestamp,
		is_from_me, is_bot_message, reply_to_id, topic, routed_to, verb, source, turn_id,
		status, platform_id, chat_name
		FROM messages WHERE chat_jid=? AND timestamp > ? ORDER BY timestamp ASC`, chatJID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, _ := scanMessages(rows, since)
	return msgs, rows.Err()
}

// History returns the chat's rows older than before (or all when before is
// empty), newest-bounded by limit, chronological.
func (d *DB) History(chatJID, before string, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, chat_jid, sender, sender_name, content, timestamp, is_from_me,
		is_bot_message, reply_to_id, topic, routed_to, verb, source, turn_id, status,
		platform_id, chat_name FROM messages WHERE chat_jid=?`
	args := []any{chatJID}
	if before != "" {
		q += " AND timestamp < ?"
		args = append(args, before)
	}
	q += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, limit)
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, _ := scanMessages(rows, "")
	// reverse to chronological
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, rows.Err()
}

func scanMessages(rows *sql.Rows, since string) ([]core.Message, string) {
	hi := since
	var out []core.Message
	for rows.Next() {
		var m core.Message
		var ts, turnID, platformID sql.NullString
		var fromMe, botMsg int
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.Name, &m.Content, &ts,
			&fromMe, &botMsg, &m.ReplyToID, &m.Topic, &m.RoutedTo, &m.Verb, &m.Source,
			&turnID, &m.Status, &platformID, &m.ChatName); err != nil {
			continue
		}
		m.FromMe = fromMe == 1
		m.BotMsg = botMsg == 1
		m.TurnID = turnID.String
		m.PlatformID = platformID.String
		if ts.Valid {
			m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts.String)
			if ts.String > hi {
				hi = ts.String
			}
		}
		out = append(out, m)
	}
	return out, hi
}

// MarkBotPlatformID stamps the platform id + sent status on an outbound row
// once the adapter confirms delivery.
func (d *DB) MarkBotPlatformID(id, platformID string) error {
	_, err := d.db.Exec("UPDATE messages SET platform_id=?, status=? WHERE id=?",
		platformID, core.MessageStatusSent, id)
	return err
}

// MarkStatus sets a row's delivery status (pending→failed by the retry
// loop).
func (d *DB) MarkStatus(id, status string) error {
	_, err := d.db.Exec("UPDATE messages SET status=? WHERE id=?", status, id)
	return err
}

// --- chat cursor + sticky ---

// GetAgentCursor reads the per-chat high-water mark fed to the agent.
func (d *DB) GetAgentCursor(chatJID string) string {
	var cur sql.NullString
	d.db.QueryRow("SELECT agent_cursor FROM chats WHERE jid=?", chatJID).Scan(&cur)
	return cur.String
}

// SetAgentCursor advances the per-chat cursor (upsert).
func (d *DB) SetAgentCursor(chatJID, cursor string) error {
	_, err := d.db.Exec(`INSERT INTO chats(jid, agent_cursor) VALUES(?,?)
		ON CONFLICT(jid) DO UPDATE SET agent_cursor=excluded.agent_cursor`, chatJID, cursor)
	return err
}

// MinAgentCursor returns the lowest agent_cursor across chats — the poll
// loop's global floor. Empty when no chat has advanced yet (feed from the
// beginning). Per-chat cursors gate the actual dispatch.
func (d *DB) MinAgentCursor() string {
	var cur sql.NullString
	d.db.QueryRow("SELECT MIN(agent_cursor) FROM chats WHERE agent_cursor IS NOT NULL").Scan(&cur)
	return cur.String
}

// SetChatIsGroup records whether a chat is a multi-party group.
func (d *DB) SetChatIsGroup(chatJID string, isGroup bool) error {
	_, err := d.db.Exec(`INSERT INTO chats(jid, is_group) VALUES(?,?)
		ON CONFLICT(jid) DO UPDATE SET is_group=excluded.is_group`, chatJID, b2i(isGroup))
	return err
}

// StickyState returns the @group / #topic navigation pins for a chat.
func (d *DB) StickyState(chatJID string) (group, topic string) {
	var g, t sql.NullString
	d.db.QueryRow("SELECT sticky_group, sticky_topic FROM chats WHERE jid=?", chatJID).Scan(&g, &t)
	return g.String, t.String
}

// --- routes ---

// Routes returns the route table by ascending seq.
func (d *DB) Routes() ([]core.Route, error) {
	rows, err := d.db.Query(`SELECT id, seq, match, target,
		COALESCE(observe_window_messages,0), COALESCE(observe_window_chars,0)
		FROM routes ORDER BY seq ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Route
	for rows.Next() {
		var r core.Route
		if err := rows.Scan(&r.ID, &r.Seq, &r.Match, &r.Target,
			&r.ObserveWindowMessages, &r.ObserveWindowChars); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddRoute appends one route, returning the assigned id.
func (d *DB) AddRoute(r core.Route) (int64, error) {
	res, err := d.db.Exec(`INSERT INTO routes(seq, match, target, observe_window_messages, observe_window_chars)
		VALUES(?,?,?,?,?)`, r.Seq, r.Match, r.Target, nz(r.ObserveWindowMessages), nz(r.ObserveWindowChars))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetRoutes replaces the whole route table, returning the new count.
func (d *DB) SetRoutes(routes []core.Route) (int, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM routes"); err != nil {
		return 0, err
	}
	for _, r := range routes {
		if _, err := tx.Exec(`INSERT INTO routes(seq, match, target, observe_window_messages, observe_window_chars)
			VALUES(?,?,?,?,?)`, r.Seq, r.Match, r.Target, nz(r.ObserveWindowMessages), nz(r.ObserveWindowChars)); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(routes), nil
}

// DeleteRoute removes a route by id; ErrNotFound when absent.
func (d *DB) DeleteRoute(id int64) error {
	res, err := d.db.Exec("DELETE FROM routes WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrNotFound signals an absent row to the HTTP layer (404).
var ErrNotFound = errors.New("not found")

// --- engagement + last-reply (5/G) ---

// SetEngagement claims an engagement window for (jid, topic) by folder
// until now+ttl.
func (d *DB) SetEngagement(jid, topic, folder string, ttl time.Duration) error {
	until := time.Now().Add(ttl).UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec(`INSERT INTO chat_reply_state(jid, topic, last_reply_id, engaged_until, engaged_folder)
		VALUES(?,?,'',?,?)
		ON CONFLICT(jid, topic) DO UPDATE SET engaged_until=excluded.engaged_until, engaged_folder=excluded.engaged_folder`,
		jid, topic, until, folder)
	return err
}

// Engaged returns (folder, true) when (jid, topic) has a live engagement.
func (d *DB) Engaged(jid, topic string) (string, bool) {
	var until sql.NullString
	var folder string
	err := d.db.QueryRow("SELECT engaged_until, engaged_folder FROM chat_reply_state WHERE jid=? AND topic=?",
		jid, topic).Scan(&until, &folder)
	if err != nil || !until.Valid {
		return "", false
	}
	t, perr := time.Parse(time.RFC3339Nano, until.String)
	if perr != nil || !t.After(time.Now()) {
		return "", false
	}
	return folder, true
}

// SetLastReply seeds the reply-to-bot threading anchor for (jid, topic).
func (d *DB) SetLastReply(jid, topic, msgID, folder string) error {
	_, err := d.db.Exec(`INSERT INTO chat_reply_state(jid, topic, last_reply_id, engaged_folder)
		VALUES(?,?,?,?)
		ON CONFLICT(jid, topic) DO UPDATE SET last_reply_id=excluded.last_reply_id, engaged_folder=excluded.engaged_folder`,
		jid, topic, msgID, folder)
	return err
}

// LastReplyID returns the thread anchor for (jid, topic).
func (d *DB) LastReplyID(jid, topic string) string {
	var id sql.NullString
	d.db.QueryRow("SELECT last_reply_id FROM chat_reply_state WHERE jid=? AND topic=?", jid, topic).Scan(&id)
	return id.String
}

// --- turn context + results ---

// PutTurnContext records a turn's (folder, topic, chat_jid) at dispatch so
// late callbacks resolve their topic from turn_id alone.
func (d *DB) PutTurnContext(turnID, folder, topic, chatJID, trigger string) error {
	_, err := d.db.Exec(`INSERT INTO turn_context(turn_id, folder, topic, chat_jid, trigger_sender, started_at, state)
		VALUES(?,?,?,?,?,?, 'running')
		ON CONFLICT(turn_id) DO UPDATE SET folder=excluded.folder, topic=excluded.topic,
		chat_jid=excluded.chat_jid, trigger_sender=excluded.trigger_sender, state='running'`,
		turnID, folder, topic, chatJID, trigger, nowTS())
	return err
}

// TurnContext is a turn's bound run-start context.
type TurnContext struct {
	TurnID  string
	Folder  string
	Topic   string
	ChatJID string
	Trigger string
	State   string
}

// GetTurnContext recovers a turn's context by turn_id.
func (d *DB) GetTurnContext(turnID string) (TurnContext, bool) {
	var tc TurnContext
	err := d.db.QueryRow(`SELECT turn_id, folder, topic, chat_jid, trigger_sender, state
		FROM turn_context WHERE turn_id=?`, turnID).Scan(
		&tc.TurnID, &tc.Folder, &tc.Topic, &tc.ChatJID, &tc.Trigger, &tc.State)
	if err != nil {
		return TurnContext{}, false
	}
	return tc, true
}

// SetTurnState flips a turn's lifecycle state (running|done|expired).
func (d *DB) SetTurnState(turnID, state string) error {
	_, err := d.db.Exec("UPDATE turn_context SET state=? WHERE turn_id=?", state, turnID)
	return err
}

// RunningTurns lists turn_ids still in state=running (crash-recovery feed).
func (d *DB) RunningTurns() ([]TurnContext, error) {
	rows, err := d.db.Query(`SELECT turn_id, folder, topic, chat_jid, trigger_sender, state
		FROM turn_context WHERE state='running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TurnContext
	for rows.Next() {
		var tc TurnContext
		if err := rows.Scan(&tc.TurnID, &tc.Folder, &tc.Topic, &tc.ChatJID, &tc.Trigger, &tc.State); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// RecordTurnResult inserts the agent-submitted outcome idempotently
// (PK folder,turn_id). Returns true on the FIRST record, false on a dup.
func (d *DB) RecordTurnResult(folder, turnID, sessionID, status string) (bool, error) {
	res, err := d.db.Exec(`INSERT OR IGNORE INTO turn_results(folder, turn_id, session_id, status, recorded_at)
		VALUES(?,?,?,?,?)`, folder, turnID, sessionID, status, nowTS())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// --- sessions (lineage; session_id opaque to routd) ---

// PutSession persists the session_id runed produced for (folder, topic).
func (d *DB) PutSession(folder, topic, sessionID string) error {
	_, err := d.db.Exec(`INSERT INTO sessions(group_folder, topic, session_id) VALUES(?,?,?)
		ON CONFLICT(group_folder, topic) DO UPDATE SET session_id=excluded.session_id`,
		folder, topic, sessionID)
	return err
}

// SessionID returns the persisted session id for (folder, topic).
func (d *DB) SessionID(folder, topic string) string {
	var id sql.NullString
	d.db.QueryRow("SELECT session_id FROM sessions WHERE group_folder=? AND topic=?", folder, topic).Scan(&id)
	return id.String
}

// --- cost ---

// PutCost writes one cost_log row per model under the (folder,turn_id)
// dedup; a duplicate submit_turn does not double-charge.
func (d *DB) PutCost(folder, turnID, model string, in, out, cents int) error {
	_, err := d.db.Exec(`INSERT OR IGNORE INTO cost_log(folder, turn_id, model, input_tokens, output_tokens, cost_cents, recorded_at)
		VALUES(?,?,?,?,?,?,?)`, folder, turnID, model, in, out, cents, nowTS())
	return err
}

// --- proactive interjection (5/33) ---

// proactiveSelfSender is the synthetic inbound's sender. Rows from it never
// reset the silence clock and never count as recent activity (the trigger
// must not feed itself). The `timed-` prefix also hits the engagement-skip
// carve-out in dispatchRun.
const proactiveSelfSender = "timed-proactive"

// ProactiveChats lists distinct chat jids that carry at least one inbound
// message — the universe the proactive scan iterates (then filters by the
// group's cached mode). Bot rows and the proactive synthetic row don't make
// a chat eligible on their own.
func (d *DB) ProactiveChats() ([]string, error) {
	rows, err := d.db.Query(`SELECT DISTINCT chat_jid FROM messages
		WHERE is_bot_message=0 AND sender<>?`, proactiveSelfSender)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		out = append(out, jid)
	}
	return out, rows.Err()
}

// LastInboundAt returns the timestamp of the newest real inbound message on
// a chat (bot rows and the proactive synthetic row excluded), or zero time
// when none. The silence-debounce clock.
func (d *DB) LastInboundAt(jid string) time.Time {
	var ts sql.NullString
	d.db.QueryRow(`SELECT MAX(timestamp) FROM messages
		WHERE chat_jid=? AND is_bot_message=0 AND sender<>?`, jid, proactiveSelfSender).Scan(&ts)
	if !ts.Valid {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, ts.String)
	return t
}

// BotSpokeSince reports whether a bot-authored row exists on a chat at or
// after since (the BotQuiet veto).
func (d *DB) BotSpokeSince(jid string, since time.Time) bool {
	var n int
	d.db.QueryRow(`SELECT 1 FROM messages WHERE chat_jid=? AND is_bot_message=1
		AND timestamp>=? LIMIT 1`, jid, since.UTC().Format(time.RFC3339Nano)).Scan(&n)
	return n == 1
}

// InboundCountSince counts real inbound rows on a chat at or after since
// (the RecentActivity floor).
func (d *DB) InboundCountSince(jid string, since time.Time) int {
	var n int
	d.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_jid=?
		AND is_bot_message=0 AND sender<>? AND timestamp>=?`,
		jid, proactiveSelfSender, since.UTC().Format(time.RFC3339Nano)).Scan(&n)
	return n
}

// LastInbound returns the newest real inbound row on a chat (content +
// timestamp), used by positive signals. ok=false when the chat has none.
func (d *DB) LastInbound(jid string) (content string, ts time.Time, ok bool) {
	var c sql.NullString
	var t sql.NullString
	err := d.db.QueryRow(`SELECT content, timestamp FROM messages
		WHERE chat_jid=? AND is_bot_message=0 AND sender<>?
		ORDER BY timestamp DESC LIMIT 1`, jid, proactiveSelfSender).Scan(&c, &t)
	if err != nil || !t.Valid {
		return "", time.Time{}, false
	}
	tt, _ := time.Parse(time.RFC3339Nano, t.String)
	return c.String, tt, true
}

// ProactiveLastFired returns when a proactive turn last fired on a chat, or
// zero time when never (the mandatory 24h cooldown).
func (d *DB) ProactiveLastFired(jid string) time.Time {
	var ts sql.NullString
	d.db.QueryRow("SELECT proactive_last_fired_at FROM chat_proactive WHERE jid=?", jid).Scan(&ts)
	if !ts.Valid {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, ts.String)
	return t
}

// FireProactive appends the synthetic inbound row AND sets the chat's
// proactive_last_fired_at in one transaction (spec 5/33 § Fire). The caller
// dispatches the run only after this commits, so a crash before dispatch
// leaves the cooldown set — at worst one missed turn, never a double-fire.
// Returns the synthetic row's id (the turn_id).
func (d *DB) FireProactive(jid string) (string, error) {
	id := "proactive-" + randHex(8)
	now := nowTS()
	tx, err := d.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// sender_name + reply_to_id set to '' (not NULL) so scanMessages, which
	// scans them into plain strings, sees a non-NULL value like PutMessage.
	if _, err := tx.Exec(`INSERT INTO messages
		(id, chat_jid, sender, sender_name, content, timestamp, reply_to_id, verb, status)
		VALUES (?,?,?,'','',?,'','message',?)`,
		id, jid, proactiveSelfSender, now, core.MessageStatusSent); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO chat_proactive(jid, proactive_last_fired_at)
		VALUES(?,?) ON CONFLICT(jid) DO UPDATE SET proactive_last_fired_at=excluded.proactive_last_fired_at`,
		jid, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// ChatHasRunningTurn reports whether a chat has a turn still in state
// 'running' — the scan skips it (the per-folder queue serializes; a
// proactive inbound never steers a live turn).
func (d *DB) ChatHasRunningTurn(jid string) bool {
	var n int
	d.db.QueryRow("SELECT 1 FROM turn_context WHERE chat_jid=? AND state='running' LIMIT 1", jid).Scan(&n)
	return n == 1
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nz(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func defaultVerb(v string) string {
	if v == "" {
		return "message"
	}
	return v
}

func defaultStatus(s string) string {
	if s == "" {
		return core.MessageStatusSent
	}
	return s
}
