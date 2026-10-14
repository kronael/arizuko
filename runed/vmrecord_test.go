package runed

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeProcs is a pid table the test owns, so the reap's order, its bounded grace
// and its confirm-gone rule are checked without starting a process.
type fakeProcs struct {
	live     map[int]bool
	cmds     map[int]string
	immortal map[int]bool
	sent     []string
}

func (f *fakeProcs) signal(pid int, sig syscall.Signal) error {
	f.sent = append(f.sent, fmt.Sprintf("%d:%v", pid, sig))
	if f.immortal[pid] {
		return nil
	}
	delete(f.live, pid)
	return nil
}

func (f *fakeProcs) alive(pid int) bool { return f.live[pid] }

// fakeNet records the tap teardown into the same log as the signals, so the
// ORDER of the two halves is what the test reads.
type fakeNet struct{ p *fakeProcs }

func (f fakeNet) rulesDown(rec *vmRecord) { f.p.sent = append(f.p.sent, "rules-down:"+rec.Tap) }
func (f fakeNet) tapDown(tap string)      { f.p.sent = append(f.p.sent, "tap-down:"+tap) }

func (f *fakeProcs) cmdline(pid int) (string, error) {
	c, ok := f.cmds[pid]
	if !ok {
		return "", os.ErrNotExist
	}
	return c, nil
}

// fullRecord is a turn with every process it starts, in a temp turn dir.
func fullRecord(t *testing.T) (*vmRecord, *fakeProcs) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "arizuko-test-g1")
	rec := &vmRecord{
		Handle: "arizuko-test-g1", Folder: "solo/inbox",
		CID: 3, CtlPort: 5555, McpPort: 5556,
		Tap: "arz-ab12-t00", GuestIP: "10.97.0.1", HostIP: "10.97.0.2", ProxyPort: 3128,
		QemuPid: 101, VirtiofsdPids: []int{102, 103}, EgressPid: 104, BridgePid: 105,
		TurnDir: dir, CreatedAt: "2026-09-24T12:00:00.000000000Z",
	}
	if err := rec.save(); err != nil {
		t.Fatalf("save record: %v", err)
	}
	p := &fakeProcs{
		live: map[int]bool{101: true, 102: true, 103: true, 104: true, 105: true},
		cmds: map[int]string{
			101: "qemu-system-x86_64\x00-pidfile\x00" + dir + "/qemu.pid",
			102: "virtiofsd\x00--shared-dir=" + dir + "/tree",
			103: "virtiofsd\x00--shared-dir=" + dir + "/tree",
			104: "egress\x00--client\x0010.97.0.1",
			105: "runed\x00" + mcpBridgeFlag + "\x003\x00/i/gated.sock",
		},
		immortal: map[int]bool{},
	}
	return rec, p
}

// The record is the label: a turn starts four host processes and none of them
// carries one, so a pid the record does not name is a process the disk scan
// cannot reap after a runed crash.
//
// The expected set is DERIVED from the record's own pid fields rather than
// listed, so a fifth process added to the record without being added to the reap
// fails here instead of leaking silently.
func TestReapTargetsCoverEveryPidTheRecordNames(t *testing.T) {
	rec, _ := fullRecord(t)
	var declared []int
	v := reflect.ValueOf(*rec)
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		switch {
		case strings.HasSuffix(name, "Pids"):
			declared = append(declared, v.Field(i).Interface().([]int)...)
		case strings.HasSuffix(name, "Pid"):
			declared = append(declared, int(v.Field(i).Int()))
		}
	}
	if len(declared) < 5 {
		t.Fatalf("the fixture must set every pid field; found %d", len(declared))
	}
	var reaped []int
	for _, tg := range rec.reapTargets() {
		reaped = append(reaped, tg.pid)
		if tg.token == "" {
			t.Errorf("%s pid %d has no cmdline token, so the reap cannot tell it from a recycled pid", tg.what, tg.pid)
		}
	}
	for _, pid := range declared {
		if !slices.Contains(reaped, pid) {
			t.Errorf("pid %d is in the record and not in the reap: it would survive a runed crash", pid)
		}
	}
}

// QEMU goes first: nothing else may be taken away while a guest can still write
// through the share or send through the tap.
func TestReapEndsQemuFirstAndTheSharesLast(t *testing.T) {
	rec, p := fullRecord(t)
	if err := reapVM(rec, p, 50*time.Millisecond, time.Millisecond, nil, fakeNet{p}); err != nil {
		t.Fatalf("reap: %v", err)
	}
	// QEMU, then the rule set and the tap, then the processes that served the
	// guest, then the two shares. A rule dropped while the guest still runs
	// leaves it on an unfiltered tap; a share taken before it stops leaves a
	// live guest writing into a half-torn tree.
	want := []string{
		"101:terminated",
		"rules-down:arz-ab12-t00", "tap-down:arz-ab12-t00",
		"104:terminated", "105:terminated",
		"102:terminated", "103:terminated",
	}
	if !slices.Equal(p.sent, want) {
		t.Errorf("reap order\ngot:  %v\nwant: %v", p.sent, want)
	}
	if _, err := os.Stat(filepath.Join(rec.TurnDir, vmRecordName)); !os.IsNotExist(err) {
		t.Errorf("a fully reaped turn must leave no record: %v", err)
	}
}

