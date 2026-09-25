package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// seed writes a data dir with the given .env body and an empty services/.
func seed(t *testing.T, env string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func gen(t *testing.T, dir string) string {
	t.Helper()
	out, _, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// genRoutes is gen for the other half of Generate's output: the proxyd route
// table the run decided on, which the CLI writes into proxyd_routes.
func genRoutes(t *testing.T, dir string) []ProxydRoute {
	t.Helper()
	_, routes, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

func hasRoute(routes []ProxydRoute, path string) bool {
	return slices.ContainsFunc(routes, func(r ProxydRoute) bool { return r.Path == path })
}

// The split plane (authd/routd/runed) is the only topology: emitted in every
// generate, carrying no profile, and gated appears nowhere.
func TestGenerateBaseDaemons(t *testing.T) {
	dir := seed(t, "ASSISTANT_NAME=test\nAPI_PORT=8080\n")
	out := gen(t, dir)

	if strings.Contains(out, "  gated:\n") || strings.Contains(out, "gated:8080") {
		t.Error("gated must not appear (removed)")
	}
	for _, svc := range []string{"authd", "routd", "runed"} {
		blk := serviceBlock(out, svc)
		if blk == "" {
			t.Fatalf("missing service %q", svc)
		}
		if strings.Contains(blk, "profiles:") {
			t.Errorf("%s must not be profile-gated (core plane)", svc)
		}
		if !strings.Contains(blk, "entrypoint: ['"+svc+"']") {
			t.Errorf("%s missing entrypoint ['%s']", svc, svc)
		}
		if !strings.Contains(blk, "/health") {
			t.Errorf("%s missing /health healthcheck", svc)
		}
		// runed is the exception and it is root: virtiofsd carries guest
		// credentials to host files and the staging tree's binds are made in its
		// own mount namespace (spec 6/41 §"The runed deploy topology").
		if svc == "runed" {
			continue
		}
		if !strings.Contains(blk, "user: '1000:1000'") {
			t.Errorf("%s missing user 1000 — would create root-owned files in the data dir", svc)
		}
	}
	// NO daemon mounts the docker socket: the turn is a KVM guest, so runed holds
	// device nodes instead (spec 6/41 step 4 deleted the container substrate).
	for _, svc := range []string{"runed", "routd", "authd"} {
		if strings.Contains(serviceBlock(out, svc), "docker.sock") {
			t.Errorf("%s must NOT mount docker.sock", svc)
		}
	}
	// routd publishes API_PORT:8080 so the host CLI reaches /v1/channels.
	if !strings.Contains(serviceBlock(out, "routd"), "'8080:8080'") {
		t.Errorf("routd must publish API_PORT:8080; got:\n%s", serviceBlock(out, "routd"))
	}
	// authd resolves login/refresh scopes against routd's ACL owner.
	if !strings.Contains(serviceBlock(out, "authd"), "GRANTS_URL: 'http://routd:8080'") {
		t.Error("authd missing GRANTS_URL=http://routd:8080")
	}
}

// Every optional daemon is ALWAYS emitted and carries a compose profile —
// COMPOSE_PROFILES is the only gate. No Go conditional decides emission.
func TestOptionalDaemonsCarryProfiles(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	out := gen(t, dir)

	want := map[string]string{
		"webd": "web", "proxyd": "web", "vited": "web", "dashd": "web",
		"timed": "timed", "onbod": "onbod", "davd": "davd",
	}
	for svc, profile := range want {
		blk := serviceBlock(out, svc)
		if blk == "" {
			t.Errorf("service %q must be emitted even when its profile is inactive", svc)
			continue
		}
		if !strings.Contains(blk, "profiles: ['"+profile+"']") {
			t.Errorf("%s must carry profiles: ['%s']; got:\n%s", svc, profile, blk)
		}
	}
	// proxyd's depends_on may only point at services sharing its profile —
	// docker rejects a depends_on into an inactive profile.
	if !strings.Contains(serviceBlock(out, "proxyd"), "depends_on: [routd, dashd, webd]") {
		t.Errorf("proxyd depends_on must be [routd, dashd, webd]; got:\n%s", serviceBlock(out, "proxyd"))
	}
}

// COMPOSE_PROFILES is derived from the feature flags and written into the data
// dir's .env, along with the identity vars the fragments interpolate.
func TestManagedEnvBlock(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		profiles string
	}{
		{"bare", "API_PORT=8080\n", "timed"},
		{"web", "WEB_PORT=8095\n", "davd,timed,web"},
		{"web no dav", "WEB_PORT=8095\nWEBDAV_ENABLED=false\n", "timed,web"},
		{"onboarding", "WEB_PORT=8095\nONBOARDING_ENABLED=true\n", "davd,onbod,timed,web"},
		{"egress", "EGRESS_IMAGE=arizuko-egress:latest\n", "timed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := seed(t, tc.env)
			gen(t, dir)
			got := read(t, dir, ".env")
			if !strings.Contains(got, "COMPOSE_PROFILES="+tc.profiles+"\n") {
				t.Errorf("want COMPOSE_PROFILES=%s; .env:\n%s", tc.profiles, got)
			}
			for _, want := range []string{"APP=", "FLAVOR=", "DATA_DIR=" + dir} {
				if !strings.Contains(got, want) {
					t.Errorf("managed block missing %q; .env:\n%s", want, got)
				}
			}
		})
	}
}

// Regenerate rewrites the managed block in place: operator lines survive and
// the block is not duplicated.
func TestManagedEnvBlockIdempotent(t *testing.T) {
	dir := seed(t, "ASSISTANT_NAME=test\nTELEGRAM_BOT_TOKEN=tok\n")
	gen(t, dir)
	gen(t, dir)
	got := read(t, dir, ".env")
	if n := strings.Count(got, managedBegin); n != 1 {
		t.Errorf("managed block written %d times, want 1; .env:\n%s", n, got)
	}
	if !strings.Contains(got, "TELEGRAM_BOT_TOKEN=tok") || !strings.Contains(got, "ASSISTANT_NAME=test") {
		t.Errorf("operator .env lines must survive regenerate; got:\n%s", got)
	}
	if st, err := os.Stat(filepath.Join(dir, ".env")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf(".env must stay 0600 (holds AUTH_SECRET); got %v %v", st.Mode().Perm(), err)
	}
}

// Each services/<name>.yml is included verbatim by docker compose — sorted, one
// include per fragment, and no `include:` key at all when there are none.
func TestIncludeFragments(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	if out := gen(t, dir); strings.Contains(out, "include:") {
		t.Errorf("no fragments → no include key; got:\n%s", out)
	}
	write(t, dir, "services/teled.yml", "services:\n  teled:\n    image: arizuko:latest\n")
	write(t, dir, "services/discd.yml", "services:\n  discd:\n    image: arizuko:latest\n")
	write(t, dir, "services/notes.txt", "ignored")
	out := gen(t, dir)
	if !strings.Contains(out, "include:\n  - ./services/discd.yml\n  - ./services/teled.yml\n") {
		t.Errorf("fragments must be included in sorted order; got:\n%s", out)
	}
	if strings.Contains(out, "notes") {
		t.Error("non-.yml files must be ignored")
	}
	// The fragment defines the service; compose must not also emit it inline.
	if strings.Contains(out, "  teled:\n") {
		t.Error("adapter service must come from the fragment, not an inline stanza")
	}
}

func TestFragmentFilenameValidated(t *testing.T) {
	dir := seed(t, "")
	write(t, dir, "services/bad name.yml", "services: {}\n")
	if _, _, err := Generate(dir); err == nil {
		t.Error("expected error for invalid fragment filename")
	}
}

func TestGenerateNoServicesDir(t *testing.T) {
	if _, _, err := Generate(t.TempDir()); err == nil {
		t.Error("expected error for missing services dir")
	}
}

// Generate returns the route table: core routes plus each fragment's sibling
// routes file, gated_by filtered. The CLI writes it into proxyd_routes — it is
// NOT config on the compose file. A `PROXYD_ROUTES_JSON` env var was read once,
// on the first boot of an empty table, and ignored for the life of the instance
// after that (BUGS L45), so proxyd's block must carry no route config at all.
func TestProxydRoutesAssembled(t *testing.T) {
	dir := seed(t, "WEB_PORT=8095\nAPI_PORT=8080\nSLACK_BOT_TOKEN=tok\n")
	write(t, dir, "services/slakd.yml", "services:\n  slakd:\n    image: arizuko:latest\n")
	write(t, dir, "services/slakd.yaml",
		"proxyd_routes:\n  - path: /slack/\n    backend: http://slakd:8080\n    auth: public\n    gated_by: SLACK_BOT_TOKEN\n")
	out, routes, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if proxyd := serviceBlock(out, "proxyd"); strings.Contains(proxyd, "PROXYD_ROUTES_JSON") {
		t.Errorf("proxyd still carries PROXYD_ROUTES_JSON; got:\n%s", proxyd)
	}
	want := map[string]ProxydRoute{
		// A package's route carries the third owner value; a core route's stays
		// unset so SyncProxydRoutes defaults it to arizuko (BUGS L46).
		"/slack/": {Path: "/slack/", Backend: "http://slakd:8080", Auth: "public",
			GatedBy: "SLACK_BOT_TOKEN", Owner: store.RouteOwnerPackage("slakd")},
		// `optional`, not `public`: proxyd identifies a caller who offers a
		// bearer so webd can attribute the message, and forwards everyone else
		// anonymously with the transit proof (specs/6/22 step 1).
		"/chat/": {Path: "/chat/", Backend: "http://webd:8080", Auth: "optional"},
		"/dash/": {Path: "/dash/", Backend: "http://dashd:8080", Auth: "user"},
		// Pairing (spec 5/31-identity-pairing): auth `user` so an anonymous visitor is bounced
		// through OAuth before the confirm page renders.
		"/pair/": {Path: "/pair/", Backend: "http://webd:8080", Auth: "user"},
	}
	got := map[string]ProxydRoute{}
	for _, r := range routes {
		got[r.Path] = r
	}
	for path, w := range want {
		if !reflect.DeepEqual(got[path], w) {
			t.Errorf("route %s = %+v, want %+v", path, got[path], w)
		}
	}
	if !hasRoute(routes, "/dav/") {
		t.Error("/dav/ missing (WEBDAV_ENABLED defaults on)")
	}
	// ONBOARDING_ENABLED unset → onbod routes dropped.
	if hasRoute(routes, "/invite/") {
		t.Error("/invite/ route must be dropped when ONBOARDING_ENABLED is unset")
	}
}

// The /me user portal (webd/me.go, 13 handlers) shipped unreachable: spec 5/V-web-vhosts
// lists /me among the globally-served reserved prefixes, but coreProxydRoutes
// never declared it, so proxyd's MatchRoute missed and the final catch-all
// turned every /me/* request into a /pub/me/* redirect. Assert the exact
// routes — path, backend and auth in one shape — and that no gate can drop
// them: a portal that only appears when some env var is set is the same dead
// surface again.
func TestProxydRoutesUserPortal(t *testing.T) {
	// Onboarding/WebDAV off, no service fragments: the barest instance still
	// serves /me.
	routes := genRoutes(t, seed(t, "WEB_PORT=8095\nWEBDAV_ENABLED=false\n"))
	for _, want := range []ProxydRoute{
		{Path: "/me", Backend: "http://webd:8080", Auth: "user"},
		{Path: "/me/", Backend: "http://webd:8080", Auth: "user"},
	} {
		if !slices.ContainsFunc(routes, func(r ProxydRoute) bool { return reflect.DeepEqual(r, want) }) {
			t.Errorf("routes missing %+v; got:\n%+v", want, routes)
		}
	}
}

func TestProxydRoutesGating(t *testing.T) {
	routesYAML := "proxyd_routes:\n  - path: /slack/\n    backend: http://slakd:8080\n    auth: public\n    gated_by: SLACK_BOT_TOKEN\n"
	for _, tc := range []struct {
		name, env string
		want      bool
	}{
		{"gate unset", "WEB_PORT=8095\n", false},
		{"gate set", "WEB_PORT=8095\nSLACK_BOT_TOKEN=tok\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := seed(t, tc.env)
			write(t, dir, "services/slakd.yml", "services:\n  slakd:\n    image: arizuko:latest\n")
			write(t, dir, "services/slakd.yaml", routesYAML)
			if got := hasRoute(genRoutes(t, dir), "/slack/"); got != tc.want {
				t.Errorf("route present=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestProxydRoutesOnboardingAndWebDAVGates(t *testing.T) {
	on := genRoutes(t, seed(t, "WEB_PORT=8095\nONBOARDING_ENABLED=true\n"))
	if !hasRoute(on, "/invite/") {
		t.Error("/invite/ route missing when ONBOARDING_ENABLED=true")
	}
	off := genRoutes(t, seed(t, "WEB_PORT=8095\nWEBDAV_ENABLED=false\n"))
	if hasRoute(off, "/dav/") {
		t.Error("/dav/ route must be dropped when WEBDAV_ENABLED=false")
	}
}

func TestProxydPortAliases(t *testing.T) {
	out := gen(t, seed(t, "WEB_PORT=49165\nWEB_PORT_ALIASES=49177\n"))
	proxyd := serviceBlock(out, "proxyd")
	if !strings.Contains(proxyd, "'49165:8080'") || !strings.Contains(proxyd, "'49177:8080'") {
		t.Errorf("proxyd must publish WEB_PORT + aliases; got:\n%s", proxyd)
	}
}

// In the split, routd OWNS secrets and dashd writes user secrets into routd.db
// directly, so both env files carry SECRETS_KEY. routd REFUSES to start without
// it and every store write path refuses to seal without it, so a daemon that
// misses it here cannot write a secret at all (BUGS S2).
//
// runed must NOT carry it (BUGS X3): it receives already-decrypted model
// credentials in RunRequest.Secrets and opens no secrets table, so the key had
// no reader there — just the fleet's decryption key sitting on the one daemon
// holding docker.sock.
func TestSplitScopesSecretsKey(t *testing.T) {
	dir := seed(t, "AUTH_SECRET=s\nSECRETS_KEY=deadbeef\n")
	gen(t, dir)
	for _, d := range []string{"routd", "dashd"} {
		if !strings.Contains(read(t, dir, "env/"+d+".env"), "SECRETS_KEY=deadbeef") {
			t.Errorf("env/%s.env must carry SECRETS_KEY", d)
		}
	}
	if strings.Contains(read(t, dir, "env/runed.env"), "SECRETS_KEY=") {
		t.Error("env/runed.env must not carry SECRETS_KEY: runed reads it nowhere")
	}
}

// A daemon's env file carries a credential only if the daemon's code reads it.
// authd owns OAuth (a56202d4), so onbod's provider block was pure exposure
// surface; webd reads neither AUTH_SECRET nor AUTH_BASE_URL, proxyd reads only
// AUTH_SECRET (its auth-enabled flag). dashd DOES read AUTH_SECRET, via
// os.Getenv, and is asserted here so a reader search does not drop it (BUGS L7).
func TestAuthKeysScopedToReaders(t *testing.T) {
	dir := seed(t, "AUTH_SECRET=s\nAUTH_BASE_URL=https://x\nGOOGLE_CLIENT_ID=g\n"+
		"GOOGLE_CLIENT_SECRET=gs\nGOOGLE_ALLOWED_EMAILS=*@x\nGITHUB_CLIENT_ID=h\n"+
		"GITHUB_CLIENT_SECRET=hs\nGITHUB_ALLOWED_ORG=o\nDISCORD_CLIENT_ID=d\n"+
		"DISCORD_CLIENT_SECRET=ds\nONBOARDING_ENABLED=true\n")
	gen(t, dir)

	unread := map[string][]string{
		"onbod": {"AUTH_SECRET", "GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET",
			"GITHUB_ALLOWED_ORG", "DISCORD_CLIENT_ID", "DISCORD_CLIENT_SECRET",
			"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_ALLOWED_EMAILS"},
		"webd":   {"AUTH_SECRET", "AUTH_BASE_URL"},
		"proxyd": {"AUTH_BASE_URL"},
	}
	for daemon, keys := range unread {
		got := read(t, dir, "env/"+daemon+".env")
		for _, key := range keys {
			if strings.Contains(got, key+"=") {
				t.Errorf("env/%s.env must not carry %s: %s reads it nowhere", daemon, key, daemon)
			}
		}
	}

	readers := map[string][]string{
		"authd":  {"AUTH_SECRET", "AUTH_BASE_URL", "GOOGLE_CLIENT_ID"},
		"dashd":  {"AUTH_SECRET"},
		"proxyd": {"AUTH_SECRET"},
		"onbod":  {"AUTH_BASE_URL"},
	}
	for daemon, keys := range readers {
		got := read(t, dir, "env/"+daemon+".env")
		for _, key := range keys {
			if !strings.Contains(got, key+"=") {
				t.Errorf("env/%s.env must carry %s: %s reads it", daemon, key, daemon)
			}
		}
	}
}

// The harness occupant is chosen by env, and runed is the only reader: it
// resolves ARIZUKO_BACKEND into cfg.AgentBackend and re-emits it as container
// env. Left out of runed's allowlist, the documented certification procedure
// (set it in the instance .env, restart, run anteval) is a silent no-op —
// measured on krons 2026-08-27, where the value never reached env/runed.env.
func TestRunedReceivesTheBackendSelector(t *testing.T) {
	dir := seed(t, "ARIZUKO_BACKEND=codex\n")
	gen(t, dir)
	if runed := read(t, dir, "env/runed.env"); !strings.Contains(runed, "ARIZUKO_BACKEND=codex") {
		t.Error("env/runed.env must carry ARIZUKO_BACKEND: runed is the only daemon that reads it")
	}
}

func TestModelCredentialsScopedToRuned(t *testing.T) {
	const credentials = "ANTHROPIC_API_KEY=anthropic\n" +
		"CLAUDE_CODE_OAUTH_TOKEN=claude\n" +
		"OPENAI_API_KEY=openai\n" +
		"CODEX_API_KEY=codex\n"
	dir := seed(t, credentials)
	gen(t, dir)

	runed := read(t, dir, "env/runed.env")
	for credential := range strings.FieldsSeq(credentials) {
		if !strings.Contains(runed, credential) {
			key, _, _ := strings.Cut(credential, "=")
			t.Errorf("env/runed.env must carry %s", key)
		}
	}
	for daemon := range daemonKeys {
		if daemon == "runed" {
			continue
		}
		got := read(t, dir, "env/"+daemon+".env")
		for credential := range strings.FieldsSeq(credentials) {
			key, _, _ := strings.Cut(credential, "=")
			if strings.Contains(got, key+"=") {
				t.Errorf("env/%s.env must not carry %s", daemon, key)
			}
		}
	}
}

// Surrogate OAuth creds (spec 5/15) reach BOTH consumers: dashd runs the
// Connect-GitHub dance, routd's broker refreshes near-expiry tokens.
func TestSurrogateKeysScopedToDashdAndRoutd(t *testing.T) {
	dir := seed(t, "SURROGATE_GITHUB_CLIENT_ID=cid\nSURROGATE_GITHUB_CLIENT_SECRET=csec\n")
	gen(t, dir)
	for _, d := range []string{"dashd", "routd"} {
		env := read(t, dir, "env/"+d+".env")
		for _, kv := range []string{"SURROGATE_GITHUB_CLIENT_ID=cid", "SURROGATE_GITHUB_CLIENT_SECRET=csec"} {
			if !strings.Contains(env, kv) {
				t.Errorf("env/%s.env must carry %s", d, kv)
			}
		}
	}
}

// A present channel adapter is wired as a service principal (spec 5/1); a
// non-adapter package is not.
func TestServiceKeyWiring(t *testing.T) {
	dir := seed(t, "AUTH_SECRET=s\nONBOARDING_ENABLED=true\n")
	write(t, dir, "services/slakd.yml", "services:\n  slakd:\n    image: arizuko:latest\n")
	write(t, dir, "services/ttsd.yml", "services:\n  ttsd:\n    image: arizuko-ttsd:latest\n")
	gen(t, dir)

	slakd := read(t, dir, "env/slakd.env")
	for _, want := range []string{"AUTHD_SERVICE_KEY=", "AUTHD_SERVICE_NAME=slakd", "AUTHD_URL="} {
		if !strings.Contains(slakd, want) {
			t.Errorf("env/slakd.env must carry %s; got:\n%s", want, slakd)
		}
	}
	authd := read(t, dir, "env/authd.env")
	for _, want := range []string{"service:slakd=", "service:onbod=", "service:routd=", "service:runed=", "service:timed="} {
		if !strings.Contains(authd, want) {
			t.Errorf("AUTHD_SERVICE_KEYS must register %q; got:\n%s", want, authd)
		}
	}
	// ttsd never posts inbound — no message principal.
	if strings.Contains(authd, "service:ttsd=") {
		t.Errorf("ttsd must not be wired as a message principal; got:\n%s", authd)
	}
	// authd holds the list, never a bare key of its own peers.
	if strings.Contains(authd, "\nAUTHD_SERVICE_KEY=") {
		t.Errorf("authd env must not carry a bare AUTHD_SERVICE_KEY; got:\n%s", authd)
	}
}

// AUTHD_URL reaches every consumer; each daemon's key is distinct and survives
// a redeploy (otherwise in-flight tokens are invalidated on every restart).
func TestServiceKeysDistinctAndStable(t *testing.T) {
	dir := seed(t, "AUTH_SECRET=s\nWEB_PORT=8095\nONBOARDING_ENABLED=true\n")
	gen(t, dir)
	seen := map[string]string{}
	for _, d := range []string{"routd", "runed", "timed", "proxyd", "webd", "onbod"} {
		env := read(t, dir, "env/"+d+".env")
		if !strings.Contains(env, "AUTHD_URL=http://authd:8080") {
			t.Errorf("%s env missing AUTHD_URL", d)
		}
		key := readEnvFileKey(filepath.Join(dir, "env", d+".env"), "AUTHD_SERVICE_KEY")
		if key == "" {
			t.Fatalf("%s has no AUTHD_SERVICE_KEY", d)
		}
		if other, dup := seen[key]; dup {
			t.Errorf("%s reuses %s's service key", d, other)
		}
		seen[key] = d
	}
	before := readEnvFileKey(filepath.Join(dir, "env", "routd.env"), "AUTHD_SERVICE_KEY")
	gen(t, dir)
	if after := readEnvFileKey(filepath.Join(dir, "env", "routd.env"), "AUTHD_SERVICE_KEY"); after != before {
		t.Errorf("service key must persist across regenerate: %q -> %q", before, after)
	}
}

// timed federates its fire loop over routd HTTP (no messages.db).
func TestTimedWiring(t *testing.T) {
	dir := seed(t, "AUTH_SECRET=s\nTZ=Europe/Prague\n")
	out := gen(t, dir)
	timed := serviceBlock(out, "timed")
	if !strings.Contains(timed, `ROUTER_URL: "http://routd:8080"`) || !strings.Contains(timed, `TIMEZONE: "Europe/Prague"`) {
		t.Errorf("timed wiring wrong; got:\n%s", timed)
	}
	// timed opens no DB and reads no file — a data-dir mount would hand the
	// scheduler every other daemon's database for nothing.
	if strings.Contains(timed, "volumes:") || strings.Contains(timed, "DATA_DIR") {
		t.Errorf("timed must have no data-dir mount; got:\n%s", timed)
	}
	if !strings.Contains(serviceBlock(out, "onbod"), `ROUTER_URL: "http://routd:8080"`) {
		t.Error("onbod ROUTER_URL must be routd")
	}
	if !strings.Contains(serviceBlock(out, "webd"), `ROUTER_URL: "http://routd:8080"`) {
		t.Error("webd ROUTER_URL must be routd")
	}
}

// Egress isolation needs no switch any more: the guest's tap accepts the proxy
// and drops everything else, and runed refuses to start without the proxy binary
// (spec 6/41 §"Egress by name, sealed below the kernel"). What compose still owes
// is the absence it always owed — no long-lived proxy service and no declared
// networks, because the proxy is one process per turn and dies with it.
func TestEgressDeclaresNoProxyService(t *testing.T) {
	dir := seed(t, "")
	out := gen(t, dir)
	if strings.Contains(out, "  crackbox:\n") {
		t.Error("compose must not declare a long-lived proxy service")
	}
	if strings.Contains(out, "\nnetworks:\n") {
		t.Error("compose must not declare networks — the guest's tap is per turn")
	}
	for _, svc := range []string{"runed", "routd", "authd"} {
		env := read(t, dir, "env/"+svc+".env")
		if strings.Contains(env, "EGRESS_IMAGE") || strings.Contains(env, "EGRESS_NETWORK_PREFIX") {
			t.Errorf("%s env still carries a docker-substrate egress var:\n%s", svc, env)
		}
	}
}

const ttsdManifest = "env:\n  TTS_ENABLED: {default: 'true'}\n  TTS_BASE_URL: {default: 'http://ttsd:8880'}\n"

// A package's `default` env needs reach the daemons that read them on every
// generate, without touching .env; an explicit .env value wins; and nothing is
// derived for a package that is not installed.
func TestPackageEnvDefaultsReachRuned(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	write(t, dir, "services/ttsd.yml", "services:\n  ttsd:\n    image: arizuko-ttsd:latest\n")
	write(t, dir, "services/ttsd.yaml", ttsdManifest)
	gen(t, dir)
	runed := read(t, dir, "env/runed.env")
	if !strings.Contains(runed, "TTS_ENABLED=true") || !strings.Contains(runed, "TTS_BASE_URL=http://ttsd:8880") {
		t.Errorf("ttsd package must enable TTS on runed; got:\n%s", runed)
	}
	if strings.Contains(read(t, dir, ".env"), "TTS_") {
		t.Error("a default is derived per generate, never written to .env")
	}

	off := seed(t, "API_PORT=8080\n")
	gen(t, off)
	if strings.Contains(read(t, off, "env/runed.env"), "TTS_ENABLED") {
		t.Error("TTS must stay off without the ttsd package")
	}

	override := seed(t, "TTS_BASE_URL=https://api.openai.com\n")
	write(t, override, "services/ttsd.yml", "services:\n  ttsd:\n    image: arizuko-ttsd:latest\n")
	write(t, override, "services/ttsd.yaml", ttsdManifest)
	gen(t, override)
	if !strings.Contains(read(t, override, "env/runed.env"), "TTS_BASE_URL=https://api.openai.com") {
		t.Error("explicit TTS_BASE_URL must win over the package default")
	}
}

const gatewayManifest = "env:\n  MODEL_GATEWAY_KEY: {generate: true}\n  MODEL_GATEWAY_URL: {default: 'http://litellm:4000'}\n"

// A `generate` need is minted once into .env above the managed block — where
// docker interpolates the fragment from and the operator edits — and the same
// run hands it to runed. A second generate keeps the value; an operator value
// set afterwards wins over the minted one.
func TestPackageEnvGenerateMintsOnceIntoDotEnv(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	write(t, dir, "services/litellm.yml", "services:\n  litellm:\n    image: x\n")
	write(t, dir, "services/litellm.yaml", gatewayManifest)
	gen(t, dir)

	dotenv := read(t, dir, ".env")
	key := envLine(t, dotenv, "MODEL_GATEWAY_KEY")
	if len(key) != 64 {
		t.Fatalf("MODEL_GATEWAY_KEY = %q, want a 32-byte hex token", key)
	}
	if strings.Index(dotenv, "MODEL_GATEWAY_KEY=") > strings.Index(dotenv, managedBegin) {
		t.Errorf("minted key must sit above the managed block:\n%s", dotenv)
	}
	if strings.Contains(dotenv, "MODEL_GATEWAY_URL=") {
		t.Error("a default is never written to .env")
	}
	if routd := read(t, dir, "env/routd.env"); !strings.Contains(routd, "MODEL_GATEWAY_KEY="+key) {
		t.Errorf("routd must receive the minted master key; it mints each turn's virtual key with it:\n%s", routd)
	}
	if runed := read(t, dir, "env/runed.env"); !strings.Contains(runed, "MODEL_GATEWAY_URL=http://litellm:4000") {
		t.Errorf("runed must receive the default URL:\n%s", runed)
	}

	gen(t, dir)
	if again := envLine(t, read(t, dir, ".env"), "MODEL_GATEWAY_KEY"); again != key {
		t.Errorf("second generate re-minted the key: %q → %q", key, again)
	}
	if st, err := os.Stat(filepath.Join(dir, ".env")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf(".env must stay 0600; got %v %v", st.Mode().Perm(), err)
	}

	mine := seed(t, "MODEL_GATEWAY_KEY=operators-own\n")
	write(t, mine, "services/litellm.yml", "services:\n  litellm:\n    image: x\n")
	write(t, mine, "services/litellm.yaml", gatewayManifest)
	gen(t, mine)
	if strings.Count(read(t, mine, ".env"), "MODEL_GATEWAY_KEY=") != 1 ||
		!strings.Contains(read(t, mine, "env/routd.env"), "MODEL_GATEWAY_KEY=operators-own") {
		t.Error("an operator-set key must win and never be duplicated")
	}
}

// envLine returns KEY's value from a KEY=VALUE file body, failing when absent.
func envLine(t *testing.T, body, key string) string {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	t.Fatalf("%s missing from:\n%s", key, body)
	return ""
}

// A `required` need with no operator value fails generate, naming the key and
// the file, rather than emitting a compose whose service will not start; and a
// key that is present but empty refuses too, since the platform never edits an
// operator line.
func TestPackageEnvRequiredRefusesLoud(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	write(t, dir, "services/tok.yml", "services:\n  tok:\n    image: x\n")
	write(t, dir, "services/tok.yaml", "env:\n  TOK_TOKEN: {required: true}\n")
	_, _, err := Generate(dir)
	if err == nil || !strings.Contains(err.Error(), "TOK_TOKEN") || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("Generate = %v, want a refusal naming TOK_TOKEN and .env", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "env", "runed.env")); serr == nil {
		t.Error("a refused generate must write no env file")
	}

	write(t, dir, ".env", "TOK_TOKEN=abc\n")
	gen(t, dir)

	empty := seed(t, "MODEL_GATEWAY_KEY=\n")
	write(t, empty, "services/litellm.yml", "services:\n  litellm:\n    image: x\n")
	write(t, empty, "services/litellm.yaml", gatewayManifest)
	if _, _, err := Generate(empty); err == nil || !strings.Contains(err.Error(), "present but empty") {
		t.Fatalf("Generate = %v, want a refusal on the empty operator line", err)
	}
}

// Where the deploy unit runs generate, a catalog link is dangling: the
// container mounts the data dir, not the checkout. A linked manifest must then
// be read from the readable catalog, or every linked package loses its routes
// and env needs with no error.
func TestLinkedManifestIsReadFromTheCatalog(t *testing.T) {
	host := t.TempDir()
	catalog := filepath.Join(host, "template", "services")
	if err := os.MkdirAll(catalog, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, catalog, "gw.yaml",
		"proxyd_routes:\n  - path: /gw/\n    backend: http://gw:8080\n    auth: user\n"+gatewayManifest)
	dir := seed(t, "HOST_APP_DIR="+host+"\n")
	write(t, dir, "services/gw.yml", "services:\n  gw:\n    image: x\n")
	if err := os.Symlink("/nowhere/template/services/gw.yaml", filepath.Join(dir, "services", "gw.yaml")); err != nil {
		t.Fatal(err)
	}
	routes := genRoutes(t, dir)
	if !hasRoute(routes, "/gw/") {
		t.Errorf("route behind a dangling catalog link was dropped: %+v", routes)
	}
	if !strings.Contains(read(t, dir, ".env"), "MODEL_GATEWAY_KEY=") {
		t.Error("env need behind a dangling catalog link was not provisioned")
	}
}

// routd's checkMigrationVersion reads ant/skills/self/MIGRATION_VERSION from
// APP_SRC_DIR, and runed seeds every group's skills from it. dashd's stock-
// skills picker reads the same tree (BUGS F54: it used to read the HOST path
// HOST_APP_DIR from inside the container and got nothing). Pointed at the
// wrong tree, /migrate is never enqueued and agents freeze at an old version.
//
// The default must be the copy baked into the image, EVEN WHEN HOST_APP_DIR is
// set — it is set on every real instance, and honouring it there is what put an
// uncommitted working tree in front of krons' live agents (BUGS M1).
func TestAppSrcDefaultsToTheBakedCopy(t *testing.T) {
	out := gen(t, seed(t, "HOST_APP_DIR=/home/op/app/arizuko\n"))
	for _, name := range []string{"routd", "runed", "dashd"} {
		s := serviceBlock(out, name)
		if !strings.Contains(s, `APP_SRC_DIR: "/opt/arizuko"`) {
			t.Errorf("%s must read ant/ from the image, got:\n%s", name, s)
		}
		if strings.Contains(s, "/srv/app/arizuko:ro") {
			t.Errorf("%s bind-mounted the host checkout without APP_SRC_DEV, got:\n%s", name, s)
		}
	}
}

// The dev loop stays reachable, but only when the operator asks for it by name.
func TestAppSrcDevMountsTheHostCheckout(t *testing.T) {
	out := gen(t, seed(t, "HOST_APP_DIR=/home/op/app/arizuko\nAPP_SRC_DEV=1\n"))
	for _, name := range []string{"routd", "runed", "dashd"} {
		s := serviceBlock(out, name)
		if !strings.Contains(s, "- /home/op/app/arizuko:/srv/app/arizuko:ro\n") {
			t.Errorf("%s missing read-only source mount, got:\n%s", name, s)
		}
		if !strings.Contains(s, `APP_SRC_DIR: "/srv/app/arizuko"`) {
			t.Errorf("%s must point APP_SRC_DIR at the mount, got:\n%s", name, s)
		}
	}
	// APP_SRC_DEV alone cannot mount anything — there is no path to mount.
	out = gen(t, seed(t, "APP_SRC_DEV=1\n"))
	for _, name := range []string{"routd", "runed", "dashd"} {
		s := serviceBlock(out, name)
		if strings.Contains(s, "/srv/app/arizuko:ro") {
			t.Errorf("%s mounted a source with no HOST_APP_DIR, got:\n%s", name, s)
		}
		if !strings.Contains(s, `APP_SRC_DIR: "/opt/arizuko"`) {
			t.Errorf("%s must fall back to the baked copy, got:\n%s", name, s)
		}
	}
}

// The per-daemon data mount table. A daemon gets the subpaths it actually
// opens and nothing else — mount topology, not convention, is what keeps one
// daemon out of another's DB and out of the live agent sockets under ipc/
// (spec 5/16, BUGS A1). Each entry is justified at its emitter in compose.go;
// widening one here means the daemon really did grow a new path, so state why
// in the emitter comment too.
//
// routd/runed take the whole tree on purpose, and only they: routd reads
// store/ + groups/ + ipc/ + web/ + tts/ + surrogate/ + connectors.toml AND
// creates audit-*.jl at the data-dir root, and runed drives the spawn path
// (groups/ + ipc/ + docker.sock). dashd was a third until it was scoped
// (BUGS F51) — the operator console needs three owner DBs, groups/ and
// surrogate/, not .env and the live agent sockets.
//
// store/ is per-owner (spec 5/16 step 7): a daemon is bound to store/<owner>
// for each database it opens, never to store/ itself.
var wantDataMounts = map[string][]string{
	"onbod": {
		"/groups:/srv/app/home/groups",
		"/web:/srv/app/home/web",
	},
	"dashd": {
		"/groups:/srv/app/home/groups",
		"/surrogate:/srv/app/home/surrogate",
	},
	"routd": {":/srv/app/home"},
	"runed": {":/srv/app/home"},
}

// noDataMount names the daemons that reach only the store server. A daemon that
// reads no file of the instance has no reason to see the tree, and mounting one
// hands it every other owner's data and every live MCP socket (BUGS F51, A1).
var noDataMount = []string{"authd", "webd", "proxyd"}

func TestDataMountsAreScopedPerDaemon(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	out := gen(t, dir)
	for svc, want := range wantDataMounts {
		s := serviceBlock(out, svc)
		if s == "" {
			t.Errorf("%s not emitted", svc)
			continue
		}
		for _, w := range want {
			if !strings.Contains(s, "- "+dir+w+"\n") {
				t.Errorf("%s missing mount %q; got:\n%s", svc, dir+w, s)
			}
		}
		// The whole-tree mount is exactly `<dataDir>:/srv/app/home`. A scoped
		// daemon must never carry it — that is the boundary this test defends.
		if want[0] != ":/srv/app/home" && strings.Contains(s, "- "+dir+":"+containerDataMount+"\n") {
			t.Errorf("%s mounts the whole data dir; it is scoped to %v:\n%s", svc, want, s)
		}
	}
	for _, svc := range noDataMount {
		s := serviceBlock(out, svc)
		if s == "" {
			t.Errorf("%s not emitted", svc)
			continue
		}
		if strings.Contains(s, "- "+dir) {
			t.Errorf("%s mounts part of the data dir; it reads only the store server:\n%s", svc, s)
		}
	}
}

// routd reads and writes four paths at the DATA-DIR ROOT, none of which is a
// subdirectory a narrowed mount could name:
//
//	connectors.toml   routd/connectors.go:35, routd/ext.go:132
//	surrogate/*.toml  auth/surrogate/registry.go:65
//	tts/              routd/cmd/routd/main.go:231
//	audit-*.jl        audit/audit.go:302-310 (routd CREATES these)
//
// Every one of them degrades SILENTLY when absent — connectors.go:39 logs Info
// and disables the connector path, registry.go:67 returns nil on ErrNotExist —
// so a routd narrowed without them looks perfectly healthy while the broker,
// surrogate OAuth, voice and the .jl audit stream are all off. That is why
// routd keeps the whole tree, and this test is the reason written down: narrow
// routd and it fails here, pointing at the four readers first.
func TestRoutdKeepsTheWholeTreeForItsRootPathReaders(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	routd := serviceBlock(gen(t, dir), "routd")
	if !strings.Contains(routd, "- "+dir+":"+containerDataMount+"\n") {
		t.Errorf("routd lost the whole-tree mount; connectors.toml, surrogate/, tts/ "+
			"and audit-*.jl all sit at the data-dir root and all fail SILENTLY when "+
			"unmounted:\n%s", routd)
	}
}

// The half that makes the table above non-vacuous: asserting the mounts a
// daemon HAS says nothing about the ones it must NOT have. Ownership is only a
// boundary if the binds a daemon is missing are pinned too — before the
// store/<owner>/ split every one of these daemons could open every other
// owner's SQLite file through a shared store/ bind (spec 5/16 step 7).
func TestScopedDaemonsGetNoOtherOwnersDB(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	out := gen(t, dir)
	owners := []string{"authd", "onbod", "routd", "runed"}
	for svc, want := range wantDataMounts {
		if want[0] == ":/srv/app/home" {
			continue // routd/runed: whole tree by design, asserted above
		}
		s := serviceBlock(out, svc)
		// A bare store/ bind would re-hand every owner's DB in one line.
		if strings.Contains(s, "- "+dir+"/store:"+containerDataMount+"/store\n") {
			t.Errorf("%s mounts store/ wholesale; it must name owner subdirs:\n%s", svc, s)
		}
		for _, owner := range owners {
			mount := "/store/" + owner + ":"
			wanted := slices.Contains(want, "/store/"+owner+":"+containerDataMount+"/store/"+owner)
			got := strings.Contains(s, "- "+dir+mount)
			if got && !wanted {
				t.Errorf("%s reaches %s's database; it is scoped to %v:\n%s", svc, owner, want, s)
			}
			if !got && wanted {
				t.Errorf("%s lost its %s mount:\n%s", svc, owner, s)
			}
		}
	}
}

// Every mounted daemon still resolves DATA_DIR to the container mount point,
// scoped or not — the subpaths land under it, so the env stays identical.
func TestScopedDaemonsKeepDataDirEnv(t *testing.T) {
	out := gen(t, seed(t, "API_PORT=8080\n"))
	// runed renders its env through writeEnv (double quotes), the rest through
	// writeSvc's own line (single quotes) — assert the value, not the quoting.
	for svc := range wantDataMounts {
		s := serviceBlock(out, svc)
		if !strings.Contains(s, `DATA_DIR: "`+containerDataMount+`"`) &&
			!strings.Contains(s, `DATA_DIR: '`+containerDataMount+`'`) {
			t.Errorf("%s lost DATA_DIR; got:\n%s", svc, s)
		}
	}
}

// vited only serves /web and writes nothing into it.
func TestVitedWebMountIsReadOnly(t *testing.T) {
	vited := serviceBlock(gen(t, seed(t, "API_PORT=8080\n")), "vited")
	if !strings.Contains(vited, "/web:/web:ro\n") {
		t.Errorf("vited must mount /web read-only (it never writes); got:\n%s", vited)
	}
}

// serviceBlock returns the YAML text for one top-level service (from
// "  <name>:\n" up to the next "  <name>:\n" or EOF). Indented keys belong to
// the service; the next 2-space-indented "<word>:" line starts the next one.
func serviceBlock(out, name string) string {
	start := strings.Index(out, "  "+name+":\n")
	if start < 0 {
		return ""
	}
	rest := out[start+len("  "+name+":\n"):]
	for i := 0; i < len(rest); i++ {
		if i == 0 || rest[i-1] == '\n' {
			// next top-level service: exactly 2-space indent + ident + ":"
			if strings.HasPrefix(rest[i:], "  ") && !strings.HasPrefix(rest[i:], "   ") &&
				rest[i+2] != ' ' {
				if j := strings.IndexByte(rest[i+2:], ':'); j >= 0 && !strings.ContainsAny(rest[i+2:i+2+j], " \n") {
					return out[start : start+len("  "+name+":\n")+i]
				}
			}
		}
	}
	return out[start:]
}

// routd sizes its dispatch deadline from RUNED_RUN_TIMEOUT (routd/cmd/routd/
// main.go:106) so it OUT-WAITS runed's container kill. If compose hands the var
// to runed but not routd, routd keeps the 20m default while runed honours a
// longer .env value: routd's deadline fires first, turn_context stays
// `running`, and the next poll re-feeds the SAME turn into a second container.
// marinade ships RUNED_RUN_TIMEOUT=30m, so this was live, not hypothetical.
func TestRunTimeoutReachesBothRoutdAndRuned(t *testing.T) {
	for _, d := range []string{"routd", "runed"} {
		found := false
		for _, k := range daemonKeys[d] {
			if k == "RUNED_RUN_TIMEOUT" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not receive RUNED_RUN_TIMEOUT — the daemon that "+
				"misses it uses a stale default and the turn runs twice", d)
		}
	}
}

// The gateway pair splits by who uses which half (spec 6/23). routd mints each
// turn's virtual key with the master key and reads back what the gateway
// charged it, so both halves reach routd. runed gets the URL — it re-emits it
// as container env and allows the gateway host through the spawn's egress
// proxy — and never the master key, because the daemon holding docker.sock has
// no call to make that the gateway would honour.
func TestTheModelGatewayMasterKeyReachesRoutdAndNotRuned(t *testing.T) {
	dir := seed(t, "MODEL_GATEWAY_URL=http://gw:4000\nMODEL_GATEWAY_KEY=master-key-abc123\n")
	gen(t, dir)
	routd := read(t, dir, "env/routd.env")
	for _, want := range []string{"MODEL_GATEWAY_URL=http://gw:4000", "MODEL_GATEWAY_KEY=master-key-abc123"} {
		if !strings.Contains(routd, want) {
			t.Errorf("env/routd.env must carry %s: routd mints the per-turn key and books its spend", want)
		}
	}
	runed := read(t, dir, "env/runed.env")
	if !strings.Contains(runed, "MODEL_GATEWAY_URL=http://gw:4000") {
		t.Error("env/runed.env must carry MODEL_GATEWAY_URL: runed re-emits it as container env")
	}
	if strings.Contains(runed, "MODEL_GATEWAY_KEY=") {
		t.Errorf("env/runed.env carries the gateway MASTER key; runed injects only the "+
			"per-turn key routd hands it in the run request:\n%s", runed)
	}
}

// TestRunedHoldsTheDockerSocketOnlyUnderDocker and its qemu twin pin the two
// shapes of one service against each other. A substrate is chosen once, in the
// instance's configuration, and the privilege the service grants has to follow
// that choice or runed refuses to start (spec 6/41 §"The runed deploy topology").
// service cuts one service's block out of the compose document. Scoping matters
// and is not pedantry: the first cut of the test below asserted on the WHOLE
// file and failed, because every other daemon also runs as uid 1000, so an
// absence claim about runed read as an absence claim about the instance.
func service(t *testing.T, doc, name string) string {
	t.Helper()
	head := "  " + name + ":\n"
	i := strings.Index(doc, head)
	if i < 0 {
		t.Fatalf("no %q service in the generated compose document", name)
	}
	rest := doc[i+len(head):]
	// The next line at two-space indent that is not deeper is the next service.
	for off := 0; ; {
		nl := strings.Index(rest[off:], "\n  ")
		if nl < 0 {
			return rest
		}
		off += nl + 1
		if len(rest) > off+2 && rest[off+2] != ' ' && rest[off+2] != '-' {
			return rest[:off]
		}
	}
}

// runed holds the VM's three device nodes and two capabilities, and no docker
// socket. There is nothing to select: the Docker substrate and RUNED_SUBSTRATE
// went at spec 6/41 step 4, so this is what every generated instance gets.
func TestRunedHoldsTheVMDevices(t *testing.T) {
	out := service(t, gen(t, seed(t, "")), "runed")
	// Every device and capability is named, not counted: a missing /dev/net/tun
	// fails the first tap and a missing SYS_ADMIN fails the first bind, and
	// neither is visible until a turn runs.
	for _, want := range []string{
		"user: '0:0'", "/dev/kvm", "/dev/vhost-vsock", "/dev/net/tun",
		"cap_add: [NET_ADMIN, SYS_ADMIN]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the QEMU substrate must grant %q", want)
		}
	}
	// The socket is gone, and so is the gid that only existed to reach it.
	for _, unwanted := range []string{"/var/run/docker.sock", "group_add:", "user: '1000:1000'"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("runed must NOT hold %q", unwanted)
		}
	}
}

