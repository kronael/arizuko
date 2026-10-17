package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth/surrogate"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/routd/msgsearch"
	"github.com/kronael/arizuko/store"
)

// DB is routd's owner tables: the message/event store + routing rules + turn
// lifecycle + route tokens. routd is the SOLE appender of messages. Times are
// core.TSLayout UTC, computed in Go.
type DB struct {
	obj objstore.Store

	// SECRETS_KEY material, raw. Empty → reads stay ciphertext and writes
	// store plaintext.
	secretKeyring [][]byte

	// nil → no refresh; ConnectorSecrets reads the stored value unchanged
	// (spec 5/15).
	surrogate *surrogate.Engine

	// release drops the private namespace OpenMem reserved; nil against the
	// instance's own tables.
	release func() error

	// All three are set by every door that builds a DB, so no door can hand
	// back one whose search is a nil dereference (search.go).
	search     *msgsearch.Index
	searchStop context.CancelFunc
	searchDone chan struct{}
}

// SetSurrogate wires the surrogate-OAuth engine ConnectorSecrets refreshes
// near-expiry credentials with. nil-safe (no refresh).
func (d *DB) SetSurrogate(e *surrogate.Engine) { d.surrogate = e }

// SetSecretKeys supplies the SECRETS_KEY keyring: the active seal key first,
// retired keys after. Empty/no-call → reads stay ciphertext, writes store
// plaintext.
func (d *DB) SetSecretKeys(raws ...[]byte) { d.secretKeyring = raws }

// Open dials the endpoint routd's tables live on and makes them: routd is
// their OWNER, and a reader dials and makes nothing (store.OpenOwner). routd
// opens NO sibling owner; every other table is federated over HTTP.
func Open() (*DB, error) {
	h, err := store.Handle(store.OwnerRoutd)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if err := store.PrepareOwner(ctx, h, store.OwnerRoutd); err != nil {
		return nil, err
	}
	// The floor writes only into an EMPTY acl, so an operator's manifest is
	// never fought; without it the instance denies everything and comes up
	// healthy (store/seed.go). Folders bind to role:member at group creation
	// (store.PutGroupRow, BUGS L119); there is no open-time backfill.
	if err := store.SeedRoutdFloor(ctx, h); err != nil {
		return nil, err
	}
	// The ROW is routd's, so routd writes it, into a store that holds no group
	// at all; `arizuko create` only writes the product choice into the env.
	if err := store.SeedDefaultGroup(ctx, h, os.Getenv("ARIZUKO_DEFAULT_PRODUCT")); err != nil {
		return nil, err
	}
	return OpenOver(h), nil
}

// OpenMem hands ONE test its own private namespace on the endpoint this process
// dials, with routd's entities and its floor made in it. Close drops it.
func OpenMem() (*DB, error) {
	h, release, err := store.TestOwner(store.OwnerRoutd)
	if err != nil {
		return nil, err
	}
	if err := store.SeedRoutdFloor(context.Background(), h); err != nil {
		if rerr := release(); rerr != nil {
			return nil, fmt.Errorf("%w (and dropping the namespace: %v)", err, rerr)
		}
		return nil, err
	}
	d := OpenOver(h)
	d.release = release
	return d, nil
}

// Close ends what the open holds. It WAITS for the boot walk: a walk still
// reading after Close returns keeps touching a namespace its owner believes it
// has released.
func (d *DB) Close() error {
	if d.searchStop != nil {
		d.searchStop()
		<-d.searchDone
	}
	if d.release != nil {
		return d.release()
	}
	return nil
}

// Obj is this database through objstore's two primitives — how audit reaches
// it, and how an audited row joins the transaction the mutation runs on.
func (d *DB) Obj() objstore.Store { return d.obj }

// PutGroup upserts a group row and binds the folder to role:member in the SAME
// transaction (store.PutGroupRow, the ONE upsert): a folder with no edge
// matches zero allow rows and comes up with no messaging tools (BUGS L119).
func (d *DB) PutGroup(g core.Group) error {
	return d.store().PutGroupRow(g)
}

// GroupExists reports whether folder is a registered group.
func (d *DB) GroupExists(folder string) bool {
	_, ok := d.store().GroupByFolder(folder)
	return ok
}

