package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLogIgnoredRetryEnv(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	logIgnoredRetryEnv()
	if logs.Len() != 0 {
		t.Fatalf("logged with no variable set: %s", logs.String())
	}
	t.Setenv("MAX_TURN_RETRY", "3")
	t.Setenv("MAX_OUTBOUND_RETRY", "-1")
	logIgnoredRetryEnv()
	for _, name := range []string{"MAX_TURN_RETRY", "MAX_OUTBOUND_RETRY"} {
		if !strings.Contains(logs.String(), "level=ERROR msg=\""+name+" is set and ignored") {
			t.Errorf("no error naming %s: %s", name, logs.String())
		}
	}
}
