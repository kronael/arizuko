// Package substrate declares what an execution substrate can do — one value per
// substrate, in the substrate's own package. The declaration is a CEILING;
// Effective is what a configuration delivers against it, and computing an
// Effective is the one place a capability may be found missing. A feature that
// quietly does not happen is the failure this package ends (spec 6/25 §3).
//
// Every Profile field is a bool or an enum and the package imports nothing: a
// field holding a folder, a grant or an allowlist could carry policy, and
// policy belongs to arizuko, never to the substrate (6/16).
package substrate

// Backend names the mechanism that runs a workload.
type Backend string

const (
	// KVM is the one backend arizuko runs a turn on (spec 6/41). `Docker` stood
	// beside it until that spec's step 4 deleted the container substrate, and it
	// went with its last reader: an enum value nothing produces is read by the
	// next author as a backend this package still supports.
	KVM Backend = "kvm"
)

// Persistence says where a workload's writes survive it.
type Persistence string

const (
	// MountedHome: the home directory is a host path bound into the
	// workload. Writes land on the host as they happen; the workload's own
	// filesystem is discarded at exit.
	MountedHome Persistence = "mounted_home"
	// ResidentDisk: the workload owns a disk image that lives until it is
	// destroyed. Mounts are additional.
	ResidentDisk Persistence = "resident_disk"
)

// Enforcement is the granularity at which outbound traffic is filtered.
type Enforcement string

const (
	EgressNone Enforcement = "none"
	// EgressAddress: a rule names an IP address or a CIDR. A host name in
	// the allowlist is not a rule.
	EgressAddress Enforcement = "address"
	// EgressDomain: host names are matched at CONNECT and the resolved
	// address is checked before the dial.
	EgressDomain Enforcement = "domain"
)

// Profile is one substrate's declaration.
type Profile struct {
	Backend     Backend     `json:"backend"`
	Persistence Persistence `json:"persistence"`
	// Sessions is true when a caller can run more than one command in a
	// live workload; false when a workload is one process from start to
	// exit.
	Sessions bool        `json:"sessions"`
	Egress   Enforcement `json:"egress"`
}

// Effective is what a substrate delivers at runtime against what it
// declares.
type Effective struct {
	Declared  Profile `json:"declared"`
	Delivered Profile `json:"delivered"`
	// Reason names what is absent when Delivered is below Declared. Empty
	// means the declaration holds.
	Reason string `json:"reason,omitempty"`
}
