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

func TestResolveRoutingTarget(t *testing.T) {
	rules := []core.RoutingRule{
		{Kind: "command", Match: "/code", Target: "main/code"},
		{Kind: "pattern", Match: `(?i)bug\s+\d+`, Target: "main/bugs"},
		{Kind: "keyword", Match: "deploy", Target: "main/ops"},
		{Kind: "sender", Match: "^Admin", Target: "main/admin"},
		{Kind: "default", Match: "", Target: "main/general"},
	}

	cases := []struct {
		name    string
		msg     core.Message
		want    string
	}{
		{"command exact", core.Message{Content: "/code"}, "main/code"},
		{"command with args", core.Message{Content: "/code fix bug"}, "main/code"},
		{"command no match", core.Message{Content: "/help"}, "main/general"},
		{"pattern match", core.Message{Content: "found bug 42"}, "main/bugs"},
		{"keyword match", core.Message{Content: "let's deploy now"}, "main/ops"},
		{"sender match", core.Message{Content: "hello", Name: "AdminUser"}, "main/admin"},
		{"default fallback", core.Message{Content: "random"}, "main/general"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRoutingTarget(tc.msg, rules)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveRoutingNoRules(t *testing.T) {
	got := ResolveRoutingTarget(core.Message{Content: "hello"}, nil)
	if got != "" {
		t.Fatalf("no rules should return empty, got %q", got)
	}
}

func TestResolveRoutingPriority(t *testing.T) {
	// command should win over keyword even if keyword also matches
	rules := []core.RoutingRule{
		{Kind: "keyword", Match: "code", Target: "keyword-target"},
		{Kind: "command", Match: "/code", Target: "command-target"},
	}
	got := ResolveRoutingTarget(core.Message{Content: "/code stuff"}, rules)
	if got != "command-target" {
		t.Fatalf("command should take priority, got %q", got)
	}
}

func TestResolveRoutingBadPattern(t *testing.T) {
	rules := []core.RoutingRule{
		{Kind: "pattern", Match: "[invalid", Target: "bad"},
		{Kind: "default", Match: "", Target: "fallback"},
	}
	got := ResolveRoutingTarget(core.Message{Content: "test"}, rules)
	if got != "fallback" {
		t.Fatalf("bad pattern should be skipped, got %q", got)
	}
}

func TestResolveRoutingLongPattern(t *testing.T) {
	rules := []core.RoutingRule{
		{Kind: "pattern", Match: strings.Repeat("a", 201), Target: "long"},
		{Kind: "default", Match: "", Target: "fallback"},
	}
	got := ResolveRoutingTarget(core.Message{Content: "test"}, rules)
	if got != "fallback" {
		t.Fatalf("long pattern should be skipped, got %q", got)
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
