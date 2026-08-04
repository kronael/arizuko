package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return out
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
		if !strings.Contains(blk, "user: '1000:1000'") {
			t.Errorf("%s missing user 1000 — would create root-owned files in the data dir", svc)
		}
	}
	// runed is the only daemon wired to docker.sock (spawns agent containers).
	runed := serviceBlock(out, "runed")
	if !strings.Contains(runed, "group_add:") || !strings.Contains(runed, "/var/run/docker.sock") {
		t.Error("runed missing docker.sock wiring")
	}
	for _, svc := range []string{"routd", "authd"} {
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
		"timed": "timed", "onbod": "onbod", "davd": "davd", "crackbox": "crackbox",
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
		{"egress", "CRACKBOX_ADMIN_API=http://crackbox:3129\n", "crackbox,timed"},
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
	if _, err := Generate(dir); err == nil {
		t.Error("expected error for invalid fragment filename")
	}
}

func TestGenerateNoServicesDir(t *testing.T) {
	if _, err := Generate(t.TempDir()); err == nil {
		t.Error("expected error for missing services dir")
	}
}

// PROXYD_ROUTES_JSON is assembled in Go (one env var on proxyd): core routes
// plus each fragment's sibling routes file, gated_by filtered.
func TestProxydRoutesAssembled(t *testing.T) {
	dir := seed(t, "WEB_PORT=8095\nAPI_PORT=8080\nSLACK_BOT_TOKEN=tok\n")
	write(t, dir, "services/slakd.yml", "services:\n  slakd:\n    image: arizuko:latest\n")
	write(t, dir, "services/slakd-routes.json",
		`[{"path":"/slack/","backend":"http://slakd:8080","auth":"public","gated_by":"SLACK_BOT_TOKEN"}]`)
	out := gen(t, dir)
	proxyd := serviceBlock(out, "proxyd")
	if !strings.Contains(proxyd, "PROXYD_ROUTES_JSON") {
		t.Fatal("proxyd missing PROXYD_ROUTES_JSON")
	}
	for _, want := range []string{
		`\"path\":\"/slack/\"`, `\"backend\":\"http://slakd:8080\"`, // per-service
		`\"path\":\"/chat/\"`, `\"path\":\"/dash/\"`, // core
		`\"path\":\"/dav/\"`, // WEBDAV_ENABLED defaults on
		// Pairing (spec 5/31): auth `user` so an anonymous visitor is bounced
		// through OAuth before the confirm page renders.
		`{\"path\":\"/pair/\",\"backend\":\"http://webd:8080\",\"auth\":\"user\"}`,
	} {
		if !strings.Contains(proxyd, want) {
			t.Errorf("PROXYD_ROUTES_JSON missing %s; got:\n%s", want, proxyd)
		}
	}
	// ONBOARDING_ENABLED unset → onbod routes dropped.
	if strings.Contains(proxyd, `\"path\":\"/invite/\"`) {
		t.Error("/invite/ route must be dropped when ONBOARDING_ENABLED is unset")
	}
}

