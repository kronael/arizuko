package objstore

import (
	"context"
	"fmt"
)

type Version string

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
