// L21: a failed MCP send must leave a durable row. internalSend used to return
// on the delivery error and never call recordOutbound, so the message vanished:
// PendingOutbound never saw it and nothing retried it.

package ipc

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/retry"
)

// outboundRow is one row the MCP layer wrote, with the delivery status it was
// written under. The status is not a field of the message: it decides whether a
// delivery item exists at all, so it travels beside the row.
type outboundRow struct {
	core.Message
	Status string
}

// captureStore records every row the MCP layer writes.
func captureStore(rows *[]outboundRow) StoreFns {
	return StoreFns{PutOutbound: func(m core.Message, status string) error {
		*rows = append(*rows, outboundRow{Message: m, Status: status})
		return nil
	}, PutOutboundFailure: func(m core.Message, failure retry.Failure) (retry.Decision, error) {
		decision := retry.Next(0, failure, time.Now(), time.Time{})
		status := core.MessageStatusPending
		if decision.Stop {
			status = core.MessageStatusFailed
		}
		*rows = append(*rows, outboundRow{Message: m, Status: status})
		return decision, nil
	}}
}

// TestSendTransientFailureRecordsPendingRow: the adapter fails with a transient
// error. The agent gets the error AND the row lands 'pending', so routd's sweep
// owns the retry.
func TestSendTransientFailureRecordsPendingRow(t *testing.T) {
	var rows []outboundRow
	routd := RoutdFns{
		SendMessage: func(string, string, string) (string, string, error) {
			return "", "", &retry.Error{Failure: retry.Failure{Kind: "transient"}, Err: fmt.Errorf("adapter down")}
		},
	}

	_, err := internalSend(routd, captureStore(&rows), "demo", "slack:T/C/U", "do not lose me", nil)
	if err == nil {
		t.Fatal("internalSend returned nil on a failed send — the agent must get the error")
	}
	if len(rows) != 1 {
		t.Fatalf("wrote %d rows, want 1 — a failed send must not disappear", len(rows))
	}
	if rows[0].Status != core.MessageStatusPending {
		t.Fatalf("status=%q want pending — a transient failure belongs to the sweep", rows[0].Status)
	}
	if rows[0].Content != "do not lose me" || !rows[0].BotMsg {
		t.Fatalf("row=%+v want the bot row with the original text", rows[0])
	}
	if rows[0].PlatformID != "" {
		t.Fatalf("platform_id=%q want empty — the platform accepted nothing", rows[0].PlatformID)
	}
}

// TestSendPermanentFailureDeadLetters: a 4xx refusal cannot be repaired by a
// retry, so the row goes straight to the dead-letter state and never enters
// PendingOutbound.
func TestSendPermanentFailureDeadLetters(t *testing.T) {
	var rows []outboundRow
	routd := RoutdFns{
		SendMessage: func(string, string, string) (string, string, error) {
			return "", "", fmt.Errorf("channel slack send: %w: status 400", chanlib.ErrPermanent)
		},
	}

	_, err := internalSend(routd, captureStore(&rows), "demo", "slack:T/C/U", "the chat is gone", nil)
	if err == nil {
		t.Fatal("internalSend returned nil on a permanent refusal")
	}
	if len(rows) != 1 {
		t.Fatalf("wrote %d rows, want 1", len(rows))
	}
	if rows[0].Status != core.MessageStatusFailed {
		t.Fatalf("status=%q want failed — a permanent refusal must dead-letter, never retry", rows[0].Status)
	}
}

// TestSendFileFailureDeadLetters: the text-only sweep would re-send a pending
// file row as a captionless, fileless message, so a failed send_file always
// dead-letters — even on a transient error.
func TestSendFileFailureDeadLetters(t *testing.T) {
	var rows []outboundRow
	routd := RoutdFns{
		SendDocument: func(string, string, string, string, string, string, string) (string, string, error) {
			return "", "", fmt.Errorf("adapter down")
		},
	}

	_, err := internalSend(routd, captureStore(&rows), "demo", "slack:T/C/U", "the report",
		[]internalSendFile{{LocalPath: "/srv/x.pdf", Filename: "x.pdf", ReplyTo: "p1"}})
	if err == nil {
		t.Fatal("internalSend returned nil on a failed file send")
	}
	if len(rows) != 1 {
		t.Fatalf("wrote %d rows, want 1", len(rows))
	}
	if rows[0].Status != core.MessageStatusFailed {
		t.Fatalf("status=%q want failed — the sweep must never re-send a file row as text", rows[0].Status)
	}
	if rows[0].ReplyToID != "p1" {
		t.Fatalf("reply_to_id=%q want p1 — the row keeps its thread target", rows[0].ReplyToID)
	}
}

