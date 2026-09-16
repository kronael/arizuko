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

// DB is routd's owner tables: the message/event store + routing rules +
// turn lifecycle + route tokens. routd is the SOLE appender of messages. Times
// are core.TSLayout UTC, computed in Go.
type DB struct {
	// obj is the handle this database is served from.
	obj objstore.Store

	// secretKeyring is the SECRETS_KEY material (raw, pre-SHA256) routd hands to
	// its OWN secretStore via SetSecretKeys so reads decrypt `v2:` values and
	// writes seal them. Empty → no key set → reads stay ciphertext (no plaintext
	// leak; the connector just gets nothing usable) and writes store plaintext.
	secretKeyring [][]byte

	// surrogate refreshes near-expiry OAuth secret rows at broker call time
	// (spec 5/15). nil → no refresh (PAT-only path); ConnectorSecrets then reads
	// the stored value unchanged. Wired in routd main from auth/surrogate.
	surrogate *surrogate.Engine

	// release drops the private namespace OpenMem reserved. A DB opened against
	// the instance's own tables leaves them alone.
	release func() error

	// search is the ranked message index every find_messages answers from;
	// searchStop ends its boot walk and searchDone is closed when that walk has
	// stopped touching the store. All three are set by every door that builds a
	// DB, so no door can hand back one whose search is a nil dereference
	// (search.go).
	search     *msgsearch.Index
	searchStop context.CancelFunc
	searchDone chan struct{}
}

// SetSurrogate wires the surrogate-OAuth engine used by ConnectorSecrets to
// refresh near-expiry capability credentials at call time. nil-safe (no refresh).
func (d *DB) SetSurrogate(e *surrogate.Engine) { d.surrogate = e }

// SetSecretKeys supplies the SECRETS_KEY keyring (raw values; the active seal
// key first, retired keys after) so secret reads off routd's own secrets table
// decrypt and writes seal. Empty/no-call → reads stay ciphertext, writes store
// plaintext.
func (d *DB) SetSecretKeys(raws ...[]byte) { d.secretKeyring = raws }

// Open dials the endpoint routd's tables live on and makes them. routd is their
// OWNER, so it is the daemon that makes them: a reader dials and makes nothing
// (store.OpenOwner). routd opens NO sibling owner: every table it needs is its
// own or federated over HTTP (identity to authd, session_log to runed).
func Open() (*DB, error) {
	h, err := store.Handle(store.OwnerRoutd)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if err := store.PrepareOwner(ctx, h, store.OwnerRoutd); err != nil {
		return nil, err
	}
	// The floor routd is born with: role:operator's root grant, role:member's
	// twelve messaging verbs and the two instance-wide egress rules. It writes
	// only into an EMPTY acl, so an operator's manifest is never fought; without
	// it the instance denies everything and comes up healthy (store/seed.go).
	//
	// Every folder is bound to role:member at group creation, in the same
	// transaction as its groups row (store.PutGroupRow, BUGS L119). There is no
	// open-time backfill above the floor — auth.Authorize reads the acl rows
	// directly.
	if err := store.SeedRoutdFloor(ctx, h); err != nil {
		return nil, err
	}
	// The group a new instance is born with. `arizuko create` makes the
	// instance's files and writes the operator's product choice into the
	// environment; the ROW is routd's, so routd is what writes it, into a store
	// that holds no group at all.
	if err := store.SeedDefaultGroup(ctx, h, os.Getenv("ARIZUKO_DEFAULT_PRODUCT")); err != nil {
		return nil, err
	}
	return OpenOver(h), nil
}

// OpenMem hands ONE test its own private namespace on the endpoint this process
// dials, with routd's entities and its floor made in it. Close drops the
// namespace.
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

// Close ends what the open holds. A routd opened against the instance's own
// tables holds no namespace, so there is nothing to drop.
//
// IT WAITS FOR THE BOOT WALK: a walk still reading when Close returns keeps
// touching a namespace its owner believes it has released.
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

