package main

import (
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

func TestParseChatID(t *testing.T) {
	tests := []struct {
		jid  string
		want int64
	}{
		{"telegram:123456", 123456},
		{"telegram:-1001234567890", -1001234567890},
	}
	for _, tt := range tests {
		got, err := parseChatID(tt.jid)
		if err != nil {
			t.Errorf("parseChatID(%q) error: %v", tt.jid, err)
		}
		if got != tt.want {
			t.Errorf("parseChatID(%q) = %d, want %d", tt.jid, got, tt.want)
		}
	}
}

func TestMdToHTML(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"**bold**", "<b>bold</b>"},
		{"`code`", "<code>code</code>"},
		{"```\nblock\n```", "<pre>block\n</pre>"},
		{"# Header", "<b>Header</b>"},
	}
	for _, tt := range tests {
		if got := mdToHTML(tt.in); got != tt.want {
			t.Errorf("mdToHTML(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestChunk(t *testing.T) {
	c := chanlib.Chunk("abcdefgh", 3)
	if len(c) != 3 || c[0] != "abc" || c[1] != "def" || c[2] != "gh" {
		t.Errorf("chunk = %v", c)
	}
	if s := chanlib.Chunk("ab", 10); len(s) != 1 || s[0] != "ab" {
		t.Errorf("single = %v", s)
	}
	if s := chanlib.Chunk("", 10); len(s) != 1 || s[0] != "" {
		t.Errorf("empty = %v", s)
	}
}
