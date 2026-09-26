package main

// What can be tested without a guest, is. A hypervisor is needed for the mounts
// themselves and for a real agent, so everything that DECIDES those — the host's
// manifest, the mount plan it produces, the frame codec, the steer ack and the
// exit code — is a pure function or takes an io.Writer, and each test names the
// values it expects rather than deriving them from the code under test.
//
// Standard library only: guest-init is its own module with no go.sum, and it
// stays that way (see the AF_VSOCK comment in main.go).

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// The two ports come off the kernel command line, and a missing one is an error
// rather than a default: the host allocates a pair per CID, so a guest that
// guessed would dial another guest's control channel.
func TestBootPortsReadsTheKernelCommandLine(t *testing.T) {
	// The string runed renders (runed/qemu.go qemuArgs -append), for the second
	// live guest: CID 4 takes the base + 2.
	const cmdline = "console=ttyS0 reboot=t panic=-1 ARIZUKO_CTL=5557 ARIZUKO_MCP=5558\n"
	ctl, mcp, err := bootPorts(cmdline)
	if err != nil {
		t.Fatalf("bootPorts: %v", err)
	}
	if ctl != 5557 || mcp != 5558 {
		t.Errorf("read ctl=%d mcp=%d, want 5557 and 5558", ctl, mcp)
	}
	for _, tc := range []struct{ name, cmdline, want string }{
		{"no ctl key", "console=ttyS0 ARIZUKO_MCP=5558", "no ARIZUKO_CTL"},
		{"no mcp key", "console=ttyS0 ARIZUKO_CTL=5557", "no ARIZUKO_MCP"},
		{"not a port", "ARIZUKO_CTL=five ARIZUKO_MCP=5558", `ARIZUKO_CTL="five"`},
		// A key the kernel would have discarded as a module parameter. The
		// undotted form is the whole reason the host writes it undotted.
		{"dotted key", "arizuko.ctl=5557 arizuko.mcp=5558", "no ARIZUKO_CTL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := bootPorts(tc.cmdline); err == nil {
				t.Fatal("a command line without both ports must be an error")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// The mount plan, from the manifest the host actually writes.
//
// The JSON below is byte for byte what runed emits: json.MarshalIndent of its own
// treeManifest with a trailing newline (runed/vmtree.go stageTargets). The two
// declarations are separate modules, so this test is the only thing on the guest
// side that reads the writer's real shape — a renamed field lands here as an
// empty plan.
func TestRootPlanMirrorsTheHostsManifest(t *testing.T) {
	const manifest = `{
  "mounts": [
    {
      "path": "/home/node",
      "ro": false
    },
    {
      "path": "/home/node/.codex/auth.json",
      "ro": true
    },
    {
      "path": "/opt/arizuko",
      "ro": true
    }
  ],
  "tmpfs": [
    "/run/ipc"
  ]
}
`
	var m treeManifest
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatalf("decode the host's manifest: %v", err)
	}
	wantManifest := treeManifest{
		Mounts: []treeEntry{
			{Path: "/home/node", RO: false},
			{Path: "/home/node/.codex/auth.json", RO: true},
			{Path: "/opt/arizuko", RO: true},
		},
		Tmpfs: []string{"/run/ipc"},
	}
	if !reflect.DeepEqual(m, wantManifest) {
		t.Fatalf("parsed %+v, want %+v", m, wantManifest)
	}

	// Every source, target, filesystem and option the guest mounts, named. The
	// bind sources are the share plus the guest path because the host stages each
	// family at the path the guest sees it; the targets are inside the assembled
	// root because that is the only / the agent has.
	want := []mountOp{
		{Source: "root", Target: "/lower", FS: "virtiofs", Flags: syscall.MS_RDONLY},
		{Source: "tmpfs", Target: "/overlay", FS: "tmpfs", Data: "size=512m"},
		{Source: "overlay", Target: "/newroot", FS: "overlay",
			Data:  "lowerdir=/lower,upperdir=/overlay/upper,workdir=/overlay/work",
			Needs: []string{"/overlay/upper", "/overlay/work"}},
		{Source: "proc", Target: "/newroot/proc", FS: "proc"},
		{Source: "sysfs", Target: "/newroot/sys", FS: "sysfs"},
		{Source: "/dev", Target: "/newroot/dev", Flags: syscall.MS_BIND | syscall.MS_REC},
		// A pty needs /dev/ptmx AND the numbered slave under /dev/pts; without the
		// mount the second open is ENOENT. The kernel has CONFIG_UNIX98_PTYS=y.
		{Source: "devpts", Target: "/newroot/dev/pts", FS: "devpts",
			Data: "gid=5,mode=620,ptmxmode=666", Needs: []string{"/newroot/dev/pts"}},
		{Source: "/turn/home/node", Target: "/newroot/home/node",
			Flags: syscall.MS_BIND | syscall.MS_REC},
		{Source: "/turn/home/node/.codex/auth.json", Target: "/newroot/home/node/.codex/auth.json",
			Flags: syscall.MS_BIND | syscall.MS_REC},
		{Source: "/turn/opt/arizuko", Target: "/newroot/opt/arizuko",
			Flags: syscall.MS_BIND | syscall.MS_REC},
		{Source: "tmpfs", Target: "/newroot/run/ipc", FS: "tmpfs", Data: "size=16m", Own: true},
	}
	got := rootPlan(m)
	if len(got) != len(want) {
		t.Fatalf("the plan has %d mounts, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("mount %d is\n  %+v\nwant\n  %+v", i, got[i], want[i])
		}
	}
	// The release rootfs is the overlay's LOWER and read-only: a virtio-fs share
	// does not work as an upper, and the agent's writes must die with the guest.
	if got[0].Flags&syscall.MS_RDONLY == 0 {
		t.Error("the root share is not mounted read-only")
	}
	if !strings.HasPrefix(got[2].Data, "lowerdir="+got[0].Target+",") {
		t.Errorf("the overlay's lowerdir is %q, not the root share's mount point %q",
			got[2].Data, got[0].Target)
	}
}

// The turn's own mount table is refused when it is empty, rather than exec'ing
// the agent into a root with no home, no release and no share.
func TestAssembleRootRefusesAManifestWithNoMounts(t *testing.T) {
	var m treeManifest
	if err := json.Unmarshal([]byte(`{"mounts":[],"tmpfs":["/run/ipc"]}`), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Mounts) != 0 {
		t.Fatalf("the fixture is wrong: %+v", m)
	}
	// rootPlan still renders the root itself, which is what makes the emptiness
	// worth refusing above it: the plan alone would succeed and mount nothing of
	// the turn's.
	for _, op := range rootPlan(m) {
		if strings.HasPrefix(op.Source, shareDir+"/") {
			t.Fatalf("an empty manifest yielded a bind from the share: %+v", op)
		}
	}
}

// The codec, both directions, including a frame that arrives split across two
// reads — which is how a stream delivers one.
func TestFrameCodecRoundTripsBothDirections(t *testing.T) {
	// Host to guest.
	in := []frame{
		{Kind: kindTurn,
			Env:   map[string]string{"ARIZUKO_FOLDER": "solo/inbox"},
			Stdin: []byte(`{"message":"hi"}`)},
		{Kind: kindSteer, Batch: "a message", ID: "7"},
		{Kind: kindStop},
	}
	var wire bytes.Buffer
	for _, f := range in {
		if err := writeFrame(&wire, f); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
	}
	got := decodeAll(t, bytes.NewReader(wire.Bytes()))
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round trip gave\n  %+v\nwant\n  %+v", got, in)
	}

	// Guest to host, on the wire. The host decodes by these names
	// (runed/vmctl.go ctlFrame), so the bytes are the contract, not the struct.
	for _, tc := range []struct {
		f    frame
		want string
	}{
		{frame{Kind: kindSteerAck, ID: "7"}, `{"kind":"steer_ack","id":"7"}`},
		{frame{Kind: kindExit, Code: 42}, `{"kind":"exit","code":42}`},
		{frame{Kind: kindLog, Line: "a line"}, `{"kind":"log","line":"a line"}`},
	} {
		var b bytes.Buffer
		if err := writeFrame(&b, tc.f); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
		if b.String() != tc.want+"\n" {
			t.Errorf("wrote %q, want %q", b.String(), tc.want+"\n")
		}
	}

	// Split mid-frame, and mid-payload: the reader must join the halves rather
	// than treat the first as a short frame.
	whole := wire.Bytes()
	cut := len(whole) / 2
	got = decodeAll(t, &chunks{parts: [][]byte{whole[:cut], whole[cut:]}})
	if !reflect.DeepEqual(got, in) {
		t.Errorf("a split read gave\n  %+v\nwant\n  %+v", got, in)
	}
}

// A steer is acked with the REQUEST's id, and only after the batch is on the
// guest tmpfs and the agent has been woken.
//
// The host's steer returns true on that ack and routd marks the turn done, so an
// ack that arrived first — or under another id — would make a user's message
// vanish with the tmpfs at power-off while the turn read as delivered
// (spec 6/41 §"Steer and stop").
func TestSteerIsAckedWithTheRequestsID(t *testing.T) {
	dir := t.TempDir()
	var wire bytes.Buffer
	tn := &turn{c: &ctl{w: &wire}, input: dir}
	var woke []syscall.Signal
	var batchAtWake string
	tn.wake = func(sig syscall.Signal) error {
		woke = append(woke, sig)
		batchAtWake = spooled(t, dir)
		return nil
	}

	tn.steer(frame{Kind: kindSteer, Batch: "a message", ID: "7"})

	frames := decodeAll(t, bytes.NewReader(wire.Bytes()))
	if len(frames) != 1 {
		t.Fatalf("a steer put %d frames on the wire, want one ack: %+v", len(frames), frames)
	}
	if frames[0].Kind != kindSteerAck {
		t.Errorf("answered a steer with %q, want %q", frames[0].Kind, kindSteerAck)
	}
	if frames[0].ID != "7" {
		t.Errorf("acked id %q, want the request's %q", frames[0].ID, "7")
	}
	if !reflect.DeepEqual(woke, []syscall.Signal{syscall.SIGUSR1}) {
		t.Errorf("woke the agent with %v, want one SIGUSR1", woke)
	}
	// The ack means the GUEST took it: at the moment the agent was woken the
	// batch was already spooled, in the shape the agent drains.
	const want = `{"text":"a message","type":"message"}`
	if batchAtWake != want {
		t.Errorf("the spool held %q when the agent was woken, want %q", batchAtWake, want)
	}

	t.Run("the batch file is one the agent reads", func(t *testing.T) {
		names, err := filepath.Glob(filepath.Join(dir, "*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 {
			t.Fatalf("the spool holds %v, want one file", names)
		}
		base := filepath.Base(names[0])
		// ant filters on .json and sorts; the temp name must not match that
		// filter, and the counter is the guest's own.
		if !strings.HasSuffix(base, "-1.json") {
			t.Errorf("the batch file is %q, want a <millis>-<seq>.json name", base)
		}
	})

	t.Run("no ack when the agent could not be woken", func(t *testing.T) {
		var wire bytes.Buffer
		tn := &turn{c: &ctl{w: &wire}, input: t.TempDir()}
		tn.wake = func(syscall.Signal) error { return io.ErrClosedPipe }
		tn.steer(frame{Kind: kindSteer, Batch: "a message", ID: "9"})
		for _, f := range decodeAll(t, bytes.NewReader(wire.Bytes())) {
			if f.Kind == kindSteerAck {
				t.Fatal("acked a steer the agent was never woken for; " +
					"the host would mark the turn done and drop the message")
			}
		}
	})
}

// The exit frame carries the agent's own code, whichever way the agent ended.
// RunResult.ExitCode IS this frame (spec 6/41 §"The turn"), and it decides
// whether routd retries, trips the breaker or does nothing.
func TestTheExitFrameCarriesTheAgentsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   int
	}{
		{"its exit status", "echo a line >&2; exit 42", 42},
		// A signalled process has no exit status of its own; a shell and docker
		// both report 128+signal, and -1 would read as a code the agent chose.
		{"the signal that ended it", "kill -TERM $$", 128 + int(syscall.SIGTERM)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire bytes.Buffer
			tn := &turn{c: &ctl{w: &wire}}
			run(tn, exec.Command("/bin/sh", "-c", tc.script))
			frames := decodeAll(t, bytes.NewReader(wire.Bytes()))
			if len(frames) == 0 {
				t.Fatal("the agent ended and the guest reported nothing")
			}
			last := frames[len(frames)-1]
			if last.Kind != kindExit {
				t.Fatalf("the last frame is %q, want %q: %+v", last.Kind, kindExit, frames)
			}
			if last.Code != tc.want {
				t.Errorf("reported code %d, want %d", last.Code, tc.want)
			}
		})
	}

	t.Run("its stderr precedes the exit frame", func(t *testing.T) {
		var wire bytes.Buffer
		tn := &turn{c: &ctl{w: &wire}}
		run(tn, exec.Command("/bin/sh", "-c", "echo first >&2; echo second >&2; exit 3"))
		var got []string
		for _, f := range decodeAll(t, bytes.NewReader(wire.Bytes())) {
			switch f.Kind {
			case kindLog:
				got = append(got, f.Line)
			case kindExit:
				got = append(got, "exit")
			}
		}
		// Ordered, and complete: Wait closes the stderr pipe, so a Wait that ran
		// before the last read would drop the agent's final lines. The wait's own
		// error follows them and precedes the exit frame — it is the one line that
		// names WHY a turn had no reply, and it used to reach the console only
		// (BUGS `L375`).
		want := []string{"first", "second", "guest-init: agent wait: exit status 3", "exit"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the wire held %v, want %v", got, want)
		}
	})

	t.Run("an agent that could not start", func(t *testing.T) {
		var wire bytes.Buffer
		tn := &turn{c: &ctl{w: &wire}}
		run(tn, exec.Command(filepath.Join(t.TempDir(), "not-a-binary")))
		frames := decodeAll(t, bytes.NewReader(wire.Bytes()))
		last := frames[len(frames)-1]
		if last.Kind != kindExit || last.Code != initExit {
			t.Errorf("an agent that never ran reported %+v, want exit %d", last, initExit)
		}
	})
}

