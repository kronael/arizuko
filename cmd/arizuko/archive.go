package main

// Spec 5/8 "The full-instance archive": `arizuko archive export`/`archive
// apply` — a single tar file superset CONTAINING the config manifest, not a
// bigger manifest. routd.yaml/onbod.yaml are byte-identical to what
// `arizuko export` produces standalone (same resreg.Export/EmitYAML call,
// literally); everything else here (routd.secrets.yaml, routd.messages.jl,
// groups.tar, archive.yaml) is new.
//
// The filesystem restore claims each folder's runed run slot before
// overwriting its tree (spec 5/8 "Filesystem restore claims the folder's run
// slot"): apply POSTs /v1/holds to a LIVE runed and releases in a defer, so
// no agent turn can start mid-extraction. Either the instance is up and the
// hold is taken, or the operator passes --stopped to assert it is down.
// Never neither — an unguarded restore is the exact race the hold exists to
// prevent, so an unreachable runed is fatal, not a warning.
//
// The config documents ride applyDocs (apply.go), the same two-phase path
// `arizuko apply` uses: buffer every config document, run the missing-group
// preflight across the whole set, then one transaction per subsystem with a
// pre-image rollback if a later one fails. Buffering is what makes both
// possible — a streamed one-at-a-time apply would have committed routd before
// it had even read onbod.yaml.

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
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
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
	"gopkg.in/yaml.v3"
)

// Tar entry names — spec 5/8 "The full-instance archive" §"Shape". Fixed
// write order (archive.yaml, routd.yaml, onbod.yaml, routd.secrets.yaml,
// routd.messages.jl, groups.tar) is load-bearing: apply processes the tar
// as one sequential pass and needs archive.yaml's format_version checked
// before anything else, and routd.yaml's `groups` rows known before
// groups.tar's folder-prefix matching runs.
const (
	archiveMetaEntry     = "archive.yaml"
	archiveRoutdEntry    = "routd.yaml"
	archiveOnbodEntry    = "onbod.yaml"
	archiveSecretsEntry  = "routd.secrets.yaml"
	archiveMessagesEntry = "routd.messages.jl"
	archiveGroupsEntry   = "groups.tar"
)

// archiveSecretsDoc is routd.secrets.yaml's shape: secret + route-token/
// invite VALUES (spec 5/8 "Secret and token values") — kept separate from
// routd.yaml/onbod.yaml so a plain `arizuko export` reader is never handed
// one. It composes types from both store (secrets) and resreg (tokens/
// invites) because it's a cross-subsystem, archive-only document that
// neither package owns alone.
type archiveSecretsDoc struct {
	Secrets     []store.ArchiveSecretRow      `yaml:"secrets,omitempty"`
	RouteTokens []resreg.ArchiveRouteTokenRow `yaml:"route_tokens,omitempty"`
	Invites     []resreg.ArchiveInviteRow     `yaml:"invites,omitempty"`
	// Onboarding carries pending admissions WITH their token_ref verifier —
	// the one column the onboarding resource omits from its RowType so no read
	// surface can render it. Admissions cannot ride the config lane (that
	// resource is SkipApplyRebuild, and rebuilding it from a token_ref-less
	// RowType would null every live setup link) and cannot be rederived on
	// import (agent_cursor marks the route-missed message seen, so it is never
	// re-offered to routeMiss). Spec 5/8, BUGS Z3.
	Onboarding []resreg.ArchiveOnboardingRow `yaml:"onboarding,omitempty"`
}

func cmdArchive(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko archive <export|apply> ...")
		os.Exit(1)
	}
	switch args[0] {
	case "export":
		cmdArchiveExport(args[1:])
	case "apply":
		cmdArchiveApply(args[1:])
	default:
		die("unknown archive subcommand: %s", args[0])
	}
}

