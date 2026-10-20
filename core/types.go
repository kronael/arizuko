package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Cross-boundary identity types live in the top-level types/ package
// (types.UserSub / Folder / Tier / Scope) — see specs/5/A-primitives-framing.md.

type Message struct {
	ID            string
	ChatJID       string
	Sender        string
	Name          string
	Content       string
	Timestamp     time.Time
	FromMe        bool
	BotMsg        bool
	ForwardedFrom string
	ReplyToID     string
	ReplyToText   string
	ReplyToSender string
	PlatformID    string // platform-native message ID (Slack TS, Telegram msg_id, etc.) for outbound messages
	Topic         string
	RoutedTo      string
	Verb          string // event type: "message" (default), "join", "edit", "delete", etc.
	Attachments   string // JSON-encoded []chanlib.InboundAttachment
	Source        string // adapter name that handled this row (inbound: receiver; outbound: deliverer)
	Errored       bool   // set when a previous agent run failed on this message; re-fed tagged for retry
	TurnID        string // for outbound: the inbound message id that triggered the run; for inbound: empty
	ChatName      string // human-readable channel/group name set by the adapter (e.g. "#general", "My Group")
	LinkContext   string // route-token context snapshotted at ingest (spec 5/W); rendered as <link-context> in the prompt
}

// Message status values for the poll-based outbound delivery path.
//
// They are not a FIELD of a message. Delivery state is mutable and a message is
// history, so the status and the attempt count live in their own entity keyed by
// the status — and 'sent' is the ABSENCE of an item there, because 98.4% of the
// rows on krons are sent and an index holding all of them never sheds one
// (spec 6/37 §"How `messages` leaves").
//
// 'failed' IS the dead-letter state: the message keeps its content and no sweep
// sends it again. The attempt count tells the two dead-letter causes apart —
// absent means the platform refused the message permanently, the cap means the
// retries ran out.
const (
	MessageStatusSent    = "sent"
	MessageStatusPending = "pending"
	MessageStatusFailed  = "failed"
)

const DefaultProduct = "assistant"

// IsolatedSenderPrefix marks a message produced by an isolated scheduled run, which
// gets its own one-off container and session precisely so a folder's crons never
// enter the conversation the people in that chat are having (BUGS L54). Declared ONCE
// here because two readers test it: routd's dispatch and the ambient-window SQL.
const IsolatedSenderPrefix = "timed-isolated:"

// IsIsolatedSender reports whether a sender or trigger names an isolated run.
func IsIsolatedSender(s string) bool { return strings.HasPrefix(s, IsolatedSenderPrefix) }

// ScheduledSender is the sender of a group-context scheduled task's prompt row.
const ScheduledSender = "timed"

// IsScheduledSender reports whether a sender is a scheduled task's prompt, in
// either context mode.
func IsScheduledSender(s string) bool { return s == ScheduledSender || IsIsolatedSender(s) }

type Group struct {
	Folder  string
	AddedAt time.Time
	Config  GroupConfig
	Product string
	Model   string // per-group model override; empty = instance default

	// Open, ObserveWindowMessages and ObserveWindowChars are read-only
	// projections of the `config` column, exactly as Model already is. They
	// exist so ONE read answers the whole group settings page instead of the
	// page reaching into another daemon's DB for them (spec 6/28 read-path).
	//
	// Safe as read-only because Store.PutGroup writes `container_config`,
	// `product` and `updated_at` and never `config` — so a round-trip through
	// this struct cannot clobber them. Their own setters (SetGroupOpen,
	// SetGroupObserveWindow) remain the only writers.
	//
	// Defaults match the accessors they replace: a group with no `open` key is
	// OPEN, and an unset window is -1 (no per-group cap), not 0 (a cap of
	// nothing).
	Open                  bool `json:"open"`
	ObserveWindowMessages int  `json:"observe_window_messages"`
	ObserveWindowChars    int  `json:"observe_window_chars"`
}

type GroupConfig struct {
	Mounts  []Mount
	Timeout time.Duration
}

type Mount struct {
	Host      string
	Container string
	RO        bool
}

type Route struct {
	ID                    int64  `json:"id"`
	Seq                   int    `json:"seq"`
	Match                 string `json:"match"`
	Target                string `json:"target"`
	ObserveWindowMessages int    `json:"observe_window_messages,omitempty"`
	ObserveWindowChars    int    `json:"observe_window_chars,omitempty"`
}

// TopicLineage is the per-topic state introduced by spec 5/F-topic-lineage. ParentTopic is *string
// so "fork from main" is distinguishable from "no parent". Empty ObservedCursor means
// no cursor recorded yet. All timestamps are core.TSLayout UTC.
type TopicLineage struct {
	Folder         string
	Topic          string
	ParentTopic    *string
	ForkedAt       string
	ObservedCursor string
}

