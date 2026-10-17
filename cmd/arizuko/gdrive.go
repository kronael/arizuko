package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// cmdGdrive: arizuko gdrive <instance> <subcommand> ...
//
//	arizuko gdrive <instance> add <file_id> <folder>
//	arizuko gdrive <instance> list [<folder>]
//	arizuko gdrive <instance> rm <file_id>
//
// add binds one Drive file to <folder> and seeds the route rows that deliver its
// comments there. The binding holds no secret, so the store opens without a
// keyring. Share the file with googd's service account before adding it.
func cmdGdrive(args []string) {
	need(args, 2, "arizuko gdrive <instance> <add|list|rm> ...")
	instance, sub := args[0], args[1]
	s := mustOpenACL(mustInstanceDir(instance)).AsCLI(os.Getenv("USER"))
	defer func() { _ = s.Close() }()

	switch sub {
	case "add":
		need(args, 4, "arizuko gdrive <instance> add <file_id> <folder>")
		if err := runGDriveAdd(s, args[2], args[3], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "list":
		folder := ""
		if len(args) > 2 {
			folder = args[2]
		}
		if err := runGDriveList(s, folder, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "rm":
		need(args, 3, "arizuko gdrive <instance> rm <file_id>")
		if err := runGDriveRm(s, args[2], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	default:
		die("unknown gdrive subcommand: %s", sub)
	}
}

func runGDriveAdd(s *store.Store, fileID, folder string, w io.Writer) error {
	if !groupfolder.IsValidFolder(folder) {
		return fmt.Errorf("invalid folder %q", folder)
	}
	if _, ok := s.GroupByFolder(folder); !ok {
		return fmt.Errorf("group %q not found", folder)
	}
	if err := s.AddGDriveFile(store.GDriveFile{FileID: fileID, OwnerFolder: folder, TargetFolder: folder}); err != nil {
		return fmt.Errorf("bind %s: %w", fileID, err)
	}
	_, _ = fmt.Fprintf(w, "bound %s; its comments route to %s\n", fileID, folder)
	return nil
}

func runGDriveList(s *store.Store, folder string, w io.Writer) error {
	rows, err := s.ListGDriveFiles(folder)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "FILE\tOWNER\tTARGET\tCREATED")
	for _, f := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.FileID, f.OwnerFolder, f.TargetFolder, f.CreatedAt.UTC().Format(time.RFC3339))
	}
	return tw.Flush()
}

// runGDriveRm names the file's owner from the binding itself: the operator CLI
// is not folder-scoped, so the owner is read, never typed.
func runGDriveRm(s *store.Store, fileID string, w io.Writer) error {
	rows, err := s.ListGDriveFiles("")
	if err != nil {
		return err
	}
	for _, f := range rows {
		if f.FileID != fileID {
			continue
		}
		if _, err := s.RemoveGDriveFile(fileID, f.OwnerFolder); err != nil {
			return fmt.Errorf("unbind %s: %w", fileID, err)
		}
		_, _ = fmt.Fprintf(w, "unbound %s (googd stops polling it; seeded routes removed)\n", fileID)
		return nil
	}
	return fmt.Errorf("no file %s is bound", fileID)
}
