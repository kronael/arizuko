package store

// github_sources.go holds every read and write of `github_sources`: one item per
// webhook delivery source, addressed by its source key.
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES.
// `github_sources` is a registered resreg resource, and an entity-backed
// resource's items live at its owner subsystem (resreg.SubsystemRoutd, which IS
// store.OwnerRoutd). A second spelling would put this file's items in one
// partition and the engine's read in another, and the failure is silent: a query
// on a partition nobody wrote to is a legal query that selects nothing.
//
// THE SOURCE KEY IS THE COORDINATE — "<kind>/<name>", the same string gitd
// resolves an inbound delivery by, so the verification read is one exact key.
// GitHubSourceKey validates it before it becomes one.
//
// LISTING BY OWNER IS A PARTITION SCAN. `owner_folder` is an attribute, and an
// attribute is not a leading fragment of the key, so ListGitHubSources reads the
// partition and filters; the read comes back in coordinate order, which IS
// `ORDER BY source`.
//
// `secret` IS AN ATTRIBUTE THE RowType DOES NOT DECLARE
// (routd/resource_columns_test.go rowTypeGaps), so this file is its only writer:
// the sealed webhook credential must never reach the export an operator reads.
// It seals through the same storeValue/open keyring a secret does.
//
// `created_at` IS core.TS, at 30 bytes. That is the width resreg's own entity
// path stamps a StampedField at (resreg/entity.go itemFor), and the two writers
// of these items have to agree byte for byte — a comparison over TEXT answers by
// byte order (BUGS L97).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a source item carries. `source` is NOT among them: it is the
// coordinate.
const (
	attrGHSecret      = "secret"
	attrGHOwnerFolder = "owner_folder"
	attrGHCreatedAt   = "created_at"
)

// githubSourceKey is the coordinate of one source.
func githubSourceKey(source string) (objstore.Key, error) {
	if source == "" {
		return objstore.Key{}, errors.New("github source carries no key, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: source}, nil
}

// githubSourceFrom reads one item back.
func githubSourceFrom(it objstore.Item) GitHubSource {
	g := GitHubSource{Source: it.Key.Sort, OwnerFolder: it.Attrs[attrGHOwnerFolder]}
	g.CreatedAt, _ = time.Parse(core.TSLayout, it.Attrs[attrGHCreatedAt])
	return g
}

// githubSourceItem reads the item one source key names.
func (s *Store) githubSourceItem(ctx context.Context, key string) (objstore.Item, bool, error) {
	k, err := githubSourceKey(key)
	if err != nil {
		return objstore.Item{}, false, err
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: GitHubSources, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: k.Sort},
	})
	if err != nil {
		return objstore.Item{}, false, fmt.Errorf("read github source %q: %w", key, err)
	}
	if len(items) == 0 {
		return objstore.Item{}, false, nil
	}
	return items[0], true, nil
}

// GitHubSource is one webhook delivery source gitd verifies (spec 6/27). Source
// is "<kind>/<name>": repo/<owner>/<repo>, org/<owner> or app/<slug>. The
// sealed secret never leaves the row except through GitHubSourceSecret.
type GitHubSource struct {
	Source      string
	OwnerFolder string
	CreatedAt   time.Time
}

const (
	GitHubSourceRepo = "repo"
	GitHubSourceOrg  = "org"
	GitHubSourceApp  = "app"
)

var githubSegRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// GitHubSourceKey validates (kind, name) and returns the row key. A repo name is
// "<owner>/<repo>"; org and app names are one segment.
func GitHubSourceKey(kind, name string) (string, error) {
	segs := strings.Split(name, "/")
	switch kind {
	case GitHubSourceRepo:
		if len(segs) != 2 {
			return "", fmt.Errorf("repo name must be <owner>/<repo>, got %q", name)
		}
	case GitHubSourceOrg, GitHubSourceApp:
		if len(segs) != 1 {
			return "", fmt.Errorf("%s name must be one segment, got %q", kind, name)
		}
	default:
		return "", fmt.Errorf("kind must be repo, org or app, got %q", kind)
	}
	for _, s := range segs {
		if !githubSegRE.MatchString(s) || s == ".." {
			return "", fmt.Errorf("invalid name segment %q", s)
		}
	}
	return kind + "/" + name, nil
}

// ParseGitHubSourceKey splits a key into kind + name, validating both.
func ParseGitHubSourceKey(key string) (kind, name string, err error) {
	kind, name, found := strings.Cut(key, "/")
	if !found {
		return "", "", fmt.Errorf("source must be <kind>/<name>, got %q", key)
	}
	if _, err := GitHubSourceKey(kind, name); err != nil {
		return "", "", err
	}
	return kind, name, nil
}

