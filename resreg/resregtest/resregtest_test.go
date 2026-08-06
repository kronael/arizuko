package resregtest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	_ "github.com/kronael/arizuko/resreg/resources"
)

// recorder implements TB and captures what an assertion reported instead of
// failing the enclosing test. Without it these guards would be unfalsifiable:
// every test below asserts the guard FAILS on the shape it claims to catch, and
// that can only be observed from outside testing.T.
type recorder struct {
	errs   []string
	fatals []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// Fatalf records and panics with stopSentinel, mirroring testing.T.Fatalf's
// "stop this test now". Callers recover via r.run.
func (r *recorder) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
	panic(stopSentinel)
}

type sentinel struct{}

var stopSentinel = sentinel{}

// run executes fn, absorbing the panic a recorded Fatalf raises.
func (r *recorder) run(fn func()) {
	defer func() {
		if p := recover(); p != nil && p != any(stopSentinel) {
			panic(p)
		}
	}()
	fn()
}

// muxServing builds a mux mounting exactly the given "VERB /path" patterns.
func muxServing(patterns ...string) *http.ServeMux {
	mux := http.NewServeMux()
	for _, p := range patterns {
		mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {})
	}
	return mux
}

// realOps is the emitted operation set for a resource that genuinely exists, so
// the tests below probe against real paths rather than invented ones. routes is
// routd's and has both a collection and an item face.
func realOps(t *testing.T) []Op {
	t.Helper()
	ops := AdvertisedOps(t, "probe", []string{"routes"})
	if len(ops) == 0 {
		t.Fatal("routes renders no operation — the emitter is not producing anything to probe")
	}
	return ops
}

// TestAssertServesWhatItAdvertises_PassesWhenMounted is the control: the guard
// must stay silent when every advertised path is on the mux. Without it, the
// failure tests below would be satisfied by a guard that fails unconditionally.
func TestAssertServesWhatItAdvertises_PassesWhenMounted(t *testing.T) {
	ops := realOps(t)
	var patterns []string
	for _, op := range ops {
		patterns = append(patterns, op.Method+" "+op.Path)
	}
	rec := &recorder{}
	n := AssertServesWhatItAdvertises(rec, "probe", []string{"routes"}, muxServing(patterns...))
	if n != len(ops) {
		t.Errorf("checked %d operations, want %d", n, len(ops))
	}
	if len(rec.errs) != 0 {
		t.Errorf("guard reported %d failures on a fully-mounted mux: %v", len(rec.errs), rec.errs)
	}
}

// TestAssertServesWhatItAdvertises_CatchesUnmountedPath is the F27 shape: the
// doc advertises a path, the mux mounts nothing at it. `GET /v1/acl` shipped
// exactly this way — declared, advertised, trimmed away at mount time.
func TestAssertServesWhatItAdvertises_CatchesUnmountedPath(t *testing.T) {
	rec := &recorder{}
	AssertServesWhatItAdvertises(rec, "probe", []string{"routes"}, http.NewServeMux())
	if len(rec.errs) == 0 {
		t.Fatal("guard stayed silent while an empty mux served none of the advertised paths — it cannot catch F27")
	}
	for _, e := range rec.errs {
		if !strings.Contains(e, "advertised endpoint that 404s") {
			t.Errorf("failure message does not name the class: %q", e)
		}
	}
}

// TestAssertServesWhatItAdvertises_CatchesRepathedMount is the F21 shape, and
// the one a "did some handler answer?" probe is blind to: the daemon mounts the
// resource, but at a DIFFERENT path than it advertises. scheduled_tasks served
// /v1/tasks while its declaration said /v1/scheduled_tasks.
func TestAssertServesWhatItAdvertises_CatchesRepathedMount(t *testing.T) {
	ops := realOps(t)
	var patterns []string
	for _, op := range ops {
		patterns = append(patterns, op.Method+" /elsewhere"+op.Path)
	}
	rec := &recorder{}
	AssertServesWhatItAdvertises(rec, "probe", []string{"routes"}, muxServing(patterns...))
	if len(rec.errs) != len(ops) {
		t.Fatalf("guard reported %d failures for %d re-pathed operations — a mount at the wrong path must fail (F21)",
			len(rec.errs), len(ops))
	}
}

// TestAssertServesWhatItAdvertises_CatchesCatchAllMasking pins the reason the
// guard compares the mux PATTERN byte-for-byte instead of asking "did anything
// answer". proxyd mounts `mux.HandleFunc("/", …)`, so every probe resolves to a
// handler; a presence-only check passes vacuously on a daemon that mounts the
// resource nowhere.
func TestAssertServesWhatItAdvertises_CatchesCatchAllMasking(t *testing.T) {
	rec := &recorder{}
	AssertServesWhatItAdvertises(rec, "probe", []string{"routes"}, muxServing("/"))
	if len(rec.errs) == 0 {
		t.Fatal("a catch-all mux satisfied the guard — pattern equality is not being enforced, " +
			"so this guard would pass vacuously on proxyd")
	}
}

// TestAssertServesNoneOf_CatchesForeignMount is the ownership half: a daemon
// mounting a resource it does not own. This is the check the deleted
// daemonOwnership map claimed to perform and structurally could not, because it
// computed both sides from the same table.
func TestAssertServesNoneOf_CatchesForeignMount(t *testing.T) {
	ops := realOps(t)
	var patterns []string
	for _, op := range ops {
		patterns = append(patterns, op.Method+" "+op.Path)
	}
	rec := &recorder{}
	AssertServesNoneOf(rec, "probe", []string{"routes"}, muxServing(patterns...))
	if len(rec.errs) == 0 {
		t.Fatal("guard stayed silent while the mux served every foreign path — it cannot catch a wire-identity collision")
	}
}

// TestAssertServesNoneOf_PassesWhenAbsent is that half's control.
func TestAssertServesNoneOf_PassesWhenAbsent(t *testing.T) {
	rec := &recorder{}
	n := AssertServesNoneOf(rec, "probe", []string{"routes"}, muxServing("GET /health"))
	if n == 0 {
		t.Fatal("anchor rendered zero operations")
	}
	if len(rec.errs) != 0 {
		t.Errorf("guard reported failures for paths the mux does not serve: %v", rec.errs)
	}
}

// TestAssertServesNoneOf_FatalsOnEmptyAnchor pins the non-vacuity guarantee: an
// anchor naming resources that render nothing must stop, not silently assert
// over an empty set.
func TestAssertServesNoneOf_FatalsOnEmptyAnchor(t *testing.T) {
	rec := &recorder{}
	rec.run(func() { AssertServesNoneOf(rec, "probe", []string{"no_such_resource"}, http.NewServeMux()) })
	if len(rec.fatals) == 0 {
		t.Fatal("anchor accepted a resource set rendering zero operations — a vacuous anchor proves nothing")
	}
}
