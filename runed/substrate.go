package runed

import (
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/substrate"
)

// NewRuntime builds the turn substrate, checks ITS preconditions and reaps what a
// previous runed left behind, returning what the configuration delivers against
// what the substrate declares.
//
// There is ONE substrate: a KVM virtual machine per turn (spec 6/41). The
// selector and the Docker runtime went at step 4 of that spec's order, because a
// setting with one legal value is a setting that stops applying without saying
// so. An error here is a refusal to start — an instance whose guests would write
// host-root-owned files is worse than one that does not come up.
func NewRuntime(cfg *core.Config, folders *groupfolder.Resolver) (Runtime, substrate.Effective, error) {
	return NewQemuRuntime(cfg, folders)
}