// A record removed over a live QEMU is a guest nothing reaps. So a pid that
// survives SIGKILL keeps the record, and the error says which pid it was.
func TestReapKeepsTheRecordWhenAPidSurvivesSIGKILL(t *testing.T) {
	rec, p := fullRecord(t)
	rootfs := filepath.Join(rec.TurnDir, rootfsDirName)
	if err := os.Mkdir(rootfs, 0o755); err != nil {
		t.Fatal(err)
	}
	p.immortal[101] = true
	err := reapVM(rec, p, 20*time.Millisecond, time.Millisecond, nil, nil)
	if err == nil {
		t.Fatal("a QEMU that survives SIGKILL must be an error, not a silent teardown")
	}
	if !strings.Contains(err.Error(), "qemu pid 101") {
		t.Errorf("the error must name the surviving process: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(rec.TurnDir, vmRecordName)); serr != nil {
		t.Errorf("the record must stay for the next disk scan: %v", serr)
	}
	if _, err := os.Stat(rootfs); err != nil {
		t.Fatalf("live guest rootfs removed: %v", err)
	}
	// SIGTERM then SIGKILL, in that order, both to the same pid.
	if !slices.Contains(p.sent, "101:terminated") || !slices.Contains(p.sent, "101:killed") {
		t.Errorf("a live pid gets its grace and then SIGKILL, got %v", p.sent)
	}
}

// A record outlives its processes by definition, and a crashed runed's pid may
// have been handed to something else by the time the scan runs. Signalling a
// stranger because a stale record named its number is the one failure a reaper
// must not have.
func TestReapWillNotSignalARecycledPid(t *testing.T) {
	rec, p := fullRecord(t)
	p.cmds[101] = "postgres\x00-D\x00/var/lib/postgresql"
	p.immortal[101] = true
	if err := reapVM(rec, p, 20*time.Millisecond, time.Millisecond, nil, nil); err != nil {
		t.Fatalf("a recycled pid is not this turn's process, so the reap completes: %v", err)
	}
	for _, s := range p.sent {
		if strings.HasPrefix(s, "101:") {
			t.Errorf("a pid whose cmdline names another program must not be signalled: %v", p.sent)
		}
	}
	if !p.live[101] {
		t.Error("the unrelated process must still be running")
	}
}

func TestCmdlineHasMatchesOneArgumentAndInsideOne(t *testing.T) {
	const raw = "virtiofsd\x00--shared-dir=/srv/vms/h1/tree\x00--cache=always"
	if !cmdlineHas(raw, "/srv/vms/h1") {
		t.Error("a token inside one argument must match: the turn dir sits in a flag's value")
	}
	if !cmdlineHas(raw, "--cache=always") {
		t.Error("a whole argument must match")
	}
	if cmdlineHas(raw, "/srv/vms/h2") {
		t.Error("another turn's dir must not match")
	}
	if cmdlineHas(raw, "") {
		t.Error("an empty token must never match, or every pid looks like this turn's")
	}
}

// The handle comes from the Manager, but it becomes a path here: a separator in
// it would put the staging tree — and the teardown's removals — somewhere else.
func TestVMDirRefusesAHandleThatIsAPath(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../../etc", "a/b"} {
		if _, err := vmDir("/srv/vms", bad); err == nil {
			t.Errorf("handle %q must be refused as a record name", bad)
		}
	}
	got, err := vmDir("/srv/vms", "arizuko-krons-solo-1")
	if err != nil || got != "/srv/vms/arizuko-krons-solo-1" {
		t.Errorf("vmDir = %q, %v", got, err)
	}
}

func TestRecordRoundTripsThroughDisk(t *testing.T) {
	rec, _ := fullRecord(t)
	got, err := loadVMRecord(rec.TurnDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(rec, got) {
		t.Errorf("record changed across the disk\ngot:  %+v\nwant: %+v", got, rec)
	}
	records, broken := listVMRecords(filepath.Dir(rec.TurnDir))
	if len(records) != 1 || len(broken) != 0 {
		t.Fatalf("listVMRecords found %d records and %d broken dirs", len(records), len(broken))
	}
	if cids := liveCIDs(records); !cids[3] {
		t.Errorf("a record on disk must hold its CID against the allocator: %v", cids)
	}
}

// A turn dir whose record cannot be read is reported, never skipped: it is a
// turn whose processes nothing can name, which is the state the record exists to
// prevent.
func TestListVMRecordsReportsAnUnreadableDir(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "arizuko-test-broken")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, vmRecordName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	records, broken := listVMRecords(root)
	if len(records) != 0 || len(broken) != 1 || broken[0] != bad {
		t.Errorf("an undecodable record must be reported: records=%d broken=%v", len(records), broken)
	}
}

