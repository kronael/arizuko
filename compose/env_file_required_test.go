package compose

import "testing"

// A service with no daemonKeys entry has no env file. Both consumers of
// envFileName used to take "" and render it: the stanza got an empty env_file
// path and the writer targeted the data directory itself. A comment claimed a
// `checkEveryServiceHasKeys` guard refused that, and no such function existed
// (BUGS `L446`).
func TestEnvFileFor_PanicsForAServiceWithNoKeys(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("envFileFor rendered a stanza for a service with no key list")
		}
	}()
	envFileFor("nosuchdaemon")
}

func TestWriteDaemonEnv_RefusesAServiceWithNoKeys(t *testing.T) {
	if got := envFileName("nosuchdaemon"); got != "" {
		t.Fatalf("envFileName(nosuchdaemon) = %q, want empty", got)
	}
	if got := envFileName("teled"); got == "" {
		t.Fatal("envFileName(teled) is empty, so the guard would refuse a real daemon")
	}
}