// DeleteGroup removes a group's subtree and every row that confers on it or
// routes into it, reporting the groups rows AND the orphans swept with them.
// store.DeleteGroup is the ONE delete, shared with `arizuko group rm` and dashd.
func (d *DB) DeleteGroup(folder string) (int, int, error) {
	return d.store().DeleteGroup(folder)
}

// GroupConfig returns the per-group model override + the opaque container_config
// for runed; empty/nil when unknown or unset → runed uses the instance defaults.
func (d *DB) GroupConfig(folder string) (model string, cfg map[string]any) {
	return d.store().GroupConfig(folder)
}

// AllGroups returns every registered group keyed by folder. A read failure
// answers nil — the caller fails closed.
func (d *DB) AllGroups() map[string]core.Group {
	return d.store().AllGroups()
}

// SetGroupOpen toggles the group's open flag (ambient turn admission).
func (d *DB) SetGroupOpen(folder string, open bool) error {
	return d.store().SetGroupOpen(folder, open)
}

// ThreadReplies reads the group's thread_replies preference; unset yields dflt
// (the chat's multi-user flag: group chats thread by default, DMs don't).
func (d *DB) ThreadReplies(folder string, dflt bool) bool {
	return d.store().ThreadReplies(folder, dflt)
}

// SetGroupObserveWindow overrides the group's observe-window (messages, chars).
// A negative value clears the key so the cfg default wins.
func (d *DB) SetGroupObserveWindow(folder string, msgs, chars int) error {
	return d.store().SetGroupObserveWindow(folder, msgs, chars)
}

// AddGroupWatcher subscribes observer to source's ambient context (idempotent).
func (d *DB) AddGroupWatcher(observer, source string) error {
	return d.store().AddGroupWatcher(observer, source)
}

// RemoveGroupWatcher drops the observe_group subscription.
func (d *DB) RemoveGroupWatcher(observer, source string) error {
	return d.store().RemoveGroupWatcher(observer, source)
}

// PutMessage appends one message row; routd is the only writer. The ranked
// index is fed from the row the store STORED, not the one handed in: an append
// buried under the chat's newest row takes the next position instead
// (store.appendInstant), and an index on the caller's instant would answer
// "no such message" about it.
func (d *DB) PutMessage(m core.Message) error {
	stored, err := d.store().AppendMessage(m)
	if err != nil {
		return err
	}
	row, err := store.IndexedFrom(stored)
	if err != nil {
		return err
	}
	d.search.Put(row)
	return nil
}

// PutOutbound appends one outbound row and the delivery state it still owes.
// `sent` writes no delivery item, which is what makes it the absence of one.
func (d *DB) PutOutbound(m core.Message, status string) error {
	return d.store().PutOutbound(context.Background(), m, status)
}

// EnrichMessage replaces a message's content with the media-enriched body and
// clears its attachments, so later turns' observed context sees the transcript.
func (d *DB) EnrichMessage(m core.Message, content string) error {
	row, err := store.IndexedFrom(m)
	if err != nil {
		return err
	}
	if err := d.store().EnrichMessage(m, content); err != nil {
		return err
	}
	row.Content = content
	d.search.Put(row)
	return nil
}

// NewMessagesForChat returns one chat's rows after its watermark, oldest first.
// The cursor is a whole message key, so a row at exactly the cursor instant is
// consumed once.
func (d *DB) NewMessagesForChat(jid, cursor string, limit int) ([]core.Message, error) {
	return d.store().NewMessagesForChat(context.Background(), jid, cursor, limit)
}

// MessagesSince returns one chat's rows strictly after an instant.
func (d *DB) MessagesSince(chatJID, since string) ([]core.Message, error) {
	t, err := readStoredInstant(since)
	if err != nil {
		return nil, err
	}
	return d.store().MessagesSince(chatJID, t)
}

// readStoredInstant parses a cursor or bound the caller holds as text; empty
// is the zero time, "no lower bound".
func readStoredInstant(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an instant", v)
	}
	return t, nil
}

// MarkDelivered records that the platform accepted an outbound row: its
// delivery item goes and the platform's own id lands on the message.
func (d *DB) MarkDelivered(m core.Message, platformID string) error {
	return d.store().MarkDelivered(context.Background(), m, platformID)
}

// DeadLetter moves an outbound row to the terminal state. attempts=0 says the
// platform refused it permanently; the cap says the retries ran out.
func (d *DB) DeadLetter(m core.Message, attempts int) error {
	return d.store().DeadLetter(context.Background(), m, attempts)
}

