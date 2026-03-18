package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
)

// cmdText strips media placeholder and routing prefix before command detection.
// "[Doc: f.txt] @root /stop" → "/stop"
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

// isGatewayCommand reports whether text (after stripping prefixes) is a
// command handled by gated — not an agent message.
func isGatewayCommand(raw string) bool {
	t := cmdText(raw)
	if !strings.HasPrefix(t, "/") {
		return false
	}
	word := strings.ToLower(strings.SplitN(t[1:], " ", 2)[0])
	switch word {
	case "new", "ping", "chatid", "stop":
		return true
	}
	return false
}

func (g *Gateway) handleCommand(msg core.Message, group core.Group) bool {
	text := cmdText(msg.Content)
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
	sessID := g.store.GetSession(group.Folder)
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

func (g *Gateway) cmdStop(chatJid string, group core.Group) bool {
	stopped := g.queue.StopProcess(chatJid)
	if stopped {
		g.sendMessage(chatJid, "Container stopped.")
	} else {
		g.sendMessage(chatJid, "No active container for this chat.")
	}
	return true
}
