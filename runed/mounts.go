package runed

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/mountsec"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

const (
	// Where the public web tree appears in the guest, and the one subtree under
	// it that is the platform's rather than a group's (buildMounts states why).
	// Declared here because the mount, its test and
	// `ant/skills/arizuko/SKILL.md` must spell the same path.
	webRoot    = "/var/lib/www"
	webDocsDir = "arizuko"
	// ipcMount is where the agent's ipc dir appears in the guest. Declared here
	// because two readers need the same path: this renderer mounts it, and the
	// QEMU substrate's staging tree makes it a guest-local tmpfs instead — a
	// unix socket does not cross virtio-fs (spec 6/41 §"The files").
	ipcMount = "/run/ipc"
)

// buildMounts renders the mount table's right-hand side: the guest paths of
// ARCHITECTURE.md §"Container mount layout" and the host paths that back them.
// It creates the host side where a missing directory would otherwise be
// materialized by the substrate as root.
func buildMounts(
	cfg *core.Config, folders *groupfolder.Resolver,
	req runedv1.RunRequest, gc core.GroupConfig, groupDir string,
) []core.Mount {
	folder := string(req.Folder)
	elevated := req.Elevated
	var m []core.Mount

	// EVERY Host path here is the path RUNED sees, never a translated host path.
	// The substrate resolves these itself: virtiofsd is runed's own child, in
	// runed's own mount namespace. `hp()` used to map each one through
	// HOST_DATA_DIR because the Docker substrate handed the path to the docker
	// DAEMON, a process on the host — and after the flip that made every turn
	// fail to stage its tree, on every folder, while the fleet reported healthy
	// (BUGS `L368`). HOST_DATA_DIR remains a compose fact; runed does not read it.

	m = append(m, core.Mount{
		Host:      groupDir,
		Container: core.ContainerHome,
	})
	media := filepath.Join(groupDir, "media")
	os.MkdirAll(media, 0o755)

	// The staged copy of the release's `ant/`, not HOST_APP_DIR: see
	// container.MaterializeAppSrc.
	m = append(m, core.Mount{
		Host:      filepath.Join(cfg.ProjectRoot, container.AppSrcDir),
		Container: "/opt/arizuko",
		RO:        true,
	})

	// Every guest gets the world's shared dir at /var/lib/share (writable):
	// a per-world scratch/handoff space all its groups see. Elevated turns
	// (world == "") share the instance-wide groups/share. A `share_mount`
	// grant with readonly=true downgrades it to RO for that folder.
	world := worldOf(folder, elevated)
	share := filepath.Join(cfg.GroupsDir, world, "share")
	os.MkdirAll(share, 0o755)
	m = append(m, core.Mount{
		Host:      share,
		Container: "/var/lib/share",
		RO:        req.ShareReadOnly, // 5/33-paths-roles: a `share_mount(readonly=true)` grant downgrades to RO
	})

	os.MkdirAll(filepath.Join(groupDir, ".claude"), 0o755)

	ipcDir, err := folders.IpcPath(folder)
	if err == nil {
		os.MkdirAll(groupfolder.IpcInputDir(ipcDir), 0o755)
		m = append(m, core.Mount{
			Host:      ipcDir,
			Container: ipcMount,
		})
	}

	// ARIZUKO_DEV is documented as bool ("true" or "1") — parse it as one
	// rather than pinning a single literal spelling (BUGS F74: this compared
	// only "1" while every test and the dashd-playwright README use "true").
	if devMode, _ := strconv.ParseBool(os.Getenv("ARIZUKO_DEV")); devMode {
		runnerSrc := filepath.Join(cfg.HostAppDir, "ant", "src")
		if _, err := os.Stat(runnerSrc); err == nil {
			m = append(m, core.Mount{
				Host:      runnerSrc,
				Container: "/app/src",
			})
		}
	}

	// Layered codex mount: per-group writable dir first, then RO file overmounts for
	// shared creds. Order matters for volume layering. No os.Stat — HOST paths
	// are resolved by the substrate at spawn time.
	//
	// The group dir mounts UNCONDITIONALLY: it holds the rollouts `codex exec resume`
	// reads (spec 5/P-runed § "The container model"). Gating it on HostCodexDir
	// silently broke resume for anyone authenticating with OPENAI_API_KEY —
	// codex wrote its rollout to a guest-local path and every resume
	// afterwards found nothing.
	groupCodex := filepath.Join(groupDir, ".codex")
	os.MkdirAll(groupCodex, 0o755)
	m = append(m, core.Mount{Host: groupCodex, Container: core.ContainerHome + "/.codex"})
	if cfg.HostCodexDir != "" {
		container.SeedCodexMountTargets(groupCodex)
		runUID, _ := container.RunUID()
		for _, p := range unreadableCodexCreds(cfg.HostCodexDir, runUID) {
			slog.Error("codex credential not readable by the agent's uid — codex in "+
				"the guest will report 'not logged in'; grant that uid read on it",
				"path", p, "uid", runUID)
		}
		for _, name := range container.CodexROMounts {
			m = append(m, core.Mount{
				Host:      filepath.Join(cfg.HostCodexDir, name),
				Container: core.ContainerHome + "/.codex/" + name,
				RO:        true,
			})
		}
	}

	if len(gc.Mounts) > 0 {
		add := make([]mountsec.AdditionalMount, len(gc.Mounts))
		for i, cm := range gc.Mounts {
			ro := cm.RO
			add[i] = mountsec.AdditionalMount{HostPath: cm.Host, ContainerPath: cm.Container, Readonly: &ro}
		}
		al := mountsec.Allowlist{NonMainReadOnly: true}
		for _, p := range cfg.MountAllowedRoots {
			al.AllowedRoots = append(al.AllowedRoots, mountsec.AllowedRoot{Path: p})
		}
		for _, v := range mountsec.ValidateAdditionalMounts(add, folder, elevated, al) {
			m = append(m, core.Mount{Host: v.HostPath, Container: v.ContainerPath, RO: v.Readonly})
		}
	}

	// specs/5/V-web-vhosts.md: a folder with the `web:publish` grant (resolved by
	// routd, 5/33-paths-roles) gets RO access to the whole public web tree at /var/lib/www, plus
	// per-group bind-mount slots for the writable web surfaces (~/public_html and
	// ~/private_html). No grant → no writable web surface.
	//
	// The grant decides HOW WIDE the read-only view is, not whether there is one.
	// The whole tree stays a publisher privilege because it is every group's
	// published content, and a group may gate part of its own with an `auth`/`deny`
	// web_routes row that proxyd applies at the edge (proxyd.matchWebRoute) and a
	// bind mount does not — ungating the whole tree would hand every folder a read
	// path around those rows. `pub/arizuko` is not a group's content: it is the doc
	// site `arizuko create` copies from `template/web/pub/arizuko/`, and every
	// folder gets a skill whose whole job is to read it (`seedSkills` seeds
	// `ant/skills/arizuko`). So a non-publisher sees that subtree and only it.
	// Gating it too left the skill naming a directory 53 of krons' 59 folders did
	// not have, with no other copy in the guest: the image stages `ant/` +
	// `template/services` + CHANGELOG.md under /opt/arizuko, never `template/web/`.
	webHost := filepath.Join(cfg.WebDir, "pub", webDocsDir)
	webTarget := filepath.Join(webRoot, webDocsDir)
	if req.WebPublish {
		webHost, webTarget = filepath.Join(cfg.WebDir, "pub"), webRoot
	}
	if fi, err := os.Stat(webHost); err == nil && fi.IsDir() {
		m = append(m, core.Mount{
			Host:      webHost,
			Container: webTarget,
			RO:        true,
		})
	}

	if req.WebPublish {
		// ~/public_html: served at /pub/<folder>/ (no auth).
		pubGroupHost := filepath.Join(cfg.WebDir, "pub", folder)
		mkSlot(pubGroupHost)
		m = append(m, core.Mount{
			Host:      pubGroupHost,
			Container: filepath.Join(core.ContainerHome, "public_html"),
		})

		// ~/private_html: served at /priv/<folder>/ (OAuth/JWT).
		privGroupHost := filepath.Join(cfg.WebDir, "priv", folder)
		mkSlot(privGroupHost)
		m = append(m, core.Mount{
			Host:      privGroupHost,
			Container: filepath.Join(core.ContainerHome, "private_html"),
		})
	}

	// The all-groups RW mount is an operator-elevation privilege, NOT a folder
	// shape: only an elevated /root turn sees every group's files (spec: root
	// elimination). A normal top-level spawn is a tenant, confined to its own home.
	if elevated {
		m = append(m, core.Mount{
			Host:      cfg.GroupsDir,
			Container: "/var/lib/groups",
		})
	}

	return m
}