// RecordDeliveryFailure counts one transient delivery failure and dead-letters
// the row when the count reaches maxAttempts.
func (d *DB) RecordDeliveryFailure(m core.Message, maxAttempts int) (int, bool, error) {
	return d.store().RecordDeliveryFailure(context.Background(), m, maxAttempts)
}

// PendingOutbound returns the bot rows still owed a delivery at or before
// cutoff — the outbound retry feed, with each row's attempt count.
func (d *DB) PendingOutbound(cutoff time.Time, limit int) ([]store.PendingMessage, error) {
	return d.store().PendingOutbound(context.Background(), cutoff, limit)
}

// MessageByID resolves a reply target WITHIN one chat, by row id or platform
// id. The chat bound is not optional: platform_id is per-platform, so an
// unscoped lookup resolves to a row in another chat and folder, and the caller
// copies that row's sender, text and topic into this message.
func (d *DB) MessageByID(chatJID, id string) (core.Message, bool) {
	return d.store().MessageByID(chatJID, id)
}

// TopicByID returns the topic of the row MessageByID matches, so a reply to a
// threaded message forks lineage from the parent's topic, not main.
func (d *DB) TopicByID(chatJID, id string) string {
	m, _ := d.MessageByID(chatJID, id)
	return m.Topic
}

// ClearChatErrored is the dual of MarkChatErrored; dispatch calls it on a
// clean turn.
func (d *DB) ClearChatErrored(chatJID string) error {
	return d.store().ClearChatErrored(chatJID)
}

// MarkChatErrored flags a chat as errored. The flag surfaces in inspection; it
// does not block re-feed (the cursor advance is the starvation guard).
func (d *DB) MarkChatErrored(chatJID string) error {
	return d.store().MarkChatErrored(chatJID)
}

// ClearErrored un-flags a chat's errored rows so dispatch feeds them again. It
// answers how many moved: a retry that retried nothing is not a retry.
func (d *DB) ClearErrored(chatJID string) (int64, error) {
	return d.store().ClearErrored(context.Background(), chatJID)
}

// MarkMessagesErrored flags a failed run's trigger rows so the circuit breaker
// can prune them; they stay visible to a later run until it trips.
func (d *DB) MarkMessagesErrored(msgs []core.Message) error {
	return d.store().MarkMessagesErrored(context.Background(), msgs)
}

// DeleteErroredMessages drops a chat's errored rows and their index entries,
// ranked index included: a deleted message that goes on answering searches
// reports content nobody can read back.
func (d *DB) DeleteErroredMessages(chatJID string) error {
	gone, err := d.store().DeleteErroredMessages(context.Background(), chatJID)
	if err != nil {
		return err
	}
	for _, sortKey := range gone {
		d.search.Remove(chatJID, sortKey)
	}
	return nil
}

// CountErroredChats counts chats flagged errored — the /status surface.
// chats.errored is the coarse flag; messages.errored is the breaker's prune
// target.
func (d *DB) CountErroredChats() int {
	jids, err := d.store().ErroredChatJIDs()
	if err != nil {
		return 0
	}
	return len(jids)
}

// ChatJIDs lists every chat the instance has seen — the partition list the
// poll loop feeds from and the archive exports.
func (d *DB) ChatJIDs() ([]string, error) {
	return d.store().ChatJIDs(context.Background())
}

// DailyVolume answers how many human messages each of the last `days` days
// carried — the usage page's histogram.
func (d *DB) DailyVolume(ctx context.Context, days int) (map[string]int, error) {
	return d.store().DailyVolume(ctx, days)
}

// RecentActivity answers the newest messages across every chat, newest first,
// optionally paged by an instant the caller has already read past.
func (d *DB) RecentActivity(ctx context.Context, before string, limit int) ([]store.ActivityRow, error) {
	return d.store().RecentActivity(ctx, before, limit)
}

// SweepActivity drops activity-feed entries past the retention window and
// answers how many went.
func (d *DB) SweepActivity(now time.Time) (int, error) {
	return d.store().SweepActivity(context.Background(), now)
}

// GetAgentCursor reads the per-chat high-water mark fed to the agent.
func (d *DB) GetAgentCursor(chatJID string) string {
	cur, err := d.store().AgentCursor(chatJID)
	if err != nil {
		return ""
	}
	return cur
}

