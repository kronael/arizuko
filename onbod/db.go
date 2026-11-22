package main

import (
	"context"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func openOwned() (*store.Store, error) {
	d, closeStore, err := store.OwnerDriver(store.OwnerOnbod)
	if err != nil {
		return nil, err
	}
	if err := prepareOwned(d); err != nil {
		closeStore() //nolint:errcheck // the open already failed
		return nil, err
	}
	s := store.NewOver(d)
	if err := s.MigrateOnboardingIndexes(context.Background()); err != nil {
		closeStore() //nolint:errcheck // the migration already failed
		return nil, err
	}
	return s, nil
}

func prepareOwned(d store.Driver) error {
	ctx := context.Background()
	if err := store.PrepareOwner(ctx, d, store.OwnerOnbod); err != nil {
		return err
	}
	return resreg.EnsureSubsystem(ctx, d, d, resreg.SubsystemOnbod)
}
