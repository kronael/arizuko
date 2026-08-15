package chanlib

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/kronael/arizuko/db_utils"
	// OpenAdapterDB opens "sqlite" DSNs, so the driver registration belongs
	// with the call — six adapter mains would each have to remember it, and the
	// one that forgot would fail at boot with "unknown driver".
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var dedupMigrations embed.FS

// OpenAdapterDB opens <dataDir>/<service>.db and applies chanlib's migrations.
// chanlib SHIPS the schema; the FILE belongs to the adapter daemon, one per
// service, so an adapter stays outage-resistant on its own and no library owns
// a database. emaid is the precedent.
//
// The file is adapter-private state that `arizuko create` does not seed, so a
// missing one is created here.
func OpenAdapterDB(dataDir, service string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dataDir, service+".db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if err := MigrateAdapterDB(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// MigrateAdapterDB applies chanlib's schema to an adapter-owned database.
// db_utils.Migrate keys on (service, version), so the chain coexists with the
// adapter's own migrations in one file — emaid keeps a single emaid.db rather
// than growing a second one for the dedup table.
func MigrateAdapterDB(db *sql.DB) error {
	return db_utils.Migrate(db, dedupMigrations, "migrations", "chanlib")
}

// ErrDeliveryUnresolved reports a redelivery whose earlier attempt never
// settled. The adapter started that platform call and never learned whether it
// landed, so posting again may duplicate the message and skipping may drop it.
// Neither is the adapter's call to make: it reports the ambiguity and routd
// dead-letters the row for an operator (spec 5/34).
var ErrDeliveryUnresolved = errors.New("earlier delivery attempt never settled")

// dedupClaim is what a claim on an idempotency key resolved to.
type dedupClaim struct {
	// PlatformID is set when the key was already delivered; the caller returns
	// it and makes no platform call.
	PlatformID string
	// Done is true when the caller must not call the platform.
	Done bool
}

// claimDelivery records the intent to deliver key and reports whether the
// caller may proceed. The record lands BEFORE the platform call, which is the
// whole point: a lost response leaves `inflight`, and the next attempt on the
// same key is refused rather than duplicated.
//
// db == nil or key == "" disables dedup and always proceeds — a local-dev
// adapter with no state mount, or a send routd issued without a key (SendVia).
func claimDelivery(db *sql.DB, key, jid string) (dedupClaim, error) {
	if db == nil || key == "" {
		return dedupClaim{}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := db.Exec(
		`INSERT OR IGNORE INTO outbound_dedup(idempotency_key, chat_jid, state, created_at)
		 VALUES(?,?,'inflight',?)`, key, jid, now)
	if err != nil {
		return dedupClaim{}, fmt.Errorf("claim delivery %s: %w", key, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return dedupClaim{}, nil
	}
	var state string
	var platformID sql.NullString
	if err := db.QueryRow(
		`SELECT state, platform_id FROM outbound_dedup WHERE idempotency_key=?`,
		key).Scan(&state, &platformID); err != nil {
		return dedupClaim{}, fmt.Errorf("read delivery claim %s: %w", key, err)
	}
	if state == "sent" {
		slog.Info("outbound already delivered; not posting again",
			"idempotency_key", key, "chat_jid", jid, "platform_id", platformID.String)
		return dedupClaim{PlatformID: platformID.String, Done: true}, nil
	}
	return dedupClaim{}, fmt.Errorf("%w: %s", ErrDeliveryUnresolved, key)
}

// settleDelivery records that the platform accepted the message.
func settleDelivery(db *sql.DB, key, platformID string) {
	if db == nil || key == "" {
		return
	}
	if _, err := db.Exec(
		`UPDATE outbound_dedup SET state='sent', platform_id=?, settled_at=?
		 WHERE idempotency_key=?`,
		platformID, time.Now().UTC().Format(time.RFC3339Nano), key); err != nil {
		// The message IS delivered; only the record failed. Say so loudly —
		// the next attempt on this key now refuses instead of resending.
		slog.Error("outbound delivered but the dedup record failed",
			"idempotency_key", key, "platform_id", platformID, "err", err)
	}
}

// deliverOnce runs post exactly once per idempotency key. It is the single
// place the outbound dedup contract is enforced, so every adapter behind
// NewAdapterMux gets it without writing any of it (spec 5/E § Outbound
// delivery).
//
// A key already delivered returns its recorded platform id and makes no call.
// A key whose earlier attempt never settled returns ErrDeliveryUnresolved: the
// adapter cannot tell a lost response from a lost request, and Slack's
// chat.postMessage takes no key of its own, so guessing either duplicates the
// message or drops it. routd retries, gets the same refusal, and dead-letters
// the row for an operator — visible, not doubled (BUGS D5, spec 5/34).
//
// An ErrPermanent failure is the one error that proves nothing was posted, so
// it releases the claim. Every other error leaves it: "the platform refused"
// and "the response was lost" look identical from here.
func deliverOnce(db *sql.DB, key, jid string, post func() (string, error)) (string, error) {
	claim, err := claimDelivery(db, key, jid)
	if err != nil {
		return "", err
	}
	if claim.Done {
		return claim.PlatformID, nil
	}
	id, err := post()
	if err != nil {
		if errors.Is(err, ErrPermanent) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrInvalidRequest) {
			releaseDelivery(db, key)
		}
		return "", err
	}
	settleDelivery(db, key, id)
	return id, nil
}

// releaseDelivery drops the claim after a platform call that definitively did
// NOT deliver. A refused call left no platform message, so the key is free for
// routd's retry; keeping the claim would refuse every later attempt.
func releaseDelivery(db *sql.DB, key string) {
	if db == nil || key == "" {
		return
	}
	if _, err := db.Exec(
		`DELETE FROM outbound_dedup WHERE idempotency_key=? AND state='inflight'`,
		key); err != nil {
		slog.Error("releasing an undelivered dedup claim failed",
			"idempotency_key", key, "err", err)
	}
}
