package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const variantStub = "services:\n  %s:\n    image: arizuko:latest\n"

func installAdapters(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		write(t, dir, "services/"+name+".yml", strings.Replace(variantStub, "%s", name, 1))
	}
}

func envLines(body string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(body, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			out[k] = v
		}
	}
	return out
}

func TestVariantResolvesToItsOwnEnvFile(t *testing.T) {
	for name, want := range map[string]string{
		"teled":       "env/teled.env",
		"teled-jynx":  "env/teled-jynx.env",
		"discd-work":  "env/discd-work.env",
		"discd-a-b":   "env/discd-a-b.env",
		"routd":       "env/routd.env",
		"unknown-one": "",
	} {
		if got := envFileName(name); got != want {
			t.Errorf("envFileName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestVariantEnvFileCarriesItsOwnCredential(t *testing.T) {
	dir := seed(t, "TELEGRAM_BOT_TOKEN=base-token\nTELEGRAM_BOT_TOKEN_JYNX=jynx-token\nTZ=Europe/Prague\n")
	installAdapters(t, dir, "teled", "teled-jynx")
	gen(t, dir)

	variant := envLines(read(t, dir, "env/teled-jynx.env"))
	base := envLines(read(t, dir, "env/teled.env"))
	if variant["TELEGRAM_BOT_TOKEN"] != "jynx-token" {
		t.Errorf("variant token = %q, want jynx-token", variant["TELEGRAM_BOT_TOKEN"])
	}
	if base["TELEGRAM_BOT_TOKEN"] != "base-token" {
		t.Errorf("base token = %q, want base-token", base["TELEGRAM_BOT_TOKEN"])
	}
	for file, body := range map[string]string{
		"env/teled.env":      read(t, dir, "env/teled.env"),
		"env/teled-jynx.env": read(t, dir, "env/teled-jynx.env"),
	} {
		if strings.Contains(body, "_JYNX") {
			t.Errorf("%s carries the override variable itself", file)
		}
	}
	if strings.Contains(read(t, dir, "env/teled.env"), "jynx-token") {
		t.Error("the base file must not carry the variant's credential")
	}
	for _, k := range []string{"AUTHD_SERVICE_KEY", "AUTHD_SERVICE_NAME", "TZ"} {
		if variant[k] == "" || variant[k] != base[k] {
			t.Errorf("%s: variant %q, base %q, want the same non-empty value", k, variant[k], base[k])
		}
	}
	if variant["AUTHD_SERVICE_NAME"] != "teled" {
		t.Errorf("variant exchanges as %q, want the daemon name teled", variant["AUTHD_SERVICE_NAME"])
	}
}

func TestVariantWithoutOverrideKeepsBaseValue(t *testing.T) {
	dir := seed(t, "TELEGRAM_BOT_TOKEN=base-token\n")
	installAdapters(t, dir, "teled", "teled-jynx")
	gen(t, dir)
	if got := envLines(read(t, dir, "env/teled-jynx.env"))["TELEGRAM_BOT_TOKEN"]; got != "base-token" {
		t.Errorf("variant token = %q, want the base value base-token", got)
	}
}

func TestVariantOverrideIsGenericAcrossAdapters(t *testing.T) {
	dir := seed(t, "DISCORD_BOT_TOKEN=base\nDISCORD_BOT_TOKEN_MY_TEAM=team\n")
	installAdapters(t, dir, "discd", "discd-my.team")
	gen(t, dir)
	if got := envLines(read(t, dir, "env/discd-my.team.env"))["DISCORD_BOT_TOKEN"]; got != "team" {
		t.Errorf("variant token = %q, want team (label my.team -> _MY_TEAM)", got)
	}
}

func TestVariantOverrideReachesOnlyDaemonKeys(t *testing.T) {
	dir := seed(t, "SECRETS_KEY_JYNX=stolen\nAUTHD_SERVICE_KEY_JYNX=forged\nTELEGRAM_BOT_TOKEN=b\n")
	installAdapters(t, dir, "teled", "teled-jynx")
	gen(t, dir)
	body := read(t, dir, "env/teled-jynx.env")
	for _, bad := range []string{"stolen", "forged", "SECRETS_KEY"} {
		if strings.Contains(body, bad) {
			t.Errorf("variant env carries %q", bad)
		}
	}
}

func TestSingleAccountEnvUnchanged(t *testing.T) {
	dir := seed(t, "TELEGRAM_BOT_TOKEN=base-token\n")
	installAdapters(t, dir, "teled")
	gen(t, dir)
	if got := envLines(read(t, dir, "env/teled.env"))["TELEGRAM_BOT_TOKEN"]; got != "base-token" {
		t.Errorf("token = %q, want base-token", got)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "env", "teled-*.env"))
	if err != nil || len(matches) != 0 {
		t.Errorf("no variant installed, yet generate wrote %v", matches)
	}
}

// The bundled variant fragment names an env_file; generate must write exactly
// that file, or `docker compose up` fails on it.
func TestBundledVariantFragmentEnvFileIsWritten(t *testing.T) {
	dir := seed(t, "TELEGRAM_BOT_TOKEN=base-token\nTELEGRAM_BOT_TOKEN_JYNX=jynx-token\n")
	installAdapters(t, dir, "teled")
	body, err := os.ReadFile(filepath.Join(templateServices, "teled-jynx.yml"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "services/teled-jynx.yml", string(body))
	gen(t, dir)

	svc := readFragment(t, "teled-jynx").Services["teled-jynx"]
	if len(svc.EnvFile) != 1 {
		t.Fatalf("env_file = %v, want one", svc.EnvFile)
	}
	file := filepath.Join(dir, "services", svc.EnvFile[0])
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the fragment's env_file was not generated: %v", err)
	}
	if got := envLines(read(t, dir, "env/teled-jynx.env"))["TELEGRAM_BOT_TOKEN"]; got != "jynx-token" {
		t.Errorf("token = %q, want jynx-token", got)
	}
}
