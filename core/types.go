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
	Seq           int    `json:"seq"`
	Match         string `json:"match"`
	Target        string `json:"target"`
	ImpulseConfig string `json:"impulse_config,omitempty"`
}

// JidRoom returns the post-colon portion of a JID, or the whole string if no colon.
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

// JidPlatform returns the pre-colon portion of a JID, or "" if no colon.
func JidPlatform(jid string) string {
	if i := strings.IndexByte(jid, ':'); i > 0 {
		return jid[:i]
	}
	return ""
}
