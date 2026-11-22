package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
	"gopkg.in/yaml.v3"
)

const (
	archiveMetaEntry     = "archive.yaml"
	archiveRoutdEntry    = "routd.yaml"
	archiveOnbodEntry    = "onbod.yaml"
	archiveSecretsEntry  = "routd.secrets.yaml"
	archiveMessagesEntry = "routd.messages.jl"
	archiveGroupsEntry   = "groups.tar"
)

type archiveSecretsDoc struct {
	Secrets     []store.ArchiveSecretRow     `yaml:"secrets,omitempty"`
	RouteTokens []store.ArchiveRouteTokenRow `yaml:"route_tokens,omitempty"`
	Invites     []store.ArchiveInviteRow     `yaml:"invites,omitempty"`
	Onboarding  []store.ArchiveOnboardingRow `yaml:"onboarding,omitempty"`
}

func cmdDump(args []string) {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	var quiesced bool
	fs.BoolVar(&quiesced, "quiesced", false, "assert no daemon writes during the dump")
	if err := flexParse(fs, args); err != nil || fs.NArg() > 2 {
		usage(err, "arizuko dump [-i instance] [file] [--quiesced]")
	}
	instance, file := fs.Arg(0), fs.Arg(1)
	dataDir := mustInstanceDir(instance)
	stores := openSubsystemStores()
	defer closeStores(stores)

	var buf bytes.Buffer
	report, err := buildArchive(context.Background(), stores, dataDir, quiesced, &buf)
	if err != nil {
		die("Failed: dump: %v", err)
	}
	if file != "" {
		if err := writeArchiveFile(file, buf.Bytes()); err != nil {
			die("Failed: write %s: %v", file, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", file, buf.Len())
	} else {
		_, _ = os.Stdout.Write(buf.Bytes())
	}
	fmt.Fprint(os.Stderr, report)
}

func writeArchiveFile(file string, data []byte) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func restoreDump(ctx context.Context, stores map[string]*store.Store, dataDir string, data []byte, force, stopped bool) {
	var err error
	var hold holdFn
	if stopped {
		fmt.Fprintln(os.Stderr, "--stopped: skipping the run-slot hold; you asserted no agent is running")
	} else {
		hold, err = runedHolder(ctx, dataDir)
		if err != nil {
			die("Failed: cannot claim the folders' run slots: %v\n"+
				"A restore overwrites a folder's tree while an agent may be writing it. Either:\n"+
				"  - run this with the instance UP and RUNED_URL reachable from here\n"+
				"    (set RUNED_URL, and RUNED_TOKEN if runed verifies bearers), or\n"+
				"  - stop the instance and re-run with --stopped.", err)
		}
	}

	report, err := applyArchive(ctx, stores, dataDir, bytes.NewReader(data), force, hold)
	if err != nil {
		fmt.Fprint(os.Stderr, report)
		die("Failed: apply: %v", err)
	}
	_, _ = fmt.Fprint(os.Stdout, report)
}

type holdFn func(folder string) (release func(), err error)

func runedHolder(ctx context.Context, dataDir string) (holdFn, error) {
	client, err := runedClient(dataDir)
	if err != nil {
		return nil, err
	}
	base := client.BaseURL

	return func(folder string) (func(), error) {
		out, herr := client.Hold(ctx, folder, "arizuko apply: filesystem restore")
		if herr != nil {
			return nil, fmt.Errorf("runed at %s: %w", base, herr)
		}
		if out.Busy {
			return nil, fmt.Errorf("folder %q has a live run (an agent turn or another hold); "+
				"wait for it to finish or stop the instance", folder)
		}
		return func() {
			if rerr := client.Kill(context.Background(), out.RunID); rerr != nil {
				fmt.Fprintf(os.Stderr,
					"WARNING: failed to release the hold on %q (run %s): %v\n"+
						"  the folder stays paused until runed's RUNED_RUN_TIMEOUT expires it\n",
					folder, out.RunID, rerr)
			}
		}, nil
	}, nil
}

func buildArchive(ctx context.Context, stores map[string]*store.Store, dataDir string, quiesced bool, w io.Writer) (string, error) {
	var report strings.Builder
	tw := tar.NewWriter(w)

	consistency := resreg.ConsistencyLive
	if quiesced {
		consistency = resreg.ConsistencyQuiesced
	}
	meta := resreg.ArchiveManifest{
		FormatVersion: resreg.ArchiveFormatVersion,
		Consistency:   consistency,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Subsystems:    map[string]resreg.ArchiveSnapshot{},
	}
	manifests := map[string]map[string]any{}
	for _, subsystem := range subsystemOrder() {
		manifest, checksum, snapshotAt, err := resreg.ExportSnapshot(ctx, resreg.On(stores[subsystem].Obj()), subsystem)
		if err != nil {
			return "", fmt.Errorf("export snapshot %s: %w", subsystem, err)
		}
		manifests[subsystem] = manifest
		meta.Subsystems[subsystem] = resreg.ArchiveSnapshot{SnapshotAt: snapshotAt, Checksum: checksum}
	}

	metaBytes, err := yaml.Marshal(meta)
	if err != nil {
		return "", err
	}
	if err := writeTarEntry(tw, archiveMetaEntry, metaBytes); err != nil {
		return "", err
	}
	fmt.Fprintf(&report, "archive.yaml: format_version=%d consistency=%s\n", meta.FormatVersion, meta.Consistency)

	entryNames := map[string]string{resreg.SubsystemRoutd: archiveRoutdEntry, resreg.SubsystemOnbod: archiveOnbodEntry}
	for _, subsystem := range subsystemOrder() {
		manifest := manifests[subsystem]
		withChecksum := make(map[string]any, len(manifest)+1)
		for k, v := range manifest {
			withChecksum[k] = v
		}
		withChecksum["checksum"] = meta.Subsystems[subsystem].Checksum
		doc, err := resreg.EmitYAML(withChecksum)
		if err != nil {
			return "", err
		}
		if err := writeTarEntry(tw, entryNames[subsystem], doc); err != nil {
			return "", err
		}
		fmt.Fprintf(&report, "%s: exported (checksum %s)\n", subsystem, meta.Subsystems[subsystem].Checksum)
	}

	secretRows, err := stores[resreg.SubsystemRoutd].ExportSecretRows(ctx)
	if err != nil {
		return "", fmt.Errorf("export secrets: %w", err)
	}
	tokenRows, err := stores[resreg.SubsystemRoutd].ExportRouteTokens(ctx)
	if err != nil {
		return "", fmt.Errorf("export route_tokens: %w", err)
	}
	onboardingRows, err := stores[resreg.SubsystemOnbod].ExportOnboarding(ctx)
	if err != nil {
		return report.String(), err
	}
	inviteRows, err := stores[resreg.SubsystemOnbod].ExportInvites(ctx)
	if err != nil {
		return "", fmt.Errorf("export invites: %w", err)
	}
	secretsBytes, err := yaml.Marshal(archiveSecretsDoc{
		Secrets: secretRows, RouteTokens: tokenRows, Invites: inviteRows, Onboarding: onboardingRows,
	})
	if err != nil {
		return "", err
	}
	if err := writeTarEntry(tw, archiveSecretsEntry, secretsBytes); err != nil {
		return "", err
	}
	fmt.Fprintf(&report, "secrets: %d rows, route_tokens: %d, invites: %d\n", len(secretRows), len(tokenRows), len(inviteRows))

	var msgBuf bytes.Buffer
	nMsg, err := stores[resreg.SubsystemRoutd].ExportMessagesJSONL(ctx, &msgBuf)
	if err != nil {
		return "", fmt.Errorf("export messages: %w", err)
	}
	if err := writeTarEntry(tw, archiveMessagesEntry, msgBuf.Bytes()); err != nil {
		return "", err
	}
	fmt.Fprintf(&report, "messages: %d rows\n", nMsg)

	groupsRows, _ := manifests[resreg.SubsystemRoutd]["groups"].([]resources.GroupsRow)
	folders := make([]string, 0, len(groupsRows))
	for _, g := range groupsRows {
		folders = append(folders, g.Folder)
	}
	sort.Strings(folders)
	var groupsBuf bytes.Buffer
	nFolders, err := tarGroups(dataDir, folders, &groupsBuf)
	if err != nil {
		return "", fmt.Errorf("tar groups: %w", err)
	}
	if err := writeTarEntry(tw, archiveGroupsEntry, groupsBuf.Bytes()); err != nil {
		return "", err
	}
	fmt.Fprintf(&report, "groups.tar: %d folders\n", nFolders)

	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("close tar: %w", err)
	}
	return report.String(), nil
}

func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Size: int64(len(data)), Mode: 0o600,
		ModTime: time.Now(), Typeflag: tar.TypeReg,
	}); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("tar write %s: %w", name, err)
	}
	return nil
}

