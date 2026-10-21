package runed

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/core"

	"github.com/kronael/arizuko/container"
)

// vmRecord is one turn's guest, written to disk before anything is started and
// removed only when every process it names is confirmed gone.
//
// It carries EVERY pid the turn starts, not only QEMU's, because the disk scan
// is the only thing that reaps after a runed crash and it can reap only what the
// record names. A container substrate finds a turn's second process by container
// label; with four host processes and no label, the record IS the label (spec
// 6/41 §"What moves into `runed`").
//
// runed keeps no in-memory index of guests: this file and the pid table are the
// one source of truth, so two readers of the same instance see the same set.
type vmRecord struct {
	Handle string `json:"handle"`
	Folder string `json:"folder"`
	// CID is the guest's vsock context id, which is the turn's folder binding:
	// the host's vhost driver drops every packet whose source is not this CID
	// (spec 6/41 §"The peer credential").
	CID uint32 `json:"cid"`
	// CtlPort and McpPort are host-side vsock ports, bound before QEMU starts.
	CtlPort uint32 `json:"ctl_port"`
	McpPort uint32 `json:"mcp_port"`
	// Tap and its /30: the guest's only route is HostIP:ProxyPort. The reap
	// re-renders the rule set from these three and deletes exactly what it
	// inserted, so no rule outlives its guest.
	Tap       string `json:"tap"`
	GuestIP   string `json:"guest_ip"`
	HostIP    string `json:"host_ip"`
	ProxyPort int    `json:"proxy_port"`

	QemuPid       int   `json:"qemu_pid"`
	VirtiofsdPids []int `json:"virtiofsd_pids"`
	EgressPid     int   `json:"egress_pid"`
	// BridgePid is the uid-1000 child holding this turn's MCP path. It is a
	// process the turn starts, so the record names it for the same reason it
	// names the others.
	BridgePid int `json:"bridge_pid"`

	// TurnDir holds the staging tree, the vhost-user sockets and this record.
	TurnDir   string `json:"turn_dir"`
	CreatedAt string `json:"created_at"`
}

// vmRecordName is the file inside TurnDir. The disk scan keys on it, so a turn
// dir without it is a dir nothing reaps and nothing believes.
const vmRecordName = "record.json"

// pidSuffix is what virtiofsd appends to its socket path for its own pid file. It
// is the daemon's convention, not ours, and the teardown must name it (BUGS `L376`).
const pidSuffix = ".pid"

// vmDir resolves one handle's turn dir under the record root.
//
// The handle comes from the Manager, not from a caller, but it becomes a path
// here: a handle carrying a separator would put the tree — and the reap's
// RemoveAll — somewhere else entirely, so the one path this file builds is
// built from the base name and nothing else.
func vmDir(root, handle string) (string, error) {
	if handle == "" || strings.ContainsAny(handle, "/\\") || handle == "." || handle == ".." {
		return "", fmt.Errorf("vm record: handle %q is not a single path element", handle)
	}
	return filepath.Join(root, handle), nil
}

