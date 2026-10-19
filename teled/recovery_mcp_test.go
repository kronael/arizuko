package main

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/tests/testutils"
)

func TestNativeTelegramMissingTargetReachesMCPAndAllowsRecovery(t *testing.T) {
	for _, tool := range []string{"reply", "send"} {
		t.Run(tool, func(t *testing.T) {
			platform := testutils.NewFakePlatform()
			defer platform.Close()
			platform.On("POST /bottok/getMe", func(testutils.PlatformReq) (int, any) {
				return 200, map[string]any{"ok": true, "result": map[string]any{"id": 1, "is_bot": true, "username": "TestBot", "first_name": "Test"}}
			})
			platform.On("POST /bottok/sendMessage", func(req testutils.PlatformReq) (int, any) {
				form, err := url.ParseQuery(string(req.Body))
				if err != nil {
					t.Error(err)
				}
				if form.Get("chat_id") != "555" || form.Get("allow_sending_without_reply") == "true" {
					t.Error("adapter changed delivery target", form)
				}
				if form.Get("reply_to_message_id") == "999" {
					return 200, map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: message to be replied not found"}
				}
				return 200, map[string]any{"ok": true, "result": map[string]any{"message_id": 42, "chat": map[string]any{"id": 555}, "date": 1700000000}}
			})
			bot := newTeledBotForPlatform(t, platform)
			defer bot.typing.Stop()
			adapter := httptest.NewServer(newServer(config{Name: "telegram"}, bot, nil, func() bool { return true }, func() int64 { return 0 }).handler())
			defer adapter.Close()
			reg := chanreg.New()
			if _, err := reg.Register("telegram", adapter.URL, []string{"telegram:"}, map[string]bool{"send_text": true}); err != nil {
				t.Fatal(err)
			}
			deliver := routd.NewChannelDeliverer(reg, nil, nil)
			gated := ipc.GatedFns{
				SendReply: func(jid, text, replyTo, id string) (string, string, error) {
					pid, err := deliver.Send(jid, text, replyTo, "", "", id)
					return pid, "", err
				},
				SendMessage: func(jid, text, id string) (string, error) { return deliver.Send(jid, text, "", "", "", id) },
			}
			statuses := make(chan string, 2)
			fns := ipc.StoreFns{
				CurrentTurnID: func(string) string { return "turn" },
				PutOutbound: func(row core.Message, status string) error {
					if row.ChatJID != "telegram:555" || row.TurnID != "turn" {
						t.Error("MCP changed delivery context", row)
					}
					statuses <- status
					return nil
				},
			}
			socket := filepath.Join(t.TempDir(), "mcp.sock")
			stop, err := ipc.ServeMCP(socket, gated, fns, "demo", true, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := map[string]any{"chatJid": "telegram:555", "text": "answer", "replyToId": "999"}
			message := telegramRecoveryCall(t, socket, "reply", args)
			if !strings.Contains(message, "message to be replied not found") || !strings.Contains(message, "not delivered, and no retry follows") {
				t.Fatal("MCP lost permanent Telegram reason", message)
			}
			if status := <-statuses; status != core.MessageStatusFailed {
				t.Fatal("permanent refusal queued a retry", status)
			}
			args["replyToId"] = "55"
			if tool == "send" {
				delete(args, "replyToId")
			}
			if message := telegramRecoveryCall(t, socket, tool, args); message != "" {
				t.Fatal(message)
			}
			if status := <-statuses; status != core.MessageStatusSent {
				t.Fatal("recovery did not deliver", status)
			}
			var targets []string
			for _, request := range platform.Requests() {
				if strings.HasSuffix(request.Path, "/sendMessage") {
					form, err := url.ParseQuery(string(request.Body))
					if err != nil {
						t.Fatal(err)
					}
					targets = append(targets, form.Get("reply_to_message_id"))
				}
			}
			want := "55"
			if tool == "send" {
				want = ""
			}
			if len(targets) != 3 || targets[0] != "999" || targets[1] != "999" || targets[2] != want {
				t.Fatalf("HTML fallback or agent recovery changed targets: %+v", targets)
			}
		})
	}
}

func telegramRecoveryCall(t *testing.T, socket, tool string, args map[string]any) string {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Error != nil {
		t.Fatal(result.Error.Message)
	}
	if len(result.Result.Content) == 0 {
		t.Fatal("empty MCP response")
	}
	if result.Result.IsError {
		return result.Result.Content[0].Text
	}
	return ""
}
