package runed

import (
	"fmt"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/substrate"
)

// SubstrateName is RUNED_SUBSTRATE: the instance-wide choice of turn substrate,
// read once at runed start.
//
// It is never per folder and never per turn — an instance runs exactly one
// substrate — and it is an env var rather than a compose fragment because two
// runed services would both serve POST /v1/runs and write the same spawn rows,
// which is one wire name with two owners (spec 6/41 §"The seam"). The last step
// of the transition deletes both the Docker runtime and this variable: a setting
// with one legal value is a setting that stops applying without saying so.
func SubstrateName() string { return core.EnvOr(envSubstrate, SubstrateDocker) }

// NewRuntime builds the substrate SubstrateName selects, checks ITS
// preconditions and reaps what a previous runed left behind, returning what the
// configuration delivers against what the substrate declares.
//
// Both substrates answer the same three questions here, each in its own terms:
// what is declared (the profile), is the machinery live (docker info; /dev/kvm
// and the clamp), and what did the last runed leave running (proxy containers;
// the VM records). An error is a refusal to start — an instance whose spawns
// would run unfiltered, or whose guests would write host-root-owned files, is
// worse than one that does not come up.
func NewRuntime(cfg *core.Config, folders *groupfolder.Resolver) (Runtime, substrate.Effective, error) {
	switch name := SubstrateName(); name {
	case SubstrateDocker:
		sub, err := Substrate(cfg)
		if err != nil {
			return nil, sub, err
		}
		if err := container.EnsureRunning(); err != nil {
			return nil, sub, fmt.Errorf("container runtime check failed: %w", err)
		}
		container.CleanupOrphans(cfg.Name, cfg.Image)
		return NewDockerRuntime(cfg, folders), sub, nil
	case SubstrateQEMU:
		return NewQemuRuntime(cfg, folders)
	default:
		return nil, substrate.Effective{}, fmt.Errorf(
			"%s=%q is not a substrate: %s or %s", envSubstrate, name, SubstrateDocker, SubstrateQEMU)
	}
}
