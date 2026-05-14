package gateway

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

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
	{"/invite", func(g *Gateway, m core.Message, gr core.Group, arg string) bool {
		return g.cmdInvite(m.ChatJID, gr, arg)
	}},
	{"/gate", func(g *Gateway, m core.Message, gr core.Group, arg string) bool {
		return g.cmdGate(m.ChatJID, gr, arg)
	}},
	{"/approve", func(g *Gateway, m core.Message, _ core.Group, _ string) bool {
		g.sendMessage(m.ChatJID, "HITL not configured")
		return true
	}},
	{"/reject", func(g *Gateway, m core.Message, _ core.Group, _ string) bool {
		g.sendMessage(m.ChatJID, "HITL not configured")
		return true
	}},
}

func lookupCommand(raw string) (*gatewayCommand, string) {
	t := cmdText(raw)
	if !strings.HasPrefix(t, "/") {
		return nil, ""
	}
	head, arg, _ := strings.Cut(t, " ")
	// Telegram appends @botname to commands (e.g. /new@rhias_fiu_bot)
	head, _, _ = strings.Cut(strings.ToLower(head), "@")
	for i := range gatewayCommands {
		if gatewayCommands[i].name == head {
			return &gatewayCommands[i], arg
		}
	}
	return nil, ""
}

func isGatewayCommand(raw string) bool {
	cmd, _ := lookupCommand(raw)
	return cmd != nil
}

func (g *Gateway) handleCommand(msg core.Message, group core.Group) bool {
	cmd, arg := lookupCommand(msg.Content)
	if cmd == nil {
		return false
	}
	return cmd.handler(g, msg, group, arg)
}

func (g *Gateway) cmdNew(chatJid string, group core.Group, arg string) bool {
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
		"pong\ngroup: %s\nsession: %s\nactive containers: %d\nregistered groups: %d",
		group.Folder, sess, active, nGroups)
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
	if err := g.delegateViaMessage(rootFolder, arg, chatJid, 0); err != nil {
		slog.Warn("cmdRoot: delegate failed",
			"jid", chatJid, "target", rootFolder, "err", err)
	}
	return true
}

func (g *Gateway) cmdInvite(chatJid string, group core.Group, arg string) bool {
	id := auth.Resolve(group.Folder)
	if id.Tier != 0 {
		g.sendMessage(chatJid, "Permission denied: root group only.")
		return true
	}
	maxUses := 1
	if arg != "" {
		n, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil || n < 1 {
			g.sendMessage(chatJid, "Usage: /invite [max_uses]")
			return true
		}
		maxUses = n
	}
	inv, err := g.store.CreateInvite(group.Folder, chatJid, maxUses, nil)
	if err != nil {
		slog.Error("create invite", "folder", group.Folder, "err", err)
		g.sendMessage(chatJid, "Failed to create invite.")
		return true
	}
	base := strings.TrimRight(g.cfg.AuthBaseURL, "/")
	link := base + "/invite/" + inv.Token
	label := fmt.Sprintf("Invite link (%d use", maxUses)
	if maxUses > 1 {
		label += "s"
	}
	label += "):\n" + link
	g.sendMessage(chatJid, label)
	return true
}

func (g *Gateway) cmdGate(chatJid string, group core.Group, arg string) bool {
	id := auth.Resolve(group.Folder)
	if id.Tier != 0 {
		g.sendMessage(chatJid, "Permission denied: root only.")
		return true
	}

	parts := strings.Fields(arg)
	action := ""
	if len(parts) > 0 {
		action = parts[0]
	}

	switch action {
	case "", "list":
		gates, err := g.store.ListGates()
		if err != nil {
			g.sendMessage(chatJid, "Failed: "+err.Error())
			return true
		}
		if len(gates) == 0 {
			g.sendMessage(chatJid, "no gates")
			return true
		}
		var b strings.Builder
		for _, gt := range gates {
			en := "on"
			if !gt.Enabled {
				en = "off"
			}
			fmt.Fprintf(&b, "%s  %d/day  %s\n", gt.Gate, gt.LimitPerDay, en)
		}
		g.sendMessage(chatJid, strings.TrimRight(b.String(), "\n"))

	case "add":
		if len(parts) < 3 {
			g.sendMessage(chatJid, "Usage: /gate add <spec> <N>/day")
			return true
		}
		spec := parts[1]
		limitStr := strings.TrimSuffix(parts[2], "/day")
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit <= 0 {
			g.sendMessage(chatJid, "Failed: invalid limit")
			return true
		}
		if err := g.store.PutGate(spec, limit); err != nil {
			g.sendMessage(chatJid, "Failed: "+err.Error())
			return true
		}
		g.sendMessage(chatJid, fmt.Sprintf("gate added: %s %d/day", spec, limit))

	case "rm":
		if len(parts) < 2 {
			g.sendMessage(chatJid, "Usage: /gate rm <spec>")
			return true
		}
		if err := g.store.DeleteGate(parts[1]); err != nil {
			g.sendMessage(chatJid, "Failed: "+err.Error())
			return true
		}
		g.sendMessage(chatJid, "gate removed: "+parts[1])

	case "enable":
		if len(parts) < 2 {
			g.sendMessage(chatJid, "Usage: /gate enable <spec>")
			return true
		}
		if err := g.store.EnableGate(parts[1], true); err != nil {
			g.sendMessage(chatJid, "Failed: "+err.Error())
			return true
		}
		g.sendMessage(chatJid, "gate enabled: "+parts[1])

	case "disable":
		if len(parts) < 2 {
			g.sendMessage(chatJid, "Usage: /gate disable <spec>")
			return true
		}
		if err := g.store.EnableGate(parts[1], false); err != nil {
			g.sendMessage(chatJid, "Failed: "+err.Error())
			return true
		}
		g.sendMessage(chatJid, "gate disabled: "+parts[1])

	default:
		g.sendMessage(chatJid,
			"Usage: /gate [list|add|rm|enable|disable]")
	}
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
