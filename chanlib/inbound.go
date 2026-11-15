package chanlib

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/retry"
)

const inboundSweepBatch = 50

// inboundSweepEvery is a var so a test can shorten the tick.
var inboundSweepEvery = 15 * time.Second

// RetainInbound makes this client's inbound durable and starts the re-delivery
// sweep, which runs until ctx is done. st is the adapter's OWN storage. Wire it
// ONLY from an adapter that consumes its platform event on arrival: slakd acks
// Slack BEFORE calling routd and discd's gateway hands each event over exactly
// once, so a routd restart would lose the message (BUGS L25). teled and emaid
// advance their platform cursor ONLY after routd accepts, so the platform IS
// their queue and they must NOT wire this. The retained row carries the
// adapter's own stable message id, the `messages` primary key, so a re-delivery
// of a row routd already took is a no-op rather than a duplicate.
func (r *RouterClient) RetainInbound(ctx context.Context, st *chanstore.Store) {
	if st == nil {
		panic("chanlib.RetainInbound: the store must not be nil")
	}
	r.regMu.Lock()
	r.pending = st
	r.regMu.Unlock()
	go r.sweepInbound(ctx)
}

func (r *RouterClient) pendingStore() *chanstore.Store {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	return r.pending
}

// ErrInboundLost marks failed delivery without confirmed retention.
var ErrInboundLost = errors.New("inbound neither delivered nor retained")

var ErrInboundUnavailable = errors.New("inbound retention unavailable")

// sweepInbound ticks for the life of ctx. A store error never parks it: the row
// budgets live in the rows, so a stale tick costs one query (BUGS L885).
func (r *RouterClient) sweepInbound(ctx context.Context) {
	t := time.NewTicker(inboundSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.SweepInboundOnce(ctx); err != nil {
				slog.Error("retained inbound sweep failed", "err", err)
			}
		}
	}
}

// SweepInboundOnce returns discovery or persistence failures, not stored delivery failures.
func (r *RouterClient) SweepInboundOnce(ctx context.Context) error {
	st := r.pendingStore()
	if st == nil {
		return nil
	}
	batch, err := st.DueForRedelivery(ctx, core.TS(time.Now()), inboundSweepBatch)
	if err != nil {
		return err
	}
	for _, d := range batch {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var msg InboundMsg
		if err := json.Unmarshal([]byte(d.Payload), &msg); err != nil {
			decision := retry.Next(d.Attempts, retry.Failure{Kind: "permanent"}, time.Now(), time.Time{})
			if err := st.Reschedule(ctx, d, decision, retry.Notice(err.Error(), decision)); err != nil {
				return err
			}
			continue
		}
		err := r.postInbound(msg)
		switch err {
		case nil:
			if err := st.Drop(ctx, d); err != nil {
				return err
			}
			slog.Info("retained inbound delivered", "id", d.ID, "chat_jid", msg.ChatJID,
				"attempts", d.Attempts+1)
		default:
			now := time.Now()
			decision := retry.Next(d.Attempts, retry.FromError(err, now), now, time.Time{})
			notice := retry.Notice(err.Error(), decision)
			if uerr := st.Reschedule(ctx, d, decision, notice); uerr != nil {
				return uerr
			}
			slog.Warn(notice, "id", d.ID,
				"chat_jid", msg.ChatJID, "attempts", decision.Failures, "err", err)
		}
	}
	return nil
}

// PendingInboundCount reports how many inbound messages the adapter is holding
// undelivered; /health reports it so the backlog is visible where an operator
// already looks.
func PendingInboundCount(ctx context.Context, st *chanstore.Store) (int, error) {
	if st == nil {
		return 0, nil
	}
	return st.HeldCount(ctx)
}
