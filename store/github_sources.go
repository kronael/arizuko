package store

// github_sources.go holds every read and write of `github_sources`: one item per
// webhook delivery source, addressed by its source key.
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd). The source key,
// "<kind>/<name>", is the string gitd resolves an inbound delivery by, so the
// verification read is one exact key. ListGitHubSources scans the partition:
// `owner_folder` is an attribute. Coordinate order IS `ORDER BY source`.
//
// `secret` is absent from the RowType (routd/resource_columns_test.go
// rowTypeGaps), so this file is its only writer: the sealed credential never
// reaches the export an operator reads. `created_at` is core.TS at 30 bytes,
// the width resreg's entity path stamps (BUGS L97).

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a source item carries. `source` is the coordinate.
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

// AddGitHubSourceInto mints the secret, seals it, and buffers the source item
// and the route rows on the caller's write set. The raw secret is returned
// exactly once and is GENERATED here, never read back out of the sealed
// attribute. The source item is the one write that may refuse (refused is
// ErrGitHubSourceExists' place); the seeded rules are Merges that tolerate a
// rule already there. Every route id is minted before anything is buffered
// (mintRouteIDs).
func (s *Store) AddGitHubSourceInto(ctx context.Context, ws *WriteSet, src GitHubSource,
	target string, refused error) (string, error) {
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
	ids, err := s.mintRouteIDs(ctx, len(routes))
	if err != nil {
		return "", err
	}
	for i := range routes {
		routes[i].ID = ids[i]
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
	// Create-if-absent: re-registering a source is REFUSED rather than
	// re-minting the secret GitHub is already signing with.
	if err := ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Put, Entity: GitHubSources, Key: key,
		Attrs: map[string]string{
			attrGHSecret:      sealed,
			attrGHOwnerFolder: src.OwnerFolder,
			attrGHCreatedAt:   core.TS(ts),
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}); err != nil {
		return "", err
	}
	for _, r := range routes {
		w, werr := routeCreateWrite(r, "", "")
		if werr != nil {
			return "", werr
		}
		ws.Add(w)
	}
	return secret, nil
}

// RemoveGitHubSourceInto buffers the deletion of the source owned by ownerFolder
// and of the route rows its registration seeded. A cross-folder key matches
// zero rows, and the answer comes from the READ; every write is unconditional,
// so the batch cannot refuse.
func (s *Store) RemoveGitHubSourceInto(ctx context.Context, ws *WriteSet, key, ownerFolder string) (bool, error) {
	it, ok, err := s.githubSourceItem(ctx, key)
	if err != nil {
		return false, err
	}
	// The owner is part of the predicate and not of the coordinate.
	if !ok || it.Attrs[attrGHOwnerFolder] != ownerFolder {
		return false, nil
	}
	ws.Add(objstore.Write{Op: objstore.Delete, Entity: GitHubSources, Key: it.Key})
	routes, err := GitHubSourceRoutes(key, ownerFolder)
	if err != nil {
		return false, err
	}
	// Deleted by MATCH, the third field of a route's coordinate, so this reads
	// the routes partition: 58 rules on the largest live instance.
	if err := s.deleteRoutesMatchingInto(ctx, ws, routes); err != nil {
		return false, err
	}
	return true, nil
}

// deleteRoutesMatchingInto buffers the removal of every route rule whose `match`
// is one a registration seeded. A rule the operator has since edited away is
// simply not there.
func (s *Store) deleteRoutesMatchingInto(ctx context.Context, ws *WriteSet, seeded []core.Route) error {
	wanted := make(map[string]bool, len(seeded))
	for _, r := range seeded {
		wanted[r.Match] = true
	}
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Routes, Partition: OwnerRoutd})
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
	ws.Add(writes...)
	return nil
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

// AddGitHubSource registers a source and the rules that route it (the CLI
// path), in ONE batch with the row that records them: a source is never
// reachable by fewer rules than it asked for.
func (s *Store) AddGitHubSource(src GitHubSource, target string) (string, error) {
	ctx := context.Background()
	ws := &WriteSet{}
	secret, err := s.AddGitHubSourceInto(ctx, ws, src, target,
		fmt.Errorf("%w: %s", ErrGitHubSourceExists, src.Source))
	if err != nil {
		return "", err
	}
	_, rec := audit.WriteFor(s.githubSourceEvent("github_source.register", src.Source, src.OwnerFolder))
	if err := s.Apply(ctx, ws, rec); err != nil {
		return "", err
	}
	return secret, nil
}

// RemoveGitHubSource removes a source and the rules that routed it, in ONE
// batch with the row that records it.
func (s *Store) RemoveGitHubSource(key, ownerFolder string) (bool, error) {
	ctx := context.Background()
	ws := &WriteSet{}
	hit, err := s.RemoveGitHubSourceInto(ctx, ws, key, ownerFolder)
	if err != nil || !hit {
		return hit, err
	}
	_, rec := audit.WriteFor(s.githubSourceEvent("github_source.revoke", key, ownerFolder))
	if err := s.Apply(ctx, ws, rec); err != nil {
		return true, err
	}
	return true, nil
}

// ListGitHubSources returns the sources owned by ownerFolder; "" lists all.
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
