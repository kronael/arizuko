package store

// gdrive_files.go holds every read and write of `gdrive_files`: one item per
// Google Drive file a folder binds, addressed by its file id.
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd). gdrid reads the partition
// each sweep and polls exactly the files it names. The item holds no secret, so
// no keyring is involved. `created_at` is core.TS at 30 bytes, the width
// resreg's entity path stamps (BUGS L97).

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a file item carries. `file_id` is the coordinate.
const (
	attrGDOwnerFolder  = "owner_folder"
	attrGDTargetFolder = "target_folder"
	attrGDCreatedAt    = "created_at"
)

// gdriveFileIDRE is the file id alphabet. It holds no `/`, no `*` and no space,
// so an id spliced into a route match can neither add a path level nor widen
// into a glob. chanlib.ParseGDriveJID applies the same alphabet to a JID
// segment; chanlib imports this package, so the rule is stated here too.
var gdriveFileIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// GDriveFile is one Drive file bound to a folder (specs/6/45-gdrive-comments-channel.md).
// TargetFolder is where its comments are routed; OwnerFolder bounds who may
// revoke it.
type GDriveFile struct {
	FileID       string
	OwnerFolder  string
	TargetFolder string
	CreatedAt    time.Time
}

// CheckGDriveFileID reports why id cannot name a bound file.
func CheckGDriveFileID(id string) error {
	if !gdriveFileIDRE.MatchString(id) {
		return fmt.Errorf("file id must be [A-Za-z0-9_-]+, got %q", id)
	}
	return nil
}

// gdriveFileKey is the coordinate of one bound file.
func gdriveFileKey(fileID string) (objstore.Key, error) {
	if fileID == "" {
		return objstore.Key{}, errors.New("gdrive file carries no file id, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: fileID}, nil
}

// gdriveFileFrom reads one item back.
func gdriveFileFrom(it objstore.Item) GDriveFile {
	f := GDriveFile{
		FileID:       it.Key.Sort,
		OwnerFolder:  it.Attrs[attrGDOwnerFolder],
		TargetFolder: it.Attrs[attrGDTargetFolder],
	}
	f.CreatedAt, _ = time.Parse(core.TSLayout, it.Attrs[attrGDCreatedAt])
	return f
}

// gdriveFileItem reads the item one file id names.
func (s *Store) gdriveFileItem(ctx context.Context, fileID string) (objstore.Item, bool, error) {
	k, err := gdriveFileKey(fileID)
	if err != nil {
		return objstore.Item{}, false, err
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: GDriveFiles, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: k.Sort},
	})
	if err != nil {
		return objstore.Item{}, false, fmt.Errorf("read gdrive file %q: %w", fileID, err)
	}
	if len(items) == 0 {
		return objstore.Item{}, false, nil
	}
	return items[0], true, nil
}

// GDriveFileRoutes renders the route rows a binding seeds. Human posts
// (verb message, or mention) trigger a turn; every other event observes.
//
// The file id is spliced in LITERALLY, so a row matches the one file the
// operator bound and no other. An id outside gdriveFileIDRE is refused, which
// is what keeps a `*` out of the id position: a wildcard there lets an
// instruction injected into a comment reach every file the service account can
// read (BUGS L417 is that hole on GitHub). The `*` after `/c/` covers one
// comment id and never crosses `/`.
func GDriveFileRoutes(fileID, folder string) ([]core.Route, error) {
	if err := CheckGDriveFileID(fileID); err != nil {
		return nil, err
	}
	if folder == "" {
		return nil, errors.New("folder required")
	}
	file := "gdrive:" + fileID
	observe := folder + "#observe"
	return []core.Route{
		{Seq: 10, Match: "chat_jid=" + file + "/c/* verb=message", Target: folder},
		{Seq: 10, Match: "chat_jid=" + file + "/c/* verb=mention", Target: folder},
		{Seq: 100, Match: "chat_jid=" + file + "/c/*", Target: observe},
		{Seq: 100, Match: "chat_jid=" + file, Target: observe},
	}, nil
}

var ErrGDriveFileExists = errors.New("gdrive file already bound")