func TestProxydRoutesGating(t *testing.T) {
	routesJSON := `[{"path":"/slack/","backend":"http://slakd:8080","auth":"public","gated_by":"SLACK_BOT_TOKEN"}]`
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
			write(t, dir, "services/slakd-routes.json", routesJSON)
			out := gen(t, dir)
			if got := strings.Contains(out, `\"path\":\"/slack/\"`); got != tc.want {
				t.Errorf("route present=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestProxydRoutesOnboardingAndWebDAVGates(t *testing.T) {
	on := gen(t, seed(t, "WEB_PORT=8095\nONBOARDING_ENABLED=true\n"))
	if !strings.Contains(on, `\"path\":\"/invite/\"`) {
		t.Error("/invite/ route missing when ONBOARDING_ENABLED=true")
	}
	off := gen(t, seed(t, "WEB_PORT=8095\nWEBDAV_ENABLED=false\n"))
	if strings.Contains(off, `\"path\":\"/dav/\"`) {
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

// In the split, routd OWNS secrets and runed injects them into spawned
// containers, so both env files carry SECRETS_KEY.
func TestSplitScopesSecretsKey(t *testing.T) {
	dir := seed(t, "AUTH_SECRET=s\nSECRETS_KEY=deadbeef\n")
	gen(t, dir)
	for _, d := range []string{"routd", "runed"} {
		if !strings.Contains(read(t, dir, "env/"+d+".env"), "SECRETS_KEY=deadbeef") {
			t.Errorf("env/%s.env must carry SECRETS_KEY", d)
		}
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

// Egress isolation: crackbox is emitted (profile-gated) and gets its scoped env;
// folder networks stay runtime-managed, never declared in compose.
func TestEgressIsolation(t *testing.T) {
	dir := seed(t, "CRACKBOX_ADMIN_API=http://crackbox:3129\n")
	out := gen(t, dir)
	if !strings.Contains(serviceBlock(out, "crackbox"), "image: crackbox:latest") {
		t.Error("crackbox service missing")
	}
	if strings.Contains(out, "\nnetworks:\n") {
		t.Error("compose must not declare networks — folder networks are runtime-managed")
	}
	runedEnv := read(t, dir, "env/runed.env")
	if !strings.Contains(runedEnv, "CRACKBOX_ADMIN_API=http://crackbox:3129") {
		t.Error("runed env missing CRACKBOX_ADMIN_API")
	}
	if !strings.Contains(runedEnv, "EGRESS_CRACKBOX=") || !strings.Contains(runedEnv, "EGRESS_NETWORK_PREFIX=") {
		t.Errorf("runed env missing derived egress names; got:\n%s", runedEnv)
	}
	for _, svc := range []string{"routd", "authd"} {
		env := read(t, dir, "env/"+svc+".env")
		if strings.Contains(env, "CRACKBOX") || strings.Contains(env, "EGRESS") {
			t.Errorf("%s env must NOT carry crackbox/egress vars", svc)
		}
	}
}

// services/ttsd.yml present → TTS auto-enabled on runed; explicit .env wins.
func TestTTSAutoEnable(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	write(t, dir, "services/ttsd.yml", "services:\n  ttsd:\n    image: arizuko-ttsd:latest\n")
	gen(t, dir)
	runed := read(t, dir, "env/runed.env")
	if !strings.Contains(runed, "TTS_ENABLED=true") || !strings.Contains(runed, "TTS_BASE_URL=http://ttsd:8880") {
		t.Errorf("ttsd package must auto-enable TTS on runed; got:\n%s", runed)
	}

	off := seed(t, "API_PORT=8080\n")
	gen(t, off)
	if strings.Contains(read(t, off, "env/runed.env"), "TTS_ENABLED") {
		t.Error("TTS must stay off without the ttsd package")
	}

	override := seed(t, "TTS_BASE_URL=https://api.openai.com\n")
	write(t, override, "services/ttsd.yml", "services:\n  ttsd:\n    image: arizuko-ttsd:latest\n")
	gen(t, override)
	if !strings.Contains(read(t, override, "env/runed.env"), "TTS_BASE_URL=https://api.openai.com") {
		t.Error("explicit TTS_BASE_URL must win over the auto default")
	}
}

// routd's checkMigrationVersion reads ant/skills/self/MIGRATION_VERSION from
// APP_SRC_DIR — without the source mount /migrate is never enqueued.
func TestAppSrcMountRoutdAndRuned(t *testing.T) {
	out := gen(t, seed(t, "HOST_APP_DIR=/home/op/app/arizuko\n"))
	for _, name := range []string{"routd", "runed"} {
		s := serviceBlock(out, name)
		if !strings.Contains(s, "- /home/op/app/arizuko:/srv/app/arizuko:ro\n") {
			t.Errorf("%s missing read-only source mount, got:\n%s", name, s)
		}
		if !strings.Contains(s, `APP_SRC_DIR: "/srv/app/arizuko"`) {
			t.Errorf("%s missing APP_SRC_DIR, got:\n%s", name, s)
		}
	}
	out = gen(t, seed(t, "API_PORT=8080\n"))
	for _, name := range []string{"routd", "runed"} {
		s := serviceBlock(out, name)
		if strings.Contains(s, "/srv/app/arizuko:ro") || strings.Contains(s, "APP_SRC_DIR") {
			t.Errorf("%s must have no source mount without HOST_APP_DIR, got:\n%s", name, s)
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
// routd/runed/dashd take the whole tree on purpose: routd reads store/ +
// groups/ + ipc/ + web/ + tts/ + surrogate/ + connectors.toml, runed drives
// the spawn path, and dashd is the operator console over four DBs plus the
// groups tree.
var wantDataMounts = map[string][]string{
	"authd":  {"/store:/srv/app/home/store"},
	"webd":   {"/store:/srv/app/home/store"},
	"proxyd": {"/store:/srv/app/home/store"},
	"onbod": {
		"/store:/srv/app/home/store",
		"/groups:/srv/app/home/groups",
		"/web:/srv/app/home/web",
	},
	"routd": {":/srv/app/home"},
	"runed": {":/srv/app/home"},
	"dashd": {":/srv/app/home"},
}

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
}

// A scoped daemon must not reach the instance secrets or another agent's live
// MCP socket. Both are children of the data dir, so a whole-tree mount hands
// them over; these are the two that cost the most if it regresses.
func TestScopedDaemonsCannotReachIpcOrDotEnv(t *testing.T) {
	dir := seed(t, "API_PORT=8080\n")
	out := gen(t, dir)
	for _, svc := range []string{"authd", "webd", "proxyd", "onbod"} {
		s := serviceBlock(out, svc)
		if strings.Contains(s, "/ipc:") {
			t.Errorf("%s must not mount ipc/ (live agent MCP sockets); got:\n%s", svc, s)
		}
		if strings.Contains(s, "- "+dir+":") {
			t.Errorf("%s must not mount the data-dir root (.env lives there); got:\n%s", svc, s)
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
