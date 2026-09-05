// Package substrate declares what an execution substrate can do. One value
// per substrate, in the substrate's own package: container.Profile for
// Docker, sandbox.Profile for KVM. The substrate reads it to decide whether
// a feature runs; runed logs it at start and shows it on its dashboard;
// tests pin it. The declaration is a ceiling. Effective is what a
// configuration delivers against it, and the function that computes an
// Effective is the one place a capability may be found missing — a feature
// that quietly does not happen is the failure this package ends (spec 6/25
// §3).
//
// Every Profile field is a bool or an enum declared here, and the package
// imports nothing. A field that could hold a folder, a grant, a principal or
// an allowlist could carry policy, and policy belongs to arizuko, never to
// the substrate (spec 6/16, the component contract). TestProfileCarriesNoPolicy
// pins the fields; tests/components pins the imports.
package substrate

// Backend names the mechanism that runs a workload.
type Backend string

const (
	Docker Backend = "docker"
	KVM    Backend = "kvm"
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
