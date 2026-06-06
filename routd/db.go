package routd

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

	// secretKeyring is the SECRETS_KEY material (raw, pre-SHA256) routd hands
	// to its OWN *store.Store (secretStore) via SetSecretKeys so reads decrypt
	// `v2:` values and writes seal them. Empty → no key set → reads stay
	// ciphertext (no plaintext leak; the connector just gets nothing usable) and
	// writes store plaintext. routd OWNS the secrets table in routd.db (spec 5/5).
	secretKeyring [][]byte
}

// SetSecretKeys supplies the SECRETS_KEY keyring (raw values; the active seal
// key first, retired keys after) so secret reads off routd's own secrets table
// decrypt and writes seal. Mirrors store.SetSecretKeys. Empty/no-call → reads
// stay ciphertext, writes store plaintext.
func (d *DB) SetSecretKeys(raws ...[]byte) { d.secretKeyring = raws }

// Open opens routd.db at dir/routd.db (WAL, FK on) and runs the routd migration
// sequence. routd opens NO sibling DB — every table it needs is in routd.db
// (acl/secrets/tasks/pane) or federated over HTTP (identity → authd, session_log
// → runed). See sibling_db.go.
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

func (d *DB) Close() error {
	return d.db.Close()
}

// SQL returns the raw handle for callers that need it (tests).
func (d *DB) SQL() *sql.DB { return d.db }

func nowTS() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// --- groups ---

// PutGroup upserts a group identity row, persisting the per-group model +
// container_config so dispatchRun can forward them to runed (GroupConfig reads
// them back). A zero Config marshals to a small JSON object — harmless; runed's
// round-trip yields the same zero core.GroupConfig.
func (d *DB) PutGroup(g core.Group) error {
	cfgJSON, _ := json.Marshal(g.Config)
	_, err := d.db.Exec(`INSERT INTO groups(folder, added_at, product, model, container_config)
		VALUES(?,?,?,?,?)
		ON CONFLICT(folder) DO UPDATE SET product=excluded.product, model=excluded.model,
			container_config=excluded.container_config, updated_at=?`,
		g.Folder, nowTS(), g.Product, g.Model, string(cfgJSON), nowTS())
	return err
}

// GroupExists reports whether folder is a registered group.
func (d *DB) GroupExists(folder string) bool {
	var n int
	d.db.QueryRow("SELECT 1 FROM groups WHERE folder=?", folder).Scan(&n)
	return n == 1
}

// GroupByFolder returns the full group identity (Config decoded from the
// container_config JSON TEXT) for folder, or (zero, false) when absent.
// Mirrors store.GroupByFolder; spawn-on-delegation reads the parent's
// Config.MaxChildren through it.
func (d *DB) GroupByFolder(folder string) (core.Group, bool) {
	var g core.Group
	var added string
	var c sql.NullString
	err := d.db.QueryRow(
		"SELECT folder, added_at, product, model, container_config FROM groups WHERE folder=?",
		folder).Scan(&g.Folder, &added, &g.Product, &g.Model, &c)
	if err != nil {
		return core.Group{}, false
	}
	g.AddedAt, _ = time.Parse(time.RFC3339Nano, added)
	if c.Valid && c.String != "" {
		_ = json.Unmarshal([]byte(c.String), &g.Config)
	}
	return g, true
}

// DeleteGroup removes a group identity row (spawn-on-delegation rollback when
// the route insert fails — never leave a route-less orphan).
func (d *DB) DeleteGroup(folder string) error {
	_, err := d.db.Exec("DELETE FROM groups WHERE folder=?", folder)
	return err
}

// GroupConfig returns the per-group model override + the opaque container_config
// (parsed from its JSON TEXT column) for dispatchRun to forward to runed. Empty
// model / nil config when the group is unknown or unset → runed uses the
// instance defaults.
func (d *DB) GroupConfig(folder string) (model string, cfg map[string]any) {
	var m sql.NullString
	var c sql.NullString
	d.db.QueryRow("SELECT model, container_config FROM groups WHERE folder=?", folder).Scan(&m, &c)
	model = m.String
	if c.Valid && c.String != "" {
		_ = json.Unmarshal([]byte(c.String), &cfg)
	}
	return model, cfg
}

