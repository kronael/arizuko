package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/core"
)

// cmdText strips media placeholder and routing prefix: "[Doc: f.txt] @root /stop" → "/stop"
func cmdText(raw string) string {
	t := strings.TrimSpace(raw)
	if strings.HasPrefix(t, "[") {
		if i := strings.Index(t, "]"); i >= 0 {
			t = strings.TrimSpace(t[i+1:])
		}
	}
	if strings.HasPrefix(t, "@") {
		if i := strings.IndexByte(t, ' '); i >= 0 {
			t = strings.TrimSpace(t[i+1:])
		} else {
			t = ""
		}
	}
	return t
}

func isGatewayCommand(raw string) bool {
	t := cmdText(raw)
	if !strings.HasPrefix(t, "/") {
		return false
	}
	word, _, _ := strings.Cut(t[1:], " ")
	switch strings.ToLower(word) {
	case "new", "ping", "chatid", "stop", "status":
		return true
	}
	return false
}

func (g *Gateway) handleCommand(msg core.Message, group core.Group) bool {
	text := cmdText(msg.Content)
	if !strings.HasPrefix(text, "/") {
		return false
	}

	head, arg, _ := strings.Cut(text, " ")
	switch strings.ToLower(head) {
	case "/new":
		return g.cmdNew(msg.ChatJID, group, arg)
	case "/ping":
		return g.cmdPing(msg.ChatJID, group)
	case "/chatid":
		return g.cmdChatID(msg.ChatJID)
	case "/stop":
		return g.cmdStop(msg.ChatJID)
	case "/status":
		return g.cmdStatus(msg.ChatJID, group)
	default:
		return false
	}
}

func (g *Gateway) cmdNew(chatJid string, group core.Group, arg string) bool {
	g.store.ClearChatErrored(chatJid)

	label := "Session cleared."
	followup := ""
	if strings.HasPrefix(arg, "#") {
		label = "Topic session cleared."
		name, rest, _ := parsePrefix(arg)
		g.store.DeleteSession(group.Folder, "#"+name)
		if rest != "" {
			followup = "#" + name + " " + rest
		}
	} else {
		g.clearSession(group.Folder)
		followup = arg
	}

	if followup != "" {
		g.store.PutMessage(core.Message{
			ID:        fmt.Sprintf("cmd-new-%d", time.Now().UnixNano()),
			ChatJID:   chatJid,
			Sender:    "user",
			Content:   followup,
			Timestamp: time.Now(),
		})
		g.queue.EnqueueMessageCheck(chatJid)
		g.sendMessage(chatJid, label+" Processing your message...")
	} else {
		g.sendMessage(chatJid, label)
	}
	return true
}

func (g *Gateway) cmdPing(chatJid string, group core.Group) bool {
	sessID, _ := g.store.GetSession(group.Folder, "")
	g.mu.RLock()
	nGroups := len(g.groups)
	g.mu.RUnlock()

	active := g.queue.ActiveCount()

	sess := "none"
	if sessID != "" {
		sess = sessID[:min(8, len(sessID))]
	}

	reply := fmt.Sprintf(
		"pong\ngroup: %s (%s)\nsession: %s\nactive containers: %d\nregistered groups: %d",
		group.Name, group.Folder, sess, active, nGroups)
	g.sendMessage(chatJid, reply)
	return true
}

func (g *Gateway) cmdChatID(chatJid string) bool {
	g.sendMessage(chatJid, chatJid)
	return true
}

func (g *Gateway) cmdStop(chatJid string) bool {
	if g.queue.StopProcess(chatJid) {
		g.sendMessage(chatJid, "Container stopped.")
	} else {
		g.sendMessage(chatJid, "No active container for this chat.")
	}
	return true
}

func (g *Gateway) cmdStatus(chatJid string, group core.Group) bool {
	id := auth.Resolve(group.Folder)
	if id.Tier != 0 {
		g.sendMessage(chatJid, "Permission denied: root only.")
		return true
	}

	g.mu.RLock()
	nGroups := len(g.groups)
	nChannels := len(g.channels)
	g.mu.RUnlock()

	active := g.queue.ActiveCount()
	errored := g.store.CountErroredChats()
	tasks := g.store.CountActiveTasks()

	reply := fmt.Sprintf(
		"status\nchannels: %d\ngroups: %d\nactive containers: %d\nerrored chats: %d\nactive tasks: %d",
		nChannels, nGroups, active, errored, tasks)
	g.sendMessage(chatJid, reply)
	return true
}