// SetAgentCursor advances the per-chat cursor (upsert).
func (d *DB) SetAgentCursor(chatJID, cursor string) error {
	return d.store().SetAgentCursor(chatJID, cursor)
}

// SetChatIsGroup records whether a chat is a multi-party group.
func (d *DB) SetChatIsGroup(chatJID string, isGroup bool) error {
	return d.store().SetChatIsGroup(chatJID, isGroup)
}

// ChatIsGroup reads the multi-party flag for a chat; unknown chats are false.
func (d *DB) ChatIsGroup(chatJID string) bool {
	is, err := d.store().ChatIsGroup(chatJID)
	return err == nil && is
}

// StickyState returns the @group / #topic navigation pins for a chat.
func (d *DB) StickyState(chatJID string) (group, topic string) {
	g, t, err := d.store().StickyPins(chatJID)
	if err != nil {
		return "", ""
	}
	return g, t
}

// SetStickyGroup pins (or clears, when folder=="") the @group navigation
// target for a chat.
func (d *DB) SetStickyGroup(chatJID, folder string) error {
	return d.store().SetStickyGroup(chatJID, folder)
}

// SetStickyTopic pins (or clears, when topic=="") the #topic navigation
// target for a chat.
func (d *DB) SetStickyTopic(chatJID, topic string) error {
	return d.store().SetStickyTopic(chatJID, topic)
}

// RoutedToByMessageID returns the folder a message of this chat routed to, or
// "". Delegation looks it up to send a reply-to-bot back to the authoring
// child. CHAT-SCOPED without narrowing: a reply names a message in the chat it
// was sent to.
func (d *DB) RoutedToByMessageID(chatJID, id string) string {
	return d.store().RoutedToByMessageID(chatJID, id)
}

// IsEngaged reports whether (jid, topic) has a live engagement window — the
// topic-root normalization probe.
func (d *DB) IsEngaged(jid, topic string) bool {
	_, ok := d.Engaged(jid, topic)
	return ok
}

// MarkMessagesObserved flags rows observed and stamps routed_to, so an observe
// rule (target=folder#observe) ingests them into the folder's ambient context
// without a turn.
func (d *DB) MarkMessagesObserved(folder string, msgs []core.Message) error {
	rows := make([]store.IndexedMessage, len(msgs))
	for i, m := range msgs {
		row, err := store.IndexedFrom(m)
		if err != nil {
			return err
		}
		row.RoutedTo = folder
		rows[i] = row
	}
	if err := d.store().MarkMessagesObserved(context.Background(), folder, msgs); err != nil {
		return err
	}
	// The folder-subtree scope of a search reads the stamp; unrouted in the
	// index means unfindable by the folder it was ingested into.
	d.search.Put(rows...)
	return nil
}

// Routes returns the route table in resolve order: seq, then id. Every live
// rule sits at seq 0, so insertion order decides which one answers a chat.
func (d *DB) Routes() ([]core.Route, error) {
	return d.store().RouteTable()
}

// AddRoute appends one route, returning the id it holds.
func (d *DB) AddRoute(r core.Route) (int64, error) {
	return d.store().PutRouteRow(r)
}

// ErrNotFound signals an absent row to the HTTP layer (404).
var ErrNotFound = errors.New("not found")

// SetEngagement claims an engagement window for (jid, topic) by folder until
// now+ttl. Un-audited on purpose: its callers are the per-turn claim sites;
// operator writes take SetEngagementAudited.
func (d *DB) SetEngagement(jid, topic, folder string, ttl time.Duration) error {
	return d.store().BumpEngagement(jid, topic, folder, time.Now().Add(ttl))
}

// SetEngagementAudited is SetEngagement plus its audit_log row in ONE batch —
// the /v1/engagement entrypoint, where this row is the only durable trace. The
// batch is all-or-nothing, so neither half can outlive the other (spec 6/40).
func (d *DB) SetEngagementAudited(ctx context.Context, jid, topic, folder string, ttl time.Duration, ev audit.Event) error {
	_, rec := audit.WriteFor(ev)
	return d.store().BumpEngagementWith(ctx, jid, topic, folder, time.Now().Add(ttl), rec)
}

// Engagement is one LIVE engagement window (spec 5/G-engagement). An alias, not a struct:
// a second copy of the four fields is a second place for the shape to drift.
type Engagement = store.Engagement