// AllGroups returns every registered group keyed by folder (mirrors
// store.AllGroups). Backs the agent's get_groups MCP tool.
func (d *DB) AllGroups() map[string]core.Group {
	rows, err := d.db.Query("SELECT folder, added_at, product, model FROM groups")
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]core.Group{}
	for rows.Next() {
		var g core.Group
		var added string
		if err := rows.Scan(&g.Folder, &added, &g.Product, &g.Model); err != nil {
			slog.Error("AllGroups scan failed; failing closed", "err", err)
			return nil
		}
		g.AddedAt, _ = time.Parse(time.RFC3339Nano, added)
		out[g.Folder] = g
	}
	if err := rows.Err(); err != nil {
		slog.Error("AllGroups iteration failed; failing closed", "err", err)
		return nil
	}
	return out
}

// SetGroupOpen toggles the group's open flag (ambient turn admission).
func (d *DB) SetGroupOpen(folder string, open bool) error {
	_, err := d.db.Exec("UPDATE groups SET open=? WHERE folder=?", b2i(open), folder)
	return err
}

// SetGroupObserveWindow overrides the group's observe-window (messages, chars).
// A negative value clears the column (NULL) so the cfg default wins.
func (d *DB) SetGroupObserveWindow(folder string, msgs, chars int) error {
	var mv, cv any
	if msgs >= 0 {
		mv = msgs
	}
	if chars >= 0 {
		cv = chars
	}
	_, err := d.db.Exec(
		"UPDATE groups SET observe_window_messages=?, observe_window_chars=? WHERE folder=?",
		mv, cv, folder)
	return err
}

// AddGroupWatcher subscribes observer to source's ambient context (idempotent).
func (d *DB) AddGroupWatcher(observer, source string) error {
	_, err := d.db.Exec(
		"INSERT OR IGNORE INTO group_watchers(observer, source) VALUES(?,?)", observer, source)
	return err
}

// RemoveGroupWatcher drops the observe_group subscription.
func (d *DB) RemoveGroupWatcher(observer, source string) error {
	_, err := d.db.Exec(
		"DELETE FROM group_watchers WHERE observer=? AND source=?", observer, source)
	return err
}

// --- messages (sole appender) ---

