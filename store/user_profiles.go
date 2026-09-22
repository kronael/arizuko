package store

// user_profiles.go holds every read and write of `user_profiles`: one item per
// person the instance knows a name for, carrying the username their folder is
// named after and the per-day spend cap their turns are metered against.
//
// The partition is OwnerRoutd and the sort key the sub, the identifier every
// caller holds off proxyd's `X-User-Sub` stamp. A person has no folder and
// outlives every folder they create. The principals datalist reads the whole
// partition (0 to 3 items on the live fleet). Nothing here is compared by
// order. The cap lives beside the principal it bounds, never in the ledger;
// routd owns the spend arithmetic.
//
// THE UNIQUE USERNAME IS THE READER'S WORK: an entity's one uniqueness is the
// coordinate, and the check is load-bearing because a world created under a
// parent is `parent + "/" + username`, so two people under different parents
// picking one name is not a folder collision.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a profile item carries. `sub` is the coordinate.
const (
	attrProfUsername = "username"
	attrProfCreated  = "created_at"
	attrProfCostCap  = "cost_cap_cents_per_day"
	// AttrProfileName is the display name. Exported because the principals
	// datalist reads it off every item in the partition.
	AttrProfileName = "name"
)

// profileKey is the coordinate of one person's profile.
func profileKey(sub string) (objstore.Key, error) {
	if sub == "" {
		return objstore.Key{}, fmt.Errorf("user profile carries no sub, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: sub}, nil
}

// CreateUserProfile records a person: the username their folder is named after
// and the display name an operator surface labels them with. It REFUSES an
// existing sub and a username another sub already holds (file header).
func (s *Store) CreateUserProfile(sub, username, name string) error {
	key, err := profileKey(sub)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := s.refuseTakenUsername(ctx, sub, username); err != nil {
		return err
	}
	// core.TS, not time.Now().Format: fixed-width UTC, so two hosts agree
	// (BUGS L97 is what a local-time stamp costs).
	attrs := map[string]string{attrProfCreated: core.TS(time.Now())}
	setAttr(attrs, attrProfUsername, username)
	setAttr(attrs, AttrProfileName, name)
	created, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: UserProfiles, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("record the profile of %s: %w", sub, err)
	}
	if !created {
		return fmt.Errorf("record the profile of %s: %w", sub, ErrProfileExists)
	}
	return nil
}

// ErrProfileExists is CreateUserProfile's refusal: the sub already has a
// profile. A sentinel because RecordPerson decides on it.
var ErrProfileExists = errors.New("the sub already has a profile")

// RecordPerson is the door the admission path takes: it creates the profile
// when the instance has never seen this sub, and renames an existing one
// otherwise. It exists because NOTHING created a profile, so SetUsername was
// an Update against an item that was never there and every admitted person
// stayed nameless (BUGS L269).
func (s *Store) RecordPerson(ctx context.Context, sub, username, name string) error {
	err := s.CreateUserProfile(sub, username, name)
	if err == nil {
		return nil
	}
	// The person is coming back, and the name they chose this time stands.
	if errors.Is(err, ErrProfileExists) {
		return s.SetUsername(ctx, sub, username)
	}
	return err
}

// Username answers the name a person's folder is named after, and whether the
// instance knows the person at all. The two are separated from a read FAILURE:
// a page that cannot read the partition must not tell an existing user they do
// not exist.
func (s *Store) Username(sub string) (string, bool, error) {
	key, err := profileKey(sub)
	if err != nil {
		return "", false, err
	}
	it, ok, err := s.itemAt(context.Background(), UserProfiles, key)
	if err != nil || !ok {
		return "", false, err
	}
	return it.Attrs[attrProfUsername], true, nil
}

// SetUsername names a person. It is THE LAST WRITE OF A WORLD CREATION, and
// deliberately not part of its batch: a caller who loses the race for a folder
// must not be left renamed. It writes the ATTRIBUTE ONLY, onto a profile that
// has to be there already.
//
// THE REFUSAL FOR A MISSING ITEM IS TOLERATED AND IT IS NOT CORRECT: a sub the
// instance never recorded is renamed silently and stays nameless. Raising it
// HERE would answer create_world 500 and roll the whole world back, which
// blocks a signup instead of fixing the missing writer; the writer is the fix,
// and it is a decision.
func (s *Store) SetUsername(ctx context.Context, sub, username string) error {
	key, err := profileKey(sub)
	if err != nil {
		return err
	}
	if err := s.refuseTakenUsername(ctx, sub, username); err != nil {
		return err
	}
	// A refusal is the missing item the doc above tolerates.
	if _, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: UserProfiles, Key: key,
		Attrs: map[string]string{attrProfUsername: username},
	}); err != nil {
		return fmt.Errorf("name %s %q: %w", sub, username, err)
	}
	return nil
}