// worldOf returns the folder's world (top segment). An elevated (operator
// /root) turn spans all worlds, so it resolves to the instance-wide "".
func worldOf(folder string, elevated bool) string {
	if elevated {
		return ""
	}
	if before, _, ok := strings.Cut(folder, "/"); ok {
		return before
	}
	return folder
}

// mkSlot is the spawn-time backstop for a web slot container.SetupGroup should
// already have created (a group registered before cfg.WebDir was set, say).
// runed runs as uid 1000, so a slot it creates here is already agent-writable —
// but if the MkdirAll fails, the substrate materializes the bind source as root
// and the agent sees a mounted-but-unwritable directory. Report; never discard.
func mkSlot(dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("web slot missing and not creatable — the substrate will materialize it as root",
			"slot", dir, "err", err)
	}
}

// unreadableCodexCreds names the shared codex credentials that the agent's uid
// certainly cannot open. The RO overmount carries the HOST file's owner and
// mode into the guest, and `HOST_CODEX_DIR` is normally an operator's own
// ~/.codex at mode 0600, so the agent's uid gets EACCES on a file the substrate
// mounted successfully. codex then says "not logged in", which reads as an
// expired login and is a permission fault on the host.
//
// Only the certain case is named. A file the uid owns with the user-read bit,
// or one carrying the world-read bit, is reachable. A group-readable file may
// be reachable through a supplementary group this process cannot enumerate, so
// it is left alone: a wrong accusation here would train the operator to ignore
// the line that matters.
func unreadableCodexCreds(hostCodexDir string, uid int) []string {
	var out []string
	for _, name := range container.CodexROMounts {
		p := filepath.Join(hostCodexDir, name)
		fi, err := os.Stat(p)
		if err != nil {
			continue // absent is the mount's own failure to report, not this one
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		mode := fi.Mode().Perm()
		if mode&0o044 != 0 || (int(st.Uid) == uid && mode&0o400 != 0) {
			continue
		}
		out = append(out, p)
	}
	return out
}
