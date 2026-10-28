package dashstore

import (
	"context"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// InstalledPackage is one installed_packages row (see compose/README.md). Folder is empty
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
//
// The coordinate is (folder, name) joined by store.KeySep, and the read arrives
// in coordinate order, which IS `ORDER BY folder, name`: the separator sorts
// below every byte a folder or a package name carries, so a shorter folder
// always precedes a longer one that extends it. Instance-wide rows carry the
// empty folder and therefore come first.
func (s *Store) InstalledPackages() ([]InstalledPackage, error) {
	items, err := s.obj.Query(context.Background(), objstore.Query{
		Entity: store.InstalledPackages, Partition: store.OwnerRoutd,
	})
	if err != nil {
		return nil, err
	}
	out := make([]InstalledPackage, 0, len(items))
	for _, it := range items {
		folder, name, err := store.InstalledPackageCoord(it)
		if err != nil {
			return nil, err
		}
		out = append(out, InstalledPackage{
			Folder:      folder,
			Name:        name,
			Source:      it.Attrs[store.AttrPkgSource],
			Revision:    it.Attrs[store.AttrPkgRevision],
			InstalledAt: it.Attrs[store.AttrPkgInstalledAt],
		})
	}
	return out, nil
}

// HeldApprovalFolders lists the folder of every call still waiting for a
// verdict, one entry per call — the portal banner counts the ones its caller may
// answer, and only the caller's own authorizer can decide that. The predicate
// mirrors routd's lazy expiry (applyExpiry: a held row past expires_at is
// expired, not waiting), and both timestamps are RFC3339 UTC, so string order is
// time order.
//
// A call with NO deadline waits forever, and there is exactly one way to say
// that: absent is the only null this model has, and routd leaves an empty field
// off the item (routd/pending_actions.go pendingAttrs), so a held call with no
// deadline carries no expires_at at all. The comparison itself runs in Go
// because the predicate is an OR and Where terms are conjuncts.
func (s *Store) HeldApprovalFolders(now string) ([]string, error) {
	items, err := s.obj.Query(context.Background(), objstore.Query{
		Entity: store.PendingActions, Partition: store.OwnerRoutd,
		Where: []objstore.Cmp{{Attr: store.AttrPendStatus, Op: objstore.Eq, Value: "held"}},
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, it := range items {
		if exp := it.Attrs[store.AttrPendExpiresAt]; exp == "" || exp > now {
			out = append(out, it.Attrs[store.AttrPendGroupFolder])
		}
	}
	return out, nil
}

// ChatCooldown is one chat's last unprompted message. It is an ALIAS: the item
// is store's, and a second copy of its two fields is a second place to drift.
type ChatCooldown = store.ChatCooldown

// cooldownPanelSize bounds the panel: past this many the answer is a report,
// not a panel.
const cooldownPanelSize = 50

// ProactiveCooldowns lists the chats that have spoken unprompted, most recent
// first, up to the panel's page size.
//
// The item's coordinate and attribute name are store's, so the read goes through
// store rather than rendering a second copy of them here (dashstore.st).
func (s *Store) ProactiveCooldowns() ([]ChatCooldown, error) {
	return s.st().ProactiveCooldowns(cooldownPanelSize)
}

// GroupRoute is one route on the group detail panel: enough to show where a
// message lands, not the whole row.
type GroupRoute struct {
	Seq    int
	Match  string
	Target string
}

// routesPanelSize bounds the panel: a group detail page shows where messages
// land, and past this many the answer is a routing table, not a panel.
const routesPanelSize = 200

// RoutesIntoFolder lists the routes that land in a folder or anywhere under it,
// in dispatch order, up to the panel's page size.
//
// The target is the second field of the routes coordinate, so containment is a
// comparison and not a key predicate: the read takes the table and filters on
// the SAME predicate the SQL half rendered — groupfolder's, with "/" as the
// separator, so the panel shows a folder and its children and never a topic.
// AllRoutes arrives in (seq, id) order, which is the order the router resolves
// in and therefore which 200 the panel keeps.
func (s *Store) RoutesIntoFolder(folder string) ([]GroupRoute, error) {
	all, err := s.st().RouteTable()
	if err != nil {
		return nil, err
	}
	out := make([]GroupRoute, 0, len(all))
	for _, r := range all {
		if !groupfolder.ContainsSep(folder, r.Target, "/") {
			continue
		}
		out = append(out, GroupRoute{Seq: r.Seq, Match: r.Match, Target: r.Target})
		if len(out) == routesPanelSize {
			break
		}
	}
	return out, nil
}

// MessageActivity is a folder's all-time message count and the timestamp of the
// latest one, empty when the folder has never been written to.
type MessageActivity struct {
	Count  int
	LastAt string
}

// MessageActivityByFolder returns the message count and last-active timestamp
// per folder, for the folders asked about and no others. A folder with no
// messages is absent from the map rather than present with a zero, so the
// caller can tell "never used" from "used and counted zero".
//
// THE COUNT IS AN ATTRIBUTE, NOT A GROUP BY. The statement this replaces scanned
// every message and folded it by `routed_to`; the count now lives on the folder,
// moved by the same write that files the message, so all 95 folders come back in
// ONE read. The last-active instant is the newest key of each folder's feed —
// one item each, never a scan of the folder's history.
func (s *Store) MessageActivityByFolder(folders []string) (map[string]MessageActivity, error) {
	ctx := context.Background()
	counts, err := s.st().FolderMessageCounts(ctx, folders)
	if err != nil {
		return nil, err
	}
	out := make(map[string]MessageActivity, len(counts))
	for folder, n := range counts {
		lastAt, err := s.st().FolderLastMessageAt(ctx, folder)
		if err != nil {
			return nil, err
		}
		out[folder] = MessageActivity{Count: n, LastAt: lastAt}
	}
	return out, nil
}