// Obj is this database through objstore's two primitives, which is how audit
// reaches it — and, for an audited write, how the row joins the transaction the
// mutation runs on.
func (d *DB) Obj() objstore.Store { return d.obj }

func nowTS() string { return core.NowTS() }

// PutGroup upserts a group identity row, persisting the per-group model +
// container_config so dispatchRun can forward them to runed (GroupConfig reads
// them back), and binds the new folder to role:member in the SAME transaction.
//
// store.PutGroupRow is the whole body: it is the ONE upsert, and the create-time
// default role rides its transaction because a groups row implies the member
// edge — a folder with no edge expands to nothing, matches zero allow rows and
// comes up with no messaging tools at all (BUGS L119). register_group itself is
// gated by db.Authorize and calls no Delegate; it seeds the floor, it does not
// delegate above it.
func (d *DB) PutGroup(g core.Group) error {
	return d.store().PutGroupRow(g)
}

// GroupExists reports whether folder is a registered group.
func (d *DB) GroupExists(folder string) bool {
	_, ok := d.store().GroupByFolder(folder)
	return ok
}

// DeleteGroup removes a group's subtree and every row that confers on it or
// routes into it, and reports how many groups rows went. The purge, its
// transaction and its audit row all live in store.DeleteGroup — the ONE delete,
// shared with the operator's `arizuko group rm` and dashd's delete page.
func (d *DB) DeleteGroup(folder string) (int, error) {
	return d.store().DeleteGroup(folder)
}

// GroupConfig returns the per-group model override + the opaque container_config
// for dispatchRun to forward to runed. Empty model / nil config when the group is
// unknown or unset → runed uses the instance defaults.
func (d *DB) GroupConfig(folder string) (model string, cfg map[string]any) {
	return d.store().GroupConfig(folder)
}

// AllGroups returns every registered group keyed by folder. Backs the agent's
// get_groups MCP tool. A read failure answers nil — the caller fails closed.
func (d *DB) AllGroups() map[string]core.Group {
	return d.store().AllGroups()
}

// SetGroupOpen toggles the group's open flag (ambient turn admission).
func (d *DB) SetGroupOpen(folder string, open bool) error {
	return d.store().SetGroupOpen(folder, open)
}

// ThreadReplies reads the group's thread_replies preference; an unset key (or an
// unknown group) yields dflt (the caller passes the chat's multi-user flag:
// group chats thread by default, DMs don't).
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