// execer is the shared subset of *sql.DB and *sql.Tx PutMessage uses, so the
// same INSERT serves both the auto-commit and in-tx (atomic-with-ledger)
// paths.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func putMessage(x execer, m core.Message) error {
	_, err := x.Exec(`INSERT OR IGNORE INTO messages
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

// PutMessage appends one messages row. routd is the only writer. A
// duplicate id is a no-op (first-written row authoritative, append-only
// log).
func (d *DB) PutMessage(m core.Message) error { return putMessage(d.db, m) }

// EnrichMessage replaces a message's content with the media-enriched body and
// clears its attachments (the raw refs are now downloaded + inlined). Mirror
// of store.EnrichMessage — called by the inbound media-enrich pass so the
// observed-context render on later turns sees the transcript, not the refs.
func (d *DB) EnrichMessage(id, content string) error {
	_, err := d.db.Exec(`UPDATE messages SET content=?, attachments='' WHERE id=?`, content, id)
	return err
}

// AppendAndFinish appends the bot row AND finishes the idempotency ledger row
// in ONE tx (spec 5/E § Idempotency step 2). A crash between the two cannot
// leave a permanent in_flight ledger row: either both commit or neither does,
// and the retry replays. msg==nil finishes only the ledger (mutation
// handlers that append no row).
func (d *DB) AppendAndFinish(msg *core.Message, endpoint, key string, status int, response string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if msg != nil {
		if err := putMessage(tx, *msg); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("UPDATE idempotency_keys SET status=?, response=? WHERE endpoint=? AND key=?",
		status, response, endpoint, key); err != nil {
		return err
	}
	return tx.Commit()
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
	rows, err := d.db.Query(`SELECT `+msgReadCols+`
		FROM messages WHERE timestamp > ? ORDER BY timestamp ASC`, since)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()
	msgs, hi, err := scanMessages(rows, since)
	return msgs, hi, err
}

// MessagesSince returns one chat's rows with timestamp > since.
func (d *DB) MessagesSince(chatJID, since string) ([]core.Message, error) {
	rows, err := d.db.Query(`SELECT `+msgReadCols+`
		FROM messages WHERE chat_jid=? AND timestamp > ? ORDER BY timestamp ASC`, chatJID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, _, err := scanMessages(rows, since)
	return msgs, err
}

// History returns the chat's rows older than before (or all when before is
// empty), newest-bounded by limit, chronological.
func (d *DB) History(chatJID, before string, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + msgReadCols + ` FROM messages WHERE chat_jid=?`
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
	msgs, _, err := scanMessages(rows, "")
	if err != nil {
		return nil, err
	}
	// reverse to chronological
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// scanMessages drains rows into core.Message values, tracking the high-water
// mark from `since`. A row scan error ABORTS (returns the error) rather than
// skipping the row: a silent skip past a malformed row would advance the
// cursor over it and lose the message permanently.
func scanMessages(rows *sql.Rows, since string) ([]core.Message, string, error) {
	hi := since
	var out []core.Message
	for rows.Next() {
		var m core.Message
		var ts, turnID, platformID, fwdFrom sql.NullString
		var fromMe, botMsg int
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.Name, &m.Content, &ts,
			&fromMe, &botMsg, &m.ReplyToID, &m.Topic, &m.RoutedTo, &m.Verb, &m.Source,
			&turnID, &m.Status, &platformID, &m.ChatName, &fwdFrom); err != nil {
			return out, hi, err
		}
		m.ForwardedFrom = fwdFrom.String
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
	return out, hi, rows.Err()
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

// PendingOutbound returns bot rows still status='pending' whose timestamp is
// at or before cutoff — the outbound retry feed (spec 5/E § Outbound is
// poll-reconciled).
func (d *DB) PendingOutbound(cutoff time.Time, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.Query(`SELECT `+msgReadCols+`
		FROM messages WHERE status='pending' AND is_bot_message=1 AND timestamp <= ?
		ORDER BY timestamp ASC LIMIT ?`, cutoff.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, _, err := scanMessages(rows, "")
	return msgs, err
}

// TopicByID returns the topic of a stored row matched by id OR platform_id —
// reaction topic-inheritance looks up the parent so a reaction to a threaded
// message routes to the parent's topic, not the main topic (spec 5/E
// § Channel ingress).
func (d *DB) TopicByID(id string) string {
	var topic string
	d.db.QueryRow("SELECT topic FROM messages WHERE id=? OR platform_id=? LIMIT 1", id, id).Scan(&topic)
	return topic
}

// MarkChatErrored flags a chat as errored (a run definitively failed). The
// flag surfaces in inspection; it does not block re-feed (the cursor advance
// past the failed batch is the starvation guard).
func (d *DB) MarkChatErrored(chatJID string) error {
	_, err := d.db.Exec(`INSERT INTO chats(jid, errored) VALUES(?,1)
		ON CONFLICT(jid) DO UPDATE SET errored=1`, chatJID)
	return err
}

// MarkMessagesErrored flags a failed run's trigger rows errored=1 so the
// circuit breaker can prune them (DeleteErroredMessages). Mirrors
// store.MarkMessagesErrored. The rows stay visible to a later successful run
// until the breaker trips. Empty list is a no-op.
func (d *DB) MarkMessagesErrored(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec("UPDATE messages SET errored=1 WHERE id=?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteErroredMessages drops a chat's errored=1 rows. The circuit-breaker
// handler calls it so the next inbound starts the chat from a clean batch
// (mirrors store.DeleteErroredMessages + gateway.onCircuitBreakerOpen).
func (d *DB) DeleteErroredMessages(chatJID string) error {
	_, err := d.db.Exec("DELETE FROM messages WHERE chat_jid=? AND errored=1", chatJID)
	return err
}

// CountErroredChats counts chats flagged errored — the /status surface. Mirrors
// store.CountErroredChats, reading routd's chats.errored (the coarse per-chat
// flag; messages.errored is the row-level prune target for the breaker).
func (d *DB) CountErroredChats() int {
	var n int
	d.db.QueryRow("SELECT COUNT(*) FROM chats WHERE errored=1").Scan(&n)
	return n
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

// SetStickyGroup pins (or clears, when folder=="") the @group navigation
// target for a chat — subsequent messages route to it until reset.
func (d *DB) SetStickyGroup(chatJID, folder string) error {
	_, err := d.db.Exec(`INSERT INTO chats(jid, sticky_group) VALUES(?,?)
		ON CONFLICT(jid) DO UPDATE SET sticky_group=excluded.sticky_group`, chatJID, folder)
	return err
}

// SetStickyTopic pins (or clears, when topic=="") the #topic navigation
// target for a chat.
func (d *DB) SetStickyTopic(chatJID, topic string) error {
	_, err := d.db.Exec(`INSERT INTO chats(jid, sticky_topic) VALUES(?,?)
		ON CONFLICT(jid) DO UPDATE SET sticky_topic=excluded.sticky_topic`, chatJID, topic)
	return err
}

// RoutedToByMessageID returns the folder a stored message was routed to
// (matched by id OR platform_id), or "" when absent. Delegation looks this
// up to send a reply-to-bot back to the child that authored the reply.
func (d *DB) RoutedToByMessageID(id string) string {
	var routed string
	d.db.QueryRow("SELECT routed_to FROM messages WHERE id=? OR platform_id=? LIMIT 1", id, id).Scan(&routed)
	return routed
}

// IsEngaged reports whether (jid, topic) has a live engagement window — the
// topic-root normalization probe (engagement recorded on root may not match a
// thread topic).
func (d *DB) IsEngaged(jid, topic string) bool {
	_, ok := d.Engaged(jid, topic)
	return ok
}

// MarkMessagesObserved flags rows as is_observed=1 and stamps routed_to so a
// route-table observe rule (target=folder#observe) ingests them silently into
// the folder's ambient context without firing a turn (spec 5/B).
func (d *DB) MarkMessagesObserved(folder string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec("UPDATE messages SET is_observed=1, routed_to=? WHERE id=?", folder, id); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// SetRoutes replaces the routes whose target (sans #fragment) is `folder` or
// under `folder/`, returning the new count. An empty folder (open mode)
// replaces the whole table. Mirrors store.SetRoutes' folder-scoped delete so a
// scoped caller never wipes another folder's routes.
func (d *DB) SetRoutes(folder string, routes []core.Route) (int, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if folder == "" {
		if _, err := tx.Exec("DELETE FROM routes"); err != nil {
			return 0, err
		}
	} else if _, err := tx.Exec(
		`DELETE FROM routes WHERE target = ? OR target LIKE ?||'#%' OR target LIKE ?||'/%'`,
		folder, folder, folder); err != nil {
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
// late callbacks resolve their topic from turn_id alone. returnTo is the
// delegation return-address (the trigger batch's forwarded_from): when set,
// the callback surface delivers reply/send/document back to it instead of the
// child folder JID the run addresses (gateway.go § deliverTo override).
//
// Returns live=false when the turn is ALREADY terminal (state='done') — a
// re-fed batch whose run completed must NOT be resurrected: when an earlier
// batch finishes and a later batch in the same poll steers, the cursor doesn't
// advance, so the next poll re-feeds the completed batch. The ON CONFLICT
// reset stays clamped to a still-live or 'expired' turn (the legitimate
// re-dispatch path); a 'done' turn keeps its terminal state and reports
// live=false so runTurn skips the re-dispatch.
func (d *DB) PutTurnContext(turnID, folder, topic, chatJID, trigger, returnTo string) (bool, error) {
	res, err := d.db.Exec(`INSERT INTO turn_context(turn_id, folder, topic, chat_jid, trigger_sender, return_to, started_at, state)
		VALUES(?,?,?,?,?,?,?, 'running')
		ON CONFLICT(turn_id) DO UPDATE SET folder=excluded.folder, topic=excluded.topic,
		chat_jid=excluded.chat_jid, trigger_sender=excluded.trigger_sender, return_to=excluded.return_to,
		state='running', run_returned=0
		WHERE turn_context.state != 'done'`,
		turnID, folder, topic, chatJID, trigger, returnTo, nowTS())
	if err != nil {
		return false, err
	}
	// A done turn matched the conflict but the WHERE clause suppressed the
	// reset → zero rows changed → not live.
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TurnContext is a turn's bound run-start context.
type TurnContext struct {
	TurnID      string
	Folder      string
	Topic       string
	ChatJID     string
	Trigger     string
	ReturnTo    string
	State       string
	RunReturned bool
}

// GetTurnContext recovers a turn's context by turn_id.
func (d *DB) GetTurnContext(turnID string) (TurnContext, bool) {
	var tc TurnContext
	var runReturned int
	err := d.db.QueryRow(`SELECT turn_id, folder, topic, chat_jid, trigger_sender, return_to, state, run_returned
		FROM turn_context WHERE turn_id=?`, turnID).Scan(
		&tc.TurnID, &tc.Folder, &tc.Topic, &tc.ChatJID, &tc.Trigger, &tc.ReturnTo, &tc.State, &runReturned)
	if err != nil {
		return TurnContext{}, false
	}
	tc.RunReturned = runReturned == 1
	return tc, true
}

// SetTurnState flips a turn's lifecycle state (running|done|expired).
func (d *DB) SetTurnState(turnID, state string) error {
	_, err := d.db.Exec("UPDATE turn_context SET state=? WHERE turn_id=?", state, turnID)
	return err
}

// SetRunReturned marks that POST /v1/runs has returned for a turn. After
// this, late callbacks 409 turn_done (the run is no longer live); before it,
// trailing frames from a still-live run stay valid even past an early
// submit_turn (spec 5/E § Post-terminal callbacks).
func (d *DB) SetRunReturned(turnID string) error {
	_, err := d.db.Exec("UPDATE turn_context SET run_returned=1 WHERE turn_id=?", turnID)
	return err
}

// SetTurnRunID records the runed-assigned run_id for a turn (turn_context.run_id),
// the reconciliation handle linking routd's turn to runed's spawn.
func (d *DB) SetTurnRunID(turnID, runID string) error {
	_, err := d.db.Exec("UPDATE turn_context SET run_id=? WHERE turn_id=?", runID, turnID)
	return err
}

// RunningTurns lists turn_ids still in state=running (crash-recovery feed).
func (d *DB) RunningTurns() ([]TurnContext, error) {
	rows, err := d.db.Query(`SELECT turn_id, folder, topic, chat_jid, trigger_sender, return_to, state, run_returned
		FROM turn_context WHERE state='running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TurnContext
	for rows.Next() {
		var tc TurnContext
		var runReturned int
		if err := rows.Scan(&tc.TurnID, &tc.Folder, &tc.Topic, &tc.ChatJID, &tc.Trigger, &tc.ReturnTo, &tc.State, &runReturned); err != nil {
			return nil, err
		}
		tc.RunReturned = runReturned == 1
		out = append(out, tc)
	}
	return out, rows.Err()
}

