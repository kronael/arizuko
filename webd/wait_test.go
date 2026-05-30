package main

import (
	"context"
	"testing"
	"time"
)

// waitForAssistant returns the first assistant-role frame from the channel.
func TestWaitForAssistant_GetsFrame(t *testing.T) {
	ch := make(chan string, 4)
	ch <- "event: message\ndata: {\"role\":\"user\",\"content\":\"hi\"}\n\n"
	ch <- "event: message\ndata: {\"role\":\"assistant\",\"content\":\"hello\"}\n\n"

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m, ok := waitForAssistant(ctx, ch)
	if !ok {
		t.Fatal("waitForAssistant returned false, want true")
	}
	if m["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", m["role"])
	}
	if m["content"] != "hello" {
		t.Errorf("content = %v, want hello", m["content"])
	}
}

// waitForAssistant skips non-assistant frames and picks the first assistant one.
func TestWaitForAssistant_SkipsNonAssistant(t *testing.T) {
	ch := make(chan string, 4)
	// Status frames and user frames should be skipped.
	ch <- "event: message\ndata: {\"role\":\"user\"}\n\n"
	ch <- "event: status\ndata: {\"kind\":\"status\"}\n\n"
	ch <- ": ping\n\n" // comment line without data prefix
	ch <- "event: message\ndata: {\"role\":\"assistant\",\"id\":\"a1\"}\n\n"

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m, ok := waitForAssistant(ctx, ch)
	if !ok {
		t.Fatal("expected assistant frame")
	}
	if m["id"] != "a1" {
		t.Errorf("id = %v, want a1", m["id"])
	}
}

// waitForAssistant returns false when context is cancelled.
func TestWaitForAssistant_ContextCancel(t *testing.T) {
	ch := make(chan string) // no frames
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, ok := waitForAssistant(ctx, ch)
	if ok {
		t.Error("want false on cancelled context")
	}
}

// waitForAssistant returns false when channel is closed with no assistant frame.
func TestWaitForAssistant_ChannelClose(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "event: message\ndata: {\"role\":\"user\"}\n\n"
	close(ch)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, ok := waitForAssistant(ctx, ch)
	if ok {
		t.Error("want false when channel closed without assistant frame")
	}
}
