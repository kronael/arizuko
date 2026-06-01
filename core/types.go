package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Cross-boundary identity types live in the top-level types/ package
// (types.UserSub / Folder / Tier / Scope) — see specs/5/U-genericization.md.

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
	Status        string // delivery state: 'sent' (default/inbound), 'pending' (outbound queued), 'failed' (terminal)
	ChatName      string // human-readable channel/group name set by the adapter (e.g. "#general", "My Group")
}

// Message status values for the poll-based outbound delivery path.
const (
	MessageStatusSent    = "sent"
	MessageStatusPending = "pending"
	MessageStatusFailed  = "failed"
)

const DefaultProduct = "assistant"

type Group struct {
	Folder  string
	AddedAt time.Time
	Config  GroupConfig
	Product string
	Model   string // per-group model override; empty = instance default
}

type GroupConfig struct {
	Mounts      []Mount
	Timeout     time.Duration
	MaxChildren int
}

type Mount struct {
	Host      string
	Container string
	RO        bool
}

type Route struct {
	ID                     int64  `json:"id"`
	Seq                    int    `json:"seq"`
	Match                  string `json:"match"`
	Target                 string `json:"target"`
	ObserveWindowMessages  int    `json:"observe_window_messages,omitempty"`
	ObserveWindowChars     int    `json:"observe_window_chars,omitempty"`
}

// TopicLineage is the per-topic state introduced by spec 6/F.
// ParentTopic is *string so "fork from main" (the empty-string topic)
// is distinguishable from "no parent" (NULL in DB). Empty
// ObservedCursor means "no cursor recorded yet" — callers treat
// that as no lower bound on observed reads. All timestamps are
// RFC3339Nano UTC.
type TopicLineage struct {
	Folder         string
	Topic          string
	ParentTopic    *string
	ForkedAt       string
	ObservedCursor string
}

// RouteTarget parses `folder`, `folder#observe`, or `folder#<topic>`
// syntax on routes.target. "observe" is the only reserved fragment
// — it sets Mode and means observe-only (no agent turn). Any other
// fragment is a topic name pinned for the routed message.
type RouteTarget struct {
	Folder string
	Topic  string // when non-empty, route pins this topic on the message
	Mode   string // "" trigger, "observe" silent ingest
}

func ParseRouteTarget(s string) RouteTarget {
	i := strings.IndexByte(s, '#')
	if i < 0 {
		return RouteTarget{Folder: s}
	}
	frag := s[i+1:]
	rt := RouteTarget{Folder: s[:i]}
	if frag == "observe" {
		rt.Mode = frag
	} else {
		rt.Topic = frag
	}
	return rt
}

func (rt RouteTarget) String() string {
	switch {
	case rt.Mode != "":
		return rt.Folder + "#" + rt.Mode
	case rt.Topic != "":
		return rt.Folder + "#" + rt.Topic
	default:
		return rt.Folder
	}
}

func JidRoom(jid string) string {
	if i := strings.IndexByte(jid, ':'); i >= 0 {
		return jid[i+1:]
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

type Channel interface {
	Name() string
	Connect(ctx context.Context) error
	Send(jid, text, replyTo, threadID, turnID string) (string, error)
	SendFile(jid, path, name, caption, replyTo, threadID string) error
	// SendVoice delivers a synthesized voice message. Adapters that don't
	// support a native voice/PTT primitive return chanlib.ErrUnsupported.
	// threadID posts into the active thread (same semantics as Send);
	// adapters without threading accept-and-ignore it.
	SendVoice(jid, audioPath, caption, threadID string) (string, error)
	Owns(jid string) bool
	Typing(jid string, on bool) error
	Disconnect() error
}

// HistoryFetcher is an optional capability implemented by channels that can
// retrieve history from the upstream platform API. Returns raw JSON bytes
// matching chanlib.HistoryResponse; the caller decodes.
type HistoryFetcher interface {
	FetchHistory(ctx context.Context, jid string, before time.Time, limit int) ([]byte, error)
}

// Socializer is the optional channel capability for social-graph verbs:
// standalone posts, likes, and post deletion. Reply sends route through
// Send(...) with replyTo set, so they're not here. Adapters that don't
// implement these should return a sentinel "unsupported" error; callers
// map that to a structured MCP response.
type Socializer interface {
	Post(ctx context.Context, jid, content string, mediaPaths []string) (string, error)
	Like(ctx context.Context, jid, targetID, reaction string) error
	Delete(ctx context.Context, jid, targetID string) error
	Forward(ctx context.Context, sourceMsgID, targetJID, comment string) (string, error)
	Quote(ctx context.Context, jid, sourceMsgID, comment string) (string, error)
	Repost(ctx context.Context, jid, sourceMsgID string) (string, error)
	Dislike(ctx context.Context, jid, targetID string) error
	Edit(ctx context.Context, jid, targetID, content string) error
	Pin(ctx context.Context, jid, targetID string) error
	Unpin(ctx context.Context, jid, targetID string, all bool) error
}

// Suggester is the optional channel capability for staging
// suggested-prompt buttons shown to the user before their next
// message (Slack assistant-pane prompts; future: Telegram
// inline_keyboard, Discord ActionRow, WhatsApp interactive buttons).
// Implemented by chanreg.HTTPChannel for any adapter whose backend
// serves POST /v1/pane/prompts; slakd serves it, others 404.
type Suggester interface {
	SetSuggestions(ctx context.Context, jid string, prompts []PanePrompt) error
}

// Namer is the optional channel capability for renaming an open
// conversation (Slack assistant-pane title; future: Telegram forum
// topic name, Discord thread name, WhatsApp group subject).
// Implemented by chanreg.HTTPChannel for any adapter whose backend
// serves POST /v1/pane/title.
type Namer interface {
	SetName(ctx context.Context, jid, name string) error
}

// PanePrompt is one suggested-prompt button (title shown on the button,
// message sent as user input on click).
type PanePrompt struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

type SessionRecord struct {
	ID        int64
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

// NewSessionID mints a fresh session ID for a topic (spec 4/23,
// extended by spec 6/F for forked children). Format: "sess-<nano>".
func NewSessionID() string {
	return fmt.Sprintf("sess-%d", time.Now().UnixNano())
}

// ErrTopicExists signals a fork attempted to overwrite an existing
// (folder, topic) without force=true. Spec 6/F.
var ErrTopicExists = fmt.Errorf("topic exists")

// ACLRow is one grant in the unified ACL (spec 6/9). Effect is "allow"
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
}

func JidPlatform(jid string) string {
	if i := strings.IndexByte(jid, ':'); i > 0 {
		return jid[:i]
	}
	return ""
}
