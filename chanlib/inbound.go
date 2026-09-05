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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
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
// sweep, which runs until ctx is done. db is the adapter's OWN database (the
// one OpenAdapterDB returns); chanlib ships the schema, the adapter owns the
// file.
//
// Wire it ONLY from an adapter that consumes its platform event on arrival —
// see the file comment for why the cursor-gated adapters must not.
func (r *RouterClient) RetainInbound(ctx context.Context, db *sql.DB) {
	if db == nil {
		panic("chanlib.RetainInbound: db must not be nil")
	}
	r.regMu.Lock()
	r.pending = db
	r.regMu.Unlock()
	go r.sweepInbound(ctx)
}

func (r *RouterClient) pendingDB() *sql.DB {
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

// retainInbound writes msg to inbound_pending so the sweep can re-deliver it.
// A permanent refusal is NOT retained — replaying it would fail identically
// forever — but it is logged at ERROR, because that message is now lost and an
// operator has to know.
func (r *RouterClient) retainInbound(msg InboundMsg, cause error) {
	db := r.pendingDB()
	if db == nil {
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
	// INSERT OR IGNORE, not upsert: a row already held keeps its attempt count
	// and its backoff. A platform that re-delivers the same id (Slack retries a
	// non-2xx ack) must not reset the clock.
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO inbound_pending
		   (id, payload, attempts, first_seen_at, next_attempt_at, last_error)
		 VALUES (?,?,0,?,?,?)`,
		msg.ID, string(payload), rfc(now), rfc(now.Add(inboundBackoffBase)), cause.Error()); err != nil {
		slog.Error("inbound retain failed; the message is lost",
			"id", msg.ID, "chat_jid", msg.ChatJID, "err", err, "cause", cause)
		return
	}
	slog.Warn("inbound retained for re-delivery", "id", msg.ID, "chat_jid", msg.ChatJID, "err", cause)
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

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

// SweepInboundOnce re-delivers every due row once. Exported so a test drives
// the sweep instead of waiting on the ticker.
func (r *RouterClient) SweepInboundOnce(ctx context.Context) {
	db := r.pendingDB()
	if db == nil {
		return
	}
	rows, err := db.Query(
		`SELECT id, payload, attempts FROM inbound_pending
		 WHERE next_attempt_at <= ? ORDER BY first_seen_at LIMIT ?`,
		rfc(time.Now()), inboundSweepBatch)
	if err != nil {
		slog.Error("inbound sweep: read failed", "err", err)
		return
	}
	type due struct {
		id, payload string
		attempts    int
	}
	var batch []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.payload, &d.attempts); err != nil {
			slog.Error("inbound sweep: scan failed", "err", err)
			continue
		}
		batch = append(batch, d)
	}
	rows.Close()
	for _, d := range batch {
		if ctx.Err() != nil {
			return
		}
		var msg InboundMsg
		if err := json.Unmarshal([]byte(d.payload), &msg); err != nil {
			// A row we cannot decode can never be delivered; keeping it would
			// re-fail every 15s forever.
			slog.Error("inbound sweep: undecodable row dropped", "id", d.id, "err", err)
			r.dropPending(db, d.id)
			continue
		}
		err := r.postInbound(msg)
		switch {
		case err == nil:
			r.dropPending(db, d.id)
			slog.Info("retained inbound delivered", "id", d.id, "chat_jid", msg.ChatJID,
				"attempts", d.attempts+1)
		case PermanentIngress(err):
			slog.Error("retained inbound refused by routd and dropped; a retry cannot repair it",
				"id", d.id, "chat_jid", msg.ChatJID, "attempts", d.attempts+1, "err", err)
			r.dropPending(db, d.id)
		default:
			next := time.Now().Add(inboundBackoff(d.attempts + 1))
			if _, uerr := db.Exec(
				`UPDATE inbound_pending SET attempts=attempts+1, next_attempt_at=?, last_error=?
				 WHERE id=?`, rfc(next), err.Error(), d.id); uerr != nil {
				slog.Error("inbound sweep: backoff update failed", "id", d.id, "err", uerr)
			}
			slog.Warn("retained inbound still undelivered", "id", d.id,
				"chat_jid", msg.ChatJID, "attempts", d.attempts+1, "retry_at", rfc(next), "err", err)
		}
	}
}

func (r *RouterClient) dropPending(db *sql.DB, id string) {
	if _, err := db.Exec(`DELETE FROM inbound_pending WHERE id=?`, id); err != nil {
		slog.Error("inbound sweep: delete failed; the row will be re-delivered",
			"id", id, "err", err)
	}
}

// PendingInboundCount reports how many inbound messages the adapter is holding
// undelivered. /health reports it so the backlog is visible where an operator
// already looks, rather than only in a WARN line nobody reads.
func PendingInboundCount(db *sql.DB) (int, error) {
	if db == nil {
		return 0, nil
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbound_pending`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending inbound: %w", err)
	}
	return n, nil
}
