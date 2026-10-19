package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestTurnHasBotReplyRejectsFailedDelivery(t *testing.T) {
	for _, status := range []string{core.MessageStatusFailed, core.MessageStatusPending, core.MessageStatusSent} {
		t.Run(status, func(t *testing.T) {
			s := openMem(t)
			defer s.Close()
			row := core.Message{ID: "attempt", ChatJID: "telegram:555", TurnID: "turn", BotMsg: true, Content: "answer", Timestamp: time.Now()}
			if err := s.PutOutbound(t.Context(), row, status); err != nil {
				t.Fatal(err)
			}
			if got := s.TurnHasBotReply(row.ChatJID, row.TurnID); got != (status != core.MessageStatusFailed) {
				t.Fatalf("status=%s satisfied turn=%t", status, got)
			}
			if status == core.MessageStatusFailed {
				row.ID = "recovery"
				row.Timestamp = row.Timestamp.Add(time.Second)
				if err := s.PutOutbound(t.Context(), row, core.MessageStatusSent); err != nil {
					t.Fatal(err)
				}
				if !s.TurnHasBotReply(row.ChatJID, row.TurnID) {
					t.Fatal("successful recovery after failed row did not satisfy turn")
				}
			}
		})
	}
}

func TestLastBotMessageExcludesProgressOnlyRows(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	const jid = "telegram:555"
	now := time.Now()
	for i, verb := range []string{verbStatus, VerbStatusSummary} {
		row := core.Message{ID: verb, ChatJID: jid, BotMsg: true,
			Verb: verb, Content: "work announcement", Timestamp: now.Add(time.Duration(i) * time.Second)}
		if err := s.PutOutbound(t.Context(), row, core.MessageStatusSent); err != nil {
			t.Fatal(err)
		}
	}
	if row, ok, err := s.LastBotMessage(t.Context(), jid); err != nil || ok {
		t.Fatalf("progress answered a waiting person: row=%+v exists=%v err=%v", row, ok, err)
	}
	answer := core.Message{ID: "answer", ChatJID: jid, BotMsg: true,
		Content: "the result", Timestamp: now.Add(-time.Second)}
	if err := s.PutOutbound(t.Context(), answer, core.MessageStatusSent); err != nil {
		t.Fatal(err)
	}
	if row, ok, err := s.LastBotMessage(t.Context(), jid); err != nil || !ok || row.ID != answer.ID {
		t.Fatalf("real answer lost behind progress: row=%+v exists=%v err=%v", row, ok, err)
	}
}
