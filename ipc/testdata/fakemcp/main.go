// fakemcp is a one-file MCP stdio server used by ipc/connector_test.go.
// It speaks the minimum JSON-RPC subset: initialize, tools/list,
// tools/call. The tools/list response is a fixed catalog; tools/call
// echoes the env var named by GHACK_ENV back as the result text so
// tests can verify env injection + result scrubbing.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type rpc struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type resp struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *errO  `json:"error,omitempty"`
}

type errO struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	rdr := bufio.NewReader(os.Stdin)
	wr := os.Stdout
	for {
		line, err := rdr.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg rpc
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Method == "notifications/initialized" {
			continue
		}
		switch msg.Method {
		case "initialize":
			send(wr, resp{JSONRPC: "2.0", ID: msg.ID, Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fakemcp", "version": "1"},
			}})
		case "tools/list":
			send(wr, resp{JSONRPC: "2.0", ID: msg.ID, Result: map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo_env",
						"description": "Echo the value of the env var named by FAKEMCP_KEY.",
						"inputSchema": map[string]any{"type": "object"},
					},
				},
			}})
		case "tools/call":
			key := os.Getenv("FAKEMCP_KEY")
			val := os.Getenv(key)
			send(wr, resp{JSONRPC: "2.0", ID: msg.ID, Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": fmt.Sprintf("env[%s]=%s", key, val)},
				},
			}})
		default:
			send(wr, resp{JSONRPC: "2.0", ID: msg.ID, Error: &errO{Code: -32601, Message: "method not found"}})
		}
	}
}

func send(w *os.File, r resp) {
	b, _ := json.Marshal(r)
	w.Write(append(b, '\n'))
}
