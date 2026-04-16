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

// gatewayCommand is a single registration table entry: the name (including
// the leading "/") plus a handler. To add a command, add one entry here.
// Both isGatewayCommand and handleCommand consult this table, so there is
// no second place to update.
type gatewayCommand struct {
	name    string
	handler func(g *Gateway, msg core.Message, group core.Group, arg string) bool
}

var gatewayCommands = []gatewayCommand{
	{"/new", func(g *Gateway, m core.Message, gr core.Group, arg string) bool {
		return g.cmdNew(m.ChatJID, gr, arg)
	}},
	{"/ping", func(g *Gateway, m core.Message, gr core.Group, _ string) bool {
		return g.cmdPing(m.ChatJID, gr)
	}},
	{"/chatid", func(g *Gateway, m core.Message, _ core.Group, _ string) bool {
		return g.cmdChatID(m.ChatJID)
	}},
	{"/stop", func(g *Gateway, m core.Message, _ core.Group, _ string) bool {
		return g.cmdStop(m.ChatJID)
	}},
	{"/status", func(g *Gateway, m core.Message, gr core.Group, _ string) bool {
		return g.cmdStatus(m.ChatJID, gr)
	}},
	{"/root", func(g *Gateway, m core.Message, gr core.Group, arg string) bool {
		return g.cmdRoot(m.ChatJID, gr, arg)
	}},
}

func lookupCommand(raw string) *gatewayCommand {
	t := cmdText(raw)
	if !strings.HasPrefix(t, "/") {
		return nil
	}
	head, _, _ := strings.Cut(t, " ")
	head = strings.ToLower(head)
	// Telegram appends @botname to commands (e.g. /new@rhias_fiu_bot)
	head, _, _ = strings.Cut(head, "@")
	for i := range gatewayCommands {
		if gatewayCommands[i].name == head {
			return &gatewayCommands[i]
		}
	}
	return nil
}

func isGatewayCommand(raw string) bool {
	return lookupCommand(raw) != nil
}

func (g *Gateway) handleCommand(msg core.Message, group core.Group) bool {
	cmd := lookupCommand(msg.Content)
	if cmd == nil {
		return false
	}
	_, arg, _ := strings.Cut(cmdText(msg.Content), " ")
	return cmd.handler(g, msg, group, arg)
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
			ID:        core.MsgID("cmd-new"),
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
	nGroups := len(g.store.AllGroups())
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

func (g *Gateway) cmdRoot(chatJid string, group core.Group, arg string) bool {
	id := auth.Resolve(group.Folder)
	if id.Tier > 1 {
		g.sendMessage(chatJid, "Permission denied.")
		return true
	}
	if arg == "" {
		g.sendMessage(chatJid, "Usage: /root <message>")
		return true
	}
	rootFolder := strings.SplitN(group.Folder, "/", 2)[0]
	if rootFolder == group.Folder {
		g.sendMessage(chatJid, "Already in root group.")
		return true
	}
	if _, found := g.store.GroupByFolder(rootFolder); !found {
		g.sendMessage(chatJid, "Root group not found.")
		return true
	}
	g.delegateViaMessage(rootFolder, arg, chatJid, 0)
	return true
}

func (g *Gateway) cmdStatus(chatJid string, group core.Group) bool {
	id := auth.Resolve(group.Folder)
	if id.Tier != 0 {
		g.sendMessage(chatJid, "Permission denied: root only.")
		return true
	}

	nGroups := len(g.store.AllGroups())
	g.mu.RLock()
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