// GitHubSourceRoutes renders the route rows a registration seeds. Human content
// (a comment, a new PR or issue: verb message, or mention once the agent has
// spoken in the thread) triggers a turn; every other event observes. Repo rows
// sit at lower seq than org rows than app rows, so a narrower registration
// wins. `*` never crosses `/` in a route glob, so each JID level gets a row.
func GitHubSourceRoutes(key, folder string) ([]core.Route, error) {
	kind, name, err := ParseGitHubSourceKey(key)
	if err != nil {
		return nil, err
	}
	var repo, owner string
	var seq int
	switch kind {
	case GitHubSourceRepo:
		repo, seq = "github:"+name, 10
	case GitHubSourceOrg:
		repo, owner, seq = "github:"+name+"/*", "github:"+name, 20
	case GitHubSourceApp:
		repo, owner, seq = "github:*/*", "github:*", 30
	}
	observe := folder + "#observe"
	rows := []core.Route{
		{Seq: seq, Match: "chat_jid=" + repo + "/pr/* verb=message", Target: folder},
		{Seq: seq, Match: "chat_jid=" + repo + "/pr/* verb=mention", Target: folder},
		{Seq: seq, Match: "chat_jid=" + repo + "/issue/* verb=message", Target: folder},
		{Seq: seq, Match: "chat_jid=" + repo + "/issue/* verb=mention", Target: folder},
		{Seq: seq * 10, Match: "chat_jid=" + repo + "/pr/*", Target: observe},
		{Seq: seq * 10, Match: "chat_jid=" + repo + "/issue/*", Target: observe},
		{Seq: seq * 10, Match: "chat_jid=" + repo, Target: observe},
	}
	if owner != "" {
		rows = append(rows, core.Route{Seq: seq * 10, Match: "chat_jid=" + owner, Target: observe})
	}
	return rows, nil
}

var ErrGitHubSourceExists = errors.New("github source already registered")

// AddGitHubSourceTx mints the secret, seals it, and writes the source item and
// the route rows to target on tx. The raw secret is returned exactly once.
//
// EVERY ROUTE ID IS MINTED BEFORE THE FIRST WRITE TO tx, and the order is
// load-bearing rather than tidy. The counter is a compare-and-swap of its own
// (entities.go nextNumber) and runs OUTSIDE the caller's transaction, so a
// transaction that has already written holds the engine's write lock and its own
// mint waits on it forever — a registration that never returns, on a lock the
// whole daemon's writes then queue behind.
func (s *Store) AddGitHubSourceTx(ctx context.Context, tx *sql.Tx, src GitHubSource, target string) (string, error) {
	if _, _, err := ParseGitHubSourceKey(src.Source); err != nil {
		return "", err
	}
	if src.OwnerFolder == "" || target == "" {
		return "", errors.New("owner_folder and target required")
	}
	routes, err := GitHubSourceRoutes(src.Source, target)
	if err != nil {
		return "", err
	}
	for i := range routes {
		n, err := s.nextNumber(ctx, OwnerRoutd, CounterRouteID)
		if err != nil {
			return "", fmt.Errorf("mint a route id: %w", err)
		}
		routes[i].ID = int64(n)
	}
	secret := GenRouteToken()
	sealed, err := s.storeValue(secret)
	if err != nil {
		return "", err
	}
	ts := src.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	key, err := githubSourceKey(src.Source)
	if err != nil {
		return "", err
	}
	// Create-if-absent, which is what `INSERT OR IGNORE` plus the zero-rows check
	// said: registering a source that is already there is REFUSED with
	// ErrGitHubSourceExists rather than re-minting its secret, which would break
	// every delivery GitHub is already signing with the first one.
	created, err := s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Put, Entity: GitHubSources, Key: key,
		Attrs: map[string]string{
			attrGHSecret:      sealed,
			attrGHOwnerFolder: src.OwnerFolder,
			attrGHCreatedAt:   core.TS(ts),
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return "", err
	}
	if !created {
		return "", fmt.Errorf("%w: %s", ErrGitHubSourceExists, src.Source)
	}
	for _, r := range routes {
		if _, err := s.putRoute(ctx, tx, r, "", ""); err != nil {
			return "", err
		}
	}
	return secret, nil
}