// AddGDriveFileInto buffers the file item and its route rows on the caller's
// write set. The file item is the one write that may refuse (refused is
// ErrGDriveFileExists' place); the seeded rules are Merges that tolerate a rule
// already there. Every route id is minted before anything is buffered
// (mintRouteIDs).
func (s *Store) AddGDriveFileInto(ctx context.Context, ws *WriteSet, f GDriveFile, refused error) error {
	if f.OwnerFolder == "" || f.TargetFolder == "" {
		return errors.New("owner_folder and target_folder required")
	}
	routes, err := GDriveFileRoutes(f.FileID, f.TargetFolder)
	if err != nil {
		return err
	}
	ids, err := s.mintRouteIDs(ctx, len(routes))
	if err != nil {
		return err
	}
	for i := range routes {
		routes[i].ID = ids[i]
	}
	ts := f.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	key, err := gdriveFileKey(f.FileID)
	if err != nil {
		return err
	}
	if err := ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Put, Entity: GDriveFiles, Key: key,
		Attrs: map[string]string{
			attrGDOwnerFolder:  f.OwnerFolder,
			attrGDTargetFolder: f.TargetFolder,
			attrGDCreatedAt:    core.TS(ts),
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}); err != nil {
		return err
	}
	for _, r := range routes {
		w, werr := routeCreateWrite(r, "", "")
		if werr != nil {
			return werr
		}
		ws.Add(w)
	}
	return nil
}

// RemoveGDriveFileInto buffers the deletion of the file owned by ownerFolder and
// of the route rows its binding seeded, rendered for the folder the BINDING
// names. A cross-folder id matches zero rows, and the answer comes from the
// READ; every write is unconditional, so the batch cannot refuse.
func (s *Store) RemoveGDriveFileInto(ctx context.Context, ws *WriteSet, fileID, ownerFolder string) (bool, error) {
	it, ok, err := s.gdriveFileItem(ctx, fileID)
	if err != nil {
		return false, err
	}
	if !ok || it.Attrs[attrGDOwnerFolder] != ownerFolder {
		return false, nil
	}
	ws.Add(objstore.Write{Op: objstore.Delete, Entity: GDriveFiles, Key: it.Key})
	routes, err := GDriveFileRoutes(fileID, it.Attrs[attrGDTargetFolder])
	if err != nil {
		return false, err
	}
	if err := s.deleteRoutesMatchingInto(ctx, ws, routes); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) gdriveFileEvent(action, fileID, owner string) audit.Event {
	actor, actorSub, surface := s.auditIdentity()
	return audit.Event{
		Category: audit.CategoryChannel,
		Action:   action,
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: "gdrive_files/" + fileID,
		Folder:   owner,
		Outcome:  audit.OutcomeOK,
	}
}

// AddGDriveFile binds a file and the rules that route it (the CLI path), in ONE
// batch with the row that records them: a file is never reachable by fewer
// rules than it asked for.
func (s *Store) AddGDriveFile(f GDriveFile) error {
	ctx := context.Background()
	ws := &WriteSet{}
	if err := s.AddGDriveFileInto(ctx, ws, f,
		fmt.Errorf("%w: %s", ErrGDriveFileExists, f.FileID)); err != nil {
		return err
	}
	_, rec := audit.WriteFor(s.gdriveFileEvent("gdrive_file.register", f.FileID, f.OwnerFolder))
	return s.Apply(ctx, ws, rec)
}

// RemoveGDriveFile unbinds a file and the rules that routed it, in ONE batch
// with the row that records it.
func (s *Store) RemoveGDriveFile(fileID, ownerFolder string) (bool, error) {
	ctx := context.Background()
	ws := &WriteSet{}
	hit, err := s.RemoveGDriveFileInto(ctx, ws, fileID, ownerFolder)
	if err != nil || !hit {
		return hit, err
	}
	_, rec := audit.WriteFor(s.gdriveFileEvent("gdrive_file.revoke", fileID, ownerFolder))
	if err := s.Apply(ctx, ws, rec); err != nil {
		return true, err
	}
	return true, nil
}

// ListGDriveFiles returns the files owned by ownerFolder; "" lists all.
func (s *Store) ListGDriveFiles(ownerFolder string) ([]GDriveFile, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: GDriveFiles, Partition: OwnerRoutd,
	})
	if err != nil {
		return nil, fmt.Errorf("read gdrive files: %w", err)
	}
	var out []GDriveFile
	for _, it := range items {
		if ownerFolder != "" && it.Attrs[attrGDOwnerFolder] != ownerFolder {
			continue
		}
		out = append(out, gdriveFileFrom(it))
	}
	return out, nil
}
