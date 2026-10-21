// L21: a failed MCP send must leave a durable row. internalSend used to return
// on the delivery error and never call recordOutbound, so the message vanished:
// PendingOutbound never saw it and nothing retried it.

package ipc

import (
	"fmt"
	"testing"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
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
	}}
}

// TestSendTransientFailureRecordsPendingRow: the adapter fails with a transient
// error. The agent gets the error AND the row lands 'pending', so routd's sweep
// owns the retry.
func TestSendTransientFailureRecordsPendingRow(t *testing.T) {
	var rows []outboundRow
	routd := RoutdFns{
		SendMessage: func(string, string, string) (string, string, error) { return "", "", fmt.Errorf("adapter down") },
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
