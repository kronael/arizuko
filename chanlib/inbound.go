package chanlib

// Durable inbound for adapters that cannot ask the platform for the message a
// second time (slakd acks Slack BEFORE calling routd, discd's gateway hands
// each event over exactly once), so a routd restart does not lose it (BUGS
// L25). teled and emaid advance their platform cursor
// ONLY after routd accepts, so the platform IS their queue — they must NOT
// wire this. The retained row carries the adapter's own stable message id,
// the `messages` primary key, so a re-delivery of a row routd already took
// is a no-op rather than a duplicate.

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
	// The sweep is the clock, not the schedule: short relative to the backoff.
	inboundSweepEvery = 15 * time.Second
	// Bounds one pass so the first tick after a long outage is not a thousand
	// serial HTTP calls.
	inboundSweepBatch = 50
	// The per-row wait: 30s doubling to 15m.
	inboundBackoffBase = 30 * time.Second
	inboundBackoffMax  = 15 * time.Minute
)

// RetainInbound makes this client's inbound durable and starts the re-delivery
// sweep, which runs until ctx is done. st is the adapter's OWN storage. Wire it
// ONLY from an adapter that consumes its platform event on arrival.
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
// payload and refused it (400, 403, 413, 422). Everything else — a dial
// failure, a 401 while authd restarts, a 5xx, a 429 — is the daemon being
// unavailable, which is what retention is for.
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

// ErrInboundLost marks an inbound neither routd nor the retention store took.
// Nothing holds the message any more, so an adapter whose HTTP response IS the
// platform's acknowledgement must answer a failure and leave the platform's
// own redelivery meaningful.
var ErrInboundLost = errors.New("inbound neither delivered nor retained")

// retainInbound holds msg so the sweep can re-deliver it, and reports whether
// anything still holds it. Without a retention store the adapter's platform
// cursor does, so there is nothing to answer for here. A permanent refusal is
// NOT retained — replaying it would fail identically forever — but logged at
// ERROR, because that message is now lost.
func (r *RouterClient) retainInbound(msg InboundMsg, cause error) (held bool) {
	st := r.pendingStore()
	if st == nil {
		return true
	}
	if PermanentIngress(cause) {
		slog.Error("inbound refused by routd and dropped; a retry cannot repair it",
			"id", msg.ID, "chat_jid", msg.ChatJID, "err", cause)
		return false
	}
	if msg.ID == "" {
		// Retention keys on the id routd stores; without one a re-delivery
		// would post the message twice. A wiring bug, not a runtime condition.
		slog.Error("inbound has no id and cannot be retained; a re-delivery would duplicate it",
			"chat_jid", msg.ChatJID, "err", cause)
		return false
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		slog.Error("inbound retain: encode failed", "id", msg.ID, "err", err)
		return false
	}
	now := time.Now().UTC()
	// Retain leaves a message already held as it is, so a platform that
	// re-delivers the same id does not reset the attempt count or the backoff.
	if err := st.Retain(context.Background(), msg.ID, string(payload),
		rfc(now), rfc(now.Add(inboundBackoffBase)), cause.Error()); err != nil {
		slog.Error("inbound retain failed; the message is lost",
			"id", msg.ID, "chat_jid", msg.ChatJID, "err", err, "cause", cause)
		return false
	}
	slog.Warn("inbound retained for re-delivery", "id", msg.ID, "chat_jid", msg.ChatJID, "err", cause)
	return true
}

func rfc(t time.Time) string { return core.TS(t) }

// inboundBackoff is the wait after `attempts` consecutive failures.
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

// SweepInboundOnce re-delivers every due message once. Exported so a test
// drives the sweep instead of waiting on the ticker.
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
			// Undecodable can never be delivered; keeping it would re-fail forever.
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
// undelivered; /health reports it so the backlog is visible where an operator
// already looks.
func PendingInboundCount(ctx context.Context, st *chanstore.Store) (int, error) {
	if st == nil {
		return 0, nil
	}
	return st.HeldCount(ctx)
}
