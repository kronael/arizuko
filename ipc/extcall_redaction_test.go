package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestExtResponseRedaction(t *testing.T) {
	secret := "a\"b\\c"
	for _, body := range []string{`{"echo":"a\"b\\c","number":12345678901234567890}`, `{"ok":true} trailing a"b\c`} {
		t.Run(body, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, body)
			}))
			defer upstream.Close()
			result, err := CallExtTool(context.Background(), ExtTool{Method: "GET", BaseURL: upstream.URL, Path: "/"}, nil, map[string]string{"TOKEN": secret}, nil)
			if err != nil {
				t.Fatal(err)
			}
			text := result.Content[0].(mcp.TextContent).Text
			if json.Valid([]byte(body)) {
				var value map[string]any
				if err := json.Unmarshal([]byte(text), &value); err != nil {
					t.Fatal(err)
				}
				if value["echo"] != "«redacted»" || !strings.Contains(text, "12345678901234567890") {
					t.Errorf("response: %s", text)
				}
			} else if text != `{"ok":true} trailing «redacted»` {
				t.Errorf("response: %s", text)
			}
		})
	}
	for _, secret := range []string{`"`, `\`} {
		t.Run(secret, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"echo": secret})
			}))
			defer upstream.Close()
			result, err := CallExtTool(context.Background(), ExtTool{Method: "GET", BaseURL: upstream.URL, Path: "/"}, nil, map[string]string{"TOKEN": secret}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]string
			err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &value)
			if err != nil || value["echo"] != "«redacted»" {
				t.Errorf("response=%v error=%v", value, err)
			}
		})
	}
}
