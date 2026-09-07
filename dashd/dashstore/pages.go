package dashstore

import (
	"strings"

	"github.com/kronael/arizuko/groupfolder"
)

// InstalledPackage is one installed_packages row (spec 5/28). Folder is empty
// for an instance-wide install and names the group a product was blended into
// otherwise; without the column the two read as the same fact and a product
// looks like it installed a sidecar.
type InstalledPackage struct {
	Folder      string
	Name        string
	Source      string
	Revision    string
	InstalledAt string
}

// InstalledPackages lists the instance's packages, instance-wide rows first.
func (s *Store) InstalledPackages() ([]InstalledPackage, error) {
	rows, err := s.db.Query(`SELECT folder, name, source, revision, installed_at
		FROM installed_packages ORDER BY folder, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstalledPackage
	for rows.Next() {
		var p InstalledPackage
		if err := rows.Scan(&p.Folder, &p.Name, &p.Source, &p.Revision, &p.InstalledAt); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HeldApprovalCount counts calls still waiting for a verdict — the portal
// banner's number. The predicate mirrors routd's lazy expiry (applyExpiry: a
// held row past expires_at is expired, not waiting), and both timestamps are
// RFC3339 UTC, so string order is time order.
func (s *Store) HeldApprovalCount(now string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pending_actions
		  WHERE status='held' AND (expires_at IS NULL OR expires_at='' OR expires_at > ?)`,
		now).Scan(&n)
	return n, err
}

// ChatCooldown is one chat's last unprompted message.
type ChatCooldown struct {
	JID     string
	FiredAt string
}

// ProactiveCooldowns lists the chats that have spoken unprompted, most recent
// first, up to the table's page size.
func (s *Store) ProactiveCooldowns() ([]ChatCooldown, error) {
	rows, err := s.db.Query(
		`SELECT jid, COALESCE(proactive_last_fired_at,'')
		 FROM chat_proactive
		 WHERE proactive_last_fired_at IS NOT NULL
		 ORDER BY proactive_last_fired_at DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatCooldown
	for rows.Next() {
		var c ChatCooldown
		if err := rows.Scan(&c.JID, &c.FiredAt); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GroupRoute is one route on the group detail panel: enough to show where a
// message lands, not the whole row.
type GroupRoute struct {
	Seq    int
	Match  string
	Target string
}

// RoutesIntoFolder lists the routes that land in a folder or anywhere under it,
// in dispatch order, up to the panel's page size.
func (s *Store) RoutesIntoFolder(folder string) ([]GroupRoute, error) {
	rows, err := s.db.Query(
		`SELECT seq, match, target FROM routes WHERE `+
			groupfolder.SQLSubtree("target", "/")+
			` ORDER BY seq LIMIT 200`,
		groupfolder.SubtreeArgs(folder, "/")...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupRoute
	for rows.Next() {
		var g GroupRoute
		if err := rows.Scan(&g.Seq, &g.Match, &g.Target); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MessageActivity is a folder's all-time message count and the timestamp of the
// latest one, empty when the folder has never been written to.
type MessageActivity struct {
	Count  int
	LastAt string
}

// UsageBatch bounds how many folders go into one IN(...) list. SQLite caps
// bound parameters per statement (SQLITE_MAX_VARIABLE_NUMBER); an unbatched
// IN-list sized to the full folder count fails outright once an instance's
// group tree passes that ceiling, so MessageActivityByFolder chunks instead.
// It is exported because the ceiling is what the batching test has to exceed.
const UsageBatch = 500

// MessageActivityByFolder returns the message count and last-active timestamp
// per folder, for the folders asked about and no others. A folder with no
// messages is absent from the map rather than present with a zero, so the
// caller can tell "never used" from "used and counted zero".
func (s *Store) MessageActivityByFolder(folders []string) (map[string]MessageActivity, error) {
	out := map[string]MessageActivity{}
	for start := 0; start < len(folders); start += UsageBatch {
		batch := folders[start:min(start+UsageBatch, len(folders))]
		if err := s.messageActivityBatch(batch, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) messageActivityBatch(batch []string, out map[string]MessageActivity) error {
	ph := make([]string, len(batch))
	args := make([]any, len(batch))
	for i, f := range batch {
		ph[i] = "?"
		args[i] = f
	}
	in := strings.Join(ph, ",")

	rows, err := s.db.Query(
		`SELECT routed_to, COUNT(*), COALESCE(MAX(timestamp),'')
		 FROM messages
		 WHERE routed_to IN (`+in+`)
		 GROUP BY routed_to`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var folder, lastAt string
		var cnt int
		if err := rows.Scan(&folder, &cnt, &lastAt); err != nil {
			return err
		}
		out[folder] = MessageActivity{Count: cnt, LastAt: lastAt}
	}
	return rows.Err()
}