func tarGroups(dataDir string, folders []string, w io.Writer) (int, error) {
	resolver := &groupfolder.Resolver{GroupsDir: filepath.Join(dataDir, "groups")}
	tw := tar.NewWriter(w)
	n := 0
	for _, folder := range folders {
		if !groupfolder.IsValidFolder(folder) {
			return n, fmt.Errorf("refusing to archive invalid folder name %q", folder)
		}
		groupDir, err := resolver.GroupPath(folder)
		if err != nil {
			return n, fmt.Errorf("resolve folder %q: %w", folder, err)
		}
		if _, err := os.Stat(groupDir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return n, err
		}
		if err := tarFolderTree(tw, groupDir, folder); err != nil {
			return n, fmt.Errorf("tar folder %q: %w", folder, err)
		}
		n++
	}
	if err := tw.Close(); err != nil {
		return n, fmt.Errorf("close groups.tar: %w", err)
	}
	return n, nil
}

func tarFolderTree(tw *tar.Writer, root, folder string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := folder
		if rel != "." {
			name = folder + "/" + filepath.ToSlash(rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{
				Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: info.ModTime(),
			})
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: int64(info.Mode().Perm()),
			Size: info.Size(), ModTime: info.ModTime(),
		}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
}

func applyArchive(ctx context.Context, stores map[string]*store.Store, dataDir string, r io.Reader, force bool, hold holdFn) (string, error) {
	var report strings.Builder
	tr := tar.NewReader(r)
	var knownFolders []string
	sawMeta := false

	var configDocs []parsedDoc
	configApplied := false
	flushConfig := func() error {
		if configApplied {
			return nil
		}
		configApplied = true
		if len(configDocs) == 0 {
			return nil
		}
		if bad := preflightFolders(stores, configDocs); len(bad) > 0 {
			return fmt.Errorf("archive references folders that are not groups: %w", errors.Join(bad...))
		}
		lines, err := applyDocs(ctx, stores, configDocs, applyRun{force: force, actor: "archive-apply"})
		for _, l := range lines {
			fmt.Fprintf(&report, "%s\n", l)
		}
		return err
	}

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return report.String(), fmt.Errorf("read tar: %w", err)
		}
		switch hdr.Name {
		case archiveMetaEntry, archiveRoutdEntry, archiveOnbodEntry:
		default:
			if ferr := flushConfig(); ferr != nil {
				return report.String(), ferr
			}
		}
		switch hdr.Name {
		case archiveMetaEntry:
			data, rerr := io.ReadAll(tr)
			if rerr != nil {
				return report.String(), rerr
			}
			var meta resreg.ArchiveManifest
			if uerr := yaml.Unmarshal(data, &meta); uerr != nil {
				return report.String(), fmt.Errorf("%s: %w", archiveMetaEntry, uerr)
			}
			if meta.FormatVersion > resreg.ArchiveFormatVersion {
				return report.String(), fmt.Errorf(
					"archive format_version %d exceeds this binary's %d — rebuild arizuko first",
					meta.FormatVersion, resreg.ArchiveFormatVersion)
			}
			sawMeta = true
			fmt.Fprintf(&report, "archive.yaml: format_version=%d consistency=%s\n", meta.FormatVersion, meta.Consistency)

		case archiveRoutdEntry, archiveOnbodEntry:
			if !sawMeta {
				return report.String(), fmt.Errorf("%s arrived before %s — refusing (format_version unchecked)", hdr.Name, archiveMetaEntry)
			}
			if configApplied {
				return report.String(), fmt.Errorf("%s arrived after the config phase had already committed", hdr.Name)
			}
			data, rerr := io.ReadAll(tr)
			if rerr != nil {
				return report.String(), rerr
			}
			parsed, perr := parseDocs(hdr.Name, data)
			if perr != nil {
				return report.String(), fmt.Errorf("%s: %w", hdr.Name, perr)
			}
			knownFolders = append(knownFolders, groupFolders(parsed)...)
			configDocs = append(configDocs, parsed...)

		case archiveSecretsEntry:
			data, rerr := io.ReadAll(tr)
			if rerr != nil {
				return report.String(), rerr
			}
			var doc archiveSecretsDoc
			if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
				return report.String(), fmt.Errorf("%s: %w", hdr.Name, uerr)
			}
			if err := applySecretsDoc(ctx, stores, doc, force, &report); err != nil {
				return report.String(), err
			}

		case archiveMessagesEntry:
			routdSt := stores[resreg.SubsystemRoutd]
			imported, chatJIDs, refused, ierr := routdSt.ImportMessagesJSONL(ctx, tr, 0)
			if ierr != nil {
				return report.String(), fmt.Errorf("messages: %w", ierr)
			}
			nc, cerr := routdSt.DeriveAgentCursors(ctx, chatJIDs)
			if cerr != nil {
				return report.String(), fmt.Errorf("agent_cursor: %w", cerr)
			}
			fmt.Fprintf(&report, "messages: %d rows imported, %d chat cursors derived\n", imported, nc)
			for _, u := range refused {
				fmt.Fprintf(&report,
					"messages: REFUSED %s — timestamp %q is not an RFC3339 instant, so it cannot be ordered against a cursor; fix it in the archive and re-run\n",
					u.ID, u.Timestamp)
			}

		case archiveGroupsEntry:
			data, rerr := io.ReadAll(tr)
			if rerr != nil {
				return report.String(), rerr
			}
			extracted, skipped, gerr := extractGroups(dataDir, knownFolders, data, force, hold)
			if gerr != nil {
				return report.String(), fmt.Errorf("groups.tar: %w", gerr)
			}
			fmt.Fprintf(&report, "groups.tar: %d folders extracted, %d skipped (non-empty target, use --force)\n", extracted, skipped)
			if err := setupMissingGroupDirs(dataDir, knownFolders); err != nil {
				return report.String(), fmt.Errorf("setup fallback: %w", err)
			}

		default:
			io.Copy(io.Discard, tr) //nolint:errcheck // draining an ignored entry; nothing actionable
		}
	}
	if err := flushConfig(); err != nil {
		return report.String(), err
	}
	return report.String(), nil
}

