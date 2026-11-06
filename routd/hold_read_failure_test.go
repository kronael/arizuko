package routd

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// BUGS L92: a hold check that cannot read the policy must block the call. A
// read error used to read as "no hold rule", so the call ran unapproved.
func TestHoldGateBlocksWhenThePolicyReadFails(t *testing.T) {
	for _, entity := range []string{store.ACL, store.ACLMembership} {
		t.Run(entity, func(t *testing.T) {
			d := memDB(t)
			dl := &recDeliverer{}
			srv := &Server{db: d, deliver: dl}
			if err := addACLRow(d, core.ACLRow{
				Principal: "folder:atlas", Action: auth.HoldPrefix + "like",
				Scope: "atlas", Effect: "allow",
			}); err != nil {
				t.Fatal(err)
			}
			gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t1"})
			dropEntity(t, d, entity)

			refusal, held := gate("like", map[string]any{"reaction": "x"})
			if !held || refusal != "" {
				t.Fatalf("gate = (%q, %v), want the empty blocked refusal (\"\", true)", refusal, held)
			}
			dl.waitSends(t, 1)
			rows, err := d.ListPendingActions("atlas", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("a blocked call recorded %d pending rows, want none", len(rows))
			}
		})
	}
}
