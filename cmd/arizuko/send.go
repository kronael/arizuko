package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// cmdSend posts a message to a group's chat-token endpoint and (optionally)
// waits for the agent's run to finish.
//
// arizuko send <instance> <folder> "<msg>"           fire and forget
// arizuko send <instance> <folder> "<msg>" --wait    block on round_done
// arizuko send <instance> <folder> "<msg>" --stream  same, but SSE
// arizuko send <instance> <folder> --stdin           read body from stdin
//
// Two paths:
//   - operator-direct (default): no chat token → inject straight into the DB
//     on web:<folder> (the root operator already holds DB authority, same as
//     `create`/`grant`/`secret`); the gateway poll loop runs the agent.
//   - chat-token: --token|-t or ARIZUKO_CHAT_TOKEN set → POST to the public
//     /chat/<token> endpoint via WEB_HOST (for tokens issued to non-operators).
func cmdSend(args []string) {
	if len(args) < 2 {
		die("usage: arizuko send <instance> <folder> [<message>] [--wait|-w | --stream|-S] [--stdin] [--from|-f <sender>] [--token|-t <raw>] [--topic|-T <topic>]")
	}
	instance, folder := args[0], args[1]

	var msg, topic, chatToken string
	sender := "operator"
	wait, stream, stdin := false, false, false
	chatToken = os.Getenv("ARIZUKO_CHAT_TOKEN")
	rest := args[2:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch a {
		case "--wait", "-w":
			wait = true
		case "--stream", "-S":
			stream = true
		case "--stdin":
			stdin = true
		case "--from", "-f":
			i++
			if i >= len(rest) {
				die("usage: --from|-f <sender>")
			}
			sender = rest[i]
		case "--token", "-t":
			i++
			if i >= len(rest) {
				die("usage: --token|-t <raw_token>")
			}
			chatToken = rest[i]
		case "--topic", "-T":
			i++
			if i >= len(rest) {
				die("usage: --topic|-T <topic>")
			}
			topic = rest[i]
		default:
			if msg != "" {
				die("usage: arizuko send <instance> <folder> <message>")
			}
			msg = a
		}
	}
	if stdin {
		buf, err := io.ReadAll(os.Stdin)
		if err != nil {
			die("Failed: read stdin: %v", err)
		}
		msg = strings.TrimSpace(string(buf))
	}
	if msg == "" {
		die("Failed: message required (positional or --stdin)")
	}

	dataDir := mustInstanceDir(instance)
	// Split: messages live in routd.db (routd reads ONLY that); monolith:
	// messages.db. mustOpenACL is the dual-path store opener (routd.db if present,
	// else messages.db) — the same path grant/secret/route use — so `send`
	// reaches the live router on both topologies instead of writing a DB the
	// split router never reads.
	st := mustOpenACL(dataDir)
	defer st.Close()

	if _, ok := st.GroupByFolder(folder); !ok {
		die("Failed: group %q not found in instance %q", folder, instance)
	}
	// No chat token → operator-direct inject (root owns the DB). Raw tokens are
	// never stored (only hash); the chat-token path is for non-operator callers
	// who hold a token from `arizuko token issue chat` / issue_chat_link.
	if chatToken == "" {
		operatorInject(st, folder, sender, msg, topic, wait || stream)
		return
	}

	cfg, err := loadInstanceEnv(dataDir)
	if err != nil {
		die("Failed: load .env: %v", err)
	}
	host := cfg["WEB_HOST"]
	if host == "" {
		die("Failed: WEB_HOST not configured for instance %q", instance)
	}
	scheme := "https"
	if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
		scheme = "http"
	}
	endpoint := fmt.Sprintf("%s://%s/chat/%s", scheme, host, chatToken)

	form := url.Values{}
	form.Set("content", msg)
	if topic != "" {
		form.Set("topic", topic)
	}
	body := strings.NewReader(form.Encode())
	req, _ := http.NewRequest("POST", endpoint, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		die("Failed: post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		die("Failed: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var posted struct {
		User   map[string]any `json:"user"`
		TurnID string         `json:"turn_id"`
		Status string         `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&posted); err != nil {
		die("Failed: decode: %v", err)
	}

	fmt.Printf("turn_id: %s\n", posted.TurnID)
	if !wait && !stream {
		fmt.Printf("status: %s\n", posted.Status)
		return
	}

	turnURL := fmt.Sprintf("%s://%s/chat/%s/%s",
		scheme, host, chatToken, url.PathEscape(posted.TurnID))

	if stream {
		os.Exit(streamRound(client, turnURL+"/sse"))
	}
	os.Exit(pollRound(client, turnURL))
}

// operatorInject writes the message straight into the instance DB as an inbound
// on web:<folder> — the 1:1 web-chat JID the gateway maps to the group
// (gateway.resolveGroup). The poll loop picks it up and runs the agent; no chat
// token because the operator already holds DB authority. When follow is set,
// polls BotRepliesSince and prints the agent's reply until the run goes quiet
// (a direct inject carries no round_done signal, so quiescence is the terminator).
func operatorInject(st *store.Store, folder, sender, msg, topic string, follow bool) {
	jid := "web:" + folder
	since := time.Now()
	m := core.Message{
		ID:        core.MsgID("cli"),
		ChatJID:   jid,
		Sender:    sender,
		Name:      sender,
		Content:   msg,
		Timestamp: since,
		Topic:     topic,
		Verb:      "mention",
		Source:    "cli",
	}
	if err := st.PutMessage(m); err != nil {
		die("Failed: inject: %v", err)
	}
	fmt.Printf("injected: %s → %s\n", m.ID, jid)
	if !follow {
		fmt.Println("status: queued (gateway poll loop will run the agent)")
		return
	}
	const (
		idleStop  = 120 * time.Second // give up waiting for the first reply (cold container start)
		quietStop = 15 * time.Second  // stop once replies stop arriving
		tick      = 1 * time.Second
	)
	start := time.Now()
	cursor := since
	gotReply := false
	lastPrinted := "" // dedup the out-/bot- pair: one agent reply is stored twice
	for {
		time.Sleep(tick)
		replies, err := st.BotRepliesSince(jid, cursor)
		if err != nil {
			die("Failed: poll replies: %v", err)
		}
		for _, r := range replies {
			if r.Content != "" && r.Content != lastPrinted {
				fmt.Println(r.Content)
				lastPrinted = r.Content
			}
			if r.Timestamp.After(cursor) {
				cursor = r.Timestamp
			}
			gotReply = true
		}
		switch {
		case gotReply && time.Since(cursor) > quietStop:
			return
		case !gotReply && time.Since(start) > idleStop:
			fmt.Fprintln(os.Stderr, "(no reply yet — agent may still be starting; re-run with --wait or check the chat)")
			os.Exit(2)
		}
	}
}

// streamRound subscribes to /sse and prints message frames as they arrive.
// Returns 0 on round_done success, 1 on failed status, 2 on transport error.
func streamRound(client *http.Client, sseURL string) int {
	req, _ := http.NewRequest("GET", sseURL, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: sse: %v\n", err)
		return 2
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Failed: sse %s: %s\n", resp.Status, raw)
		return 2
	}
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 64*1024), 1<<20)
	var event, data string
	for scan.Scan() {
		line := scan.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "":
			handled := handleSSEFrame(event, data)
			if handled >= 0 {
				return handled
			}
			event, data = "", ""
		}
	}
	return 2
}

func handleSSEFrame(event, data string) int {
	switch event {
	case "message", "status":
		var f struct{ Content, Kind string }
		if json.Unmarshal([]byte(data), &f) == nil {
			fmt.Println(f.Content)
		}
	case "round_done":
		var f struct{ Status, Error string }
		json.Unmarshal([]byte(data), &f)
		if f.Status == "success" {
			return 0
		}
		fmt.Fprintf(os.Stderr, "round failed: %s\n", f.Error)
		return 1
	}
	return -1
}

// pollRound polls /slink/<token>/<id>?after=<seq> at 1s cadence, prints
// frames as they arrive, exits 0 on done, 1 on failed.
func pollRound(client *http.Client, turnURL string) int {
	after := ""
	for {
		u := turnURL
		if after != "" {
			u += "?after=" + url.QueryEscape(after)
		}
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed: poll: %v\n", err)
			return 2
		}
		var snap struct {
			Status       string `json:"status"`
			LastFrameID  string `json:"last_frame_id"`
			Frames       []struct {
				Content, Kind string
			} `json:"frames"`
		}
		json.NewDecoder(resp.Body).Decode(&snap)
		resp.Body.Close()
		for _, f := range snap.Frames {
			fmt.Println(f.Content)
		}
		if snap.LastFrameID != "" {
			after = snap.LastFrameID
		}
		switch snap.Status {
		case "success":
			return 0
		case "failed":
			fmt.Fprintln(os.Stderr, "round failed")
			return 1
		}
		time.Sleep(1 * time.Second)
	}
}

// loadInstanceEnv parses .env in the instance dir into a key→value map.
// Only KEY=value lines are honored; quotes stripped, comments ignored.
func loadInstanceEnv(dataDir string) (map[string]string, error) {
	path := filepath.Join(dataDir, ".env")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, "\"'")
		out[k] = v
	}
	return out, scan.Err()
}
