package resreg

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// Args is where one handler reads from two wire formats. MCP delivers JSON, so
// numbers arrive as float64; REST query params arrive as strings straight off
// url.Values, so `?limit=50` is "50". Int's own contract is that both land on
// the same number, "or a filter silently ignores itself on one face". None of
// the accessors, and neither decoder, had a test.

func TestArgsStrReadsOnlyStrings(t *testing.T) {
	a := Args{"s": "hello", "n": float64(7), "b": true, "nil": nil, "list": []string{"x"}}
	if got := a.Str("s"); got != "hello" {
		t.Errorf("Str(s) = %q", got)
	}
	// A wrong type is "" and not a rendered value: a handler that gets "7" for
	// a field the caller sent as a number would write the wrong row rather
	// than reject the call.
	for _, key := range []string{"n", "b", "nil", "list", "absent"} {
		if got := a.Str(key); got != "" {
			t.Errorf("Str(%s) = %q, want the empty string", key, got)
		}
	}
}

func TestArgsIntAcceptsEveryNumericShapeBothFacesProduce(t *testing.T) {
	cases := map[string]struct {
		val  any
		want int64
	}{
		"MCP JSON number":     {float64(50), 50},
		"a Go int":            {int(50), 50},
		"a Go int64":          {int64(50), 50},
		"a REST query string": {"50", 50},
		"a negative string":   {"-3", -3},

		// Everything unparseable is 0, never a partial parse.
		"a float string":       {"50.5", 0},
		"a non-numeric string": {"fifty", 0},
		"an empty string":      {"", 0},
		"a bool":               {true, 0},
		"nil":                  {nil, 0},
	}
	for name, c := range cases {
		if got := (Args{"limit": c.val}).Int("limit"); got != c.want {
			t.Errorf("%s: Int = %d, want %d", name, got, c.want)
		}
	}
	if got := (Args{}).Int("absent"); got != 0 {
		t.Errorf("Int on an absent key = %d, want 0", got)
	}
	// A JSON float truncates rather than rounds; a limit of 50.9 is 50 rows.
	if got := (Args{"limit": 50.9}).Int("limit"); got != 50 {
		t.Errorf("Int(50.9) = %d, want 50", got)
	}
}

