package main

import (
	"bufio"
	"context"
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

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

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
	if chatToken == "" {
		operatorInject(st, dataDir, folder, sender, msg, topic, wait || stream)
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

	os.Exit(streamRound(client, turnURL+"/sse"))
}

func operatorInject(st *store.Store, dataDir, folder, sender, msg, topic string, follow bool) {
	jid := groupfolder.WebJID(folder)
	since := time.Now()
	env, err := loadInstanceEnv(dataDir)
	if err != nil {
		die("Failed: load .env: %v", err)
	}
	if env["API_PORT"] == "" {
		die("Failed: API_PORT not configured in %s/.env", dataDir)
	}
	token, _, err := mintBearer(folder, sender,
		[]string{"messages:write"}, time.Minute)
	if err != nil {
		die("Failed: mint bearer: %v", err)
	}
	routd := chanlib.NewRouterClient("http://localhost:" + env["API_PORT"])
	routd.SetServiceToken(func(context.Context) (string, error) { return token, nil })
	m := chanlib.InboundMsg{ID: core.MsgID("cli"), ChatJID: jid, Sender: sender,
		SenderName: sender, Content: msg, Topic: topic, Verb: "mention", Source: "cli"}
	if err := routd.SendMessage(m); err != nil {
		die("Failed: inject: %v", err)
	}
	fmt.Printf("injected: %s → %s\n", m.ID, jid)
	if !follow {
		fmt.Println("status: queued")
		return
	}
	const (
		idleStop  = 120 * time.Second
		quietStop = 15 * time.Second
		tick      = 1 * time.Second
	)
	start := time.Now()
	cursor := since
	gotReply := false
	var lastReply time.Time
	lastPrinted := ""
	for {
		time.Sleep(tick)
		replies, err := st.BotRepliesSince(jid, cursor, 0)
		if err != nil {
			die("Failed: poll replies: %v", err)
		}
		for _, r := range replies {
			if r.Timestamp.After(cursor) {
				cursor = r.Timestamp
			}
			if r.Topic != topic {
				continue
			}
			if r.Content != "" && r.Content != lastPrinted {
				fmt.Println(r.Content)
				lastPrinted = r.Content
			}
			lastReply = r.Timestamp
			gotReply = true
		}
		switch {
		case gotReply && time.Since(lastReply) > quietStop:
			return
		case !gotReply && time.Since(start) > idleStop:
			fmt.Fprintln(os.Stderr, "(no reply yet — agent may still be starting; re-run with --wait or check the chat)")
			os.Exit(2)
		}
	}
}

func streamRound(client *http.Client, sseURL string) int {
	streamClient := *client
	streamClient.Timeout = 0
	resp, code := openSSE(&streamClient, sseURL, nil)
	if resp == nil {
		return code
	}
	defer func() { _ = resp.Body.Close() }()
	return readSSE(resp.Body)
}

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
			if f.Role == "user" {
				return -1
			}
			fmt.Println(f.Content)
		}
	case "round_done":
		var f struct{ Status, Error string }
		if err := json.Unmarshal([]byte(data), &f); err != nil {
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

func loadInstanceEnv(dataDir string) (map[string]string, error) {
	return godotenv.Read(filepath.Join(dataDir, ".env"))
}