func cmdArchiveExport(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko archive export <instance> [file] [--quiesced]")
		os.Exit(1)
	}
	instance := args[0]
	file := ""
	quiesced := false
	for _, a := range args[1:] {
		if a == "--quiesced" {
			quiesced = true
			continue
		}
		if file == "" {
			file = a
		}
	}
	dataDir := mustInstanceDir(instance)
	stores, err := openSubsystemStores(dataDir)
	if err != nil {
		die("Failed: open store: %v", err)
	}
	defer closeStores(stores)

	var buf bytes.Buffer
	report, err := buildArchive(context.Background(), stores, dataDir, quiesced, &buf)
	if err != nil {
		die("Failed: archive export: %v", err)
	}
	if file != "" {
		if err := os.WriteFile(file, buf.Bytes(), 0o644); err != nil {
			die("Failed: write %s: %v", file, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", file, buf.Len())
	} else {
		os.Stdout.Write(buf.Bytes())
	}
	fmt.Fprint(os.Stderr, report)
}

func cmdArchiveApply(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko archive apply <instance> <archive.tar> [--force] [--stopped]")
		os.Exit(1)
	}
	instance := args[0]
	file := args[1]
	force := false
	stopped := false
	for _, a := range args[2:] {
		switch a {
		case "--force", "-f":
			force = true
		case "--stopped":
			stopped = true
		}
	}
	dataDir := mustInstanceDir(instance)
	stores, err := openSubsystemStores(dataDir)
	if err != nil {
		die("Failed: open store: %v", err)
	}
	defer closeStores(stores)

	ctx := context.Background()
	// Resolve the run-slot claim BEFORE opening the archive: a restore that
	// cannot guard the filesystem step must not half-apply the config
	// subsystems first (spec 5/8).
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

	data, err := os.ReadFile(file)
	if err != nil {
		die("Failed: read %s: %v", file, err)
	}
	report, err := applyArchive(ctx, stores, dataDir, bytes.NewReader(data), force, hold)
	if err != nil {
		fmt.Fprint(os.Stderr, report)
		die("Failed: archive apply: %v", err)
	}
	fmt.Fprint(os.Stdout, report)
}

// holdFn claims folder's runed run slot for the duration of its filesystem
// step and returns the release. A nil holdFn means the caller asserted the
// instance is stopped (--stopped): no live agent exists to race, so no hold
// is needed. It is never nil merely because runed was unreachable — that is
// fatal at the call site above.
type holdFn func(folder string) (release func(), err error)

// runedHolder builds the holdFn that claims each folder's run slot in the
// LIVE runed (POST /v1/holds — spec 5/8) before its tree is overwritten.
//
// RUNED_URL is the same var routd and dashd already use to reach runed, with
// the same in-network default; the CLI runs on the host, so an operator
// restoring from there points it at a reachable runed. RUNED_TOKEN is the
// bearer runed's authz wants — runs:run to claim, runs:kill to release; runed
// in open mode (single-tenant / local-dev) needs none.
func runedHolder(ctx context.Context, dataDir string) (holdFn, error) {
	env, err := loadInstanceEnv(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load .env: %w", err)
	}
	base := env["RUNED_URL"]
	if base == "" {
		base = "http://runed:8080"
	}
	client := runedv1.NewClient(base, env["RUNED_TOKEN"], 30*time.Second)

	return func(folder string) (func(), error) {
		out, herr := client.Hold(ctx, folder, "arizuko archive apply: filesystem restore")
		if herr != nil {
			return nil, fmt.Errorf("runed at %s: %w", base, herr)
		}
		if out.Busy {
			return nil, fmt.Errorf("folder %q has a live run (an agent turn or another hold); "+
				"wait for it to finish or stop the instance", folder)
		}
		return func() {
			// A defer cannot return an error, so surface a failed release
			// loudly instead of dropping it. runed's RunTTL
			// (RUNED_RUN_TIMEOUT) is the backstop that frees the folder.
			if rerr := client.ReleaseHold(context.Background(), out.RunID); rerr != nil {
				fmt.Fprintf(os.Stderr,
					"WARNING: failed to release the hold on %q (run %s): %v\n"+
						"  the folder stays paused until runed's RUNED_RUN_TIMEOUT expires it\n",
					folder, out.RunID, rerr)
			}
		}, nil
	}, nil
}

// buildArchive writes one tar stream to w — the same subsystem set + fixed
// order documented on the entry-name consts above — and returns a
// human-readable summary. Reused directly by tests (avoids exercising the
// os.Exit paths in cmdArchiveExport).
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
		manifest, checksum, snapshotAt, err := resreg.ExportSnapshot(ctx, stores[subsystem].DB(), subsystem)
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

	routdDB := stores[resreg.SubsystemRoutd].DB()
	onbodDB := stores[resreg.SubsystemOnbod].DB()

	secretRows, err := stores[resreg.SubsystemRoutd].ExportSecretRows(ctx)
	if err != nil {
		return "", fmt.Errorf("export secrets: %w", err)
	}
	tokenRows, err := resreg.ExportRouteTokens(ctx, routdDB)
	if err != nil {
		return "", fmt.Errorf("export route_tokens: %w", err)
	}
	onboardingRows, err := resreg.ExportOnboarding(ctx, onbodDB)
	if err != nil {
		return report.String(), err
	}
	inviteRows, err := resreg.ExportInvites(ctx, onbodDB)
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
	nMsg, err := resreg.ExportMessagesJSONL(ctx, routdDB, &msgBuf)
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
		Name: name, Size: int64(len(data)), Mode: 0o644,
		ModTime: time.Now(), Typeflag: tar.TypeReg,
	}); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("tar write %s: %w", name, err)
	}
	return nil
}

