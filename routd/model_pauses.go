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

func (l *Loop) modelAdmission(folder string, candidate core.Message) (*store.ModelPause, error) {
	p, err := l.db.store().ReadModelPause(context.Background(), folder)
	if err != nil || (p != nil && (p.NextAttemptAt == "" || p.NextAttemptAt > core.NowTS())) {
		return nil, err
	}
	oldest, err := l.oldestModelInput(folder)
	if err != nil {
		return nil, err
	}
	if p != nil && (oldest.ID == "" || oldest.ID != candidate.ID ||
		oldest.ChatJID != candidate.ChatJID || !oldest.Timestamp.Equal(candidate.Timestamp)) {
		return nil, store.ErrModelPaused
	}
	if p == nil && oldest.ID != "" && modelInputBefore(oldest, candidate) {
		return nil, errModelInputOrder
	}
	return p, nil
}

func (l *Loop) oldestModelInput(folder string) (core.Message, error) {
	running, err := l.db.RunningTurns()
	if err != nil {
		return core.Message{}, err
	}
	jids, err := l.db.ChatJIDs()
	if err != nil {
		return core.Message{}, err
	}
	var oldest core.Message
	for _, jid := range jids {
		msgs, batch, _, err := l.pendingBatch(jid, l.db.GetAgentCursor(jid), pollBatch)
		if err != nil {
			return core.Message{}, err
		}
		if newestUserIdx(msgs) < 0 {
			continue
		}
		if !batch.ok || batch.Folder != folder {
			continue
		}
		for i, m := range msgs {
			admitted := slices.ContainsFunc(running, func(tc TurnContext) bool {
				return tc.State == store.TurnRunning && !tc.RunReturned &&
					tc.ChatJID == jid && tc.InputLast >= store.MessageCursor(m)
			})
			if admitted {
				continue
			}
			if !m.BotMsg && batch.rows[i].ok && (oldest.ID == "" || modelInputBefore(m, oldest)) {
				oldest = m
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

func (l *Loop) recoverModelProbe(startup bool) error {
	pauses, err := l.db.store().ListModelPauses(context.Background())
	if err != nil {
		return err
	}
	for i := range pauses {
		if err := l.recoverFolderModelProbe(&pauses[i], startup); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) recoverFolderModelProbe(p *store.ModelPause, startup bool) error {
	if p.ProbeTurnID == "" {
		return nil
	}
	_, err := l.q.WithIdleFolder(p.Folder, func() error {
		return l.recoverIdleModelProbe(p, startup)
	})
	return err
}

func (l *Loop) recoverIdleModelProbe(p *store.ModelPause, startup bool) error {
	current, err := l.db.store().ReadModelPause(context.Background(), p.Folder)
	if err != nil || current == nil {
		return err
	}
	if current.OpenedAt != p.OpenedAt || current.ProbeTurnID != p.ProbeTurnID || current.ProbeAttemptID != p.ProbeAttemptID {
		return nil
	}
	tc, ok := l.db.GetTurnContext(p.ProbeTurnID)
	if !ok {
		return fmt.Errorf("paused probe has no turn %s", p.ProbeTurnID)
	}
	if tc.Folder != p.Folder || tc.ProbeEpisode != p.OpenedAt || tc.AttemptID != p.ProbeAttemptID {
		return nil
	}
	if !startup && !tc.RunReturned {
		return nil
	}
	if err := l.db.SetRunReturned(tc.TurnID); err != nil {
		return err
	}
	if err := l.stopModelProbe(tc); err != nil {
		return err
	}
	if tc.State == store.TurnDone && l.db.TurnResultRecorded(tc.Folder, tc.TurnID) {
		return l.db.store().CompleteModelProbe(context.Background(), tc)
	}
	return l.db.store().RearmModelProbe(context.Background(), *p, time.Now())
}

func (l *Loop) stopModelProbe(tc TurnContext) error {
	if tc.RunID != "" {
		killer, ok := l.runner.(RunKiller)
		if !ok {
			return fmt.Errorf("run recovery cannot kill the interrupted model probe")
		}
		err := killer.Kill(context.Background(), tc.RunID)
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
	_, err := stopper.StopFolder(context.Background(), tc.Folder)
	return err
}

func (l *Loop) requeueModelProbe(p *store.ModelPause, tc TurnContext, mayBeRunning bool) {
	if p == nil || tc.ProbeEpisode == "" {
		return
	}
	current, err := l.db.store().ReadModelPause(context.Background(), p.Folder)
	if err != nil || current == nil || current.OpenedAt != p.OpenedAt ||
		current.ProbeTurnID != tc.TurnID || current.ProbeAttemptID != tc.AttemptID {
		return
	}
	bound, ok := l.db.GetTurnContext(tc.TurnID)
	if !ok || bound.Folder != tc.Folder || bound.ProbeEpisode != tc.ProbeEpisode || bound.AttemptID != tc.AttemptID {
		return
	}
	if mayBeRunning {
		if err := l.db.SetRunReturned(tc.TurnID); err != nil {
			slog.Error("close interrupted model probe", "err", err)
			return
		}
		if err := l.stopModelProbe(tc); err != nil {
			slog.Error("stop interrupted model probe", "err", err)
			return
		}
	}
	if err := l.db.store().RearmModelProbe(context.Background(), *p, time.Now()); err != nil {
		slog.Error("requeue model probe", "err", err)
	}
}
