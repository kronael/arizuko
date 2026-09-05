package runed

import (
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/substrate"
)

// Substrate reads the same mapping the per-spawn proxy reads, so a full
// config delivers the declared profile — which also proves the mapping
// supplies the allowlist resolver runed owns — and a half config is the
// error main refuses to start on.
func TestSubstrate(t *testing.T) {
	sub, err := Substrate(&core.Config{})
	if err != nil {
		t.Fatalf("isolation off by choice must not error: %v", err)
	}
	if sub.Delivered.Egress != substrate.EgressNone || sub.Reason == "" {
		t.Errorf("no image must deliver egress none with a reason, got %+v", sub)
	}

	if _, err := Substrate(&core.Config{EgressImage: "arizuko-egress:latest"}); err == nil {
		t.Fatal("an image with no network prefix or uplink must be a misconfiguration error")
	}

	sub, err = Substrate(&core.Config{
		EgressImage:         "arizuko-egress:latest",
		EgressNetworkPrefix: "arizuko_t",
		EgressUplinkNetwork: "arizuko_t_default",
	})
	if err != nil {
		t.Fatalf("full config: %v", err)
	}
	if sub.Delivered != sub.Declared || sub.Delivered.Egress != substrate.EgressDomain {
		t.Errorf("full config must deliver the declaration, got %+v", sub)
	}
}
