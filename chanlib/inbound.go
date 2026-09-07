package chanlib

// Durable inbound for adapters that cannot ask the platform for the message a
// second time.
//
// slakd answers Slack `200 OK` BEFORE it calls routd, because Slack requires
// the ack inside 3 seconds and redelivers on non-2xx — deferring the ack until
// routd accepts would turn one slow routd into several copies of the message.
// discd's gateway hands each event over exactly once. For both, a routd that is
// restarting (the documented `systemctl restart arizuko_<instance>` deploy
// restarts routd and the adapters together) used to lose the message with one
// ERROR line as the only trace (BUGS L25).
//
// teled, bskyd, reditd, linkd and emaid already hold the guarantee the other
// way round: each advances its platform cursor ONLY after routd accepts, so the
// platform IS their queue. They must NOT wire this — a second queue beside a
// working one is a second thing to drift.
//
// The retained row carries the adapter's own stable message id, which is the
// `messages` primary key routd stores, so re-delivering a row routd already
// took is an INSERT OR IGNORE no-op rather than a duplicate.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
)

const (
	// inboundSweepEvery is how often the sweep looks for due rows. Short
	// relative to the backoff below: the sweep is the clock, not the schedule.
	inboundSweepEvery = 15 * time.Second
	// inboundSweepBatch bounds one sweep pass so a long outage does not turn
	// the first tick after recovery into a thousand serial HTTP calls.
	inboundSweepBatch = 50
	// inboundBackoffBase/Max bound the per-row wait: 30s doubling to 15m.
	inboundBackoffBase = 30 * time.Second
	inboundBackoffMax  = 15 * time.Minute
)

// RetainInbound makes this client's inbound durable and starts the re-delivery
// sweep, which runs until ctx is done. st is the adapter's OWN storage (the one
// OpenAdapterDB returns); chanlib ships the entities, the adapter owns the file.
//
// Wire it ONLY from an adapter that consumes its platform event on arrival —
// see the file comment for why the cursor-gated adapters must not.
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

// PermanentIngress reports a routd answer no retry can repair: routd read the
// payload and refused it (400 malformed / missing field / jid-prefix or
// sender-scheme mismatch, 403 forbidden, 413 too large, 422 unprocessable).
// Everything else — a dial failure, a 401 while authd is restarting, a 5xx, a
// 429 — is the daemon being unavailable, which is exactly what retention is for.
func PermanentIngress(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code {
	case http.StatusBadRequest, http.StatusForbidden,
		http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// retainInbound holds msg so the sweep can re-deliver it. A permanent refusal is
// NOT retained — replaying it would fail identically forever — but it is logged
// at ERROR, because that message is now lost and an operator has to know.
func (r *RouterClient) retainInbound(msg InboundMsg, cause error) {
	st := r.pendingStore()
	if st == nil {
		return
	}
	if PermanentIngress(cause) {
		slog.Error("inbound refused by routd and dropped; a retry cannot repair it",
			"id", msg.ID, "chat_jid", msg.ChatJID, "err", cause)
		return
	}
	if msg.ID == "" {
		// Retention keys on the id routd stores. Without one, routd mints a
		// random id per attempt and a re-delivery would post the message twice,
		// so refusing to retain is the honest answer — and it is a wiring bug,
		// not a runtime condition: every adapter that calls RetainInbound sets
		// an id on every inbound.
		slog.Error("inbound has no id and cannot be retained; a re-delivery would duplicate it",
			"chat_jid", msg.ChatJID, "err", cause)
		return
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		slog.Error("inbound retain: encode failed", "id", msg.ID, "err", err)
		return
	}
	now := time.Now().UTC()
	// Retain leaves a message already held exactly as it is, so a platform that
	// re-delivers the same id (Slack retries a non-2xx ack) does not reset the
	// clock — the row keeps its attempt count and its backoff.
	if err := st.Retain(context.Background(), msg.ID, string(payload),
		rfc(now), rfc(now.Add(inboundBackoffBase)), cause.Error()); err != nil {
		slog.Error("inbound retain failed; the message is lost",
			"id", msg.ID, "chat_jid", msg.ChatJID, "err", err, "cause", cause)
		return
	}
	slog.Warn("inbound retained for re-delivery", "id", msg.ID, "chat_jid", msg.ChatJID, "err", cause)
}

func rfc(t time.Time) string { return core.TS(t) }

// inboundBackoff is the wait after `attempts` consecutive failures: 30s
// doubling to a 15m ceiling.
func inboundBackoff(attempts int) time.Duration {
	d := inboundBackoffBase
	for range attempts {
		d *= 2
		if d >= inboundBackoffMax {
			return inboundBackoffMax
		}
	}
	return d
}

func (r *RouterClient) sweepInbound(ctx context.Context) {
	t := time.NewTicker(inboundSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.SweepInboundOnce(ctx)
		}
	}
}

// SweepInboundOnce re-delivers every due message once. Exported so a test drives
// the sweep instead of waiting on the ticker.
func (r *RouterClient) SweepInboundOnce(ctx context.Context) {
	st := r.pendingStore()
	if st == nil {
		return
	}
	batch, err := st.DueForRedelivery(ctx, rfc(time.Now()), inboundSweepBatch)
	if err != nil {
		slog.Error("inbound sweep: read failed", "err", err)
		return
	}
	for _, d := range batch {
		if ctx.Err() != nil {
			return
		}
		var msg InboundMsg
		if err := json.Unmarshal([]byte(d.Payload), &msg); err != nil {
			// A held message we cannot decode can never be delivered; keeping it
			// would re-fail every 15s forever.
			slog.Error("inbound sweep: undecodable row dropped", "id", d.ID, "err", err)
			r.dropPending(ctx, st, d.ID)
			continue
		}
		err := r.postInbound(msg)
		switch {
		case err == nil:
			r.dropPending(ctx, st, d.ID)
			slog.Info("retained inbound delivered", "id", d.ID, "chat_jid", msg.ChatJID,
				"attempts", d.Attempts+1)
		case PermanentIngress(err):
			slog.Error("retained inbound refused by routd and dropped; a retry cannot repair it",
				"id", d.ID, "chat_jid", msg.ChatJID, "attempts", d.Attempts+1, "err", err)
			r.dropPending(ctx, st, d.ID)
		default:
			next := time.Now().Add(inboundBackoff(d.Attempts + 1))
			if uerr := st.Reschedule(ctx, d.ID, d.Attempts+1, rfc(next), err.Error()); uerr != nil {
				slog.Error("inbound sweep: backoff update failed", "id", d.ID, "err", uerr)
			}
			slog.Warn("retained inbound still undelivered", "id", d.ID,
				"chat_jid", msg.ChatJID, "attempts", d.Attempts+1, "retry_at", rfc(next), "err", err)
		}
	}
}

func (r *RouterClient) dropPending(ctx context.Context, st *chanstore.Store, id string) {
	if err := st.Drop(ctx, id); err != nil {
		slog.Error("inbound sweep: delete failed; the row will be re-delivered",
			"id", id, "err", err)
	}
}

// PendingInboundCount reports how many inbound messages the adapter is holding
// undelivered. /health reports it so the backlog is visible where an operator
// already looks, rather than only in a WARN line nobody reads.
func PendingInboundCount(ctx context.Context, st *chanstore.Store) (int, error) {
	if st == nil {
		return 0, nil
	}
	return st.HeldCount(ctx)
}