// TestSendSuccessRecordsSentRow is the control: a delivered send still writes
// exactly one 'sent' row carrying its own platform id.
func TestSendSuccessRecordsSentRow(t *testing.T) {
	var rows []outboundRow
	routd := RoutdFns{
		SendMessage: func(string, string, string) (string, string, error) { return "pid-7", "", nil },
	}

	id, err := internalSend(routd, captureStore(&rows), "demo", "slack:T/C/U", "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "pid-7" {
		t.Fatalf("id=%q want pid-7 — the caller must get the platform id back", id)
	}
	if len(rows) != 1 || rows[0].Status != core.MessageStatusSent || rows[0].PlatformID != "pid-7" {
		t.Fatalf("rows=%+v want one sent row with platform_id pid-7", rows)
	}
}

func TestFailureNoticeRequiresPersistedDecision(t *testing.T) {
	for _, tc := range []struct {
		name         string
		decision     retry.Decision
		saveErr      error
		want, reject string
	}{
		{name: "queued", decision: retry.Decision{Failures: 1, NextAt: time.Now()}, want: "router will resend"},
		{name: "legacy pending", decision: retry.Decision{NextAt: time.Now()}, want: "router will resend"},
		{name: "sent duplicate", reject: "router will resend"},
		{name: "store failure", saveErr: fmt.Errorf("store down"), want: "store down", reject: "router will resend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := StoreFns{PutOutboundFailure: func(core.Message, retry.Failure) (retry.Decision, error) { return tc.decision, tc.saveErr }}
			err := recordOutboundFailure(RoutdFns{}, db, "chat", "text", "folder", "", "root", "id", "reply", true, &retry.Error{Failure: retry.Failure{Kind: "transient"}, Err: fmt.Errorf("failure")})
			if err == nil || tc.want != "" && !strings.Contains(err.Error(), tc.want) || tc.reject != "" && strings.Contains(err.Error(), tc.reject) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestUnknownFailureStops(t *testing.T) {
	var rows []outboundRow
	_, err := internalSend(RoutdFns{SendMessage: func(string, string, string) (string, string, error) { return "", "root", fmt.Errorf("unknown outcome") }}, captureStore(&rows), "folder", "chat", "text", nil)
	if err == nil || len(rows) != 1 || rows[0].Status != core.MessageStatusFailed || rows[0].Topic != "root" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestMCPFailureReportsCommittedOwnership(t *testing.T) {
	for _, tc := range []struct {
		name         string
		saveErr      error
		want, reject string
	}{
		{name: "queued", want: "router will resend"},
		{name: "store failure", saveErr: fmt.Errorf("store unavailable"), want: "store unavailable", reject: "router will resend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := StoreFns{PutOutboundFailure: func(_ core.Message, failure retry.Failure) (retry.Decision, error) {
				return retry.Next(0, failure, time.Now(), time.Time{}), tc.saveErr
			}}
			routd := RoutdFns{SendMessage: func(string, string, string) (string, string, error) {
				return "", "root", &retry.Error{Failure: retry.Failure{Kind: "transient"}, Err: fmt.Errorf("unavailable")}
			}}
			sock := t.TempDir() + "/mcp.sock"
			stop, err := ServeMCP(sock, routd, db, "demo", true, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			_, msg := callTool(t, sock, "send", map[string]any{"chatJid": "slack:channel", "text": "hello"})
			if !strings.Contains(msg, tc.want) || tc.reject != "" && strings.Contains(msg, tc.reject) {
				t.Fatalf("error=%q", msg)
			}
		})
	}
}

func TestMissingFailureCallbackDoesNotClaimOwnership(t *testing.T) {
	err := recordOutboundFailure(RoutdFns{}, StoreFns{}, "chat", "text", "demo", "", "", "id", "", true, &retry.Error{Failure: retry.Failure{Kind: "transient"}, Err: fmt.Errorf("send failed")})
	if err == nil || !strings.Contains(err.Error(), "not configured") || strings.Contains(err.Error(), "router will resend") {
		t.Fatalf("error=%v", err)
	}
}