// The agent's process, as rendered. It runs inside the assembled root as the
// agent's uid, and its environment is the turn's — never this process's, which
// holds the kernel command line's own keys.
func TestAgentCmdRunsTheImagesEntrypointAsTheAgent(t *testing.T) {
	t.Setenv("ARIZUKO_CTL", "5557")
	cmd := agentCmd(map[string]string{"B": "2", "A": "1"}, []byte("payload"))
	if cmd.Path != "/app/entrypoint.sh" {
		t.Errorf("execs %q, want the image's entrypoint /app/entrypoint.sh", cmd.Path)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"A=1", "B=2"}) {
		t.Errorf("environment %v, want exactly the turn's, sorted", cmd.Env)
	}
	if cmd.SysProcAttr.Chroot != "/newroot" {
		t.Errorf("chroots to %q, want the assembled root /newroot", cmd.SysProcAttr.Chroot)
	}
	cred := cmd.SysProcAttr.Credential
	if cred == nil || cred.Uid != 1000 || cred.Gid != 1000 {
		t.Errorf("credential %+v, want uid and gid 1000", cred)
	}
	b, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "payload" {
		t.Errorf("stdin is %q, want the turn's payload", b)
	}
}

// decodeAll reads every frame off a stream through the production reader.
func decodeAll(t *testing.T, r io.Reader) []frame {
	t.Helper()
	var out []frame
	if err := readFrames(r, func(f frame) error { out = append(out, f); return nil }); err != nil {
		t.Fatalf("readFrames: %v", err)
	}
	return out
}

// spooled reads the one batch file in a spool dir, the way the agent does.
func spooled(t *testing.T, dir string) string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		return ""
	}
	b, err := os.ReadFile(names[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// chunks hands out its parts one Read at a time, so a frame split across two
// reads is read the way a stream actually delivers one.
type chunks struct{ parts [][]byte }

func (c *chunks) Read(p []byte) (int, error) {
	for len(c.parts) > 0 && len(c.parts[0]) == 0 {
		c.parts = c.parts[1:]
	}
	if len(c.parts) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.parts[0])
	c.parts[0] = c.parts[0][n:]
	return n, nil
}