// PutMessage appends one message row. routd is the only writer, and store owns
// the item shape and the three indexes one write fans out into.
//
// The ranked index is fed here, from the row rendered BEFORE the write, so the
// refusal a bad position earns is the store's own and a committed message is
// never reported as a failure (search.go).
func (d *DB) PutMessage(m core.Message) error {
	row, err := store.IndexedFrom(m)
	if err != nil {
		return err
	}
	if err := d.store().PutMessage(m); err != nil {
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
// clears its attachments, so the observed-context render on later turns sees the
// transcript rather than the raw refs.
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

// NewMessagesForChat returns one chat's rows after its own watermark, oldest
// first — the poll loop's feed. The cursor is a whole message key, so a message
// at exactly the cursor instant is consumed once.
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

// readStoredInstant parses a cursor or bound the caller holds as text. An empty
// value is the zero time, which is "no lower bound".
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

// MarkDelivered records that the platform accepted an outbound row: its delivery
// item goes and the platform's own id lands on the message.
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

// PendingOutbound returns the bot rows still owed a delivery whose position is at
// or before cutoff — the outbound retry feed, with each row's attempt count.
func (d *DB) PendingOutbound(cutoff time.Time, limit int) ([]store.PendingMessage, error) {
	return d.store().PendingOutbound(context.Background(), cutoff, limit)
}

// MessageByID resolves a reply target WITHIN one chat, by the row's own id or by
// the platform's id for it. The chat bound is not optional: `platform_id` is a
// per-platform value, so an unscoped lookup resolves a reply id to a row in
// another chat and another folder, and the caller then copies that row's sender,
// text and topic into this message. The partition is what makes that impossible.
func (d *DB) MessageByID(chatJID, id string) (core.Message, bool) {
	return d.store().MessageByID(chatJID, id)
}

// TopicByID returns the topic of the row MessageByID matches, so a reply to a
// threaded message forks lineage from the parent's topic, not the main topic.
func (d *DB) TopicByID(chatJID, id string) string {
	m, _ := d.MessageByID(chatJID, id)
	return m.Topic
}

// ClearChatErrored takes the errored flag away on a clean turn. It is the dual
// of MarkChatErrored and dispatch calls it where the circuit breaker clears.
func (d *DB) ClearChatErrored(chatJID string) error {
	return d.store().ClearChatErrored(chatJID)
}

// MarkChatErrored flags a chat as errored (a run definitively failed). The
// flag surfaces in inspection; it does not block re-feed (the cursor advance
// past the failed batch is the starvation guard).
func (d *DB) MarkChatErrored(chatJID string) error {
	return d.store().MarkChatErrored(chatJID)
}

// ClearErrored un-flags a chat's errored rows so the dispatch loop feeds them
// again — the operator's "retry this chat". It answers how many rows moved, so a
// caller can refuse to report success on zero: a retry that retried nothing is
// not a retry.
func (d *DB) ClearErrored(chatJID string) (int64, error) {
	return d.store().ClearErrored(context.Background(), chatJID)
}

// MarkMessagesErrored flags a failed run's trigger rows so the circuit breaker
// can prune them. The rows stay visible to a later successful run until it
// trips. An empty list is a no-op.
func (d *DB) MarkMessagesErrored(msgs []core.Message) error {
	return d.store().MarkMessagesErrored(context.Background(), msgs)
}

// DeleteErroredMessages drops a chat's errored rows and the index entries that
// point at them, so the next inbound starts the chat from a clean batch. The
// positions it answers with leave the ranked index too: a deleted message that
// goes on answering searches reports content nobody can read back.
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

// CountErroredChats counts chats flagged errored — the /status surface. Reads
// chats.errored (the coarse per-chat flag; messages.errored is the row-level
// prune target for the breaker).
func (d *DB) CountErroredChats() int {
	jids, err := d.store().ErroredChatJIDs()
	if err != nil {
		return 0
	}
	return len(jids)
}

// ChatJIDs lists every chat the instance has seen — the partition list the poll
// loop feeds from and the archive exports.
func (d *DB) ChatJIDs() ([]string, error) {
	return d.store().ChatJIDs(context.Background())
}

// DailyVolume answers how many human messages each of the last `days` days
// carried — the usage page's histogram, read off the activity feed.
func (d *DB) DailyVolume(ctx context.Context, days int) (map[string]int, error) {
	return d.store().DailyVolume(ctx, days)
}

// RecentActivity answers the newest messages across every chat, newest first,
// optionally paged by an instant the caller has already read past.
func (d *DB) RecentActivity(ctx context.Context, before string, limit int) ([]store.ActivityRow, error) {
	return d.store().RecentActivity(ctx, before, limit)
}

// SweepActivity drops activity-feed entries past the retention window and
// answers how many went. The hourly housekeeping calls it.
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
// target for a chat — subsequent messages route to it until reset.
func (d *DB) SetStickyGroup(chatJID, folder string) error {
	return d.store().SetStickyGroup(chatJID, folder)
}

// SetStickyTopic pins (or clears, when topic=="") the #topic navigation
// target for a chat.
func (d *DB) SetStickyTopic(chatJID, topic string) error {
	return d.store().SetStickyTopic(chatJID, topic)
}

// RoutedToByMessageID returns the folder a stored message of this chat was routed
// to, or "" when absent. Delegation looks this up to send a reply-to-bot back to
// the child that authored the reply.
//
// It is CHAT-SCOPED, and the reply path is why that is not a narrowing: a reply
// names a message in the chat it was sent to, so the parent is in that partition
// by construction. The statement it replaces searched every chat.
func (d *DB) RoutedToByMessageID(chatJID, id string) string {
	return d.store().RoutedToByMessageID(chatJID, id)
}

// IsEngaged reports whether (jid, topic) has a live engagement window — the
// topic-root normalization probe (engagement recorded on root may not match a
// thread topic).
func (d *DB) IsEngaged(jid, topic string) bool {
	_, ok := d.Engaged(jid, topic)
	return ok
}

// MarkMessagesObserved flags rows as observed and stamps routed_to, so a route-
// table observe rule (target=folder#observe) ingests them silently into the
// folder's ambient context without firing a turn. The folder feed is stamped in
// the same write, because that feed is what the ambient read asks.
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
	// The stamp is what the folder-subtree scope of a search reads, so an
	// observed message the index still holds as unrouted is unfindable by the
	// folder it was ingested into.
	d.search.Put(rows...)
	return nil
}

// Routes returns the route table in the order the router resolves it: seq
// first, then the id that records which rule was written first. store.AllRoutes
// already sorts that way — every live rule sits at seq 0, so insertion order is
// what decides which one answers a chat.
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
// now+ttl.
//
// Un-audited on purpose: its callers are the PER-TURN claim sites, which fire on
// every conversational turn and are already recorded. An audit_log row per turn
// would bury the operator trail in the noise it exists to make findable.
// Operator-originated writes take SetEngagementAudited instead.
func (d *DB) SetEngagement(jid, topic, folder string, ttl time.Duration) error {
	return d.store().BumpEngagement(jid, topic, folder, time.Now().Add(ttl))
}

// SetEngagementAudited is SetEngagement plus its audit_log row, both in ONE
// batch. This is the entrypoint for /v1/engagement: an operator ending someone's
// conversation early leaves no message row and no turn behind, so this row is
// the only durable trace.
//
// The row RIDES the window's own write rather than joining a transaction with
// it: an audit row that outlived a rolled-back write would describe a window
// that never moved, and a write whose audit insert failed would be the
// unrecorded mutation this closes. A batch is all-or-nothing, so it gives both
// without a transaction (spec 6/40 §"The audited write commits in a
// GuardedBatch").
func (d *DB) SetEngagementAudited(ctx context.Context, jid, topic, folder string, ttl time.Duration, ev audit.Event) error {
	_, rec := audit.WriteFor(ev)
	return d.store().BumpEngagementWith(ctx, jid, topic, folder, time.Now().Add(ttl), rec)
}

// Engagement is one LIVE engagement window: which folder claims (jid, topic) and
// until when. Spec 5/G.
//
// It is an ALIAS rather than a struct of its own, because the item it describes
// is store's and a second copy of the same four fields is a second place for the
// shape to drift.
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

// ListEngaged enumerates the live engagement windows visible to folder, newest
// deadline first. `all` widens to the whole fleet and belongs ONLY to a
// root/service caller; the handler derives it from an EMPTY folder claim, never
// from depth.
//
// Containment is descendant(row.Folder, folder) — self-or-below — deliberately
// NOT ownsFolder, which reports true for an empty target and would show every
// scoped caller the rows no folder has claimed. The window filter runs here
// because the deadline is core.TSLayout TEXT, and the subtree filter runs here so
// containment has ONE implementation instead of a LIKE pattern that a folder name
// containing _ or % would silently widen.
func (d *DB) ListEngaged(folder string, all bool) ([]Engagement, error) {
	live, err := d.store().LiveEngagements(time.Now())
	if err != nil {
		return nil, err
	}
	out := []Engagement{}
	for _, e := range live {
		if !all && !descendant(e.Folder, folder) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// SetLastReply seeds the reply-to-bot threading anchor for (jid, topic).
// Only updates last_reply_id on conflict — never clobbers engaged_folder on an
// existing row, since an active engagement window (set by SetEngagement) would
// be silently hijacked by a broadcast send from a different group.
func (d *DB) SetLastReply(jid, topic, msgID, folder string) error {
	return d.store().SetLastReply(jid, topic, msgID, folder)
}

// LastReplyID returns the thread anchor for (jid, topic).
func (d *DB) LastReplyID(jid, topic string) string {
	return d.store().LastReplyID(jid, topic)
}

// PutTurnContext binds a turn to the (folder, topic, chat_jid) it came from so
// late callbacks resolve their target from turn_id alone. returnTo is the
// delegation return-address: when set, the callback surface delivers back to it
// instead of the child folder JID.
//
// Returns live=false when the turn is ALREADY terminal — a re-fed batch whose
// run completed must NOT be resurrected. When an earlier batch finishes and a
// later batch in the same poll steers, the cursor does not advance, so the next
// poll re-feeds the completed batch.
func (d *DB) PutTurnContext(turnID, folder, topic, chatJID, trigger, returnTo string) (bool, error) {
	return d.store().OpenTurnContext(turnID, folder, topic, chatJID, trigger, returnTo)
}

// TurnContext is a turn's bound run-start context.
type TurnContext = store.TurnContext

// GetTurnContext recovers a turn's context by turn_id.
func (d *DB) GetTurnContext(turnID string) (TurnContext, bool) {
	return d.store().TurnContextOf(turnID)
}

// SetTurnTriggerMsg records the trigger message's platform id for a turn — the
// message a threaded reply roots a new platform thread on. Set at dispatch right
// after PutTurnContext (the SetTurnRunID pattern).
func (d *DB) SetTurnTriggerMsg(turnID, msgID string) error {
	return d.store().SetTurnTriggerMsg(turnID, msgID)
}

// SetTurnState flips a turn's lifecycle state (running|done|expired).
func (d *DB) SetTurnState(turnID, state string) error {
	return d.store().SetTurnState(turnID, state)
}

// SetRunReturned marks that POST /v1/runs has returned for a turn. After this,
// late callbacks 409 turn_done (the run is no longer live); before it, trailing
// frames from a still-live run stay valid even past an early submit_turn.
func (d *DB) SetRunReturned(turnID string) error {
	return d.store().SetRunReturned(turnID)
}

// SetTurnRunID records the runed-assigned run_id for a turn, the reconciliation
// handle linking routd's turn to runed's spawn.
func (d *DB) SetTurnRunID(turnID, runID string) error {
	return d.store().SetTurnRunID(turnID, runID)
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
	return d.store().TypingJIDsForRun(runID)
}

// IncrementRetryCount moves a turn to its next attempt and returns which
// attempt that is. The count and the pending_retry state land in ONE write: a
// turn whose count moved but whose state stayed running has burned an attempt
// and is still fed to crash recovery as live.
func (d *DB) IncrementRetryCount(turnID string) (int, error) {
	return d.store().RecordRetryAttempt(turnID)
}

// ResetTurnForRetry resets a turn's state to running for the next retry attempt.
func (d *DB) ResetTurnForRetry(turnID string) error {
	return d.store().ResetTurnForRetry(turnID)
}

// RunningTurns lists turn_ids still in state=running or pending_retry
// (crash-recovery feed).
func (d *DB) RunningTurns() ([]TurnContext, error) {
	return d.store().RunningTurns()
}

// SweepExpiredRunning flips stale state='running' turns older than the run
// timeout to the DISTINCT terminal 'expired' (NOT 'done') so they stop being
// re-fed by crash recovery, without tripping the done-guard against a legitimate
// re-dispatch. Returns the count swept.
func (d *DB) SweepExpiredRunning(timeout time.Duration) (int64, error) {
	return d.store().SweepExpiredRunning(timeout)
}

// RecordTurnResult records the agent-submitted outcome for one turn. Returns
// true on the FIRST record, false on a repeat — the claim that makes one report
// one delivery.
func (d *DB) RecordTurnResult(folder, turnID, sessionID, status string) (bool, error) {
	return d.store().RecordTurnResult(folder, turnID, sessionID, status)
}

// TurnHasBotReply reports whether the agent already delivered a bot message for
// this turn via the reply/send tools, so recordTurnResult does not double-send the
// prose result. Interim verb="status" rows are excluded: an auto-status must NEVER
// count as the reply, or the agent's real answer is swallowed (spec 5/24).
//
// The chat is a parameter because a message is addressed by its chat: every
// caller holds the turn's own chat, on the turn context it just read.
func (d *DB) TurnHasBotReply(chatJID, turnID string) bool {
	return d.store().TurnHasBotReply(chatJID, turnID)
}

// LastStatusPlatformID returns the platform id of the most recent delivered
// verb="status" (⏳) row for the turn, or "" if none landed yet. submit_status
// edits that message in place instead of streaming a new notice (spec 5/24).
func (d *DB) LastStatusPlatformID(chatJID, turnID string) string {
	return d.store().LastStatusPlatformID(chatJID, turnID)
}

// TurnResultRecorded reports whether submit_turn already recorded an outcome for
// (folder, turn_id). The run-response path checks this before persisting its
// session_id backstop so submit_turn's value wins.
//
// A read that FAILED answers false, which is what the statement it replaces did
// by discarding its scan error — but it says so now, because both consumers act
// on a false: the backstop overwrites the agent's own session id and the
// silent-turn alarm fires at an operator.
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

// GetSession returns the persisted session id for (folder, topic) and whether a
// row exists. copyParentSession reads the parent topic's session id through this
// before copying its jsonl.
func (d *DB) GetSession(folder, topic string) (string, bool) {
	return d.store().GetSession(folder, topic)
}

// EnsureTopicLineage inserts a sessions row for (folder, topic) with lineage if
// none exists yet. Idempotent: a no-op when the row already exists.
// parentTopic="" forks from main; main topic "" is skipped (main has no parent).
// Returns inserted=true when a new row was created — the caller then copies the
// parent session file.
func (d *DB) EnsureTopicLineage(folder, topic, parentTopic, newSessionID string) (bool, error) {
	return d.store().EnsureTopicLineage(folder, topic, parentTopic, newSessionID)
}

// DeleteSession drops the persisted session for (folder, topic) — the /new
// command's session reset.
func (d *DB) DeleteSession(folder, topic string) error {
	return d.store().DeleteSession(folder, topic)
}

// ForkTopic seeds a fresh session row for child, recording its parent topic +
// fork time, with observed_cursor=now so the child reads only post-fork context.
// force overwrites an existing child; otherwise a collision returns
// core.ErrTopicExists.
func (d *DB) ForkTopic(folder, parent, child, newSessionID string, force bool) error {
	return d.store().ForkTopic(folder, parent, child, newSessionID, force)
}

// TopicsPerFolder answers how many topics each folder holds — the Status page's
// session tally.
func (d *DB) TopicsPerFolder() (map[string]int, error) {
	return d.store().TopicsPerFolder()
}

// SessionID returns the persisted session id for (folder, topic).
func (d *DB) SessionID(folder, topic string) string {
	id, _ := d.store().GetSession(folder, topic)
	return id
}

// SetTurnGatewayKey records the sha256 of the key the gateway minted for this
// turn. The gateway names a key in its spend log by that hash and never by the
// key itself, so this is the handle that resolves a turn to the gateway's own
// numbers — the pointer arizuko keeps in place of a copy of them (spec 6/39).
func (d *DB) SetTurnGatewayKey(turnID, keyHash string) error {
	return d.store().SetTurnGatewayKey(turnID, keyHash)
}

// TurnGatewayKey returns the key hash that metered a turn. Empty means the turn
// ran with no gateway, so no meter holds a number for it.
func (d *DB) TurnGatewayKey(turnID string) (string, error) {
	return d.store().TurnGatewayKey(turnID)
}

// FolderCap returns the per-day spend cap for a folder in cents. Zero means
// uncapped (the default).
func (d *DB) FolderCap(folder string) (int, error) {
	return d.store().FolderCap(folder)
}

// SetFolderCap writes the per-day spend cap on the groups row for folder.
// Zero = uncapped. The operator write half of FolderCap, so `arizuko budget
// set folder` lands on the row budgetGate reads.
func (d *DB) SetFolderCap(folder string, cents int) error {
	return d.store().SetFolderCap(folder, cents)
}

// ProactiveChats lists the chats that have ever carried a real inbound message —
// the universe the proactive scan iterates, then filtered by the group's cached
// mode. It is an attribute of the chat now rather than a DISTINCT over every
// message ever written.
func (d *DB) ProactiveChats() ([]string, error) {
	return d.store().ProactiveChats(context.Background())
}

// LastInboundAt returns the timestamp of the newest real inbound message on a
// chat, or zero time when none. The silence-debounce clock.
func (d *DB) LastInboundAt(jid string) time.Time {
	return d.store().LastInboundAt(context.Background(), jid)
}

// LatestSource returns the adapter name that delivered the newest real inbound
// row on a chat, used by the Deliverer to route an outbound back to the adapter
// the conversation came in on. Empty when the chat has no sourced inbound.
func (d *DB) LatestSource(jid string) string {
	return d.store().LatestSource(context.Background(), jid)
}

// BotSpokeSince reports whether a bot-authored row exists on a chat at or after
// since (the BotQuiet veto).
func (d *DB) BotSpokeSince(jid string, since time.Time) bool {
	return d.store().BotSpokeSince(context.Background(), jid, since)
}

// InboundCountSince counts real inbound rows on a chat at or after since (the
// RecentActivity floor).
func (d *DB) InboundCountSince(jid string, since time.Time) int {
	return d.store().InboundCountSince(context.Background(), jid, since)
}

// LastInbound returns the newest real inbound row on a chat (content +
// timestamp), used by positive signals. ok=false when the chat has none.
func (d *DB) LastInbound(jid string) (content string, ts time.Time, ok bool) {
	return d.store().LastInbound(context.Background(), jid)
}

// ProactiveLastFired returns when a proactive turn last fired on a chat, or
// zero time when never (the mandatory 24h cooldown).
func (d *DB) ProactiveLastFired(jid string) time.Time {
	return d.store().ProactiveLastFired(jid)
}

// FireProactive appends the synthetic inbound row AND sets the chat's proactive
// cooldown in one transaction. The caller dispatches the run only after this
// commits, so a crash before dispatch leaves the cooldown set — at worst one
// missed turn, never a double-fire. Returns the synthetic row's id, which is the
// turn id.
func (d *DB) FireProactive(jid string) (string, error) {
	id := "proactive-" + randHex(8)
	if err := d.store().FireProactive(context.Background(), jid, id); err != nil {
		return "", err
	}
	return id, nil
}

// ChatHasRunningTurn reports whether a chat has a turn still in state
// 'running' — the scan skips it (the per-folder queue serializes; a
// proactive inbound never steers a live turn).
func (d *DB) ChatHasRunningTurn(jid string) bool {
	return d.store().ChatHasRunningTurn(jid)
}

// ThreadHasBotMessage reports whether the bot has posted in (chatJID, topic).
// Empty topic → false (main timeline is not a thread).
func (d *DB) ThreadHasBotMessage(chatJID, topic string) bool {
	return d.store().ThreadHasBotMessage(chatJID, topic)
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

// OpenOver serves routd from a handle the caller owns. The caller has already
// made the entities and there is no chain to run, because a chain does not
// belong to a store addressed by key. A caller that answers searches calls
// StartSearch itself.
func OpenOver(obj objstore.Store) *DB {
	return &DB{obj: obj}
}
