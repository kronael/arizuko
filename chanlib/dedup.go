package chanlib

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	// OpenAdapterDB opens "sqlite" DSNs, so the driver registration belongs
	// with the call — seven adapter mains would each have to remember it, and the
	// one that forgot would fail at boot with "unknown driver".
	_ "modernc.org/sqlite"
)

// OpenAdapterDB opens <dataDir>/<service>.db and prepares it to answer chanlib's
// storage questions. chanlib SHIPS the entities; the FILE belongs to the adapter
// daemon, one per service, so an adapter stays outage-resistant on its own and no
// library owns a database.
//
// The file is adapter-private state that `arizuko create` does not seed, so a
// missing one is created here.
//
// It hands back BOTH halves and it is the only door: the store is what every
// adapter passes to NewAdapterMux and RetainInbound, and the pool is the
// engine handle whose lifetime the adapter owns — it closes it on shutdown, and
// emaid builds its own emaistore on the same file (emaid/storage.go). A second
// opener for the daemon that needs the pool is how the two would drift.
//
// This function names a driver, which is the composition root's job (spec 6/37
// step 5) — chanstore itself names none, so a second engine is one line here.
func OpenAdapterDB(dataDir, service string) (*sql.DB, *chanstore.Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, nil, err
	}
	db, err := db_utils.Open(filepath.Join(dataDir, service+".db"))
	if err != nil {
		return nil, nil, err
	}
	s := sqlstore.New(db)
	for _, entity := range chanstore.Entities {
		if err := s.CreateEntity(context.Background(), entity); err != nil {
			db.Close() //nolint:errcheck // the open already failed
			return nil, nil, err
		}
	}
	return db, chanstore.New(s), nil
}

// ErrDeliveryUnresolved reports a redelivery whose earlier attempt never
// settled. The adapter started that platform call and never learned whether it
// landed, so posting again may duplicate the message and skipping may drop it.
// Neither is the adapter's call to make: it reports the ambiguity and routd
// dead-letters the row for an operator (spec 5/34).
var ErrDeliveryUnresolved = errors.New("earlier delivery attempt never settled")

// deliverOnce runs post exactly once per idempotency key. It is the single
// place the outbound dedup contract is enforced, so every adapter behind
// NewAdapterMux gets it without writing any of it (spec 5/E § Outbound
// delivery).
//
// The claim lands BEFORE the platform call, which is the whole point: a lost
// response leaves the key inflight, and the next attempt on it is refused rather
// than duplicated.
//
// A key already delivered returns its recorded platform id and makes no call.
// A key whose earlier attempt never settled returns ErrDeliveryUnresolved: the
// adapter cannot tell a lost response from a lost request, and Slack's
// chat.postMessage takes no key of its own, so guessing either duplicates the
// message or drops it. routd retries, gets the same refusal, and dead-letters
// the row for an operator — visible, not doubled (BUGS D5, spec 5/34).
//
// A failure that proves nothing was posted releases the claim: the permanent
// family (ErrPermanent/ErrUnsupported/ErrInvalidRequest, which routd
// dead-letters) and ErrNotDelivered (a transient refusal — a 429, an
// `ok:false` — which routd's sweep retries on a fresh claim). Every other
// error leaves it: "the platform failed" and "the response was lost" look
// identical from here.
//
// st == nil or key == "" disables dedup and always proceeds — a local-dev
// adapter with no state mount, or a send routd issued without a key (SendVia).
//
// EVERY STORE CALL BELOW RUNS ON context.Background(), not on the request's. The
// caller here is routd, and routd gives up on a slow send; carrying its context
// into the record would cancel the write that says "this message IS delivered"
// at exactly the moment the platform has accepted it, leaving the key inflight.
// routd's next attempt would then get ErrDeliveryUnresolved and dead-letter a
// message the user already received.
func deliverOnce(st *chanstore.Store, key, jid string,
	post func() (string, error)) (string, error) {
	if st == nil || key == "" {
		return post()
	}
	ctx := context.Background()
	claim, err := st.ClaimDelivery(ctx, key, jid)
	if err != nil {
		return "", err
	}
	switch claim.State {
	case chanstore.ClaimSent:
		slog.Info("outbound already delivered; not posting again",
			"idempotency_key", key, "chat_jid", jid, "platform_id", claim.PlatformID)
		return claim.PlatformID, nil
	case chanstore.ClaimInflight:
		return "", fmt.Errorf("%w: %s", ErrDeliveryUnresolved, key)
	}
	id, err := post()
	if err != nil {
		if errors.Is(err, ErrPermanent) || errors.Is(err, ErrUnsupported) ||
			errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrNotDelivered) {
			if rerr := st.ReleaseUnsentClaim(ctx, key); rerr != nil {
				// The key stays claimed, so routd's retry will get
				// ErrDeliveryUnresolved and dead-letter a message nothing was
				// posted for. Loud, because only a log says why.
				slog.Error("releasing an undelivered dedup claim failed",
					"idempotency_key", key, "err", rerr)
			}
		}
		return "", err
	}
	if err := st.RecordDelivered(ctx, key, id); err != nil {
		// The message IS delivered; only the record failed. Say so loudly —
		// the next attempt on this key now refuses instead of resending.
		slog.Error("outbound delivered but the dedup record failed",
			"idempotency_key", key, "platform_id", id, "err", err)
	}
	return id, nil
}