// refuseTakenUsername fails when another sub already holds username.
func (s *Store) refuseTakenUsername(ctx context.Context, sub, username string) error {
	if username == "" {
		return nil
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: UserProfiles, Partition: OwnerRoutd,
		Where:   []objstore.Cmp{{Attr: attrProfUsername, Op: objstore.Eq, Value: username}},
		Project: []string{attrProfUsername},
	})
	if err != nil {
		return fmt.Errorf("check whether %q is taken: %w", username, err)
	}
	for _, it := range items {
		if it.Key.Sort != sub {
			return fmt.Errorf("username %q belongs to %s", username, it.Key.Sort)
		}
	}
	return nil
}

// ProfileNames answers every sub the instance knows a profile for and the
// display name it holds: the principals datalist an operator's grant form
// offers. The whole partition, because the question names nobody.
func (s *Store) ProfileNames(ctx context.Context) (map[string]string, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: UserProfiles, Partition: OwnerRoutd,
		Project: []string{AttrProfileName},
	})
	if err != nil {
		return nil, fmt.Errorf("read the user profiles: %w", err)
	}
	out := make(map[string]string, len(items))
	for _, it := range items {
		out[it.Key.Sort] = it.Attrs[AttrProfileName]
	}
	return out, nil
}

// UserCap answers the per-day cap for one sub, in cents. Zero is uncapped, and
// a sub with NO profile is uncapped rather than an error: platform senders
// (telegram:user/…, anon:…) and service:routd never own one, and they are the
// callers cost attribution actually sees (BUGS L69).
func (s *Store) UserCap(userSub string) (int, error) {
	key, err := profileKey(userSub)
	if err != nil {
		return 0, err
	}
	it, ok, err := s.itemAt(context.Background(), UserProfiles, key)
	if err != nil || !ok {
		return 0, err
	}
	raw, has := it.Attrs[attrProfCostCap]
	if !has {
		return 0, nil
	}
	cents, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("the cap of %s is %q, which is not a number of cents: %w",
			userSub, raw, err)
	}
	return cents, nil
}

// SetUserCap writes the per-day cap for one person. Zero is uncapped, and it
// TAKES THE ATTRIBUTE AWAY rather than storing "0": the read above answers
// zero for an item that carries none.
func (s *Store) SetUserCap(userSub string, cents int) error {
	key, err := profileKey(userSub)
	if err != nil {
		return err
	}
	w := objstore.Write{Op: objstore.Update, Entity: UserProfiles, Key: key}
	if cents == 0 {
		w.Remove = []string{attrProfCostCap}
	} else {
		w.Attrs = map[string]string{attrProfCostCap: strconv.Itoa(cents)}
	}
	if err := s.WriteEntities(context.Background(), nil, w); err != nil {
		return fmt.Errorf("cap %s at %d cents a day: %w", userSub, cents, err)
	}
	return nil
}

// CountUserProfiles answers how many people the instance knows. `arizuko
// migrate-hot` reads it back to check that every row travelled.
func (s *Store) CountUserProfiles(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: UserProfiles, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the user profiles: %w", err)
	}
	return len(items), nil
}