// Engagement returns the live window for (jid, topic). Not live: no row, a NULL
// deadline or folder, an unparseable deadline, or a deadline already past.
func (d *DB) Engagement(jid, topic string) (Engagement, bool) {
	return d.store().Engagement(jid, topic)
}

// Engaged returns (folder, true) when (jid, topic) has a live engagement.
func (d *DB) Engaged(jid, topic string) (string, bool) {
	e, live := d.Engagement(jid, topic)
	return e.Folder, live
}

// ListEngaged enumerates the live windows visible to folder, newest deadline
// first; an EMPTY folder is the root or a service token and sees the fleet
// (visibleTo). Both filters run in memory: the deadline is TEXT, and a LIKE
// pattern on a folder containing _ or % would silently widen.
func (d *DB) ListEngaged(folder string) ([]Engagement, error) {
	live, err := d.store().LiveEngagements(time.Now())
	if err != nil {
		return nil, err
	}
	out := []Engagement{}
	for _, e := range live {
		if visibleTo(folder, e.Folder) {
			out = append(out, e)
		}
	}
	return out, nil
}

// SetLastReply seeds the reply-to-bot threading anchor for (jid, topic). It
// never clobbers engaged_folder on an existing row: a broadcast send from
// another group would hijack a live engagement window.
func (d *DB) SetLastReply(jid, topic, msgID, folder string) error {
	return d.store().SetLastReply(jid, topic, msgID, folder)
}

// LastReplyID returns the thread anchor for (jid, topic).
func (d *DB) LastReplyID(jid, topic string) string {
	return d.store().LastReplyID(jid, topic)
}

// PutTurnContext binds a turn to (folder, topic, chat_jid) so late callbacks
// resolve their target from turn_id alone; returnTo is the delegation return
// address. live=false when the turn is ALREADY terminal: a re-fed batch whose
// run completed must NOT be resurrected.
func (d *DB) PutTurnContext(turnID, folder, topic, chatJID, trigger, returnTo string) (bool, error) {
	return d.store().OpenTurnContext(turnID, folder, topic, chatJID, trigger, returnTo)
}

// TurnContext is a turn's bound run-start context.
type TurnContext = store.TurnContext

// GetTurnContext recovers a turn's context by turn_id.
func (d *DB) GetTurnContext(turnID string) (TurnContext, bool) {
	return d.store().TurnContextOf(turnID)
}

// SetTurnTriggerMsg records the trigger message's platform id — the message a
// threaded reply roots a new platform thread on.
func (d *DB) SetTurnTriggerMsg(turnID, msgID string) error {
	return d.store().SetTurnTriggerMsg(turnID, msgID)
}

// SetTurnState flips a turn's lifecycle state (running|done|expired).
func (d *DB) SetTurnState(turnID, state string) error {
	return d.store().SetTurnState(turnID, state)
}

// SetRunReturned marks that POST /v1/runs returned: after it, late callbacks
// 409 turn_done; before it, trailing frames stay valid past an early
// submit_turn.
func (d *DB) SetRunReturned(turnID string) error {
	return d.store().SetRunReturned(turnID)
}

// SetTurnRunID records the runed-assigned run_id for a turn, the handle
// linking routd's turn to runed's spawn.
func (d *DB) SetTurnRunID(turnID, runID string) error {
	return d.store().SetTurnRunID(turnID, runID)
}

// TypingJIDsForRun returns the chats whose typing indicator a run owns, one
// per turn recorded against run_id. Steering is by FOLDER, so a steered turn's
// chat is not always the running turn's chat (BUGS T1).
func (d *DB) TypingJIDsForRun(runID string) []string {
	return d.store().TypingJIDsForRun(runID)
}

// IncrementRetryCount moves a turn to its next attempt and returns which. The
// count and the pending_retry state land in ONE write, or a turn burns an
// attempt and is still fed to crash recovery as live.
func (d *DB) IncrementRetryCount(turnID string) (int, error) {
	return d.store().RecordRetryAttempt(turnID)
}

// RunningTurns lists turns still in state=running or pending_retry — the
// crash-recovery feed.
func (d *DB) RunningTurns() ([]TurnContext, error) {
	return d.store().RunningTurns()
}

