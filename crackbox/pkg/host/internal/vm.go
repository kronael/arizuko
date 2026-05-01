package internal

import "fmt"

// VMState represents the current state of a VM in its lifecycle.
type VMState int

const (
	VMStateCreating VMState = iota
	VMStateStarting
	VMStateRunning
	VMStateStopped
	VMStateError
	VMStateDeleted
)

func (s VMState) String() string {
	switch s {
	case VMStateCreating:
		return "creating"
	case VMStateStarting:
		return "starting"
	case VMStateRunning:
		return "running"
	case VMStateStopped:
		return "stopped"
	case VMStateError:
		return "error"
	case VMStateDeleted:
		return "deleted"
	default:
		return "unknown"
	}
}

// VM holds runtime state for a live VM instance. Derived from persisted Meta.
type VM struct {
	ID       string
	Name     string
	State    VMState
	IP       string
	NetIndex int
	SSHPort  int
	PID      int
	SSHKeys  string
}

func (v *VM) BackendURL() string {
	if v.IP == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:11435", v.IP)
}

// Constants for VM configuration.
const (
	PIDDiscoveryTimeout = 60 // seconds
	ShutdownTimeout     = 10 // seconds
	MaxNetIndex         = 255
	VMCPUs              = "2"
	VMMemory            = "2G"
	VMSubnetPrefix      = "10.1"
	VMSubnetMask        = "255.255.255.252"
	VMSubnetHostOffset  = 1
	VMSubnetGuestIP     = 2
	DefaultRetention    = 7 * 24 // hours
)

// Alpine Linux base image configuration.
const (
	AlpineImageURL = "https://dl-cdn.alpinelinux.org/alpine/" +
		"v3.21/releases/cloud/nocloud_alpine-3.21.0-x86_64-bios-cloudinit-r0.qcow2"
	AlpineImageName = "alpine-3.21-x86_64.qcow2"
)