// TestRunedEnvKeysCoverEveryRunedVar derives the set from runed's own source
// instead of repeating it. A var the daemon reads and compose never emits is
// SILENTLY dead: it stops applying and the fleet looks healthy, which is the
// class that shipped three times (BUGS `ant_env_delivery`). A hand-listed guard
// stops covering what moves and stays green, so this one fails on an empty set
// too.
func TestRunedEnvKeysCoverEveryRunedVar(t *testing.T) {
	sources, err := filepath.Glob(filepath.Join("..", "runed", "*.go"))
	if err != nil || len(sources) == 0 {
		t.Fatalf("no runed sources found (%v): a guard that reads nothing cannot fail", err)
	}
	declared := map[string]bool{}
	for _, k := range daemonKeys["runed"] {
		declared[k] = true
	}
	re := regexp.MustCompile(`"(RUNED_[A-Z_]+)"`)
	found := 0
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		body, rerr := os.ReadFile(src)
		if rerr != nil {
			t.Fatalf("read %s: %v", src, rerr)
		}
		for _, m := range re.FindAllStringSubmatch(string(body), -1) {
			found++
			if !declared[m[1]] {
				t.Errorf("%s reads %s and daemonKeys[\"runed\"] does not carry it, "+
					"so compose never emits it and the value silently does not apply",
					filepath.Base(src), m[1])
			}
		}
	}
	if found == 0 {
		t.Fatal("no RUNED_ vars found in runed's sources — the pattern stopped matching")
	}
	t.Logf("checked %d RUNED_ references against %d declared keys", found, len(daemonKeys["runed"]))
}
