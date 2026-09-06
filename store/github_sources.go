package store

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
)

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

// AddGitHubSourceTx mints the secret, seals it, inserts the source row and the
// route rows to target on tx. The raw secret is returned exactly once.
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
	secret := GenRouteToken()
	sealed, err := s.storeValue(secret)
	if err != nil {
		return "", err
	}
	ts := src.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO github_sources (source, secret, owner_folder, created_at) VALUES (?, ?, ?, ?)`,
		src.Source, sealed, src.OwnerFolder, core.TS(ts))
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("%w: %s", ErrGitHubSourceExists, src.Source)
	}
	for _, r := range routes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO routes (seq, match, target) VALUES (?, ?, ?)`,
			r.Seq, r.Match, r.Target); err != nil {
			return "", err
		}
	}
	return secret, nil
}

// RemoveGitHubSourceTx deletes the source owned by ownerFolder and the route
// rows its registration seeded. A cross-folder key matches zero rows.
func (s *Store) RemoveGitHubSourceTx(ctx context.Context, tx *sql.Tx, key, ownerFolder string) (bool, error) {
	res, err := tx.ExecContext(ctx,
		`DELETE FROM github_sources WHERE source = ? AND owner_folder = ?`, key, ownerFolder)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	routes, err := GitHubSourceRoutes(key, ownerFolder)
	if err != nil {
		return false, err
	}
	for _, r := range routes {
		if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE match = ?`, r.Match); err != nil {
			return false, err
		}
	}
	return true, nil
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

// ListGitHubSources returns the sources owned by ownerFolder; "" lists all.
func (s *Store) ListGitHubSources(ownerFolder string) ([]GitHubSource, error) {
	rows, err := s.db.Query(
		`SELECT source, owner_folder, created_at FROM github_sources
		 WHERE (? = '' OR owner_folder = ?) ORDER BY source`, ownerFolder, ownerFolder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubSource
	for rows.Next() {
		var g GitHubSource
		var created string
		if err := rows.Scan(&g.Source, &g.OwnerFolder, &created); err != nil {
			return nil, err
		}
		g.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, g)
	}
	return out, rows.Err()
}

// GitHubSourceSecret returns the raw secret for key, unsealed. ok=false when no
// such source exists.
func (s *Store) GitHubSourceSecret(key string) (secret string, ok bool, err error) {
	var sealed string
	err = s.db.QueryRow(`SELECT secret FROM github_sources WHERE source = ?`, key).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	secret, err = s.open(sealed)
	if err != nil {
		return "", false, err
	}
	return secret, true, nil
}