// save writes the record atomically: the disk scan may read it at any moment,
// and a half-written record names half a turn's processes.
func (r *vmRecord) save() error {
	if r.TurnDir == "" {
		return fmt.Errorf("vm record %q: no turn dir", r.Handle)
	}
	// 0o710 and owned by the agent's uid: the turn's virtiofsd runs UNPRIVILEGED
	// (the id map is legal for no other caller) and creates its socket AND its pid
	// file in here, while the tree/ below stays root-owned because root makes its
	// binds.
	if err := os.MkdirAll(r.TurnDir, 0o710); err != nil {
		return err
	}
	// The ROOT above it must be traversable by that uid too, and 0o711 is the
	// narrowest mode that allows it: reach your own turn dir, enumerate nobody
	// else's. Chmod and not MkdirAll's mode, because an instance that ran an older
	// runed already has the root at 0o700 and MkdirAll leaves an existing dir
	// alone — which is exactly how this shipped as "Permission denied" on a pid
	// file in a directory the daemon owned (BUGS `L372`).
	if err := os.Chmod(filepath.Dir(r.TurnDir), 0o711); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm root not traversable by the agent uid; the turn share will not open",
			"root", filepath.Dir(r.TurnDir), "err", err)
	}
	if uid, _ := container.RunUID(); uid > 0 {
		// Report and carry on: a record that cannot be chowned is a turn whose
		// virtiofsd will fail to create its socket, and THAT failure names the
		// socket, which is the better message.
		if err := os.Chown(r.TurnDir, uid, uid); err != nil && !os.IsNotExist(err) {
			slog.Warn("turn dir not chowned to the agent uid; the turn share may not open",
				"dir", r.TurnDir, "uid", uid, "err", err)
		}
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(r.TurnDir, vmRecordName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// loadVMRecord reads one turn dir's record.
func loadVMRecord(turnDir string) (*vmRecord, error) {
	b, err := os.ReadFile(filepath.Join(turnDir, vmRecordName))
	if err != nil {
		return nil, err
	}
	var r vmRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("vm record %s: %w", turnDir, err)
	}
	return &r, nil
}

// listVMRecords returns every record under the root, oldest first. A directory
// without a readable record is reported rather than skipped: it is a turn dir
// whose processes nothing can name, which is the state the record exists to
// prevent.
func listVMRecords(root string) ([]*vmRecord, []string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("vm records unreadable", "dir", root, "err", err)
		}
		return nil, nil
	}
	var out []*vmRecord
	var broken []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		rec, err := loadVMRecord(dir)
		if err != nil {
			// NO record and a CORRUPT record are different states and only one
			// needs a person. The record is written before anything starts, so a
			// dir without one is a turn whose reap finished — possibly leaving a
			// staging tree `remove` refused, which `reapVM` already logged by path
			// (BUGS L362). There is nothing to reap and nothing to decide, so
			// reporting it on every scan forever would bury the corrupt case that
			// does need reading. A record that EXISTS and will not parse still
			// reports: that is the property the disk scan is for.
			if os.IsNotExist(err) {
				continue
			}
			broken = append(broken, dir)
			continue
		}
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b *vmRecord) int { return strings.Compare(a.CreatedAt, b.CreatedAt) })
	return out, broken
}

// liveCIDs is the set the allocator must avoid: every CID a record on disk
// claims. Derived from the disk on every allocation rather than held in memory,
// so a runed that restarted mid-turn still sees the guest it left behind.
func liveCIDs(records []*vmRecord) map[uint32]bool {
	used := map[uint32]bool{}
	for _, r := range records {
		used[r.CID] = true
	}
	return used
}

// vsockCIDMin is the lowest CID a guest may hold: 0 is any, 1 is hypervisor,
// 2 is the host (vsock(7)).
const vsockCIDMin = 3

// allocateCID returns the lowest free guest CID at or above base, bounded by the
// concurrency cap: a cap of 5 needs 5 CIDs, and a request for a sixth is the
// Manager's slot accounting having failed rather than a CID shortage.
//
// base is an INSTANCE setting because the CID space is the HOST's, not an
// instance's. Two instances that both start at 3 ask the host for the same CID and
// the same derived vsock ports, and the second one's turn fails with
// "vsock bind port 5555: address already in use" — measured on this host the moment
// a second instance ran a turn (BUGS `L378`).
func allocateCID(used map[uint32]bool, cap int, base uint32) (uint32, error) {
	if cap < 1 {
		cap = 1
	}
	if base < vsockCIDMin {
		base = vsockCIDMin
	}
	for cid := base; cid < base+uint32(cap); cid++ {
		if !used[cid] {
			return cid, nil
		}
	}
	return 0, fmt.Errorf("no free guest CID in [%d,%d): %d live guests at MAX_CONCURRENT_CONTAINERS=%d",
		base, base+uint32(cap), len(used), cap)
}

// vsockPorts derives a guest's two host-side ports from its SLOT. Derived, not
// searched: the acceptance test binds "the port runed would take" before the
// turn starts, and a port runed picks by probing is a port no test can squat
// (spec 6/41 §"The peer credential"). A bind failure fails the turn — runed
// never moves to another port, because a silent retry is what would turn a
// denial into a guest talking to whoever answered.
//
// THE SLOT, not the CID. A slot is `cid - RUNED_VSOCK_CID_BASE`, so it runs 0 to
// MAX_CONCURRENT_CONTAINERS-1 whatever window the instance owns. Deriving from the
// absolute CID instead tied the two bases together: an instance at CID base 20 with
// port base 5655 got ports 5689 upward, neither number where the operator put it,
// and the tap subnet's index ran off the end of a /24 at any CID base over 64 —
// every turn failing, at an instance the precondition still called healthy (BUGS
// `L382`).
func vsockPorts(base uint32, slot uint32) (ctl, mcp uint32) {
	off := slot * 2
	return base + off, base + off + 1
}