// TurnsSince lists every turn dispatched at or after `since`, in whatever
// state it reached — the outcome window the answering read folds into counts.
func (d *DB) TurnsSince(since time.Time) ([]TurnContext, error) {
	return d.store().TurnsSince(since)
}

// LastHumanMessage answers a chat's newest row a person wrote, and whether
// there is one.
func (d *DB) LastHumanMessage(jid string) (core.Message, bool, error) {
	return d.store().LastHumanMessage(context.Background(), jid)
}

// LastBotMessage answers a chat's newest agent reply, and whether there is one.
func (d *DB) LastBotMessage(jid string) (core.Message, bool, error) {
	return d.store().LastBotMessage(context.Background(), jid)
}

// SweepExpiredRunning flips stale running turns older than the timeout to the
// DISTINCT terminal 'expired' (NOT 'done'), so crash recovery stops re-feeding
// them without tripping the done-guard on a legitimate re-dispatch.
func (d *DB) SweepExpiredRunning(timeout time.Duration) (int64, error) {
	return d.store().SweepExpiredRunning(timeout)
}

// RecordTurnResult records the agent-submitted outcome for one turn. Returns
// true on the FIRST record, false on a repeat — the claim that makes one
// report one delivery.
func (d *DB) RecordTurnResult(folder, turnID, sessionID, status, failure string) (bool, error) {
	return d.store().RecordTurnResult(folder, turnID, sessionID, status, failure)
}

// TurnHasBotReply reports whether the agent already delivered a bot message
// for this turn, so recordTurnResult does not double-send. Interim
// verb="status" rows are excluded: an auto-status must NEVER count as the
// reply, or the agent's real answer is swallowed (spec 5/24).
func (d *DB) TurnHasBotReply(chatJID, turnID string) bool {
	return d.store().TurnHasBotReply(chatJID, turnID)
}

// LastStatusRow returns the turn's newest verb="status" row, delivered or not
// (routd/progress.go).
func (d *DB) LastStatusRow(chatJID, turnID string) (core.Message, bool) {
	return d.store().LastStatusRow(chatJID, turnID)
}

// SetMessageContent rewrites a held message's content in the log and the
// search index, so a row edited on the platform reads as the user sees it.
func (d *DB) SetMessageContent(m core.Message, content string) error {
	if err := d.store().SetMessageContent(m, content); err != nil {
		return err
	}
	m.Content = content
	row, err := store.IndexedFrom(m)
	if err != nil {
		return err
	}
	d.search.Put(row)
	return nil
}

// TurnResultRecorded reports whether submit_turn already recorded an outcome
// for (folder, turn_id), so the run-response backstop lets submit_turn's value
// win. A FAILED read answers false and says so: both consumers act on a false.
func (d *DB) TurnResultRecorded(folder, turnID string) bool {
	recorded, err := d.store().TurnResultRecorded(folder, turnID)
	if err != nil {
		slog.Error("turn result lookup", "folder", folder, "turn_id", turnID, "err", err)
		return false
	}
	return recorded
}

// PutSession persists the session_id runed produced for (folder, topic).
func (d *DB) PutSession(folder, topic, sessionID string) error {
	return d.store().SetSession(folder, topic, sessionID)
}

// GetSession returns the persisted session id for (folder, topic) and whether
// a row exists.
func (d *DB) GetSession(folder, topic string) (string, bool) {
	return d.store().GetSession(folder, topic)
}

// EnsureTopicLineage inserts a sessions row for (folder, topic) with lineage
// if none exists; parentTopic="" forks from main, and main itself is skipped.
// inserted=true tells the caller to copy the parent session file.
func (d *DB) EnsureTopicLineage(folder, topic, parentTopic, newSessionID string) (bool, error) {
	return d.store().EnsureTopicLineage(folder, topic, parentTopic, newSessionID)
}

// DeleteSession drops the persisted session for (folder, topic) — the /new
// command's session reset.
func (d *DB) DeleteSession(folder, topic string) error {
	return d.store().DeleteSession(folder, topic)
}

// ForkTopic seeds a fresh session row for child with its parent topic, fork
// time and observed_cursor=now, so the child reads only post-fork context.
// force overwrites an existing child; otherwise a collision returns
// core.ErrTopicExists.
func (d *DB) ForkTopic(folder, parent, child, newSessionID string, force bool) error {
	return d.store().ForkTopic(folder, parent, child, newSessionID, force)
}

