package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
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
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	var wait, stream, stdin bool
	var sender, chatToken, topic string
	fs.BoolVar(&wait, "wait", false, "block until the round is done")
	fs.BoolVar(&wait, "w", false, "block until the round is done")
	fs.BoolVar(&stream, "stream", false, "stream the round")
	fs.BoolVar(&stream, "S", false, "stream the round")
	fs.BoolVar(&stdin, "stdin", false, "read the message from stdin")
	fs.StringVar(&sender, "from", "operator", "sender")
	fs.StringVar(&chatToken, "token", os.Getenv("ARIZUKO_CHAT_TOKEN"), "raw chat token")
	fs.StringVar(&chatToken, "t", os.Getenv("ARIZUKO_CHAT_TOKEN"), "raw chat token")
	fs.StringVar(&topic, "topic", "", "topic")
	fs.StringVar(&topic, "T", "", "topic")
	if err := flexParse(fs, args); err != nil || fs.NArg() < 2 || fs.NArg() > 3 {
		usage(err, "arizuko send [-i instance] <folder> [<message>] [--wait|-w | --stream|-S] [--stdin] [--from <sender>] [--token|-t <raw>] [--topic|-T <topic>]")
	}
	instance, folder, msg := fs.Arg(0), fs.Arg(1), fs.Arg(2)
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
	st := mustOpen(store.OwnerRoutd)
	defer func() { _ = st.Close() }()

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
	base := core.PublicBaseURL(cfg["WEB_HOST"])
	if base == "" {
		die("Failed: WEB_HOST not configured for instance %q", instance)
	}
	endpoint := fmt.Sprintf("%s/chat/%s", base, chatToken)

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
	defer func() { _ = resp.Body.Close() }()
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

	turnURL := fmt.Sprintf("%s/chat/%s/%s", base, chatToken, url.PathEscape(posted.TurnID))

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
	jid := groupfolder.WebJID(folder)
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
		quietStop = 15 * time.Second
		tick      = 1 * time.Second
	)
	start := time.Now()
	cursor := since
	gotReply := false
	lastPrinted := "" // dedup the out-/bot- pair: one agent reply is stored twice
	for {
		time.Sleep(tick)
		replies, err := st.BotRepliesSince(jid, cursor, 0)
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
	resp, code := openSSE(client, sseURL, nil)
	if resp == nil {
		return code
	}
	defer func() { _ = resp.Body.Close() }()
	return readSSE(resp.Body)
}

// openSSE opens an event stream, or reports the transport exit code. Split from
// readSSE for `arizuko talk`, which must SUBSCRIBE BEFORE IT SENDS: the /me
// stream it reads replays nothing, so a reply landing between the send and the
// subscribe is lost and the round never ends. `send --stream` reads a turn
// stream that does replay, so it opens and reads in one step.
func openSSE(client *http.Client, sseURL string, hdr map[string]string) (*http.Response, int) {
	req, _ := http.NewRequest("GET", sseURL, nil)
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: sse: %v\n", err)
		return nil, 2
	}
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		fmt.Fprintf(os.Stderr, "Failed: sse %s: %s\n", resp.Status, raw)
		return nil, 2
	}
	return resp, 0
}

// readSSE prints frames off an open event stream until round_done. The single
// reader both `send --stream` and `talk` go through — one parser, one printer.
func readSSE(body io.Reader) int {
	scan := bufio.NewScanner(body)
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
		var f struct{ Content, Kind, Role string }
		if json.Unmarshal([]byte(data), &f) == nil {
			// webd's /mcp `send` echoes the caller's own message onto the same
			// hub topic as role=user, so `talk` would quote the operator back at
			// themselves. Turn-stream frames (webd/turn.go) carry no role field,
			// so this skips nothing on the `send --stream` path.
			if f.Role == "user" {
				return -1
			}
			fmt.Println(f.Content)
		}
	case "round_done":
		var f struct{ Status, Error string }
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			// The round is over either way; without the payload the parse failure
			// IS the reason, and an empty one reads as a round that failed silently.
			f.Error = "unreadable round_done payload: " + err.Error()
		}
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
			Status      string `json:"status"`
			LastFrameID string `json:"last_frame_id"`
			Frames      []struct {
				Content, Kind string
			} `json:"frames"`
		}
		derr := json.NewDecoder(resp.Body).Decode(&snap)
		_ = resp.Body.Close()
		if derr != nil {
			// A snapshot that will not parse has no status, and the loop would poll
			// on for it forever.
			fmt.Fprintf(os.Stderr, "Failed: poll decode: %v\n", derr)
			return 2
		}
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

func loadInstanceEnv(dataDir string) (map[string]string, error) {
	return godotenv.Read(filepath.Join(dataDir, ".env"))
}
