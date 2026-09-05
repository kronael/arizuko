package routd

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth/surrogate"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "routd"

// DB is routd's owner of routd.db: the message/event store + routing rules +
// turn lifecycle + route tokens. routd is the SOLE appender of messages. Times
// are RFC3339Nano UTC, computed in Go.
type DB struct {
	db *sql.DB

	// secretKeyring is the SECRETS_KEY material (raw, pre-SHA256) routd hands to
	// its OWN secretStore via SetSecretKeys so reads decrypt `v2:` values and
	// writes seal them. Empty → no key set → reads stay ciphertext (no plaintext
	// leak; the connector just gets nothing usable) and writes store plaintext.
	secretKeyring [][]byte

	// surrogate refreshes near-expiry OAuth secret rows at broker call time
	// (spec 5/15). nil → no refresh (PAT-only path); ConnectorSecrets then reads
	// the stored value unchanged. Wired in routd main from auth/surrogate.
	surrogate *surrogate.Engine
}

// SetSurrogate wires the surrogate-OAuth engine used by ConnectorSecrets to
// refresh near-expiry capability credentials at call time. nil-safe (no refresh).
func (d *DB) SetSurrogate(e *surrogate.Engine) { d.surrogate = e }

// SetSecretKeys supplies the SECRETS_KEY keyring (raw values; the active seal
// key first, retired keys after) so secret reads off routd's own secrets table
// decrypt and writes seal. Empty/no-call → reads stay ciphertext, writes store
// plaintext.
func (d *DB) SetSecretKeys(raws ...[]byte) { d.secretKeyring = raws }

// Open attaches to an EXISTING routd.db at dir/routd.db (WAL, FK on) and runs the
// routd migration sequence. It never creates the file — see Create. routd opens
// NO sibling DB: every table it needs is in routd.db or federated over HTTP
// (identity → authd, session_log → runed). The one-time monolith→split copy
// belongs to `arizuko migrate-split`, the sole tool that reads messages.db.
func Open(dir string) (*DB, error) {
	path := store.OwnerDBPath(dir, store.OwnerRoutd)
	if err := db_utils.RequireDBFile(path); err != nil {
		return nil, err
	}
	return open(db_utils.FileDSN(path))
}

// Create seeds dir/routd.db when absent, then migrates it. `arizuko create` and
// the split-cutover tool only: every other caller Opens, so a wrong path fails
// loud instead of yielding a migrated instance with zero groups and zero acl.
func Create(dir string) (*DB, error) {
	path := store.OwnerDBPath(dir, store.OwnerRoutd)
	if err := db_utils.CreateDBFile(path); err != nil {
		return nil, err
	}
	return open(db_utils.FileDSN(path))
}

// OpenMem opens a fresh isolated in-memory routd.db for tests. The DB name
// is unique per call so concurrent / sequential tests don't share state via
// the shared cache.
func OpenMem() (*DB, error) {
	name := "routd_mem_" + randHex(8)
	return open("file:" + name + "?mode=memory&cache=shared&_pragma=foreign_keys(on)")
}

