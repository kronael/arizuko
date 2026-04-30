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

	"github.com/onvos/arizuko/store"
)

// cmdSend posts a message to a group's slink endpoint and (optionally) waits
// for the agent's run to finish. Trivial wrapper over the slink round-handle
// protocol — see specs/1/W-slink.md.
//
// arizuko send <instance> <folder> "<msg>"           fire and forget
// arizuko send <instance> <folder> "<msg>" --wait    block on round_done
// arizuko send <instance> <folder> "<msg>" --stream  same, but SSE
// arizuko send <instance> <folder> --stdin           read body from stdin
//
// Server-side: reads the folder's slink_token directly from the instance
// store and posts to the local webd over the configured WEB_HOST.
func cmdSend(args []string) {
	if len(args) < 2 {
		die("usage: arizuko send <instance> <folder> [<message>] [--wait | --stream] [--stdin] [--steer <turn_id>]")
	}
	instance, folder := args[0], args[1]

	var msg, steer string
	wait, stream, stdin := false, false, false
	rest := args[2:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch a {
		case "--wait":
			wait = true
		case "--stream":
			stream = true
		case "--stdin":
			stdin = true
		case "--steer":
			i++
			if i >= len(rest) {
				die("usage: --steer <turn_id>")
			}
			steer = rest[i]
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
	st, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer st.Close()

	g, ok := st.GroupByFolder(folder)
	if !ok {
		die("Failed: group %q not found in instance %q", folder, instance)
	}
	if g.SlinkToken == "" {
		die("Failed: group %q has no slink_token", folder)
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
	endpoint := fmt.Sprintf("%s://%s/slink/%s", scheme, host, g.SlinkToken)
	if steer != "" {
		endpoint += "?steer=" + url.QueryEscape(steer)
	}

	body := strings.NewReader("content=" + url.QueryEscape(msg))
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
		User        map[string]any `json:"user"`
		TurnID      string         `json:"turn_id"`
		Status      string         `json:"status"`
		ChainedFrom string         `json:"chained_from"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&posted); err != nil {
		die("Failed: decode: %v", err)
	}

	fmt.Printf("turn_id: %s\n", posted.TurnID)
	if posted.ChainedFrom != "" {
		fmt.Printf("chained_from: %s\n", posted.ChainedFrom)
	}
	if !wait && !stream {
		fmt.Printf("status: %s\n", posted.Status)
		return
	}

	turnURL := fmt.Sprintf("%s://%s/slink/%s/turn/%s",
		scheme, host, g.SlinkToken, url.PathEscape(posted.TurnID))

	if stream {
		os.Exit(streamRound(client, turnURL+"/sse"))
	}
	os.Exit(pollRound(client, turnURL))
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

// pollRound polls /turn/<id>?after=<seq> at 1s cadence, prints frames as
// they arrive, exits 0 on done, 1 on failed.
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
