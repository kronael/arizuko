package resregtest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
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

// muxServing builds a mux mounting exactly the given "VERB /path" patterns as
// plain handlers — the hand-rolled shape, never a RegisterREST mount.
func muxServing(patterns ...string) *http.ServeMux {
	mux := http.NewServeMux()
	for _, p := range patterns {
		mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {})
	}
	return mux
}

// realOps is the operation set of a resource that genuinely exists, so the
// tests below probe real paths rather than invented ones. routes is routd's and
// has both a collection and an item face.
func realOps(t *testing.T) []Op {
	t.Helper()
	ops := opsOf(t, "probe", byName([]string{"routes"}))
	if len(ops) == 0 {
		t.Fatal("routes renders no operation — the emitter is not producing anything to probe")
	}
	return ops
}

// opStrings renders ops as the "VERB /path" strings AssertAdvertises compares.
func opStrings(ops []Op) []string {
	var out []string
	for _, op := range ops {
		out = append(out, op.Method+" "+op.Path)
	}
	return out
}

// TestAssertAdvertises_PassesOnExactSurface is the control: a mux mounting
// exactly `routes` must match a want listing exactly its operations. Without
// this, the failure tests below would be satisfied by an assertion that fails
// unconditionally.
func TestAssertAdvertises_PassesOnExactSurface(t *testing.T) {
	rec := &recorder{}
	AssertAdvertises(rec, "probe", MuxOf("routes"), opStrings(realOps(t)))
	if len(rec.errs) != 0 {
		t.Errorf("assertion reported %d failures on an exactly-matching surface: %v", len(rec.errs), rec.errs)
	}
}

// TestAssertAdvertises_CatchesUnstatedMount is the review-visibility half: a
// REST face is mounted that the expected surface does not list. This is the
// shape that would otherwise let `DELETE /v1/acl_membership` appear silently.
func TestAssertAdvertises_CatchesUnstatedMount(t *testing.T) {
	rec := &recorder{}
	AssertAdvertises(rec, "probe", MuxOf("routes"), opStrings(realOps(t))[1:])
	if len(rec.errs) == 0 {
		t.Fatal("assertion stayed silent while the mux served a face the expected surface omits")
	}
}

// TestAssertAdvertises_CatchesVanishedMount is the other direction: the
// expected surface lists a face nothing mounts. An endpoint quietly dropped
// from a resource is as much a wire break as one quietly added.
func TestAssertAdvertises_CatchesVanishedMount(t *testing.T) {
	rec := &recorder{}
	AssertAdvertises(rec, "probe", MuxOf("routes"),
		append(opStrings(realOps(t)), "GET /v1/routes/never_mounted"))
	if len(rec.errs) == 0 {
		t.Fatal("assertion stayed silent while the expected surface named a face nothing mounts")
	}
}

// TestAssertAdvertises_CatchAllIsNotASurface pins that proxyd's `/` catch-all
// contributes nothing. A daemon mounting only a catch-all documents nothing, so
// an empty want must PASS and a non-empty one must fail — the reverse would
// mean the catch-all was being read as a mount for every registered resource.
func TestAssertAdvertises_CatchAllIsNotASurface(t *testing.T) {
	rec := &recorder{}
	AssertAdvertises(rec, "probe", muxServing("/"), nil)
	if len(rec.errs) != 0 {
		t.Errorf("a catch-all-only mux documented something: %v", rec.errs)
	}

	rec = &recorder{}
	AssertAdvertises(rec, "probe", muxServing("/"), opStrings(realOps(t)))
	if len(rec.errs) == 0 {
		t.Fatal("a catch-all satisfied a non-empty expected surface — it is being read as a mount")
	}
}