// cidBase and portBase are one window, and an instance owns one window of each.
// The offsets are derived from the CID so the two stay in step: an instance whose
// CID base is higher gets ports higher by the same step, which is what keeps two
// instances on one host from asking for the same port.

// procs is the pid table the reap acts on. Tests replace it, so the reap's
// order and its confirm-gone rule are checked without starting a process.
type procs interface {
	signal(pid int, sig syscall.Signal) error
	alive(pid int) bool
	// cmdline returns /proc/<pid>/cmdline, NUL-separated as the kernel writes it.
	cmdline(pid int) (string, error)
}

// hostProcs is the production pid table.
type hostProcs struct{}

func (hostProcs) signal(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, sig)
}

func (hostProcs) alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func (hostProcs) cmdline(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	return string(b), err
}

// reapTarget is one process the reap ends.
//
// token is what the pid's own cmdline must contain before any signal is sent.
// A record outlives its processes by definition — that is what makes the disk
// scan work — and by the time a crashed runed restarts, the kernel may have
// handed that pid to something else. Signalling an unrelated process because a
// stale record named its number is the one failure a reaper must not have.
type reapTarget struct {
	pid   int
	what  string
	token string
}

// reapTargets lists this turn's processes in the order spec 6/41 §"The turn"
// step 9 fixes: QEMU first — nothing else may go while a guest can still write
// through the share — then the host processes that served it, then the two
// virtiofsd whose sockets the guest held.
func (r *vmRecord) reapTargets() []reapTarget {
	t := []reapTarget{{r.QemuPid, "qemu", r.TurnDir}}
	if r.EgressPid > 0 {
		// The proxy's argv carries --client <guest ip>, and one /30 belongs to
		// one live guest, so the address is this turn's own token.
		t = append(t, reapTarget{r.EgressPid, "egress", r.GuestIP})
	}
	if r.BridgePid > 0 {
		t = append(t, reapTarget{r.BridgePid, "mcp-bridge", mcpBridgeFlag})
	}
	for _, pid := range r.VirtiofsdPids {
		t = append(t, reapTarget{pid, "virtiofsd", r.TurnDir})
	}
	return t
}

// cmdlineHas reports whether a /proc cmdline contains the token. The kernel
// separates argv with NUL, so a plain substring test over the raw bytes matches
// a token that sits inside one argument (a path in --socket-path=<dir>/x) and
// one that is a whole argument.
func cmdlineHas(raw, token string) bool {
	if token == "" {
		return false
	}
	return strings.Contains(raw, token)
}