// tarGroups tars groups/<folder>/ for every folder, as ONE nested tar
// written to w (spec 5/8 "Group filesystem trees": "groups.tar stays a
// tar-inside-the-tar rather than flattened entries"). A folder with a
// groups row but no on-disk dir yet (never provisioned) is skipped, not an
// error. Returns the number of folders actually archived.
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

// tarFolderTree walks root (a group's on-disk dir) and writes every entry
// under name folder+"/"+relpath. Symlinks are skipped, never followed —
// mirrors chanlib.CopyDirNoSymlinks' discipline.
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
			return nil // sockets/devices/etc: never archived
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
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

// applyArchive reads one tar stream and restores it, in one sequential
// pass (entry order = write order above). Returns a human-readable summary
// regardless of error (a partial report is still useful when apply fails
// partway through — config/messages steps proceed even when a later
// document errors, per spec 5/8 "each has its own recovery story").
//
// hold claims each folder's runed run slot around its filesystem step; nil
// skips the claim (--stopped, or a test with no live instance).
func applyArchive(ctx context.Context, stores map[string]*store.Store, dataDir string, r io.Reader, force bool, hold holdFn) (string, error) {
	var report strings.Builder
	tr := tar.NewReader(r)
	var knownFolders []string
	sawMeta := false

	// The config documents are BUFFERED, not applied as they stream past.
	// Two spec rules need the whole set in hand before the first tx opens:
	// the missing-group preflight ("refuses the whole restore rather than
	// importing a half-wired instance") and the cross-subsystem pre-image
	// rollback, which must snapshot every subsystem it will touch. Streaming
	// them one-at-a-time could do neither — routd would already be committed
	// by the time onbod's document was read. Config is bounded declarative
	// config (spec 5/8 §"Three transports"), so buffering it costs nothing;
	// the unbounded documents (messages) stay streamed.
	var configDocs []parsedDoc
	configApplied := false
	// flushConfig commits the buffered config subsystems exactly once, before
	// anything that depends on them (groups.tar's folder matching needs the
	// groups rows) and at EOF.
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
		lines, err := applyDocs(ctx, stores, configDocs, force, "archive-apply", "")
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
		// Everything past the config documents depends on config having
		// committed, so the first non-config entry closes the config phase.
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
			for _, d := range parsed {
				if rows, ok := d.manifest[resreg.GroupsResource].([]resources.GroupsRow); ok {
					for _, g := range rows {
						knownFolders = append(knownFolders, g.Folder)
					}
				}
			}
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
			routdDB := stores[resreg.SubsystemRoutd].DB()
			imported, chatJIDs, ierr := resreg.ImportMessagesJSONL(ctx, routdDB, tr, 0)
			if ierr != nil {
				return report.String(), fmt.Errorf("messages: %w", ierr)
			}
			nc, cerr := resreg.DeriveAgentCursors(ctx, routdDB, chatJIDs)
			if cerr != nil {
				return report.String(), fmt.Errorf("agent_cursor: %w", cerr)
			}
			fmt.Fprintf(&report, "messages: %d rows imported, %d chat cursors derived\n", imported, nc)

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
			// Forward-compat: an entry this binary doesn't recognize. The
			// format_version check above already gates the load-bearing
			// case (a genuinely newer shape); an unknown-but-declared-old
			// entry is not fatal.
			io.Copy(io.Discard, tr) //nolint:errcheck // draining an ignored entry; nothing actionable
		}
	}
	// A config-only archive never hits a non-config entry, so EOF is where its
	// config phase commits.
	if err := flushConfig(); err != nil {
		return report.String(), err
	}
	return report.String(), nil
}

// applySecretsDoc always validates+imports secrets (their own per-row keyring
// check is the correctness gate — no revive risk for a value with no
// revoke-by-delete lifecycle). The three documents that DO carry a credential
// verifier — route_tokens, invites, onboarding — ride ONE gate: off by
// default and, even with --force, refused unless the target table is already
// empty (spec 5/8 "the UPSERT lane can revive a revoked or consumed
// credential").
func applySecretsDoc(ctx context.Context, stores map[string]*store.Store, doc archiveSecretsDoc, force bool, report *strings.Builder) error {
	routdSt := stores[resreg.SubsystemRoutd]
	onbodSt := stores[resreg.SubsystemOnbod]

	n, err := routdSt.ValidateAndImportSecrets(ctx, doc.Secrets)
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	fmt.Fprintf(report, "secrets: %d rows validated + imported\n", n)

	if err := restoreGated(report, "route_tokens", len(doc.RouteTokens), force,
		func() (int, error) { return resreg.CountRouteTokens(ctx, routdSt.DB()) },
		func() (int, error) { return resreg.ImportRouteTokens(ctx, routdSt.DB(), doc.RouteTokens) }); err != nil {
		return err
	}
	if err := restoreGated(report, "invites", len(doc.Invites), force,
		func() (int, error) { return resreg.CountInvites(ctx, onbodSt.DB()) },
		func() (int, error) { return resreg.ImportInvites(ctx, onbodSt.DB(), doc.Invites) }); err != nil {
		return err
	}
	if err := restoreGated(report, "onboarding", len(doc.Onboarding), force,
		func() (int, error) { return resreg.CountOnboarding(ctx, onbodSt.DB()) },
		func() (int, error) { return resreg.ImportOnboarding(ctx, onbodSt.DB(), doc.Onboarding) }); err != nil {
		return err
	}
	return nil
}

