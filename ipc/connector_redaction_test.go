package ipc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestConnectorRedactsEscapedSecrets(t *testing.T) {
	for _, secret := range []string{"token\"value", "token\nvalue", "<token>&value", "token\\value"} {
		t.Run(secret, func(t *testing.T) {
			result := mcp.NewToolResultText("prefix " + secret + " suffix")
			result.StructuredContent = map[string]any{"nested": []any{map[string]any{"value": secret}}}
			got, err := scrubCallResult(result, map[string]string{"TOKEN": secret})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var values struct {
				Content    []struct{ Text string }        `json:"content"`
				Structured map[string][]map[string]string `json:"structuredContent"`
			}
			if err := json.Unmarshal(encoded, &values); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(values.Content[0].Text, secret) ||
				values.Structured["nested"][0]["value"] != "«redacted»" {
				t.Fatalf("secret returned: %s", encoded)
			}
		})
	}
}