// A guest holds a WRITABLE export by design, so any guest can leave a file in the
// staging tree and `remove` then refuses the tree — step 0's own uid-clamp probe
// does exactly that. Before BUGS L362 the refusal kept the RECORD, and the record
// is the only thing that marks a CID used, so five such turns left `allocateCID`
// with nothing to return and no live guest anywhere. The record's job is to name
// the processes a dead runed must reap; once every pid is gone it has done it.
func TestReapReleasesTheCIDWhenOnlyTheTreeIsStuck(t *testing.T) {
	rec, p := fullRecord(t)
	tree := &turnTree{dir: filepath.Join(rec.TurnDir, "tree")}
	if err := os.MkdirAll(tree.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 17 bytes, the size of guest-init's own probe write.
	if err := os.WriteFile(filepath.Join(tree.dir, "from-guest.txt"),
		[]byte("written-as-uid-0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := reapVM(rec, p, 20*time.Millisecond, time.Millisecond, tree, nil); err != nil {
		t.Fatalf("every pid was reaped, so the turn is reaped: %v", err)
	}
	// The record goes, which is what frees the CID.
	if _, err := os.Stat(filepath.Join(rec.TurnDir, vmRecordName)); !os.IsNotExist(err) {
		t.Fatalf("the record must go once every pid is gone, else its CID never comes back: %v", err)
	}
	// And the CID is free again for the next turn, which is the claim that matters.
	records, bad := listVMRecords(filepath.Dir(rec.TurnDir))
	if len(bad) != 0 {
		t.Fatalf("unreadable records: %v", bad)
	}
	if used := liveCIDs(records); used[rec.CID] {
		t.Errorf("CID %d is still marked live with no guest and no record", rec.CID)
	}
	// The tree itself stays, because removing it could delete the group's files.
	if _, err := os.Stat(filepath.Join(tree.dir, "from-guest.txt")); err != nil {
		t.Errorf("the guest's file must NOT be deleted: %v", err)
	}
}

// TestReapRemovesTheDaemonsOwnPidFiles pins BUGS L376. virtiofsd writes
// `<socket>.pid` beside its socket, which the teardown's name list did not carry —
// so every turn left three files and its directory behind, 250 of them in one
// evening on krons, and said only "directory not empty".
func TestReapRemovesTheDaemonsOwnPidFiles(t *testing.T) {
	rec, p := fullRecord(t)
	if err := os.Mkdir(filepath.Join(rec.TurnDir, rootfsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	// Exactly what a finished turn leaves: the two sockets, a pid file beside each,
	// and QEMU's own.
	for _, name := range []string{
		qemuPidName,
		rootSockName, rootSockName + pidSuffix,
		turnSockName, turnSockName + pidSuffix,
	} {
		if err := os.WriteFile(filepath.Join(rec.TurnDir, name), []byte("7\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := reapVM(rec, p, time.Millisecond, time.Millisecond, nil, nil); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if _, err := os.Stat(rec.TurnDir); !os.IsNotExist(err) {
		left, _ := os.ReadDir(rec.TurnDir)
		names := make([]string, 0, len(left))
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Fatalf("the turn dir survived, holding %v: a name the teardown does not know "+
			"is a directory that leaks once per turn", names)
	}
}

func TestReapKeepsRootfsFile(t *testing.T) {
	rec, p := fullRecord(t)
	rootfs := filepath.Join(rec.TurnDir, rootfsDirName)
	if err := os.WriteFile(rootfs, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reapVM(rec, p, time.Millisecond, time.Millisecond, nil, nil); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(rootfs)
	if err != nil || string(content) != "keep" {
		t.Fatalf("rootfs file changed: %q, %v", content, err)
	}
}

func TestReapKeepsNonemptyRootfs(t *testing.T) {
	rec, p := fullRecord(t)
	rootfs := filepath.Join(rec.TurnDir, rootfsDirName)
	if err := os.Mkdir(rootfs, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(rootfs, "foreign.txt")
	if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reapVM(rec, p, time.Millisecond, time.Millisecond, nil, nil); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(foreign)
	if err != nil || string(content) != "keep" {
		t.Fatalf("nonempty rootfs contents changed: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(rec.TurnDir, vmRecordName)); !os.IsNotExist(err) {
		t.Fatalf("reaped processes retained their record: %v", err)
	}
}

func TestReapKeepsMountedRootfs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires mount privilege; run in a private mount namespace")
	}
	rec, p := fullRecord(t)
	rootfs := filepath.Join(rec.TurnDir, rootfsDirName)
	if err := os.Mkdir(rootfs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount("tmpfs", rootfs, "tmpfs", 0, "size=4096"); err != nil {
		if err == syscall.EPERM {
			t.Skip("mount privilege unavailable")
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Unmount(rootfs, 0); err != nil {
			t.Errorf("unmount test rootfs: %v", err)
		}
	})
	if err := reapVM(rec, p, time.Millisecond, time.Millisecond, nil, nil); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	mounts := mountedUnder(f, rootfs)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0] != rootfs {
		t.Fatalf("cleanup changed live mount: %v", mounts)
	}
	if _, err := os.Stat(rec.TurnDir); err != nil {
		t.Fatalf("mounted turn directory disappeared: %v", err)
	}
}
