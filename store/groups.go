package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/router"
)

func (s *Store) PutGroup(g core.Group) error {
	cfgJSON, _ := json.Marshal(g.Config)
	product := g.Product
	if product == "" {
		product = core.DefaultProduct
	}
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`INSERT INTO groups
			 (folder, added_at, container_config, product, updated_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(folder) DO UPDATE SET
			   container_config=excluded.container_config,
			   product=excluded.product,
			   updated_at=excluded.updated_at`,
			g.Folder,
			g.AddedAt.Format(time.RFC3339),
			string(cfgJSON),
			product,
			time.Now().Format(time.RFC3339),
		)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "group.upsert",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "groups/" + g.Folder,
			Folder:   g.Folder,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"product": product,
			},
		}, err
	})
}

// PutGroupRow upserts a group identity row WITHOUT emitting an audit_log row —
// the audit-free twin of PutGroup, for the CLI `group add`, which records the
// act itself with auditCLI; a second row would double-count one creation.
// Mirrors PutGroup's column set so the host-admin CLI writes the same shape
// dashd/onbod write.
func (s *Store) PutGroupRow(g core.Group) error {
	cfgJSON, _ := json.Marshal(g.Config)
	product := g.Product
	if product == "" {
		product = core.DefaultProduct
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO groups
		 (folder, added_at, container_config, product, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(folder) DO UPDATE SET
		   container_config=excluded.container_config,
		   product=excluded.product,
		   updated_at=excluded.updated_at`,
		g.Folder,
		g.AddedAt.Format(time.RFC3339),
		string(cfgJSON),
		product,
		time.Now().Format(time.RFC3339),
	); err != nil {
		return err
	}
	// A groups row implies the member edge, in the same transaction as the row
	// (BUGS L119).
	if err := SeedMemberRoleTx(tx, g.Folder); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteGroup removes the folder's subtree and every row that confers on it or
// routes into it, in one transaction, and returns how many groups rows went. It
// is the ONE delete: the operator's `arizuko group rm`, dashd's delete page and
// routd's registration rollback all land here. What it removes is declared once
// in folderScopePurge.
//
// The purge is the whole point, and it has to be total. A bare
// `DELETE FROM groups` leaves routes aimed at a folder that no longer resolves,
// engagement state pinned to it, and scheduled_tasks that timed keeps firing
// forever — sloth ran `Coach-mem-0..4` nightly from 2026-05-05 without one of
// them ever executing, and each firing reached onbod, which read the unroutable
// jid as a new account to onboard (BUGS L66, L67). Worse, an acl row scoped
// under the folder makes whoever administered it administer the next tenant of
// the same path (BUGS L198).
//
// The subtree, not the row: a child folder left behind is an orphan holding
// grants of its own, and a child's acl rows sit under the parent's path.
//
// One transaction, not one statement per table: a partial purge IS the state
// this exists to prevent. A folder with no groups row in its subtree returns 0
// and writes nothing, audit row included — there is no delete to record.
func (s *Store) DeleteGroup(folder string) (int, error) {
	actor, actorSub, surface := s.auditIdentity()
	removed := 0
	err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		ev := audit.Event{
			Category: audit.CategoryMutation,
			Action:   "group.delete",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: "groups/" + folder,
			Folder:   folder,
			Outcome:  audit.OutcomeOK,
		}
		n, err := purgeFolder(tx, folder)
		if err != nil {
			return ev, err
		}
		if n == 0 {
			return ev, errNoSuchGroup
		}
		removed = n
		ev.ParamsSummary = map[string]any{"groups_removed": n}
		return ev, nil
	})
	if errors.Is(err, errNoSuchGroup) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return removed, nil
}

const groupCols = `folder, added_at, container_config, product,
	COALESCE(json_extract(config, '$.model'), ''),
	json_extract(config, '$.open'),
	json_extract(config, '$.observe_window_messages'),
	json_extract(config, '$.observe_window_chars')`

func (s *Store) AllGroups() map[string]core.Group {
	rows, err := s.db.Query(`SELECT ` + groupCols + ` FROM groups`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]core.Group)
	for rows.Next() {
		g, ok := scanGroupFull(rows)
		if ok {
			out[g.Folder] = g
		}
	}
	if err := rows.Err(); err != nil {
		slog.Error("AllGroups iteration failed; failing closed", "err", err)
		return nil
	}
	return out
}

func (s *Store) DefaultFolderForJID(jid string) string {
	msg := core.Message{ChatJID: jid, Verb: "message"}
	t := router.ResolveRoute(msg, s.AllRoutes())
	return core.ParseRouteTarget(t).Folder
}

func scanGroupFull(r rowScanner) (core.Group, bool) {
	var g core.Group
	var addedAt string
	var cfgJSON *string
	var open, owMsgs, owChars sql.NullInt64

	if err := r.Scan(&g.Folder, &addedAt, &cfgJSON, &g.Product, &g.Model,
		&open, &owMsgs, &owChars); err != nil {
		return g, false
	}

	g.AddedAt, _ = time.Parse(time.RFC3339, addedAt)
	if cfgJSON != nil {
		json.Unmarshal([]byte(*cfgJSON), &g.Config)
	}
	// Defaults match IsGroupOpen and GroupObserveWindow exactly: absent `open`
	// means OPEN, and an unset window is -1 (no per-group cap) rather than 0,
	// which would read as a cap of nothing.
	g.Open = !open.Valid || open.Int64 != 0
	g.ObserveWindowMessages, g.ObserveWindowChars = -1, -1
	if owMsgs.Valid {
		g.ObserveWindowMessages = int(owMsgs.Int64)
	}
	if owChars.Valid {
		g.ObserveWindowChars = int(owChars.Int64)
	}
	return g, true
}

func (s *Store) GroupByFolder(folder string) (core.Group, bool) {
	row := s.db.QueryRow(`SELECT `+groupCols+` FROM groups WHERE folder = ?`, folder)
	return scanGroupFull(row)
}

// IsGroupOpen reports the folder's `open` flag. Missing rows default to
// true so a freshly-created or pre-migration group stays visible to its
// siblings until an operator explicitly closes it. Spec 6/F.
func (s *Store) IsGroupOpen(folder string) bool {
	var open sql.NullInt64
	s.db.QueryRow(
		`SELECT json_extract(config, '$.open') FROM groups WHERE folder = ?`,
		folder).Scan(&open)
	if !open.Valid {
		return true
	}
	return open.Int64 != 0
}

// SetGroupOpen flips the visibility bit. The row must exist (groups are
// created via SetupGroup/PutGroup before any caller can flip the flag).
func (s *Store) SetGroupOpen(folder string, open bool) error {
	_, err := s.db.Exec(
		`UPDATE groups
		   SET config = json_set(COALESCE(config, '{}'), '$.open', ?)
		 WHERE folder = ?`,
		btoi(open), folder)
	return err
}

// GroupObserveWindow returns the per-group observe-window caps. (-1,-1)
// means "not set; fall back to env defaults". Per-route caps still win
// over per-group; per-group still wins over env. Spec 6/F.
func (s *Store) GroupObserveWindow(folder string) (msgs, chars int) {
	var m, c sql.NullInt64
	s.db.QueryRow(
		`SELECT json_extract(config, '$.observe_window_messages'),
		        json_extract(config, '$.observe_window_chars')
		 FROM groups WHERE folder = ?`,
		folder,
	).Scan(&m, &c)
	msgs = -1
	chars = -1
	if m.Valid {
		msgs = int(m.Int64)
	}
	if c.Valid {
		chars = int(c.Int64)
	}
	return
}

// SetGroupObserveWindow writes per-group caps; pass -1 to clear (the
// gateway then falls back to the env default for that field). A cleared
// field is set to JSON null so json_extract yields NULL again.
func (s *Store) SetGroupObserveWindow(folder string, msgs, chars int) error {
	_, err := s.db.Exec(
		`UPDATE groups SET config = json_set(
		   json_set(COALESCE(config, '{}'),
		     '$.observe_window_messages',
		     CASE WHEN ? >= 0 THEN ? END),
		   '$.observe_window_chars',
		   CASE WHEN ? >= 0 THEN ? END)
		 WHERE folder = ?`,
		msgs, msgs, chars, chars, folder,
	)
	return err
}

// SetThreadReplies sets the per-group thread_replies preference. nil = reset to
// NULL (default: group chats thread, DMs do not). true = always thread. false =
// never thread (replies land inline regardless of chat kind).
func (s *Store) SetThreadReplies(folder string, on *bool) error {
	if on == nil {
		_, err := s.db.Exec(
			`UPDATE groups SET config = json_remove(config, '$.thread_replies')
			 WHERE folder = ?`, folder)
		return err
	}
	_, err := s.db.Exec(
		`UPDATE groups
		   SET config = json_set(COALESCE(config, '{}'), '$.thread_replies', ?)
		 WHERE folder = ?`,
		btoi(*on), folder)
	return err
}

// SiblingFolders returns folders that share folder's immediate parent,
// excluding folder itself and any closed sibling. Root folders (no
// parent) have no siblings — returns nil. Spec 6/F ambient join.
func (s *Store) SiblingFolders(folder string) []string {
	parent := groupfolder.ParentOf(folder)
	if parent == "" {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT folder FROM groups
		 WHERE folder LIKE ? AND folder != ?
		   AND COALESCE(json_extract(config, '$.open'), 1) = 1`,
		parent+"/%", folder,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if rows.Scan(&f) != nil {
			continue
		}
		// Direct children only — exclude grandchildren like "parent/x/y".
		if groupfolder.ParentOf(f) != parent {
			continue
		}
		out = append(out, f)
	}
	return out
}

// AddGroupWatcher registers observer to receive source's inbound messages
// as <observed> context. Idempotent. Spec: specs/5/F-topic-lineage.md.
func (s *Store) AddGroupWatcher(observer, source string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO group_watchers (observer, source) VALUES (?, ?)`,
		observer, source,
	)
	return err
}

// RemoveGroupWatcher cancels an observe_group subscription.
func (s *Store) RemoveGroupWatcher(observer, source string) error {
	_, err := s.db.Exec(
		`DELETE FROM group_watchers WHERE observer = ? AND source = ?`,
		observer, source,
	)
	return err
}

// WatchedSources returns the list of source folders that observer has
// subscribed to via observe_group.
func (s *Store) WatchedSources(observer string) []string {
	rows, err := s.db.Query(
		`SELECT source FROM group_watchers WHERE observer = ?`, observer,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil {
			out = append(out, f)
		}
	}
	return out
}

func (s *Store) SetStickyTopic(jid, topic string) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, sticky_topic) VALUES (?, ?)
		 ON CONFLICT(jid) DO UPDATE SET sticky_topic = excluded.sticky_topic`,
		jid, nilIfEmpty(topic),
	)
	return err
}

func (s *Store) GetStickyTopic(jid string) string {
	var topic sql.NullString
	s.db.QueryRow(`SELECT sticky_topic FROM chats WHERE jid = ?`, jid).Scan(&topic)
	return topic.String
}

// SetGroupModel sets the per-group model override. Empty string clears it
// (gateway then falls back to instance default).
func (s *Store) SetGroupModel(folder, model string) error {
	if model == "" {
		_, err := s.db.Exec(
			`UPDATE groups SET config = json_remove(config, '$.model')
			 WHERE folder = ?`, folder)
		return err
	}
	_, err := s.db.Exec(
		`UPDATE groups
		   SET config = json_set(COALESCE(config, '{}'), '$.model', ?)
		 WHERE folder = ?`,
		model, folder)
	return err
}