// RouteTarget parses `folder`, `folder#observe`, `folder#announce` or `folder#<topic>`
// on routes.target. observe sets Mode (observe-only, no agent turn); announce is a
// no-op for inbound that flags the channel as a release-announcement target for the
// migrate skill. Any other fragment is a topic name pinned for the routed message.
type RouteTarget struct {
	Folder   string
	Topic    string // when non-empty, route pins this topic on the message
	Mode     string // "" trigger, "observe" silent ingest
	Announce bool   // #announce: release-note target; inbound-neutral (trigger)
}

func ParseRouteTarget(s string) RouteTarget {
	before, after, ok := strings.Cut(s, "#")
	if !ok {
		return RouteTarget{Folder: s}
	}
	frag := after
	rt := RouteTarget{Folder: before}
	switch frag {
	case "observe":
		rt.Mode = frag
	case "announce":
		rt.Announce = true
	default:
		rt.Topic = frag
	}
	return rt
}

func (rt RouteTarget) String() string {
	switch {
	case rt.Mode != "":
		return rt.Folder + "#" + rt.Mode
	case rt.Announce:
		return rt.Folder + "#announce"
	case rt.Topic != "":
		return rt.Folder + "#" + rt.Topic
	default:
		return rt.Folder
	}
}

func JidRoom(jid string) string {
	if _, after, ok := strings.Cut(jid, ":"); ok {
		return after
	}
	return jid
}

const (
	TaskActive = "active"
	TaskPaused = "paused"
)

type Task struct {
	ID          string
	Owner       string
	ChatJID     string
	Prompt      string
	Cron        string // cron expr, interval ms, or empty for one-shot
	NextRun     *time.Time
	Status      string // TaskActive | TaskPaused
	Created     time.Time
	ContextMode string // "group" | "isolated"; default "group"
}

// PanePrompt is one suggested-prompt button (title shown on the button,
// message sent as user input on click). chanreg.HTTPChannel stages them
// through the adapter's POST /v1/pane/prompts; slakd serves it, others 404.
type PanePrompt struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

// SessionRecord is one entry of a folder's run history, as routd federates it
// from runed's GET /v1/spawn_sessions/recent and as store reads its own
// session_log.
//
// ID IS A STRING because runed's is: the row's identity there is its sort key,
// the instant it started plus a suffix, and a generated sequence is what no
// document store has (spec 6/37 rule 4). The same move made audit.Row.ID a
// string for the same reason.
type SessionRecord struct {
	ID        string
	Folder    string
	SessionID string
	StartedAt time.Time
	EndedAt   *time.Time
	Result    string
	Error     string
	MsgCount  int
}

func randBytes() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return b
}

// GenHexToken returns a 256-bit hex-encoded random token.
func GenHexToken() string { return hex.EncodeToString(randBytes()) }

var instanceNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,31}$`)

// SanitizeInstance rejects names unsafe for filesystem paths, docker
// container_name, and unquoted YAML scalars.
func SanitizeInstance(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("instance name is empty")
	}
	if len(name) > 32 {
		return "", fmt.Errorf("instance name too long: %d chars (max 32)", len(name))
	}
	if !instanceNameRE.MatchString(name) {
		return "", fmt.Errorf("invalid instance name %q (allowed: [A-Za-z0-9_-], max 32, no leading '-')", name)
	}
	return name, nil
}

func MsgID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// NewSessionID mints a fresh session ID for a topic. It MUST be a UUID: the id names
// the agent's Claude Code transcript, and `claude --resume` accepts nothing else. A
// "sess-<nano>" placeholder made every forked topic start blind — routd copied the
// parent transcript to a filename the agent then refused to resume (2026-06-01 to
// 2026-08-20).
func NewSessionID() string {
	return uuid.New().String()
}

// ErrTopicExists signals a fork attempted to overwrite an existing
// (folder, topic) without force=true. Spec 5/F-topic-lineage.
var ErrTopicExists = fmt.Errorf("topic exists")

// ACLRow is one grant in the unified ACL (spec 5/32-acl-unified). Effect is "allow"
// or "deny" (deny wins). Params/predicate are empty strings when absent.
type ACLRow struct {
	Principal string
	Action    string
	Scope     string
	Effect    string
	Params    string
	Predicate string
	GrantedBy string
	GrantedAt string
	// GrantOption is Postgres WITH GRANT OPTION (spec 5/33-paths-roles): when true the holder
	// may re-delegate this row (or a subset) onward. The delegation axis,
	// orthogonal to the action lattice's read/write coverage.
	GrantOption bool
}

func JidPlatform(jid string) string {
	if i := strings.IndexByte(jid, ':'); i > 0 {
		return jid[:i]
	}
	return ""
}
