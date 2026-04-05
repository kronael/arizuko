package core

import (
	"context"
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
}

type OutboundEntry struct {
	ChatJID       string
	Content       string
	Source        string // "agent" | "mcp" | "scheduler" | "control" | "error"
	GroupFolder   string
	ReplyToID     string
	PlatformMsgID string
	Topic         string
}

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
	Sidecars    map[string]Sidecar
	MaxChildren int
}

type Mount struct {
	Host      string
	Container string
	RO        bool
}

type Sidecar struct {
	Image string
	Env   map[string]string
	MemMB int
	CPUs  float64
	Net   string   // "bridge"|"none"
	Tools []string // ["*"] or specific
}

type Route struct {
	ID            int64  `json:"id"`
	JID           string `json:"jid,omitempty"`
	Seq           int    `json:"seq"`
	Type          string `json:"type"` // command|verb|pattern|keyword|sender|default
	Match         string `json:"match"`
	Target        string `json:"target"`
	ImpulseConfig string `json:"impulse_config,omitempty"`
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

// JidPlatform extracts the platform prefix from a JID (e.g. "telegram:123" -> "telegram").
func JidPlatform(jid string) string {
	if i := strings.IndexByte(jid, ':'); i > 0 {
		return jid[:i]
	}
	return ""
}
