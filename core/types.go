package core

import (
	"context"
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
}

type Group struct {
	JID        string
	Name       string
	Folder     string
	Trigger    string
	AddedAt    time.Time
	Config     GroupConfig
	NeedTrig   bool
	SlinkToken string
	Parent     string
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
	ID     int64
	JID    string
	Seq    int
	Type   string // command|verb|pattern|keyword|sender|trigger|default
	Match  string
	Target string
}

type Task struct {
	ID      string
	Owner   string
	ChatJID string
	Prompt  string
	Cron    string // cron expr; empty for one-shot
	NextRun *time.Time
	Status  string // active|paused|completed
	Created time.Time
}

type Channel interface {
	Name() string
	Connect(ctx context.Context) error
	Send(jid, text string) error
	SendFile(jid, path, name string) error
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

type ChatInfo struct {
	JID      string
	Name     string
	Channel  string
	IsGroup  bool
	LastTime string
	Errored  bool
}
