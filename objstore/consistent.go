package objstore

import (
	"context"
	"fmt"
	"sync"
)

type Version string

// Snapshot values are immutable after load. Every reuse verifies the scope's
// settled version; errors return no value, including while a replace is held.
type Snapshot[T any] struct {
	mu      sync.Mutex
	version Version
	value   T
}

func (c *Snapshot[T]) Read(ctx context.Context, s Store, sc Scope, load func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	marker, err := settledMarker(ctx, s, sc)
	if err != nil {
		return zero, err
	}
	if c.version == Version(marker.version) {
		return c.value, nil
	}
	var value T
	version, err := ReadConsistent(ctx, s, sc, func(ctx context.Context) error {
		var err error
		value, err = load(ctx)
		return err
	})
	if err != nil {
		return zero, err
	}
	c.version, c.value = version, value
	return value, nil
}

// ReadConsistent runs read between two point reads of the scope's marker, and
// runs it again if the scope moved in between. It detects exactly the writers
// that carry Bump (marker.go). A replace holding the scope answers
// ErrScopeHeld and the caller retries at its own pace.
//
// read may run more than once and must DISCARD what it accumulated at the top
// of each run.
func ReadConsistent(ctx context.Context, s Store, sc Scope, read func(context.Context) error) (Version, error) {
	for range maxCASAttempts {
		before, err := settledMarker(ctx, s, sc)
		if err != nil {
			return "", err
		}
		if err := read(ctx); err != nil {
			return "", err
		}
		after, err := settledMarker(ctx, s, sc)
		if err != nil {
			return "", err
		}
		if before.version == after.version {
			return Version(after.version), nil
		}
	}
	return "", fmt.Errorf("read %s/%s consistently: the scope moved under %d reads in a row",
		sc.Entity, sc.Key.Sort, maxCASAttempts)
}

func CurrentVersion(ctx context.Context, s Store, sc Scope) (Version, error) {
	m, err := readMarker(ctx, s, sc)
	if err != nil {
		return "", err
	}
	if !m.present {
		return "", ErrNoMarker
	}
	return Version(m.version), nil
}

func settledMarker(ctx context.Context, s Store, sc Scope) (marker, error) {
	m, err := readMarker(ctx, s, sc)
	if err != nil {
		return marker{}, err
	}
	if !m.present || m.version == "" {
		return marker{}, fmt.Errorf("read %s/%s consistently: %w, so nothing would report a "+
			"writer that committed during the read", sc.Entity, sc.Key.Sort, ErrNoMarker)
	}
	if !m.settled() {
		return marker{}, fmt.Errorf("read %s/%s consistently: %w since %s, so its rows are "+
			"part-replaced", sc.Entity, sc.Key.Sort, ErrScopeHeld, m.since)
	}
	return m, nil
}
