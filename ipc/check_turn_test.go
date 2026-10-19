package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// The hidden method validates its turn and returns the bound handler's decision.
func TestHandleCheckTurn(t *testing.T) {
	for _, tt := range []struct {
		params    string
		wantCalls int
		wantCode  float64
	}{
		{`{}`, 0, -32602},
		{`{"turn_id":7}`, 0, -32602},
		{`{"turn_id":"t1"}`, 1, 0},
	} {
		calls := 0
		gated := GatedFns{CheckTurn: func(folder, id string) (TurnCheck, error) {
			calls++
			if folder != "demo" || id != "t1" {
				t.Fatalf("folder=%q id=%q", folder, id)
			}
			return TurnCheck{Owed: true, Nudge: "deliver"}, nil
		}}
		var got map[string]any
		handleCheckTurn([]byte(`{"params":`+tt.params+`}`), 1, gated, "demo", func(v any) {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
		})
		if calls != tt.wantCalls {
			t.Fatalf("calls=%d want=%d", calls, tt.wantCalls)
		}
		if tt.wantCode != 0 {
			if got["error"].(map[string]any)["code"] != tt.wantCode {
				t.Fatalf("response=%+v", got)
			}
		} else if got["result"].(map[string]any)["nudge"] != "deliver" {
			t.Fatalf("response=%+v", got)
		}
	}
}

// The socket dispatches check_turn directly and never advertises it as a tool.
func TestServeMCPCheckTurn(t *testing.T) {
	sock := t.TempDir() + "/gated.sock"
	stop, err := ServeMCP(sock, GatedFns{CheckTurn: func(folder, id string) (TurnCheck, error) {
		if folder != "demo" || id != "t1" {
			t.Fatalf("folder=%q id=%q", folder, id)
		}
		return TurnCheck{Owed: true, Nudge: "deliver"}, nil
	}}, StoreFns{}, "demo", false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(c)
	for _, method := range []string{"check_turn", "tools/list"} {
		if _, err := c.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"turn_id":"t1"}}` + "\n")); err != nil {
			t.Fatal(err)
		}
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if method == "check_turn" && !strings.Contains(line, `"nudge":"deliver"`) {
			t.Fatalf("check response=%s", line)
		}
		if method == "tools/list" && strings.Contains(line, "check_turn") {
			t.Fatalf("hidden method advertised: %s", line)
		}
	}
}
