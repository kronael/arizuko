package store

// user_profiles.go holds every read and write of `user_profiles`: one item per
// person the instance knows a name for, carrying the username their folder is
// named after and the per-day spend cap their turns are metered against.
//
// THE PARTITION IS OwnerRoutd AND THE SORT KEY IS THE SUB. Five of the six
// operations name one sub — the cap read and its operator write, the username
// read and the write world creation makes, and the create — and each is a point
// read or a single write on one item. A sub is the identifier every caller
// already holds: it arrives in `X-User-Sub` off proxyd's trust stamp, and no
// operation looks a person up by anything else.
//
// A PERSON HAS NO FOLDER, which is why the partition is the constant and not a
// tenancy coordinate: folder_scope.go says the same thing as a verdict ("a
// person, not a folder"), and a person outlives every folder they create. The
// sixth operation names no sub at all — the principals datalist reads the whole
// set — and under any other partition it would have none to name.
//
// WHAT THE CONSTANT COSTS is that one read, and it was already a scan on the
// engine this replaces: `SELECT sub, COALESCE(name,'') FROM user_profiles`
// answers `SCAN user_profiles USING COVERING INDEX` (EXPLAIN QUERY PLAN, krons'
// routd.db, 2026-09-09). What bounds the partition is the number of people who
// have ever been given a profile, and the live fleet holds 0 on krons, 2 on
// marinade and 3 on sloth (2026-09-09).
//
// THE CAP LIVES BESIDE THE PRINCIPAL IT BOUNDS — a group's on the groups item,
// a person's here — and never in the ledger, so reading one costs no aggregate.
// routd owns the ledger and the spend arithmetic and reaches this through
// routd/sibling_db.go.
//
// NOTHING HERE IS COMPARED BY ORDER, so no attribute carries a width
// declaration. `created_at` is written once and read by nothing; the cap is
// scanned as a number and compared in Go. A declaration is a promise about
// every value in the entity, and this entity has nothing resting on one.
//
// THE UNIQUE USERNAME IS THE READER'S WORK NOW. An entity has one uniqueness
// and it is the coordinate, so the second unique index the column table carried
// has no structural equivalent — and it is load-bearing: a world created under
// a parent is `parent + "/" + username`, so the folder collision the caller
// checks first does not fire when two people under different parents pick one
// name, and the index was what refused them. The check runs here instead, on
// the caller's own transaction, over a partition of at most a few items.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kronael/arizuko/objstore"
)

// The attributes a profile item carries. `sub` is not among them: it is the
// coordinate, and storing it twice is two facts that can disagree.
const (
	attrProfUsername = "username"
	attrProfCreated  = "created_at"
	attrProfCostCap  = "cost_cap_cents_per_day"
	// AttrProfileName is the display name. It is exported because the principals
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
// and the display name an operator surface labels them with.
//
// It REFUSES an existing sub, which is what the coordinate's own uniqueness
// says, and it refuses a username another sub already holds — see the file
// header for why the second one cannot be structural.
func (s *Store) CreateUserProfile(sub, username, name string) error {
	key, err := profileKey(sub)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := s.refuseTakenUsername(ctx, sub, username); err != nil {
		return err
	}
	attrs := map[string]string{attrProfCreated: time.Now().Format(time.RFC3339)}
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
		return fmt.Errorf("record the profile of %s: the sub already has one", sub)
	}
	return nil
}

// Username answers the name a person's folder is named after, and whether the
// instance knows the person at all. The two are separated from a read FAILURE,
// because a page that cannot read the partition must not tell an existing user
// they do not exist.
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

// SetUsername names a person.
//
// IT IS THE LAST WRITE OF A WORLD CREATION, and deliberately not part of its
// batch. A caller who loses the race for a folder must not be left renamed, and
// onbod gets that by naming the person only after the batch that won the folder
// committed — where the transaction this replaces got it by rolling both back.
//
// It writes the ATTRIBUTE ONLY, onto a profile that has to be there already: an
// Update against a missing item writes nothing, and that tolerated refusal is
// the other reason it cannot ride in an all-or-nothing batch.
//
// THAT REFUSAL IS TOLERATED AND IT IS NOT CORRECT. Nothing in production creates
// a user_profiles item — CreateUserProfile has callers in tests only, and so did
// the CreateAuthUser it replaced — so a sub the instance has never recorded is
// renamed silently and stays nameless. Raising it HERE would answer create_world
// 500 for that person and roll their whole world back, which blocks a signup
// instead of fixing the missing writer. The writer is the fix and it is a
// decision, not an edit.
func (s *Store) SetUsername(ctx context.Context, sub, username string) error {
	key, err := profileKey(sub)
	if err != nil {
		return err
	}
	if err := s.refuseTakenUsername(ctx, sub, username); err != nil {
		return err
	}
	// A refusal is the missing item the doc above measures.
	if _, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: UserProfiles, Key: key,
		Attrs: map[string]string{attrProfUsername: username},
	}); err != nil {
		return fmt.Errorf("name %s %q: %w", sub, username, err)
	}
	return nil
}

// refuseTakenUsername fails when another sub already holds username.
//
// It reads through the CALLER'S transaction when there is one, so the read and
// the write it guards see one state: reading through the pool instead would be
// a torn read, and on SQLite in shared-cache memory it waits for the write lock
// the same transaction holds.
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
// display name it holds, which is the principals datalist an operator's grant
// form offers.
//
// The whole partition is read because the question names nobody. The file
// header says what that costs and that the statement it replaces read the same
// rows.
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
// TAKES THE ATTRIBUTE AWAY rather than storing "0": absent is the only null this
// model has, and the read above already answers zero for an item that carries
// none.
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