// TopicsPerFolder answers how many topics each folder holds.
func (d *DB) TopicsPerFolder() (map[string]int, error) {
	return d.store().TopicsPerFolder()
}

// SessionID returns the persisted session id for (folder, topic).
func (d *DB) SessionID(folder, topic string) string {
	id, _ := d.store().GetSession(folder, topic)
	return id
}

// SetTurnGatewayKey records the sha256 of the turn's gateway key: the gateway
// names a key in its spend log by that hash, so this is the pointer arizuko
// keeps in place of a copy of its numbers (spec 6/39).
func (d *DB) SetTurnGatewayKey(turnID, keyHash string) error {
	return d.store().SetTurnGatewayKey(turnID, keyHash)
}

// TurnGatewayKey returns the key hash that metered a turn. Empty means the
// turn ran with no gateway, so no meter holds a number for it.
func (d *DB) TurnGatewayKey(turnID string) (string, error) {
	return d.store().TurnGatewayKey(turnID)
}

// FolderCap returns the per-day spend cap for a folder in cents; zero is
// uncapped.
func (d *DB) FolderCap(folder string) (int, error) {
	return d.store().FolderCap(folder)
}

// SetFolderCap writes the per-day spend cap on the groups row; zero is
// uncapped. The operator write half of FolderCap.
func (d *DB) SetFolderCap(folder string, cents int) error {
	return d.store().SetFolderCap(folder, cents)
}

// ProactiveChats lists the chats that ever carried a real inbound message —
// the proactive scan's universe. An attribute of the chat, not a DISTINCT over
// every message ever written.
func (d *DB) ProactiveChats() ([]string, error) {
	return d.store().ProactiveChats(context.Background())
}

// LastInboundAt returns the timestamp of the newest real inbound message on a
// chat, or zero time — the silence-debounce clock.
func (d *DB) LastInboundAt(jid string) time.Time {
	return d.store().LastInboundAt(context.Background(), jid)
}

// LatestSource returns the adapter that delivered the newest real inbound row
// on a chat, so the Deliverer routes an outbound back the way it came; empty
// when the chat has no sourced inbound.
func (d *DB) LatestSource(jid string) string {
	return d.store().LatestSource(context.Background(), jid)
}

// BotSpokeSince reports whether a bot-authored row exists on a chat at or
// after since (the BotQuiet veto).
func (d *DB) BotSpokeSince(jid string, since time.Time) bool {
	return d.store().BotSpokeSince(context.Background(), jid, since)
}

// InboundCountSince counts real inbound rows on a chat at or after since (the
// RecentActivity floor).
func (d *DB) InboundCountSince(jid string, since time.Time) int {
	return d.store().InboundCountSince(context.Background(), jid, since)
}

// LastInbound returns the newest real inbound row on a chat; ok=false when the
// chat has none.
func (d *DB) LastInbound(jid string) (content string, ts time.Time, ok bool) {
	return d.store().LastInbound(context.Background(), jid)
}

// ProactiveLastFired returns when a proactive turn last fired on a chat, or
// zero time when never (the mandatory 24h cooldown).
func (d *DB) ProactiveLastFired(jid string) time.Time {
	return d.store().ProactiveLastFired(jid)
}

// FireProactive appends the synthetic inbound row AND sets the chat's cooldown
// in one transaction; a crash before dispatch leaves the cooldown set — one
// missed turn, never a double-fire. Returns the row's id, which is the turn id.
func (d *DB) FireProactive(jid string) (string, error) {
	id := "proactive-" + randHex(8)
	if err := d.store().FireProactive(context.Background(), jid, id); err != nil {
		return "", err
	}
	return id, nil
}

// ChatHasRunningTurn reports whether a chat has a turn still 'running' — the
// proactive scan skips it, since a proactive inbound never steers a live turn.
func (d *DB) ChatHasRunningTurn(jid string) bool {
	return d.store().ChatHasRunningTurn(jid)
}

// OpenOver serves routd from a handle the caller owns and has made the entities
// on; there is no chain to run. A caller that answers searches calls
// StartSearch itself.
func OpenOver(obj objstore.Store) *DB {
	return &DB{obj: obj}
}

// TurnFailure answers the agent's own report of why a turn failed.
func (d *DB) TurnFailure(folder, turnID string) string {
	return d.store().TurnFailure(folder, turnID)
}
