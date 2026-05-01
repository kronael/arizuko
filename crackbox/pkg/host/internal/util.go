package internal

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateID generates a random hex ID of the given byte size.
func GenerateID(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// GenerateVMID generates a VM ID (16 hex characters).
func GenerateVMID() string {
	return GenerateID(8)
}

// GenerateToken generates a provisioning token (64 hex characters).
func GenerateToken() string {
	return GenerateID(32)
}

// VMIDToMAC generates a deterministic MAC address from a VM ID.
// Format: 52:54:XX:XX:XX:XX (QEMU's OUI prefix).
func VMIDToMAC(vmID string) string {
	b := make([]byte, 4)
	hex.Decode(b, []byte(vmID[:8])) //nolint:errcheck
	return fmt.Sprintf("52:54:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3])
}
