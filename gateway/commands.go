package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
)

// handleCommand checks if a message is a gateway command.
// Returns true if handled (caller should skip normal processing).
func (g *Gateway) handleCommand(msg core.Message, group core.Group) bool {
	text := strings.TrimSpace(msg.Content)
	if !strings.HasPrefix(text, "/") {
		return false
	}

	parts := strings.SplitN(text, " ", 2)
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}

	switch cmd {
	case "/new":
		return g.cmdNew(msg.ChatJID, group, arg)
	case "/ping":
		return g.cmdPing(msg.ChatJID, group)
	case "/chatid":
		return g.cmdChatID(msg.ChatJID)
	case "/stop":
		return g.cmdStop(msg.ChatJID, group)
	default:
		return false
	}
}

func (g *Gateway) cmdNew(chatJid string, group core.Group, arg string) bool {
	g.clearSession(group.Folder)
	g.store.ClearChatErrored(chatJid)

	if arg != "" {
		g.store.PutMessage(core.Message{
			ID:        fmt.Sprintf("cmd-new-%d", time.Now().UnixNano()),
			ChatJID:   chatJid,
			Sender:    "user",
			Content:   arg,
			Timestamp: time.Now(),
		})
		g.queue.EnqueueMessageCheck(chatJid)
		g.sendMessage(chatJid, "Session cleared. Processing your message...")
	} else {
		g.sendMessage(chatJid, "Session cleared.")
	}
	return true
}

func (g *Gateway) cmdPing(chatJid string, group core.Group) bool {
	g.mu.RLock()
	sessID := g.sessions[group.Folder]
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

func (g *Gateway) cmdStop(chatJid string, group core.Group) bool {
	stopped := g.queue.StopProcess(chatJid)
	if stopped {
		g.sendMessage(chatJid, "Container stopped.")
	} else {
		g.sendMessage(chatJid, "No active container for this chat.")
	}
	return true
}