func applySecretsDoc(ctx context.Context, stores map[string]*store.Store, doc archiveSecretsDoc, force bool, report *strings.Builder) error {
	routdSt := stores[resreg.SubsystemRoutd]
	onbodSt := stores[resreg.SubsystemOnbod]

	n, err := routdSt.ValidateAndImportSecrets(ctx, doc.Secrets)
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	fmt.Fprintf(report, "secrets: %d rows validated + imported\n", n)

	if err := restoreGated(report, "route_tokens", len(doc.RouteTokens), force,
		func() (int, error) { return routdSt.CountRouteTokens(ctx) },
		func() (int, error) { return routdSt.ImportRouteTokens(ctx, doc.RouteTokens) }); err != nil {
		return err
	}
	if err := restoreGated(report, "invites", len(doc.Invites), force,
		func() (int, error) { return onbodSt.CountInvites(ctx) },
		func() (int, error) { return onbodSt.ImportInvites(ctx, doc.Invites) }); err != nil {
		return err
	}
	if err := restoreGated(report, "onboarding", len(doc.Onboarding), force,
		func() (int, error) { return onbodSt.CountOnboarding(ctx) },
		func() (int, error) { return onbodSt.ImportOnboarding(ctx, doc.Onboarding) }); err != nil {
		return err
	}
	return nil
}