// restoreGated is the single implementation of the credential-revival gate
// spec 5/8 §"Restoring onto a populated instance" states once and three
// documents obey: skip unless --force, and even then refuse unless the target
// table is proven empty (genuine DR onto a fresh instance, never a merge or a
// re-run that could clobber a post-export revocation). One helper rather than
// three copies — the third copy is where the policy would have drifted.
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

// matchFolder returns the longest folder in knownFoldersLongestFirst that
// entryName (a groups.tar path, e.g. "acme/eng/PERSONA.md") is rooted
// under. Folder names may themselves contain "/" (nested groups), so a
// plain first-segment split is ambiguous — "acme/eng/PERSONA.md" could be
// folder "acme" file "eng/PERSONA.md", or folder "acme/eng" file
// "PERSONA.md". Matching against the manifest's OWN folder set (known from
// routd.yaml, parsed earlier in this same tar pass) resolves it exactly.
func matchFolder(entryName string, knownFoldersLongestFirst []string) string {
	name := strings.TrimSuffix(entryName, "/")
	for _, f := range knownFoldersLongestFirst {
		if groupfolder.Contains(f, name) {
			return f
		}
	}
	return ""
}

// extractGroups extracts groups.tar's per-folder trees. Two passes over
// the same in-memory tar bytes: the first decides, per folder, whether the
// target dir is empty/missing (extract) or non-empty (skip unless force —
// spec 5/8 "Restoring onto a populated instance": "refuses a folder's
// filesystem step... when the target tree is non-empty, unless --force");
// the second actually writes only the allowed folders. Two passes avoid
// extracting some of a folder's files before discovering a later entry
// belongs to a folder that should have been skipped.
//
// hold (nil = --stopped) claims each to-be-written folder's runed run slot
// between the two passes — after the decision, before the first byte lands —
// and releases every one when this function returns. Holding the whole set
// for the whole write is deliberate: pass 2 interleaves entries across
// folders, so no folder is provably finished until the pass is.
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
				continue // never written; nothing to guard
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
		groupDir, rerr := resolver.GroupPath(folder)
		if rerr != nil {
			return extracted, skipped, rerr
		}
		if err := extractTarEntry(groupDir, folder, hdr, tr); err != nil {
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

// extractTarEntry writes one groups.tar entry under groupDir, refusing any
// path that would escape it (zip-slip defense) and any non-regular,
// non-directory entry type (symlinks/hardlinks/devices never travel —
// export never wrote one, so a hostile or corrupted archive is the only
// way one appears).
func extractTarEntry(groupDir, folder string, hdr *tar.Header, r io.Reader) error {
	rel := strings.TrimPrefix(strings.TrimSuffix(hdr.Name, "/"), folder)
	rel = strings.TrimPrefix(rel, "/")
	target := groupDir
	if rel != "" {
		target = filepath.Join(groupDir, rel)
	}
	cleanRel, rerr := filepath.Rel(groupDir, target)
	if rerr != nil || strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return fmt.Errorf("entry %q escapes its folder", hdr.Name)
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode) & 0o777
		if mode == 0 {
			mode = 0o644
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, r)
		return err
	default:
		return fmt.Errorf("entry %q: unsupported tar type %d", hdr.Name, hdr.Typeflag)
	}
}

// setupMissingGroupDirs is the archive-apply-specific slice of "Apply is a
// restore, so filesystem prep follows the commit" (spec 5/8, unchanged
// section): a folder the config manifest names but the archive's
// groups.tar doesn't cover (a narrower or older archive) still needs
// SOMETHING on disk, or routing docker-runs against a missing path exits
// 125. container.SetupGroup is the ONLY provisioning code path
// (CLAUDE.md "no parallel second path"); this calls it with no seed dir
// (an empty scaffold) for exactly the folders left untouched above.
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
		if err := container.SetupGroup(cfg, folder, ""); err != nil {
			return fmt.Errorf("setup group %q: %w", folder, err)
		}
	}
	return nil
}
