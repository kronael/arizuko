package chanlib

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/store"
)

// AdapterStore is one adapter's storage: the driver its tables live on, the
// chanlib half over that driver, and the close. One value rather than three
// returns because emaid wants the driver as well — it ships entities of its
// own and makes them on the same store.
type AdapterStore struct {
	// Driver is what an adapter with entities of its own makes them on.
	Driver store.Driver
	// Chan is what every adapter passes to NewAdapterMux and RetainInbound.
	Chan *chanstore.Store
	// Close ends the pool a file-backed store holds; a served one holds none.
	Close func() error
}

// OpenAdapterStore prepares one adapter's storage: chanlib SHIPS the entities,
// the TABLES belong to the adapter daemon, one set per service, so no library
// owns a database. It is the only door, so the dial is written here once, and
// it names a driver, which is the composition root's job (spec 6/37 step 5).
func OpenAdapterStore(service string) (AdapterStore, error) {
	as := AdapterStore{Close: func() error { return nil }}
	// The prefix is the adapter's name, so two accounts of ONE adapter share
	// these tables: every key carries the chat JID, which carries the channel
	// name (`telegram-rhias:group/1`), so a held inbound and a delivery claim
	// are already scoped to the account that made them.
	h, err := dynstore.FromEnv(dynstore.Prefix(service))
	if err != nil {
		return AdapterStore{}, err
	}
	as.Driver = h
	for _, entity := range chanstore.Entities {
		if err := as.Driver.CreateEntity(context.Background(), entity); err != nil {
			as.Close() //nolint:errcheck // the open already failed
			return AdapterStore{}, err
		}
	}
	as.Chan = chanstore.New(as.Driver)
	return as, nil
}

// ErrDeliveryUnresolved reports a redelivery whose earlier attempt never
// settled: posting again may duplicate the message and skipping may drop it,
// so the adapter reports the ambiguity and routd dead-letters the row for an
// operator (spec 5/34).
var ErrDeliveryUnresolved = errors.New("earlier delivery attempt never settled")

// deliverOnce runs post exactly once per idempotency key — the single place
// the outbound dedup contract is enforced (spec 5/E). The claim lands BEFORE
// the platform call: a lost response leaves the key inflight, and the next
// attempt is refused (ErrDeliveryUnresolved) rather than duplicated — routd
// dead-letters it, visible, not doubled (BUGS D5). A failure that proves
// nothing was posted (the permanent family, ErrNotDelivered) releases the
// claim; every other error leaves it, because "the platform failed" and "the
// response was lost" look identical from here. st == nil or key == ""
// disables dedup. EVERY STORE CALL RUNS ON context.Background(): routd's
// context would cancel the write that says "delivered" at the moment the
// platform accepted it, leaving the key inflight.
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
				// The key stays claimed, so routd's retry dead-letters a message
				// nothing was posted for. Loud, because only a log says why.
				slog.Error("releasing an undelivered dedup claim failed",
					"idempotency_key", key, "err", rerr)
			}
		}
		return "", err
	}
	if err := st.RecordDelivered(ctx, key, id); err != nil {
		// The message IS delivered; only the record failed. The next attempt on
		// this key now refuses instead of resending.
		slog.Error("outbound delivered but the dedup record failed",
			"idempotency_key", key, "platform_id", id, "err", err)
	}
	return id, nil
}
