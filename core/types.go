package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"
)

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
	Topic         string
	RoutedTo      string
	Verb          string // event type: "message" (default), "join", "edit", "delete", etc.
	Attachments   string // JSON-encoded []chanlib.InboundAttachment
	Source        string // adapter name that handled this row (inbound: receiver; outbound: deliverer)
	Errored       bool   // set when a previous agent run failed on this message; re-fed tagged for retry
}

// Chat is the persisted state for a chat_jid: routing stickiness,
// agent cursor, and the group/dm classification set by the adapter on
// first inbound.
type Chat struct {
	JID          string
	IsGroup      bool
	AgentCursor  *time.Time
	StickyGroup  string
	StickyTopic  string
}

// IsSingleUser reports whether this chat is provably 1:1 with one
// human. False for group chats, channels, public threads. The Phase C
// secrets resolver gates user-scope secret injection on this predicate:
// in multi-user contexts personal credentials would leak across users
// sharing the spawn.
func (c Chat) IsSingleUser() bool { return !c.IsGroup }

type Group struct {
	Name       string
	Folder     string
	AddedAt    time.Time
	Config     GroupConfig
	SlinkToken string
	Parent     string
	State      string // "active" | "closed" | "archived"; default "active"
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
	ID            int64  `json:"id"`
	Seq           int    `json:"seq"`
	Match         string `json:"match"`
	Target        string `json:"target"`
	ImpulseConfig string `json:"impulse_config,omitempty"`
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
	Send(jid, text, replyTo, threadID string) (string, error)
	SendFile(jid, path, name, caption string) error
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

// GenSlinkToken returns a 256-bit base64url-encoded random token. Panics
// on RNG failure — a zero-entropy token would be a guessable credential.
func GenSlinkToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

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

func JidPlatform(jid string) string {
	if i := strings.IndexByte(jid, ':'); i > 0 {
		return jid[:i]
	}
	return ""
}