// RemoveGitHubSourceTx deletes the source owned by ownerFolder and the route
// rows its registration seeded. A cross-folder key matches zero rows.
func (s *Store) RemoveGitHubSourceTx(ctx context.Context, tx *sql.Tx, key, ownerFolder string) (bool, error) {
	it, ok, err := s.githubSourceItem(ctx, key)
	if err != nil {
		return false, err
	}
	// The owner is part of the predicate and not of the coordinate, so it is
	// compared here: a cross-folder key names an item this caller may not delete,
	// and the answer is the same "no such row" the statement gave.
	if !ok || it.Attrs[attrGHOwnerFolder] != ownerFolder {
		return false, nil
	}
	if _, err := s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: GitHubSources, Key: it.Key,
	}); err != nil {
		return false, err
	}
	routes, err := GitHubSourceRoutes(key, ownerFolder)
	if err != nil {
		return false, err
	}
	// The seeded rules are deleted by MATCH, which is the third field of a
	// route's coordinate rather than a leading fragment, so this reads the routes
	// partition and compares. It is 58 rules on the largest live instance, and a
	// revoke is an operator action.
	if err := s.deleteRoutesMatchingTx(ctx, tx, routes); err != nil {
		return false, err
	}
	return true, nil
}

// deleteRoutesMatchingTx removes every route rule whose `match` is one of the
// rules a registration seeded. A rule the operator has since edited away is
// simply not there, which is what the statement's zero matched rows said.
func (s *Store) deleteRoutesMatchingTx(ctx context.Context, tx *sql.Tx, seeded []core.Route) error {
	wanted := make(map[string]bool, len(seeded))
	for _, r := range seeded {
		wanted[r.Match] = true
	}
	items, err := s.reads(tx).Query(ctx, objstore.Query{Entity: Routes, Partition: OwnerRoutd})
	if err != nil {
		return fmt.Errorf("read routes: %w", err)
	}
	var writes []objstore.Write
	for _, it := range items {
		r, err := routeFrom(it)
		if err != nil {
			return err
		}
		if !wanted[r.Match] {
			continue
		}
		writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: Routes, Key: it.Key})
	}
	if len(writes) == 0 {
		return nil
	}
	_, err = applyChunks(ctx, "remove seeded routes", writes,
		func(ctx context.Context, chunk []objstore.Write) (bool, error) {
			return s.WriteEntitiesInTx(ctx, tx, nil, chunk...)
		})
	return err
}

func (s *Store) githubSourceEvent(action, key, owner string) audit.Event {
	actor, actorSub, surface := s.auditIdentity()
	return audit.Event{
		Category: audit.CategoryChannel,
		Action:   action,
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: "github_sources/" + key,
		Folder:   owner,
		Outcome:  audit.OutcomeOK,
	}
}

// AddGitHubSource is AddGitHubSourceTx in its own audited tx (the CLI path).
func (s *Store) AddGitHubSource(src GitHubSource, target string) (string, error) {
	var secret string
	err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		var err error
		secret, err = s.AddGitHubSourceTx(context.Background(), tx, src, target)
		return s.githubSourceEvent("github_source.register", src.Source, src.OwnerFolder), err
	})
	return secret, err
}

// RemoveGitHubSource is RemoveGitHubSourceTx in its own audited tx.
func (s *Store) RemoveGitHubSource(key, ownerFolder string) (bool, error) {
	var hit bool
	err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		var err error
		hit, err = s.RemoveGitHubSourceTx(context.Background(), tx, key, ownerFolder)
		return s.githubSourceEvent("github_source.revoke", key, ownerFolder), err
	})
	return hit, err
}

// ListGitHubSources returns the sources owned by ownerFolder; "" lists all. The
// read comes back in coordinate order, which IS `ORDER BY source`.
func (s *Store) ListGitHubSources(ownerFolder string) ([]GitHubSource, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: GitHubSources, Partition: OwnerRoutd,
	})
	if err != nil {
		return nil, fmt.Errorf("read github sources: %w", err)
	}
	var out []GitHubSource
	for _, it := range items {
		if ownerFolder != "" && it.Attrs[attrGHOwnerFolder] != ownerFolder {
			continue
		}
		out = append(out, githubSourceFrom(it))
	}
	return out, nil
}

// GitHubSourceSecret returns the raw secret for key, unsealed. ok=false when no
// such source exists.
func (s *Store) GitHubSourceSecret(key string) (secret string, ok bool, err error) {
	it, ok, err := s.githubSourceItem(context.Background(), key)
	if err != nil || !ok {
		return "", false, err
	}
	secret, err = s.open(it.Attrs[attrGHSecret])
	if err != nil {
		return "", false, err
	}
	return secret, true, nil
}