func restoreGated(report *strings.Builder, name string, n int, force bool,
	count func() (int, error), importRows func() (int, error),
) error {
	if n == 0 {
		return nil
	}
	if !force {
		fmt.Fprintf(report, "%s: skipped %d rows (revival risk — use --force onto a proven-empty target)\n", name, n)
		return nil
	}
	live, err := count()
	if err != nil {
		return fmt.Errorf("count %s: %w", name, err)
	}
	if live > 0 {
		fmt.Fprintf(report, "%s: skipped %d rows (--force requires an EMPTY target table; found %d live rows)\n", name, n, live)
		return nil
	}
	imported, err := importRows()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	fmt.Fprintf(report, "%s: %d rows restored (--force, target was empty)\n", name, imported)
	return nil
}

func matchFolder(entryName string, knownFoldersLongestFirst []string) string {
	name := strings.TrimSuffix(entryName, "/")
	for _, f := range knownFoldersLongestFirst {
		if groupfolder.Contains(f, name) {
			return f
		}
	}
	return ""
}

func extractGroups(dataDir string, knownFolders []string, tarBytes []byte, force bool, hold holdFn) (extracted, skipped int, err error) {
	longestFirst := append([]string(nil), knownFolders...)
	sort.Slice(longestFirst, func(i, j int) bool { return len(longestFirst[i]) > len(longestFirst[j]) })
	resolver := &groupfolder.Resolver{GroupsDir: filepath.Join(dataDir, "groups")}

	seen := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		hdr, terr := tr.Next()
		if errors.Is(terr, io.EOF) {
			break
		}
		if terr != nil {
			return 0, 0, terr
		}
		folder := matchFolder(hdr.Name, longestFirst)
		if folder == "" {
			return 0, 0, fmt.Errorf("entry %q matches no known folder", hdr.Name)
		}
		seen[folder] = true
	}

	allowed := map[string]bool{}
	folderNames := make([]string, 0, len(seen))
	for f := range seen {
		folderNames = append(folderNames, f)
	}
	sort.Strings(folderNames)
	for _, folder := range folderNames {
		groupDir, rerr := resolver.GroupPath(folder)
		if rerr != nil {
			return 0, 0, fmt.Errorf("resolve folder %q: %w", folder, rerr)
		}
		empty, eerr := dirEmptyOrMissing(groupDir)
		if eerr != nil {
			return 0, 0, eerr
		}
		if empty || force {
			allowed[folder] = true
		} else {
			skipped++
		}
	}

	if hold != nil {
		for _, folder := range folderNames {
			if !allowed[folder] {
				continue
			}
			release, herr := hold(folder)
			if herr != nil {
				return 0, 0, fmt.Errorf("claim run slot for %q: %w", folder, herr)
			}
			defer release()
		}
	}

	extractedSet := map[string]bool{}
	tr = tar.NewReader(bytes.NewReader(tarBytes))
	for {
		hdr, terr := tr.Next()
		if errors.Is(terr, io.EOF) {
			break
		}
		if terr != nil {
			return extracted, skipped, terr
		}
		folder := matchFolder(hdr.Name, longestFirst)
		if !allowed[folder] {
			continue
		}
		root, rerr := openRestoreGroup(resolver.GroupsDir, folder)
		if rerr != nil {
			return extracted, skipped, rerr
		}
		err := extractTarEntry(root, folder, hdr, tr)
		_ = root.Close()
		if err != nil {
			return extracted, skipped, fmt.Errorf("extract %s: %w", hdr.Name, err)
		}
		extractedSet[folder] = true
	}
	return len(extractedSet), skipped, nil
}

