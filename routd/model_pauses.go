package routd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

var errModelInputOrder = errors.New("older model input pending")

func (l *Loop) oldestModelInput(folder string) (core.Message, error) {
	turns, err := l.db.RunningTurns()
	if err != nil {
		return core.Message{}, err
	}
	var oldest core.Message
	for _, tc := range turns {
		if tc.Folder != folder || tc.State != store.TurnPendingRetry {
			continue
		}
		msgs, err := l.db.store().DeferredMessages(context.Background(), tc.InputKeys)
		if err != nil {
			return core.Message{}, err
		}
		if len(msgs) > 0 && (oldest.ID == "" || modelInputBefore(msgs[0], oldest)) {
			oldest = msgs[0]
		}
	}
	jids, err := l.db.ChatJIDs()
	if err != nil {
		return core.Message{}, err
	}
	for _, jid := range jids {
		msgs, batch, _, err := l.pendingBatch(jid, l.db.GetAgentCursor(jid), pollBatch)
		if err != nil {
			return core.Message{}, err
		}
		for i, msg := range msgs {
			admitted := slices.ContainsFunc(turns, func(tc TurnContext) bool {
				return tc.State == store.TurnRunning && !tc.RunReturned && tc.ChatJID == jid && tc.InputLast >= store.MessageCursor(msg)
			})
			if !msg.BotMsg && !admitted && batch.rows[i].ok && batch.rows[i].Folder == folder &&
				(oldest.ID == "" || modelInputBefore(msg, oldest)) {
				oldest = msg
			}
		}
	}
	return oldest, nil
}

func modelInputBefore(a, b core.Message) bool {
	if !a.Timestamp.Equal(b.Timestamp) {
		return a.Timestamp.Before(b.Timestamp)
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	return a.ChatJID < b.ChatJID
}

func (l *Loop) heldInput(jid string, msgs []core.Message) ([]core.Message, *TurnContext, error) {
	turns, err := l.db.store().HeldTurns(context.Background(), jid)
	if err != nil {
		return nil, nil, err
	}
	var held *TurnContext
	for i := range turns {
		tc := &turns[i]
		if i := slices.IndexFunc(msgs, func(m core.Message) bool {
			return store.MessageCursor(m) == tc.InputLast
		}); i >= 0 {
			msgs = msgs[:i+1]
			held = tc
		}
	}
	return msgs, held, nil
}

func (l *Loop) pendingBatch(jid, cursor string, limit int) ([]core.Message, batchResolution, *TurnContext, error) {
	msgs, err := l.db.NewMessagesForChat(jid, cursor, limit)
	if err != nil {
		return nil, batchResolution{}, nil, err
	}
	msgs, held, err := l.heldInput(jid, msgs)
	if err != nil || newestUserIdx(msgs) < 0 {
		return msgs, batchResolution{}, held, err
	}
	if held == nil {
		return msgs, l.resolveBatch(jid, msgs), nil, nil
	}
	r := resolution{Folder: held.Folder, Addressed: held.Addressed, ok: true}
	b := batchResolution{resolution: r, rows: make([]resolution, len(msgs))}
	for i, m := range msgs {
		if !m.BotMsg && m.Sender == held.Trigger {
			b.rows[i] = r
		}
	}
	return msgs, b, held, nil
}

func (l *Loop) noticeModelPause(p *store.ModelPause, tc TurnContext) error {
	if p == nil {
		return nil
	}
	jid := returnTarget(tc, tc.ChatJID)
	at, err := time.Parse(time.RFC3339Nano, p.OpenedAt)
	if err != nil {
		return err
	}
	labels := map[string]string{"spend_limit": "provider spend limit", "rate_limit": "provider rate limit",
		"authentication": "provider authentication refused", "outage": "provider outage"}
	row := core.Message{
		ID:      fmt.Sprintf("model-pause-%x", sha256.Sum256([]byte(p.Folder+"\x00"+p.OpenedAt+"\x00"+jid))),
		ChatJID: jid, Timestamp: at, Sender: tc.Folder, BotMsg: true, FromMe: true,
		Topic: tc.Topic, RoutedTo: tc.Folder,
		Content: "Model access is paused: " + labels[p.Reason] +
			". Your messages are saved. I will retry automatically.",
	}
	created, err := l.db.store().PutOutboundOnce(context.Background(), row)
	if err != nil || !created || l.deliver == nil {
		return err
	}
	pid, err := l.deliver.Send(jid, row.Content, "", tc.Topic, "", row.ID)
	if err == nil {
		return l.db.store().MarkDelivered(context.Background(), row, pid)
	}
	slog.Warn("model pause notice queued for delivery", "jid", jid, "err", err)
	return nil
}

func (l *Loop) holdModelInput(chatJID string, trigger []core.Message) error {
	l.waiting.Delete(chatJID)
	l.busyWait.Delete(chatJID)
	if l.deliver != nil {
		clearPresence(l.deliver, []string{replyChat(chatJID, trigger[len(trigger)-1])})
	}
	p, err := l.db.store().ReadModelPause(context.Background(), l.folderForJid(chatJID))
	if err != nil || p == nil {
		return err
	}
	last := trigger[len(trigger)-1]
	return l.noticeModelPause(p, TurnContext{ChatJID: chatJID, ReturnTo: last.ForwardedFrom,
		Folder: l.folderForJid(chatJID), Topic: last.Topic})
}

func (l *Loop) stopModelProbe(tc TurnContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if tc.RunID != "" {
		killer, ok := l.runner.(RunKiller)
		if !ok {
			return fmt.Errorf("run recovery cannot kill the interrupted model probe")
		}
		err := killer.Kill(ctx, tc.RunID)
		var apiErr *runedv1.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound && apiErr.Code == "unknown_run" {
			return nil
		}
		return err
	}
	stopper, ok := l.runner.(RunStopper)
	if !ok {
		return fmt.Errorf("run recovery cannot stop the interrupted model probe")
	}
	_, err := stopper.StopFolder(ctx, tc.Folder)
	return err
}
