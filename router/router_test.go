package router

import (
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
)

func TestFormatMessages(t *testing.T) {
	ts := time.Date(2025, 3, 7, 12, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{Sender: "alice", Name: "Alice", Content: "hello", Timestamp: ts},
		{Sender: "bob", Content: "world", Timestamp: ts.Add(time.Minute)},
	}
	got := FormatMessages(msgs)

	if !strings.Contains(got, "<messages>") {
		t.Fatal("should contain <messages> tag")
	}
	if !strings.Contains(got, `sender="Alice"`) {
		t.Fatal("should use Name when available")
	}
	if !strings.Contains(got, `sender="bob"`) {
		t.Fatal("should fall back to Sender when Name is empty")
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Fatal("should contain message content")
	}
}

func TestFormatMessagesEscape(t *testing.T) {
	msgs := []core.Message{
		{Sender: "a", Name: `"A&B"`, Content: "<script>", Timestamp: time.Now()},
	}
	got := FormatMessages(msgs)
	if strings.Contains(got, "<script>") {
		t.Fatal("should escape angle brackets")
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatal("should have escaped content")
	}
	if !strings.Contains(got, "&quot;A&amp;B&quot;") {
		t.Fatal("should escape name")
	}
}

func TestFormatOutbound(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"strips internal", "before<internal>secret</internal>after", "beforeafter"},
		{"multiline internal", "a<internal>\nfoo\nbar\n</internal>b", "ab"},
		{"trims whitespace", "  hello  ", "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatOutbound(tc.in)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsAuthorizedRoutingTarget(t *testing.T) {
	cases := []struct {
		name           string
		source, target string
		want           bool
	}{
		{"direct child", "main", "main/child", true},
		{"sibling tree", "main", "other/child", false},
		{"same level", "main", "main", false},
		{"grandchild", "main", "main/child/deep", false},
		{"child of nested", "main/child", "main/child/sub", true},
		{"different root", "foo", "bar/child", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAuthorizedRoutingTarget(tc.source, tc.target)
			if got != tc.want {
				t.Fatalf("IsAuthorizedRoutingTarget(%q, %q) = %v, want %v",
					tc.source, tc.target, got, tc.want)
			}
		})
	}
}

func TestStripThinkBlocks(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no blocks", "hello world", "hello world"},
		{"simple", "before<think>hidden</think>after", "beforeafter"},
		{"nested", "a<think>x<think>y</think>z</think>b", "ab"},
		{"unclosed hides rest", "before<think>rest", "before"},
		{"multiple", "a<think>1</think>b<think>2</think>c", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripThinkBlocks(tc.in)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractStatusBlocks(t *testing.T) {
	cleaned, statuses := ExtractStatusBlocks("before<status>working on it</status>after")
	if cleaned != "beforeafter" {
		t.Fatalf("cleaned = %q", cleaned)
	}
	if len(statuses) != 1 || statuses[0] != "working on it" {
		t.Fatalf("statuses = %v", statuses)
	}

	cleaned, statuses = ExtractStatusBlocks("no status here")
	if cleaned != "no status here" || len(statuses) != 0 {
		t.Fatal("should pass through")
	}

	cleaned, statuses = ExtractStatusBlocks("<status>  </status>text")
	if len(statuses) != 0 {
		t.Fatal("empty status should be filtered")
	}
	if strings.TrimSpace(cleaned) != "text" {
		t.Fatalf("cleaned = %q", cleaned)
	}
}

func TestSenderToUserFileID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"telegram:123456", "tg-123456"},
		{"whatsapp:5551234", "wa-5551234"},
		{"discord:789", "dc-789"},
		{"email:user@example.com", "em-user@example.com"},
		{"unknown:abc", "un-abc"},
	}
	for _, tc := range cases {
		got := SenderToUserFileID(tc.in)
		if got != tc.want {
			t.Fatalf("SenderToUserFileID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUserContextXml(t *testing.T) {
	got := UserContextXml("", "/tmp/group")
	if got != "" {
		t.Fatal("empty sender should return empty")
	}
	got = UserContextXml("system", "/tmp/group")
	if got != "" {
		t.Fatal("system sender should return empty")
	}
	got = UserContextXml("telegram:123", "/nonexistent/group")
	if !strings.Contains(got, `id="tg-123"`) {
		t.Fatalf("should contain user id, got %q", got)
	}
	if !strings.Contains(got, "<user ") {
		t.Fatalf("should be user tag, got %q", got)
	}
}

func TestFormatOutboundThinkAndStatus(t *testing.T) {
	got := FormatOutbound("text<think>hidden</think> more<status>s</status> end")
	if strings.Contains(got, "hidden") || strings.Contains(got, "<think>") {
		t.Fatal("should strip think blocks")
	}
	if strings.Contains(got, "<status>") {
		t.Fatal("should strip status blocks")
	}
	if !strings.Contains(got, "text") || !strings.Contains(got, "more") {
		t.Fatal("should keep surrounding text")
	}
}

func TestEscapeXml(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"<>&\"", "&lt;&gt;&amp;&quot;"},
		{"a&b<c>d", "a&amp;b&lt;c&gt;d"},
	}
	for _, tc := range cases {
		got := EscapeXml(tc.in)
		if got != tc.want {
			t.Fatalf("EscapeXml(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