// TestAssertAdvertises_HandRolledMountIsNotASurface is the collision shape at
// the daemon level: routd and runed hand-roll GET /v1/sessions with plain
// mux.HandleFunc over their own tables. Neither may thereby document authd's
// sessions resource.
func TestAssertAdvertises_HandRolledMountIsNotASurface(t *testing.T) {
	rec := &recorder{}
	AssertAdvertises(rec, "probe", muxServing("GET /v1/sessions"), nil)
	if len(rec.errs) != 0 {
		t.Errorf("a hand-rolled GET /v1/sessions documented authd's sessions resource: %v", rec.errs)
	}
}

// TestAssertServesNoForeignResource_CatchesForeignMount is the ownership half:
// a daemon serving a resource it does not own. The mount is HAND-ROLLED, which
// is both the F46 shape (routd and runed each answered GET /v1/sessions with
// plain mux.HandleFunc) and the only shape the derived foreign set can see —
// a RegisterREST mount would make the daemon the owner instead.
func TestAssertServesNoForeignResource_CatchesForeignMount(t *testing.T) {
	rec := &recorder{}
	AssertServesNoForeignResource(rec, "probe", muxServing(opStrings(realOps(t))...))
	if len(rec.errs) == 0 {
		t.Fatal("guard stayed silent while the mux served every foreign path — it cannot catch a wire-identity collision")
	}
}

// TestAssertServesNoForeignResource_CatchesForeignWildcardMount is the same
// check on the one path shape the rendering has to translate: proxyd's route
// keys contain slashes, so it mounts `/v1/proxyd_routes/{path...}` while
// OpenAPI 3.1, having no multi-segment template, documents `{path}`. A foreign
// daemon mounting that must still be caught — the translation must not become
// an excuse.
func TestAssertServesNoForeignResource_CatchesForeignWildcardMount(t *testing.T) {
	const pattern = "GET /v1/proxyd_routes/{path...}"
	mux := muxServing(pattern)
	_, p := mux.Handler(httptest.NewRequest("GET", "/v1/proxyd_routes/a/b", nil))
	if !strings.Contains(p, "...") {
		t.Fatalf("mux resolved %q, not a multi-segment wildcard — nothing to exercise", p)
	}
	rec := &recorder{}
	AssertServesNoForeignResource(rec, "probe", mux)
	if len(rec.errs) == 0 {
		t.Fatal("a foreign {path...} mount was not reported — the {path} rendering is excusing a real collision")
	}
}

// TestAssertServesNoForeignResource_PassesWhenAbsent is that half's control.
func TestAssertServesNoForeignResource_PassesWhenAbsent(t *testing.T) {
	rec := &recorder{}
	n := AssertServesNoForeignResource(rec, "probe", muxServing("GET /health"))
	if n == 0 {
		t.Fatal("anchor rendered zero operations")
	}
	if len(rec.errs) != 0 {
		t.Errorf("guard reported failures for paths the mux does not serve: %v", rec.errs)
	}
}

// TestAssertServesNoForeignResource_OwnedIsNotForeign is the derivation's own
// control: mounting a resource through RegisterREST makes the daemon its owner,
// so its paths must not be reported. Without this the guard would fail every
// daemon on the resources it legitimately serves.
func TestAssertServesNoForeignResource_OwnedIsNotForeign(t *testing.T) {
	rec := &recorder{}
	AssertServesNoForeignResource(rec, "probe", MuxOf("routes"))
	if len(rec.errs) != 0 {
		t.Errorf("guard reported a resource the mux owns: %v", rec.errs)
	}
}

// TestAssertServesNoForeignResource_FatalsOnEmptyAnchor pins the non-vacuity
// guarantee: a mux owning every registered resource leaves nothing foreign to
// probe, and that must stop rather than silently assert over an empty set.
func TestAssertServesNoForeignResource_FatalsOnEmptyAnchor(t *testing.T) {
	var names []string
	for _, r := range resreg.All() {
		names = append(names, r.Name)
	}
	rec := &recorder{}
	rec.run(func() { AssertServesNoForeignResource(rec, "probe", MuxOf(names...)) })
	if len(rec.fatals) == 0 {
		t.Fatal("anchor accepted an empty foreign set — a vacuous anchor proves nothing")
	}
}