func open(dsn string) (*DB, error) {
	sqldb, err := db_utils.OpenWAL(dsn)
	if err != nil {
		return nil, err
	}
	if err := db_utils.Migrate(sqldb, migrationFS, "migrations", serviceName); err != nil {
		sqldb.Close()
		return nil, err
	}
	// role:member (the 5/33 messaging floor) is seeded by migration 0023; every
	// folder is bound to it at group creation (PutGroup). No open-time grant seeding
	// or backfill — auth.Authorize reads the acl rows directly.
	return &DB{db: sqldb}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

// SQL returns the raw handle for callers that need it (tests).
func (d *DB) SQL() *sql.DB { return d.db }

func nowTS() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// PutGroup upserts a group identity row, persisting the per-group model +
// container_config so dispatchRun can forward them to runed (GroupConfig reads
// them back). A zero Config marshals to a small JSON object — harmless; runed's
// round-trip yields the same zero core.GroupConfig.
func (d *DB) PutGroup(g core.Group) error {
	cfgJSON, _ := json.Marshal(g.Config)
	var model any
	if g.Model != "" {
		model = g.Model
	}
	if _, err := d.db.Exec(`INSERT INTO groups(folder, added_at, product, config, container_config)
		VALUES(?,?,?, json_set('{}', '$.model', ?), ?)
		ON CONFLICT(folder) DO UPDATE SET product=excluded.product,
			config=json_set(COALESCE(groups.config, '{}'), '$.model', ?),
			container_config=excluded.container_config, updated_at=?`,
		g.Folder, nowTS(), g.Product, model, string(cfgJSON), model, nowTS()); err != nil {
		return err
	}
	// Create-time default role (5/33): bind the new folder to role:member so it holds
	// the messaging floor immediately. Everything above the floor is an explicit acl
	// row. register_group itself is gated by db.Authorize and calls no Delegate — it
	// seeds this floor, it does not delegate anything above it.
	return assignDefaultRole(store.New(d.db), g.Folder)
}

// GroupExists reports whether folder is a registered group.
func (d *DB) GroupExists(folder string) bool {
	var n int
	d.db.QueryRow("SELECT 1 FROM groups WHERE folder=?", folder).Scan(&n)
	return n == 1
}

// DeleteGroup removes a group and cascades every row pointing at it. The
// cascade, its transaction and its audit row all live in store.DeleteGroup —
// the ONE delete, shared with the operator's `arizuko group rm`. routd having
// its own copy is what let the CLI ship a delete with no cascade at all.
func (d *DB) DeleteGroup(folder string) error {
	return d.userStore().DeleteGroup(folder)
}

// GroupConfig returns the per-group model override + the opaque container_config
// (parsed from its JSON TEXT column) for dispatchRun to forward to runed. Empty
// model / nil config when the group is unknown or unset → runed uses the
// instance defaults.
func (d *DB) GroupConfig(folder string) (model string, cfg map[string]any) {
	var m sql.NullString
	var c sql.NullString
	d.db.QueryRow(
		"SELECT json_extract(config, '$.model'), container_config FROM groups WHERE folder=?",
		folder).Scan(&m, &c)
	model = m.String
	if c.Valid && c.String != "" {
		_ = json.Unmarshal([]byte(c.String), &cfg)
	}
	return model, cfg
}

// AllGroups returns every registered group keyed by folder. Backs the agent's
// get_groups MCP tool.
func (d *DB) AllGroups() map[string]core.Group {
	rows, err := d.db.Query("SELECT folder, added_at, product, COALESCE(json_extract(config, '$.model'), ''), COALESCE(container_config, '{}') FROM groups")
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]core.Group{}
	for rows.Next() {
		var g core.Group
		var added string
		var cfgJSON string
		if err := rows.Scan(&g.Folder, &added, &g.Product, &g.Model, &cfgJSON); err != nil {
			slog.Error("AllGroups scan failed; failing closed", "err", err)
			return nil
		}
		g.AddedAt, _ = time.Parse(time.RFC3339Nano, added)
		if cfgJSON != "" && cfgJSON != "{}" {
			_ = json.Unmarshal([]byte(cfgJSON), &g.Config)
		}
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
	return store.New(d.db).SetGroupOpen(folder, open)
}

// ThreadReplies reads the group's thread_replies preference; an unset key
// (NULL / unknown group) yields dflt (the caller passes the chat's multi-user
// flag: group chats thread by default, DMs don't).
func (d *DB) ThreadReplies(folder string, dflt bool) bool {
	var v sql.NullInt64
	d.db.QueryRow(
		"SELECT json_extract(config, '$.thread_replies') FROM groups WHERE folder=?",
		folder).Scan(&v)
	if !v.Valid {
		return dflt
	}
	return v.Int64 == 1
}

// SetGroupObserveWindow overrides the group's observe-window (messages, chars).
// A negative value clears the key (json NULL) so the cfg default wins.
func (d *DB) SetGroupObserveWindow(folder string, msgs, chars int) error {
	return store.New(d.db).SetGroupObserveWindow(folder, msgs, chars)
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

// execer is the shared subset of *sql.DB and *sql.Tx PutMessage uses, so the
// same INSERT serves both the auto-commit path and the in-tx re-issue
// (pending_actions.go).
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func putMessage(x execer, m core.Message) error {
	_, err := x.Exec(`INSERT OR IGNORE INTO messages
		(id, chat_jid, sender, sender_name, content, timestamp, is_from_me,
		 is_bot_message, forwarded_from, reply_to_id, reply_to_text, reply_to_sender,
		 topic, routed_to, verb, attachments, source, is_observed, turn_id, status,
		 platform_id, chat_name, link_context)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.ChatJID, m.Sender, m.Name, m.Content, m.Timestamp.UTC().Format(time.RFC3339Nano),
		b2i(m.FromMe), b2i(m.BotMsg), m.ForwardedFrom, m.ReplyToID, m.ReplyToText, m.ReplyToSender,
		m.Topic, m.RoutedTo, defaultVerb(m.Verb), m.Attachments, m.Source, b2i(false),
		nullStr(m.TurnID), defaultStatus(m.Status), nullStr(m.PlatformID), m.ChatName,
		nullStr(m.LinkContext))
	return err
}

// PutMessage appends one messages row. routd is the only writer. A
// duplicate id is a no-op (first-written row authoritative, append-only
// log).
func (d *DB) PutMessage(m core.Message) error { return putMessage(d.db, m) }

// EnrichMessage replaces a message's content with the media-enriched body and
// clears its attachments (the raw refs are now downloaded + inlined) so the
// observed-context render on later turns sees the transcript, not the refs.
func (d *DB) EnrichMessage(id, content string) error {
	_, err := d.db.Exec(`UPDATE messages SET content=?, attachments='' WHERE id=?`, content, id)
	return err
}

// NewMessages returns rows with timestamp > since across all chats, plus the new
// high-water mark (the pollOnce feed).
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
			&fromMe, &botMsg, &m.ReplyToID, &m.ReplyToText, &m.ReplyToSender,
			&m.Topic, &m.RoutedTo, &m.Verb, &m.Source,
			&turnID, &m.Status, &platformID, &m.ChatName, &fwdFrom, &m.Attachments,
			&m.LinkContext, &m.DeliveryAttempts); err != nil {
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

// RecordDeliveryFailure counts one transient delivery failure on an outbound row
// and dead-letters the row when the count reaches maxAttempts. One statement does
// both, so a crash between them is impossible. SQLite reads the SET expressions
// from the pre-update row, so both uses of delivery_attempts + 1 give the same
// new count.
func (d *DB) RecordDeliveryFailure(id string, maxAttempts int) (int, bool, error) {
	var attempts int
	var status string
	err := d.db.QueryRow(`UPDATE messages
		SET delivery_attempts = delivery_attempts + 1,
		    status = CASE WHEN delivery_attempts + 1 >= ? THEN ? ELSE status END
		WHERE id = ?
		RETURNING delivery_attempts, status`,
		maxAttempts, core.MessageStatusFailed, id).Scan(&attempts, &status)
	return attempts, status == core.MessageStatusFailed, err
}

// PendingOutbound returns bot rows still status='pending' whose timestamp is at
// or before cutoff — the outbound retry feed.
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

// MessageByID returns the stored row matched by id OR platform_id: an adapter
// names a reply's parent by the platform's id, which a bot row carries in
// platform_id. ok=false when no row matches.
func (d *DB) MessageByID(id string) (core.Message, bool) {
	rows, err := d.db.Query(`SELECT `+msgReadCols+`
		FROM messages WHERE id=? OR platform_id=? LIMIT 1`, id, id)
	if err != nil {
		return core.Message{}, false
	}
	defer rows.Close()
	msgs, _, err := scanMessages(rows, "")
	if err != nil || len(msgs) == 0 {
		return core.Message{}, false
	}
	return msgs[0], true
}

// TopicByID returns the topic of the row MessageByID matches, so a reply to a
// threaded message forks lineage from the parent's topic, not the main topic.
func (d *DB) TopicByID(id string) string {
	m, _ := d.MessageByID(id)
	return m.Topic
}

// MarkChatErrored flags a chat as errored (a run definitively failed). The
// flag surfaces in inspection; it does not block re-feed (the cursor advance
// past the failed batch is the starvation guard).
func (d *DB) MarkChatErrored(chatJID string) error {
	_, err := d.db.Exec(`INSERT INTO chats(jid, errored) VALUES(?,1)
		ON CONFLICT(jid) DO UPDATE SET errored=1`, chatJID)
	return err
}

// ClearErrored un-flags a chat's errored rows so the dispatch loop feeds them
// again — the operator's "retry this chat" action. Returns the number of rows
// cleared so a caller can refuse to report success on zero (a retry that
// retried nothing is not a retry).
//
// routd owns the messages table, so this is its writer. dashd used to do the
// same UPDATE over a direct handle on routd.db, which is the cross-daemon read
// path spec 6/28 retires.
func (d *DB) ClearErrored(chatJID string) (int64, error) {
	res, err := d.db.Exec("UPDATE messages SET errored=0 WHERE chat_jid=? AND errored=1", chatJID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkMessagesErrored flags a failed run's trigger rows errored=1 so the circuit
// breaker can prune them (DeleteErroredMessages). The rows stay visible to a
// later successful run until the breaker trips. Empty list is a no-op.
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
// handler calls it so the next inbound starts the chat from a clean batch.
func (d *DB) DeleteErroredMessages(chatJID string) error {
	_, err := d.db.Exec("DELETE FROM messages WHERE chat_jid=? AND errored=1", chatJID)
	return err
}

// CountErroredChats counts chats flagged errored — the /status surface. Reads
// chats.errored (the coarse per-chat flag; messages.errored is the row-level
// prune target for the breaker).
func (d *DB) CountErroredChats() int {
	var n int
	d.db.QueryRow("SELECT COUNT(*) FROM chats WHERE errored=1").Scan(&n)
	return n
}

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

// ChatIsGroup reads the multi-party flag for a chat; unknown chats are false.
func (d *DB) ChatIsGroup(chatJID string) bool {
	var v sql.NullInt64
	d.db.QueryRow("SELECT is_group FROM chats WHERE jid=?", chatJID).Scan(&v)
	return v.Valid && v.Int64 == 1
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
// the folder's ambient context without firing a turn.
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

// ErrNotFound signals an absent row to the HTTP layer (404).
var ErrNotFound = errors.New("not found")

// engagementUpsert is the ONE statement that moves an engagement window. Both
// SetEngagement and SetEngagementAudited execute it, so the audited and
// un-audited entrypoints cannot drift into writing different rows.
const engagementUpsert = `INSERT INTO chat_reply_state(jid, topic, last_reply_id, engaged_until, engaged_folder)
	VALUES(?,?,'',?,?)
	ON CONFLICT(jid, topic) DO UPDATE SET engaged_until=excluded.engaged_until, engaged_folder=excluded.engaged_folder`

func engagementArgs(jid, topic, folder string, ttl time.Duration) []any {
	return []any{jid, topic, time.Now().Add(ttl).UTC().Format(time.RFC3339Nano), folder}
}

// SetEngagement claims an engagement window for (jid, topic) by folder until
// now+ttl.
//
// Un-audited on purpose: its callers are the PER-TURN claim sites, which fire on
// every conversational turn and are already recorded. An audit_log row per turn
// would bury the operator trail in the noise it exists to make findable.
// Operator-originated writes take SetEngagementAudited instead.
func (d *DB) SetEngagement(jid, topic, folder string, ttl time.Duration) error {
	_, err := d.db.Exec(engagementUpsert, engagementArgs(jid, topic, folder, ttl)...)
	return err
}

// SetEngagementAudited is SetEngagement plus its audit_log row, both in ONE
// transaction. This is the entrypoint for /v1/engagement: an operator ending
// someone's conversation early leaves no message row and no turn behind, so this
// row is the only durable trace.
//
// EmitInTx rather than Emit: an audit row that outlived a rolled-back write would
// describe a window that never moved, and a write whose audit insert failed would
// be the unrecorded mutation this closes.
func (d *DB) SetEngagementAudited(ctx context.Context, jid, topic, folder string, ttl time.Duration, ev audit.Event) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, engagementUpsert, engagementArgs(jid, topic, folder, ttl)...); err != nil {
		return err
	}
	if err := audit.EmitInTx(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit()
}

// Engagement is one LIVE chat_reply_state window: which folder claims
// (jid, topic) and until when. Spec 5/G.
type Engagement struct {
	JID    string
	Topic  string
	Folder string
	Until  time.Time
}

// Engagement returns the live window for (jid, topic). Not live: no row, a NULL
// deadline or folder, an unparseable deadline, or a deadline already past.
func (d *DB) Engagement(jid, topic string) (Engagement, bool) {
	var until sql.NullString
	var folder string
	err := d.db.QueryRow("SELECT engaged_until, engaged_folder FROM chat_reply_state WHERE jid=? AND topic=?",
		jid, topic).Scan(&until, &folder)
	if err != nil || !until.Valid {
		return Engagement{}, false
	}
	t, perr := time.Parse(time.RFC3339Nano, until.String)
	if perr != nil || !t.After(time.Now()) {
		return Engagement{}, false
	}
	return Engagement{JID: jid, Topic: topic, Folder: folder, Until: t}, true
}

// Engaged returns (folder, true) when (jid, topic) has a live engagement.
func (d *DB) Engaged(jid, topic string) (string, bool) {
	e, live := d.Engagement(jid, topic)
	return e.Folder, live
}

// ListEngaged enumerates the live engagement windows visible to folder, newest
// deadline first. `all` widens to the whole fleet and belongs ONLY to a
// root/service caller; the handler derives it from an EMPTY folder claim, never
// from depth.
//
// Containment is descendant(row.Folder, folder) — self-or-below — deliberately
// NOT ownsFolder, which reports true for an empty target and would show every
// scoped caller the rows no folder has claimed. The window filter runs here
// because the deadline is RFC3339Nano TEXT, and the subtree filter runs here so
// containment has ONE implementation instead of a LIKE pattern that a folder name
// containing _ or % would silently widen.
func (d *DB) ListEngaged(folder string, all bool) ([]Engagement, error) {
	rows, err := d.db.Query(
		`SELECT jid, topic, engaged_until, engaged_folder FROM chat_reply_state
		 WHERE engaged_until IS NOT NULL AND engaged_folder IS NOT NULL
		 ORDER BY engaged_until DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	out := []Engagement{}
	for rows.Next() {
		var jid, topic, until, owner string
		if err := rows.Scan(&jid, &topic, &until, &owner); err != nil {
			return nil, err
		}
		t, perr := time.Parse(time.RFC3339Nano, until)
		if perr != nil || !t.After(now) {
			continue
		}
		if !all && !descendant(owner, folder) {
			continue
		}
		out = append(out, Engagement{JID: jid, Topic: topic, Folder: owner, Until: t})
	}
	return out, rows.Err()
}

// SetLastReply seeds the reply-to-bot threading anchor for (jid, topic).
// Only updates last_reply_id on conflict — never clobbers engaged_folder on an
// existing row, since an active engagement window (set by SetEngagement) would
// be silently hijacked by a broadcast send from a different group.
func (d *DB) SetLastReply(jid, topic, msgID, folder string) error {
	_, err := d.db.Exec(`INSERT INTO chat_reply_state(jid, topic, last_reply_id, engaged_folder)
		VALUES(?,?,?,?)
		ON CONFLICT(jid, topic) DO UPDATE SET last_reply_id=excluded.last_reply_id`,
		jid, topic, msgID, folder)
	return err
}

// LastReplyID returns the thread anchor for (jid, topic).
func (d *DB) LastReplyID(jid, topic string) string {
	var id sql.NullString
	d.db.QueryRow("SELECT last_reply_id FROM chat_reply_state WHERE jid=? AND topic=?", jid, topic).Scan(&id)
	return id.String
}

// PutTurnContext records a turn's (folder, topic, chat_jid) at dispatch so late
// callbacks resolve their topic from turn_id alone. returnTo is the delegation
// return-address: when set, the callback surface delivers back to it instead of
// the child folder JID.
//
// Returns live=false when the turn is ALREADY terminal — a re-fed batch whose run
// completed must NOT be resurrected. When an earlier batch finishes and a later
// batch in the same poll steers, the cursor does not advance, so the next poll
// re-feeds the completed batch.
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
	TurnID       string
	Folder       string
	Topic        string
	ChatJID      string
	Trigger      string
	TriggerMsgID string
	ReturnTo     string
	State        string
	RunReturned  bool
	RetryCount   int
}

// GetTurnContext recovers a turn's context by turn_id.
func (d *DB) GetTurnContext(turnID string) (TurnContext, bool) {
	var tc TurnContext
	var trigMsg sql.NullString
	var runReturned int
	err := d.db.QueryRow(`SELECT turn_id, folder, topic, chat_jid, trigger_sender, trigger_msg_id, return_to, state, run_returned, retry_count
		FROM turn_context WHERE turn_id=?`, turnID).Scan(
		&tc.TurnID, &tc.Folder, &tc.Topic, &tc.ChatJID, &tc.Trigger, &trigMsg, &tc.ReturnTo, &tc.State, &runReturned, &tc.RetryCount)
	if err != nil {
		return TurnContext{}, false
	}
	tc.TriggerMsgID = trigMsg.String
	tc.RunReturned = runReturned == 1
	return tc, true
}

// SetTurnTriggerMsg records the trigger message's platform id for a turn
// (turn_context.trigger_msg_id) — the message a threaded reply roots a new
// platform thread on. Set at dispatch right after PutTurnContext (the
// SetTurnRunID pattern).
func (d *DB) SetTurnTriggerMsg(turnID, msgID string) error {
	_, err := d.db.Exec("UPDATE turn_context SET trigger_msg_id=? WHERE turn_id=?", msgID, turnID)
	return err
}

// SetTurnState flips a turn's lifecycle state (running|done|expired).
func (d *DB) SetTurnState(turnID, state string) error {
	_, err := d.db.Exec("UPDATE turn_context SET state=? WHERE turn_id=?", state, turnID)
	return err
}

// SetRunReturned marks that POST /v1/runs has returned for a turn. After this,
// late callbacks 409 turn_done (the run is no longer live); before it, trailing
// frames from a still-live run stay valid even past an early submit_turn.
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

// TypingJIDsForRun returns the chats whose typing indicator a run owns: one per
// turn recorded against run_id, resolved the same way dispatch picks the
// indicator's chat.
//
// A follow-up that arrives mid-run is STEERED into the live container, and
// steering is by FOLDER, so the steered turn's chat is not always the running
// turn's chat. Its indicator was handed to a submit_turn that never fires when the
// run dies at the timeout wall (BUGS T1).
func (d *DB) TypingJIDsForRun(runID string) []string {
	rows, err := d.db.Query(
		`SELECT DISTINCT COALESCE(NULLIF(return_to,''), chat_jid)
		 FROM turn_context WHERE run_id=?`, runID)
	if err != nil {
		slog.Error("typing jids for run", "run_id", runID, "err", err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			slog.Error("typing jids for run: scan", "run_id", runID, "err", err)
			continue
		}
		out = append(out, jid)
	}
	return out
}

// IncrementRetryCount bumps a turn's retry_count and sets state to pending_retry.
// Returns the new count.
func (d *DB) IncrementRetryCount(turnID string) (int, error) {
	_, err := d.db.Exec("UPDATE turn_context SET retry_count=retry_count+1, state='pending_retry' WHERE turn_id=?", turnID)
	if err != nil {
		return 0, err
	}
	var count int
	d.db.QueryRow("SELECT retry_count FROM turn_context WHERE turn_id=?", turnID).Scan(&count)
	return count, nil
}

// ResetTurnForRetry resets a turn's state to running for the next retry attempt.
func (d *DB) ResetTurnForRetry(turnID string) error {
	_, err := d.db.Exec("UPDATE turn_context SET state='running', run_returned=0 WHERE turn_id=?", turnID)
	return err
}

// RunningTurns lists turn_ids still in state=running or pending_retry
// (crash-recovery feed).
func (d *DB) RunningTurns() ([]TurnContext, error) {
	rows, err := d.db.Query(`SELECT turn_id, folder, topic, chat_jid, trigger_sender, return_to, state, run_returned
		FROM turn_context WHERE state IN ('running', 'pending_retry')`)
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
// re-fed by crash recovery, without tripping the done-guard against a legitimate
// re-dispatch. Returns the count swept.
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

// TurnHasBotReply reports whether the agent already delivered a bot message for
// this turn via the reply/send tools, so recordTurnResult does not double-send the
// prose result. Interim verb="status" rows are excluded: an auto-status must NEVER
// count as the reply, or the agent's real answer is swallowed (spec 5/24).
func (d *DB) TurnHasBotReply(turnID string) bool {
	var n int
	d.db.QueryRow("SELECT 1 FROM messages WHERE turn_id=? AND is_bot_message=1 AND verb!='status' LIMIT 1", turnID).Scan(&n)
	return n == 1
}

// LastStatusPlatformID returns the platform id of the most recent delivered
// verb="status" (⏳) row for the turn, or "" if none landed yet. submit_status
// edits that message in place instead of streaming a new notice (spec 5/24).
func (d *DB) LastStatusPlatformID(turnID string) string {
	var pid string
	d.db.QueryRow(
		"SELECT platform_id FROM messages WHERE turn_id=? AND verb='status' AND platform_id!='' ORDER BY rowid DESC LIMIT 1",
		turnID,
	).Scan(&pid)
	return pid
}

// TurnResultRecorded reports whether submit_turn already recorded an outcome for
// (folder, turn_id). The run-response path checks this before persisting its
// session_id backstop so submit_turn's value wins.
func (d *DB) TurnResultRecorded(folder, turnID string) bool {
	var n int
	d.db.QueryRow("SELECT 1 FROM turn_results WHERE folder=? AND turn_id=?", folder, turnID).Scan(&n)
	return n == 1
}

// PutSession persists the session_id runed produced for (folder, topic).
func (d *DB) PutSession(folder, topic, sessionID string) error {
	_, err := d.db.Exec(`INSERT INTO sessions(group_folder, topic, session_id) VALUES(?,?,?)
		ON CONFLICT(group_folder, topic) DO UPDATE SET session_id=excluded.session_id`,
		folder, topic, sessionID)
	return err
}

// GetSession returns the persisted session id for (folder, topic) and whether a
// row exists. copyParentSession reads the parent topic's session id through this
// before copying its jsonl.
func (d *DB) GetSession(folder, topic string) (string, bool) {
	var id sql.NullString
	err := d.db.QueryRow("SELECT session_id FROM sessions WHERE group_folder=? AND topic=?", folder, topic).Scan(&id)
	return id.String, err == nil
}

// EnsureTopicLineage inserts a sessions row for (folder, topic) with lineage if
// none exists yet. Idempotent: a no-op when the row already exists.
// parentTopic="" forks from main; main topic "" is skipped (main has no parent).
// Returns inserted=true when a new row was created — the caller then copies the
// parent session file.
func (d *DB) EnsureTopicLineage(folder, topic, parentTopic, newSessionID string) (bool, error) {
	if topic == "" {
		return false, nil
	}
	// The child inherits the PARENT's observed cursor, not `now`. Stamping now
	// discarded the channel's whole ambient backlog at the moment a thread was
	// created: the copied transcript carries only turns that RAN in the parent, and a
	// busy channel's chatter is `is_observed=1` rows that never ran one, so a new
	// Discord thread opened blind to everything said before it existed (BUGS L87).
	//
	// No parent row (cold root) leaves it empty, the normal cold path. Re-showing a
	// message the parent already saw is benign — observed messages are context, not
	// requests.
	res, err := d.db.Exec(
		`INSERT OR IGNORE INTO sessions(group_folder, topic, session_id, parent_topic, forked_at, observed_cursor)
		 VALUES(?,?,?,?,?,
		   COALESCE((SELECT observed_cursor FROM sessions WHERE group_folder=? AND topic=?), ''))`,
		folder, topic, newSessionID, parentTopic, nowTS(), folder, parentTopic)
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
// fork time, with observed_cursor=now so the child reads only post-fork context.
// force overwrites an existing child; otherwise a collision returns
// core.ErrTopicExists.
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

// PutCost writes one cost_log row per model under the (folder,turn_id)
// dedup; a duplicate submit_turn does not double-charge. userSub is the turn's
// caller (turnCallerSub of its trigger sender — the platform's own triggers
// resolve to service:routd) so SpendTodayUser can aggregate per-user spend.
func (d *DB) PutCost(folder, turnID, userSub, model string, in, out, cents int) error {
	_, err := d.db.Exec(`INSERT OR IGNORE INTO cost_log(folder, turn_id, user_sub, model, input_tokens, output_tokens, cost_cents, recorded_at)
		VALUES(?,?,?,?,?,?,?,?)`, folder, turnID, userSub, model, in, out, cents, nowTS())
	return err
}

// CostByTurn aggregates cost_log across models for one turn. found=false when
// the turn has no cost rows yet (still running, or an adapter-side turn that
// never reported) — the REST face maps that to 404 so a caller can tell
// "no cost recorded" from a genuine zero-cost row.
// CostSummaryRow is one folder's spend over a window.
type CostSummaryRow struct {
	Folder    string `json:"folder"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
}

// CostSummary aggregates cost_log per folder since `since` (RFC3339).
//
// The per-TURN reader below answers "what did this turn cost"; an operator
// asking "what is this instance spending" needs the other axis, and dashd's
// usage page had to read cost_log directly for want of it (spec 6/30 lists
// this endpoint as routd's required /v1 work; 6/28 is why it must exist at
// all — no dashboard reads another daemon's DB).
//
// Folder containment is exact-or-subtree through groupfolder.SQLSubtree, never
// a LIKE (BUGS L122). An empty folder means every folder.
func (d *DB) CostSummary(folder, since string) ([]CostSummaryRow, error) {
	q := `SELECT folder,
	             COALESCE(SUM(input_tokens+output_tokens),0),
	             COALESCE(SUM(cost_cents),0)
	      FROM cost_log WHERE recorded_at >= ?`
	args := []any{since}
	if folder != "" {
		q += ` AND ` + groupfolder.SQLSubtree("folder", "/")
		args = append(args, groupfolder.SubtreeArgs(folder, "/")...)
	}
	q += ` GROUP BY folder ORDER BY folder`

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CostSummaryRow{}
	for rows.Next() {
		var r CostSummaryRow
		if err := rows.Scan(&r.Folder, &r.Tokens, &r.CostCents); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) CostByTurn(turnID string) (folder string, in, out, cents int, found bool, err error) {
	err = d.db.QueryRow(
		`SELECT folder, COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost_cents),0)
		 FROM cost_log WHERE turn_id=? GROUP BY folder`, turnID).
		Scan(&folder, &in, &out, &cents)
	if err == sql.ErrNoRows {
		return "", 0, 0, 0, false, nil
	}
	if err != nil {
		return "", 0, 0, 0, false, err
	}
	return folder, in, out, cents, true, nil
}

// FolderCap returns the per-day spend cap for a folder in cents
// (groups.cost_cap_cents_per_day). Zero means uncapped (the default).
func (d *DB) FolderCap(folder string) (int, error) {
	var cents int
	err := d.db.QueryRow(
		"SELECT COALESCE(cost_cap_cents_per_day, 0) FROM groups WHERE folder=?", folder).Scan(&cents)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return cents, err
}

// SetFolderCap writes the per-day spend cap on the groups row for folder.
// Zero = uncapped. The operator write half of FolderCap, so `arizuko budget
// set folder` lands on the row budgetGate reads.
func (d *DB) SetFolderCap(folder string, cents int) error {
	_, err := d.db.Exec(
		"UPDATE groups SET cost_cap_cents_per_day = ? WHERE folder=?", cents, folder)
	return err
}

// SpendTodayFolder sums cost_log.cost_cents for a folder since the UTC start of
// today — the pre-spawn budget gate's spend view.
func (d *DB) SpendTodayFolder(folder string) (int, error) {
	var cents int
	err := d.db.QueryRow(
		`SELECT COALESCE(SUM(cost_cents), 0) FROM cost_log WHERE folder=? AND recorded_at >= ?`,
		folder, startOfTodayUTC()).Scan(&cents)
	return cents, err
}

// SpendTodayUser sums cost_log.cost_cents for a user_sub since the UTC start of
// today — the per-user half of the budget gate.
func (d *DB) SpendTodayUser(userSub string) (int, error) {
	var cents int
	err := d.db.QueryRow(
		`SELECT COALESCE(SUM(cost_cents), 0) FROM cost_log WHERE user_sub=? AND recorded_at >= ?`,
		userSub, startOfTodayUTC()).Scan(&cents)
	return cents, err
}

// startOfTodayUTC is the RFC3339Nano timestamp at 00:00 UTC today — the budget
// window's lower bound.
func startOfTodayUTC() string {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
}

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

// LatestSource returns the adapter name that delivered the newest real inbound
// row on a chat (the messages.source column), used by the Deliverer to route an
// outbound back to the same adapter the conversation came in on. Empty when the
// chat has no sourced inbound.
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
// proactive_last_fired_at in one transaction. The caller dispatches the run only
// after this commits, so a crash before dispatch leaves the cooldown set — at
// worst one missed turn, never a double-fire. Returns the synthetic row's id
// (the turn_id).
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

// ThreadHasBotMessage reports whether the bot has posted in (chatJID, topic).
// Empty topic → false (main timeline is not a thread).
func (d *DB) ThreadHasBotMessage(chatJID, topic string) bool {
	if topic == "" {
		return false
	}
	var bot int
	d.db.QueryRow(
		"SELECT 1 FROM messages WHERE chat_jid=? AND (topic=? OR turn_id=?) AND is_bot_message=1 LIMIT 1",
		chatJID, topic, topic,
	).Scan(&bot)
	return bot == 1
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
