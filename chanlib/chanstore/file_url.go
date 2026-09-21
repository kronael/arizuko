package chanstore

// file_url.go holds every read and write of `file_urls`: one item per
// attachment the adapter published behind its own `/files/<id>` proxy, mapping
// the opaque id back to the platform URL it stands for.
//
// The adapter rewrites an inbound attachment's URL to `<listen>/files/<id>`
// before the envelope reaches routd, so the platform URL survives NOWHERE else:
// the envelope carries the proxy URL, and so does the message routd stores.
// Losing the mapping therefore loses the file. routd's enrich downloads through
// the proxy when the turn runs, which can be long after the message arrived —
// the adapter may have restarted, and the inbound may have waited out a routd
// outage in `inbound_pending` first (BUGS L163).
//
// NOTHING SWEEPS THIS ENTITY, and that is the point rather than an oversight.
// An expiry is a deliberate loss of the mapping, which is the bug this entity
// exists to end: a message enriched after the window would get the same 404 and
// the same `unavailable=` notice. What it costs is one item per inbound
// attachment, strictly fewer than `outbound_dedup`'s one per outbound delivery,
// which this set already accepts unswept.

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes an item carries. `url` is the whole answer; `created_at` is the
// operator's evidence for when a mapping was made and nothing selects on it, so
// it is not declared ordered (the reasoning `init` states for the other two).
const (
	attrURL          = "url"
	attrURLCreatedAt = "created_at"
)

// fileURLKey is the coordinate of one mapping.
//
// THE PARTITION IS THE ID, for `outbound_dedup`'s reason rather than
// `inbound_pending`'s: both reads below address exactly one id handed to them by
// an HTTP path, neither filters, and no read wants two mappings co-located. A
// constant partition would pile an unswept ledger into one partition for no
// read's benefit.
//
// The sort key repeats the id because the item has no second coordinate, and
// DynamoDB refuses an empty key attribute.
func fileURLKey(id string) objstore.Key { return objstore.Key{Partition: id, Sort: id} }

// RecordFileURL stores the platform URL an id stands for.
//
// The write is UNCONDITIONAL, which is safe only because the id is derived from
// the URL — chanlib hashes the URL to get it — so a second write on an id
// carries the same URL as the first. A condition here would buy nothing and
// would turn the re-publication of an unchanged attachment into a refusal the
// caller has to classify.
func (s *Store) RecordFileURL(ctx context.Context, id, url string) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: FileURL, Key: fileURLKey(id),
		Attrs: map[string]string{
			attrURL:          url,
			attrURLCreatedAt: core.NowTS(),
		},
	}})
	if err != nil {
		return fmt.Errorf("record file url %s: %w", id, err)
	}
	return nil
}

// FileURL answers what URL an id stands for, writing nothing.
//
// A miss is a normal outcome — an id from an adapter that never recorded one, or
// one a hand-built request invented — so it is reported rather than raised, and
// the proxy turns it into the 404 that is already its contract.
func (s *Store) FileURL(ctx context.Context, id string) (string, bool, error) {
	it, held, err := s.point(ctx, FileURL, fileURLKey(id), attrURL)
	if err != nil {
		return "", false, fmt.Errorf("read file url %s: %w", id, err)
	}
	if !held {
		return "", false, nil
	}
	return it.Attrs[attrURL], true, nil
}