// ScopeClaims and Caller.Scopes are one encoding with two owners. An empty
// scope must decode to empty and never read as "unrestricted".
func TestScopeClaimsRoundTripAndEmptyIsNotUnrestricted(t *testing.T) {
	scopes := []string{"tasks:read", "secrets:write"}
	c := Caller{Claims: ScopeClaims(scopes)}
	got := c.Scopes()
	if len(got) != 2 || got[0] != "tasks:read" || got[1] != "secrets:write" {
		t.Errorf("Scopes = %v, want the two encoded", got)
	}

	if ScopeClaims(nil) != nil {
		t.Error("ScopeClaims(nil) must be nil, not an empty map with an empty scope string")
	}
	for name, c := range map[string]Caller{
		"no claims at all": {},
		"an empty map":     {Claims: map[string]string{}},
		"an empty value":   {Claims: map[string]string{ScopeClaimKey: ""}},
		"only whitespace":  {Claims: map[string]string{ScopeClaimKey: "   "}},
	} {
		if got := c.Scopes(); len(got) != 0 {
			t.Errorf("%s: Scopes = %v, want empty — a Gate reads this as 'no scope proven'", name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// decodeRESTArgs
// ---------------------------------------------------------------------------

func TestDecodeRESTArgsLayersQueryBodyAndPath(t *testing.T) {
	e := Endpoint{Verb: "POST", Path: "/v1/tasks/{id}"}

	r := httptest.NewRequest("POST", "/v1/tasks/42?limit=50&folder=acme",
		strings.NewReader(`{"folder":"from-body","title":"x"}`))
	r.SetPathValue("id", "42")

	args, err := decodeRESTArgs(r, e)
	if err != nil {
		t.Fatalf("decodeRESTArgs: %v", err)
	}
	if args.Int("limit") != 50 {
		t.Errorf("limit = %v; a query param must survive as a number", args["limit"])
	}
	if args.Str("title") != "x" {
		t.Errorf("title = %q, want the body field", args.Str("title"))
	}
	// The body overrides the query on a collision, and the path overrides both.
	if args.Str("folder") != "from-body" {
		t.Errorf("folder = %q, want the body to win over the query", args.Str("folder"))
	}
	if args.Str("id") != "42" {
		t.Errorf("id = %q, want the path value", args.Str("id"))
	}
}

// DisallowUnknownFields is off for the map decode, but malformed JSON must be
// an error rather than an empty Args the handler acts on.
func TestDecodeRESTArgsRejectsMalformedJSON(t *testing.T) {
	e := Endpoint{Verb: "POST", Path: "/v1/tasks"}
	r := httptest.NewRequest("POST", "/v1/tasks", strings.NewReader(`{"folder":`))
	if _, err := decodeRESTArgs(r, e); err == nil {
		t.Fatal("malformed JSON decoded as an empty argument set")
	}
}

// An empty body on a POST is not an error: several endpoints take everything
// from the path.
func TestDecodeRESTArgsAcceptsAnEmptyBody(t *testing.T) {
	e := Endpoint{Verb: "POST", Path: "/v1/tasks/{id}/run"}
	r := httptest.NewRequest("POST", "/v1/tasks/9/run", strings.NewReader(""))
	r.SetPathValue("id", "9")
	args, err := decodeRESTArgs(r, e)
	if err != nil {
		t.Fatalf("an empty body was rejected: %v", err)
	}
	if args.Str("id") != "9" {
		t.Errorf("id = %q", args.Str("id"))
	}
}

func TestDecodeRESTArgsIgnoresABodyOnGET(t *testing.T) {
	e := Endpoint{Verb: "GET", Path: "/v1/tasks"}
	r := httptest.NewRequest("GET", "/v1/tasks?folder=acme", strings.NewReader(`{"folder":"other"}`))
	args, err := decodeRESTArgs(r, e)
	if err != nil {
		t.Fatal(err)
	}
	if args.Str("folder") != "acme" {
		t.Errorf("folder = %q; a body on a GET must not override the query", args.Str("folder"))
	}
}

// ---------------------------------------------------------------------------
// decodeMCPArgs
// ---------------------------------------------------------------------------

func mcpCall(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = "t"
	req.Params.Arguments = args
	return req
}

func TestDecodeMCPArgsCarriesEachDeclaredType(t *testing.T) {
	spec := []MCPArg{
		{Name: "folder", Type: "string"},
		{Name: "limit", Type: "number"},
		{Name: "dry_run", Type: "bool"},
		{Name: "tags", Type: "array"},
	}
	got := decodeMCPArgs(mcpCall(map[string]any{
		"folder":  "acme",
		"limit":   float64(50),
		"dry_run": false,
		"tags":    []any{"a", "b"},
	}), spec)

	if got.Str("folder") != "acme" {
		t.Errorf("folder = %q", got.Str("folder"))
	}
	if got.Int("limit") != 50 {
		t.Errorf("limit = %v", got["limit"])
	}
	// false must survive as false rather than be dropped as a zero value: a
	// dropped dry_run turns a rehearsal into the real thing.
	v, present := got["dry_run"]
	if !present {
		t.Error("dry_run=false was dropped; the handler would read its own default")
	} else if v != false {
		t.Errorf("dry_run = %v, want false", v)
	}
	if list, _ := got["tags"].([]string); len(list) != 2 {
		t.Errorf("tags = %#v, want two entries", got["tags"])
	}
}

// An argument the caller did not send must be absent, not present-and-zero:
// the handler distinguishes "not asked for" from "asked for nothing".
func TestDecodeMCPArgsOmitsWhatWasNotSent(t *testing.T) {
	spec := []MCPArg{
		{Name: "folder", Type: "string"},
		{Name: "limit", Type: "number"},
		{Name: "dry_run", Type: "bool"},
		{Name: "tags", Type: "array"},
	}
	got := decodeMCPArgs(mcpCall(map[string]any{}), spec)
	for _, k := range []string{"folder", "limit", "dry_run", "tags"} {
		if _, present := got[k]; present {
			t.Errorf("%s is present with value %#v for a call that did not send it", k, got[k])
		}
	}
}

// An argument the resource never declared must not reach the handler, whatever
// the caller sends.
func TestDecodeMCPArgsDropsUndeclaredArguments(t *testing.T) {
	got := decodeMCPArgs(mcpCall(map[string]any{
		"folder": "acme", "sudo": true, "folder_override": "other",
	}), []MCPArg{{Name: "folder", Type: "string"}})

	if len(got) != 1 || got.Str("folder") != "acme" {
		t.Errorf("decoded %#v, want only the declared folder", got)
	}
}

func TestMCPMetaStringReadsTheMetaBlockOnly(t *testing.T) {
	req := mcpCall(map[string]any{
		"_meta":   map[string]any{"turn_id": "t-1", "n": float64(3)},
		"turn_id": "not-this-one",
	})
	if got := mcpMetaString(req, "turn_id"); got != "t-1" {
		t.Errorf("mcpMetaString = %q, want the _meta value", got)
	}
	if got := mcpMetaString(req, "n"); got != "" {
		t.Errorf("a non-string _meta value returned %q, want empty", got)
	}
	if got := mcpMetaString(req, "absent"); got != "" {
		t.Errorf("an absent key returned %q", got)
	}
	if got := mcpMetaString(mcpCall(map[string]any{"_meta": "not-a-map"}), "turn_id"); got != "" {
		t.Errorf("a non-map _meta returned %q", got)
	}
	if got := mcpMetaString(mcpCall(nil), "turn_id"); got != "" {
		t.Errorf("no arguments at all returned %q", got)
	}
}

// BUGS L123: numArg accepts float64/int/int64 and nothing else, and
// decodeMCPArgs only writes the key when numArg reports ok — so an agent that
// stringifies a number has the argument silently DROPPED and the handler reads
// its own default. The REST twin of the same call parses "50" into 50, and
// MCP's own bool decoder coerces "true", so the surface disagrees with itself
// as well as with REST.
//
// Skipped rather than deleted: the entry is open and this is the assertion the
// coerce fix has to satisfy. Un-skip with the fix.
func TestDecodeMCPArgsMatchesRESTOnAStringifiedNumber(t *testing.T) {
	t.Skip("BUGS L123 — open: a stringified number is dropped by decodeMCPArgs")

	got := decodeMCPArgs(mcpCall(map[string]any{"limit": "50"}),
		[]MCPArg{{Name: "limit", Type: "number"}})
	if _, present := got["limit"]; !present {
		t.Fatal("a stringified number was dropped; the handler reads its own default")
	}

	r := httptest.NewRequest("GET", "/v1/x?limit=50", strings.NewReader(""))
	rest, err := decodeRESTArgs(r, Endpoint{Verb: "GET", Path: "/v1/x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Int("limit") != rest.Int("limit") {
		t.Errorf("MCP gave %d and REST gave %d for the same value",
			got.Int("limit"), rest.Int("limit"))
	}
}
