package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
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
	case "new", "ping", "chatid", "stop", "status", "approve", "reject":
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
	case "/approve", "/reject":
		return g.cmdOnbod(msg.ChatJID, text)
	default:
		return false
	}
}

// cmdOnbod forwards /approve and /reject to the onbod daemon over HTTP.
// Returns true to suppress agent dispatch. When onboarding is disabled,
// returns false so the message falls through to the agent as before.
func (g *Gateway) cmdOnbod(chatJid, text string) bool {
	if !g.cfg.OnboardingEnabled {
		return false
	}
	url := os.Getenv("ONBOD_URL")
	if url == "" {
		url = "http://onbod:8092"
	}
	body, _ := json.Marshal(map[string]string{"jid": chatJid, "text": text})
	req, err := http.NewRequest("POST", url+"/send", bytes.NewReader(body))
	if err != nil {
		slog.Error("onbod dispatch build", "err", err)
		g.sendMessage(chatJid, "Failed: onbod dispatch error.")
		return true
	}
	req.Header.Set("Content-Type", "application/json")
	if g.cfg.ChannelSecret != "" {
		req.Header.Set("Authorization", "Bearer "+g.cfg.ChannelSecret)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Error("onbod dispatch failed", "url", url, "err", err)
		g.sendMessage(chatJid, "Failed: onbod unreachable.")
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true
	}
	b, _ := io.ReadAll(resp.Body)
	slog.Warn("onbod dispatch non-2xx",
		"status", resp.StatusCode, "body", strings.TrimSpace(string(b)))
	switch resp.StatusCode {
	case http.StatusForbidden:
		g.sendMessage(chatJid, "Permission denied.")
	case http.StatusNotFound:
		g.sendMessage(chatJid, "Onboarding request not found.")
	case http.StatusBadRequest:
		g.sendMessage(chatJid, "Failed: "+strings.TrimSpace(string(b)))
	default:
		g.sendMessage(chatJid, "Failed: onbod error.")
	}
	return true
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
