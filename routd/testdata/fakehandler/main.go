package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
)

type input struct {
	Prompt    string `json:"prompt"`
	ChatJID   string `json:"chatJid"`
	MessageID string `json:"messageId"`
}

func main() {
	var in input
	data, err := io.ReadAll(os.Stdin)
	if err == nil {
		err = json.Unmarshal(data, &in)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake: stdin:", err)
		os.Exit(78)
	}
	sock := os.Getenv("FAKE_MCP_SOCK")
	if sock == "" {
		sock = "/run/ipc/gated.sock"
	}
	reply := map[string]any{"chatJid": in.ChatJID, "text": "fake: " + in.Prompt}
	if err := call(sock, "tools/call", map[string]any{"name": "reply", "arguments": reply}); err != nil {
		fmt.Fprintln(os.Stderr, "fake: reply:", err)
		os.Exit(1)
	}
	if err := call(sock, "submit_turn", map[string]any{"turn_id": in.MessageID, "status": "success"}); err != nil {
		fmt.Fprintln(os.Stderr, "fake: submit_turn:", err)
		os.Exit(1)
	}
}

func call(sock, method string, params any) error {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return err
	}
	defer conn.Close()
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return err
	}
	var resp struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct{ Text string }
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s", resp.Error.Message)
	}
	if resp.Result.IsError && len(resp.Result.Content) > 0 {
		return fmt.Errorf("%s", resp.Result.Content[0].Text)
	}
	return nil
}
