package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateEnvCarriesNoEgressSwitch: the `.env` `arizuko create` generates must
// name NO egress switch, because there is nothing to switch.
//
// It used to name `EGRESS_IMAGE` and warn that an empty value ran the agent
// unfiltered — the discoverability half of BUGS `L24`, which existed because
// `create` writes its own key set and never reads `template/env.example`. Spec
// `6/41` step 4 closed the other half by construction: the turn runs in a KVM
// guest whose tap accepts the proxy and drops everything else, and runed refuses
// to start without the proxy binary. So the assertion inverts — a re-introduced
// switch would be a way back to an unfiltered turn, and it would look like
// configuration.
func TestCreateEnvCarriesNoEgressSwitch(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")
	seedCoreSource(t, filepath.Join(base, "release"))
	unsealPackageFixtures(t, filepath.Join(base, "arizuko_egressinst"))

	cmdCreate([]string{"egressinst"})

	b, err := os.ReadFile(filepath.Join(base, "arizuko_egressinst", ".env"))
	if err != nil {
		t.Fatalf("create wrote no .env: %v", err)
	}
	env := string(b)
	if env == "" {
		t.Fatal("create wrote an empty .env: a guard that reads nothing cannot fail")
	}

	for _, gone := range []string{"EGRESS_IMAGE", "EGRESS_NETWORK_PREFIX", "EGRESS_UPLINK_NETWORK", "EGRESS_SUBNET"} {
		if strings.Contains(env, gone) {
			t.Errorf("the generated .env names %s; the docker substrate's egress settings "+
				"went with it (spec 6/41 step 4), and one back in this file is a turn that "+
				"can run unfiltered again:\n%s", gone, env)
		}
	}
	// The keys create DOES owe, so this test fails if the file stops being the
	// instance's identity rather than merely losing a line.
	for _, want := range []string{"ASSISTANT_NAME=", "AUTH_SECRET=", "SECRETS_KEY=", "API_PORT="} {
		if !strings.Contains(env, want) {
			t.Errorf("the generated .env is missing %s:\n%s", want, env)
		}
	}
}

// TestCreateEnvNamesNoAgentImage pins BUGS `L384`. `CONTAINER_IMAGE` had no reader
// left: `core.Config.Image` was assigned from it and read by nothing, because the
// guest boots the exported root filesystem tree `RUNED_ROOTFS_DIR` names and
// `make rootfs` exports that from the hardcoded `arizuko-ant:latest`.
//
// A var that nothing reads is worse than an absent one: the operator who retags an
// image and sets it here gets a silent no-op, and the next reader believes the
// platform resolves an image name per instance. `create` is where the file is
// written, so this is where the absence is pinned.
func TestCreateEnvNamesNoAgentImage(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")
	seedCoreSource(t, filepath.Join(base, "release"))
	unsealPackageFixtures(t, filepath.Join(base, "arizuko_imginst"))

	cmdCreate([]string{"imginst"})

	b, err := os.ReadFile(filepath.Join(base, "arizuko_imginst", ".env"))
	if err != nil {
		t.Fatalf("create wrote no .env: %v", err)
	}
	if strings.Contains(string(b), "CONTAINER_IMAGE") {
		t.Errorf("the generated .env names CONTAINER_IMAGE; nothing reads it, so an "+
			"operator who sets it gets a silent no-op — the tree is named by "+
			"RUNED_ROOTFS_DIR (spec 6/41):\n%s", b)
	}
}