// reapVM ends every process the record names and removes the record only when
// each pid is confirmed gone. A record removed over a live QEMU is a guest
// nothing reaps (spec 6/41 §"The turn" step 9), so a pid that survives SIGKILL
// leaves the record in place and returns an error.
//
// grace is how long a process gets to exit on its own before SIGKILL. poll is
// how often the pid is re-checked.
func reapVM(rec *vmRecord, p procs, grace, poll time.Duration, tree *turnTree, net vmNetOps) error {
	var failed []string
	targets := rec.reapTargets()
	// QEMU first, and alone: while the guest runs, taking away its filter, its
	// proxy or its share would leave it running with a route or a write path
	// nobody is watching.
	if err := endProcess(p, targets[0], grace, poll); err != nil {
		failed = append(failed, err.Error())
	}
	// Then the rule set and the tap, then the host processes that served the
	// guest, in the order spec 6/41 §"The turn" step 9 fixes.
	if net != nil && rec.Tap != "" {
		net.rulesDown(rec)
		net.tapDown(rec.Tap)
	}
	for _, t := range targets[1:] {
		if err := endProcess(p, t, grace, poll); err != nil {
			failed = append(failed, err.Error())
		}
	}
	// The staging tree unmounts before it is removed, and the removal is
	// refused while anything is still mounted under it: a recursive delete over
	// a live bind deletes the group's own files, not the tree.
	//
	// A tree that will not go is NOT a reason to keep the record, and that
	// distinction is the whole of BUGS L362. `remove` refuses while the tree
	// holds a file it did not create, and a guest holds a WRITABLE export by
	// design, so any guest can leave one — step 0's own uid-clamp probe does. A
	// kept record costs a CID forever, because `liveCIDs` reads the records on
	// disk and `allocateCID` searches only MAX_CONCURRENT_CONTAINERS of them, so
	// five such turns leave every later spawn with no CID and no live guest.
	//
	// The record's job is to name the processes a dead runed must reap. Once every
	// pid it names is confirmed gone that job is done, so the record goes and the
	// CID comes back. The tree is reported by path for a person instead — an
	// operator action on live data, the same split `arizuko images --prune` takes.
	if tree != nil {
		if err := tree.teardown(); err != nil {
			slog.Error("vm teardown: staging tree kept, record released anyway",
				"vm", rec.Handle, "dir", rec.TurnDir, "err", err,
				"action", "remove the tree by hand once nothing is mounted under it")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("vm %s not fully reaped, record kept: %s", rec.Handle, strings.Join(failed, "; "))
	}
	if err := os.Remove(filepath.Join(rec.TurnDir, vmRecordName)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("vm %s: remove record: %w", rec.Handle, err)
	}
	// Whatever is left in the turn dir is this substrate's own: the vhost-user
	// sockets and the pid file BESIDE EACH of them, the QEMU pid file, the emptied
	// tree. Named removals only — the tree's own teardown is what proves nothing is
	// mounted under it.
	//
	// virtiofsd writes `<socket>.pid` next to its socket, which this list did not
	// name, so every turn left three files and its directory behind: 250 of them in
	// one evening on krons (BUGS `L376`).
	rootfs := filepath.Join(rec.TurnDir, rootfsDirName)
	if err := syscall.Rmdir(rootfs); err != nil && !os.IsNotExist(err) {
		slog.Warn("vm teardown: rootfs kept", "path", rootfs, "err", err)
	}
	for _, name := range []string{
		qemuPidName,
		rootSockName, rootSockName + pidSuffix,
		turnSockName, turnSockName + pidSuffix,
	} {
		if err := os.Remove(filepath.Join(rec.TurnDir, name)); err != nil && !os.IsNotExist(err) {
			slog.Warn("vm teardown: leftover file", "path", filepath.Join(rec.TurnDir, name), "err", err)
		}
	}
	if err := os.Remove(rec.TurnDir); err != nil && !os.IsNotExist(err) {
		// NAME what survived. "directory not empty" is the one message that cannot
		// be acted on, and it is what this said for every turn.
		left, _ := os.ReadDir(rec.TurnDir)
		names := make([]string, 0, len(left))
		for _, e := range left {
			names = append(names, e.Name())
		}
		slog.Warn("vm teardown: turn dir kept", "dir", rec.TurnDir,
			"left", strings.Join(names, ","), "err", err)
	}
	return nil
}

// endProcess is the bounded grace, the SIGKILL and the confirmation, for one pid.
func endProcess(p procs, t reapTarget, grace, poll time.Duration) error {
	if t.pid <= 0 || !p.alive(t.pid) {
		return nil
	}
	// A live pid whose cmdline does not name this turn is not this turn's
	// process. Leave it and say so: the alternative is killing a stranger.
	if raw, err := p.cmdline(t.pid); err == nil && !cmdlineHas(raw, t.token) {
		slog.Warn("vm teardown: recorded pid belongs to another process, not signalled",
			"what", t.what, "pid", t.pid, "expected", t.token)
		return nil
	}
	_ = p.signal(t.pid, syscall.SIGTERM)
	if waitGone(p, t.pid, grace, poll) {
		return nil
	}
	_ = p.signal(t.pid, syscall.SIGKILL)
	if waitGone(p, t.pid, grace, poll) {
		return nil
	}
	return fmt.Errorf("%s pid %d survived SIGKILL", t.what, t.pid)
}

// waitGone polls until the pid is gone or the bound elapses.
func waitGone(p procs, pid int, grace, poll time.Duration) bool {
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	deadline := time.Now().Add(grace)
	for {
		if !p.alive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(poll)
	}
}

// newVMRecord fills the fields that are known before anything is started. Every
// later step appends its pid and saves again, so a runed that dies at any point
// leaves a record naming what it started (spec 6/41 §"The turn" step 1).
func newVMRecord(root, handle, folder string, cid, slot, base uint32) (*vmRecord, error) {
	dir, err := vmDir(root, handle)
	if err != nil {
		return nil, err
	}
	ctl, mcp := vsockPorts(base, slot)
	return &vmRecord{
		Handle:    handle,
		Folder:    folder,
		CID:       cid,
		CtlPort:   ctl,
		McpPort:   mcp,
		TurnDir:   dir,
		CreatedAt: core.TS(time.Now()),
	}, nil
}