func dirEmptyOrMissing(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return len(entries) == 0, nil
}

func openRestoreGroup(groupsDir, folder string) (*os.Root, error) {
	if err := os.MkdirAll(groupsDir, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(groupsDir)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(folder, "/") {
		if err := root.Mkdir(part, 0o755); err != nil && !os.IsExist(err) {
			_ = root.Close()
			return nil, err
		}
		info, err := root.Lstat(part)
		if err != nil || !info.IsDir() {
			_ = root.Close()
			return nil, fmt.Errorf("invalid group directory %q", folder)
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		opened, err := next.Stat(".")
		_ = root.Close()
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			return nil, fmt.Errorf("group directory changed %q", folder)
		}
		root = next
	}
	return root, nil
}

func extractTarEntry(root *os.Root, folder string, hdr *tar.Header, r io.Reader) error {
	rel := strings.TrimPrefix(strings.TrimSuffix(hdr.Name, "/"), folder)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "."
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return root.MkdirAll(rel, 0o755)
	case tar.TypeReg:
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode) & 0o777
		if mode == 0 {
			mode = 0o644
		}
		f, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, r)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	default:
		return fmt.Errorf("entry %q: unsupported tar type %d", hdr.Name, hdr.Typeflag)
	}
}

func setupMissingGroupDirs(dataDir string, knownFolders []string) error {
	if len(knownFolders) == 0 {
		return nil
	}
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	for _, folder := range knownFolders {
		groupDir := filepath.Join(cfg.GroupsDir, folder)
		empty, eerr := dirEmptyOrMissing(groupDir)
		if eerr != nil {
			return eerr
		}
		if !empty {
			continue
		}
		if err := container.SetupGroup(cfg, folder); err != nil {
			return fmt.Errorf("setup group %q: %w", folder, err)
		}
	}
	return nil
}