// SweepExpiredRunning flips stale state='running' turns older than the run
// timeout to the DISTINCT terminal 'expired' (NOT 'done') so they stop being
// re-fed by crash recovery, without tripping the done-guard against a
// legitimate re-dispatch (spec 5/E § turn lifecycle). Returns the count
// swept.
func (d *DB) SweepExpiredRunning(timeout time.Duration) (int64, error) {
	cutoff := time.Now().Add(-timeout).UTC().Format(time.RFC3339Nano)
	res, err := d.db.Exec("UPDATE turn_context SET state='expired' WHERE state='running' AND started_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// TurnResultRecorded reports whether submit_turn already recorded an outcome
// for (folder, turn_id). The run-response path checks this before persisting
// its session_id backstop so submit_turn's value wins (spec 5/E § Completion
// reconciliation).
func (d *DB) TurnResultRecorded(folder, turnID string) bool {
	var n int
	d.db.QueryRow("SELECT 1 FROM turn_results WHERE folder=? AND turn_id=?", folder, turnID).Scan(&n)
	return n == 1
}

// --- sessions (lineage; session_id opaque to routd) ---

// PutSession persists the session_id runed produced for (folder, topic).
func (d *DB) PutSession(folder, topic, sessionID string) error {
	_, err := d.db.Exec(`INSERT INTO sessions(group_folder, topic, session_id) VALUES(?,?,?)
		ON CONFLICT(group_folder, topic) DO UPDATE SET session_id=excluded.session_id`,
		folder, topic, sessionID)
	return err
}

// GetSession returns the persisted session id for (folder, topic) and whether
// a row exists (mirrors store.GetSession). copyParentSession reads the parent
// topic's session id through this before copying its jsonl.
func (d *DB) GetSession(folder, topic string) (string, bool) {
	var id sql.NullString
	err := d.db.QueryRow("SELECT session_id FROM sessions WHERE group_folder=? AND topic=?", folder, topic).Scan(&id)
	return id.String, err == nil
}

// EnsureTopicLineage inserts a sessions row for (folder, topic) with lineage if
// none exists yet (mirrors store.EnsureTopicLineage). Idempotent: a no-op when
// the row already exists. parentTopic="" forks from main; main topic "" is
// skipped (main has no parent). Returns inserted=true when a new row was
// created — the caller then copies the parent session file (spec 6/F).
func (d *DB) EnsureTopicLineage(folder, topic, parentTopic, newSessionID string) (bool, error) {
	if topic == "" {
		return false, nil
	}
	now := nowTS()
	res, err := d.db.Exec(
		`INSERT OR IGNORE INTO sessions(group_folder, topic, session_id, parent_topic, forked_at, observed_cursor)
		 VALUES(?,?,?,?,?,?)`,
		folder, topic, newSessionID, parentTopic, now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteSession drops the persisted session for (folder, topic) — the /new
// command's session reset.
func (d *DB) DeleteSession(folder, topic string) error {
	_, err := d.db.Exec("DELETE FROM sessions WHERE group_folder=? AND topic=?", folder, topic)
	return err
}

// ForkTopic seeds a fresh session row for child, recording its parent topic +
// fork time, with observed_cursor=now so the child reads only post-fork context
// (mirrors store.ForkTopic against routd's sessions table). force overwrites an
// existing child; otherwise a collision returns core.ErrTopicExists.
func (d *DB) ForkTopic(folder, parent, child, newSessionID string, force bool) error {
	if child == "" {
		return fmt.Errorf("fork: child topic empty")
	}
	now := nowTS()
	if force {
		_, err := d.db.Exec(
			`INSERT INTO sessions(group_folder, topic, session_id, parent_topic, forked_at, observed_cursor)
			 VALUES(?,?,?,?,?,?)
			 ON CONFLICT(group_folder, topic) DO UPDATE SET
			   session_id=excluded.session_id, parent_topic=excluded.parent_topic,
			   forked_at=excluded.forked_at, observed_cursor=excluded.observed_cursor`,
			folder, child, newSessionID, parent, now, now)
		return err
	}
	res, err := d.db.Exec(
		`INSERT OR IGNORE INTO sessions(group_folder, topic, session_id, parent_topic, forked_at, observed_cursor)
		 VALUES(?,?,?,?,?,?)`,
		folder, child, newSessionID, parent, now, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrTopicExists
	}
	return nil
}

// SessionID returns the persisted session id for (folder, topic).
func (d *DB) SessionID(folder, topic string) string {
	var id sql.NullString
	d.db.QueryRow("SELECT session_id FROM sessions WHERE group_folder=? AND topic=?", folder, topic).Scan(&id)
	return id.String
}

// --- cost ---

// PutCost writes one cost_log row per model under the (folder,turn_id)
// dedup; a duplicate submit_turn does not double-charge. userSub is the
// JWT-derived caller (callerSubOfMsg of the turn's trigger sender; "" for
// adapter/anon/system turns) so SpendTodayUser can aggregate per-user spend.
func (d *DB) PutCost(folder, turnID, userSub, model string, in, out, cents int) error {
	_, err := d.db.Exec(`INSERT OR IGNORE INTO cost_log(folder, turn_id, user_sub, model, input_tokens, output_tokens, cost_cents, recorded_at)
		VALUES(?,?,?,?,?,?,?,?)`, folder, turnID, userSub, model, in, out, cents, nowTS())
	return err
}

// FolderCap returns the per-day spend cap for a folder in cents. Zero means
// uncapped (the default). Mirrors store.FolderCap against routd's own groups
// table (groups.cost_cap_cents_per_day).
func (d *DB) FolderCap(folder string) (int, error) {
	var cents int
	err := d.db.QueryRow(
		"SELECT COALESCE(cost_cap_cents_per_day, 0) FROM groups WHERE folder=?", folder).Scan(&cents)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return cents, err
}

// SpendTodayFolder sums cost_log.cost_cents for a folder since the UTC start
// of today — the pre-spawn budget gate's spend view. Mirrors
// store.SpendTodayFolder against routd's own cost_log (logged by PutCost).
func (d *DB) SpendTodayFolder(folder string) (int, error) {
	var cents int
	err := d.db.QueryRow(
		`SELECT COALESCE(SUM(cost_cents), 0) FROM cost_log WHERE folder=? AND recorded_at >= ?`,
		folder, startOfTodayUTC()).Scan(&cents)
	return cents, err
}

// SpendTodayUser sums cost_log.cost_cents for a user_sub since the UTC start of
// today — the per-user half of the budget gate. Mirrors store.SpendTodayUser
// against routd's own cost_log (rows logged by PutCost with the caller sub).
func (d *DB) SpendTodayUser(userSub string) (int, error) {
	var cents int
	err := d.db.QueryRow(
		`SELECT COALESCE(SUM(cost_cents), 0) FROM cost_log WHERE user_sub=? AND recorded_at >= ?`,
		userSub, startOfTodayUTC()).Scan(&cents)
	return cents, err
}

// startOfTodayUTC is the RFC3339Nano timestamp at 00:00 UTC today — the
// budget window's lower bound (mirrors store.startOfTodayUTC).
func startOfTodayUTC() string {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
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

// LatestSource returns the adapter name that delivered the newest real
// inbound row on a chat (the messages.source column), used by the Deliverer
// to route an outbound back to the same adapter the conversation came in on.
// Empty when the chat has no sourced inbound. Mirrors store.LatestSource.
func (d *DB) LatestSource(jid string) string {
	var src string
	d.db.QueryRow(
		`SELECT source FROM messages
		 WHERE chat_jid=? AND source<>'' AND is_bot_message=0
		 ORDER BY timestamp DESC LIMIT 1`, jid).Scan(&src)
	return src
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
