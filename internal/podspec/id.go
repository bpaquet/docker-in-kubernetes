// Package podspec builds Kubernetes Pod specs from Docker container requests
// and derives identifiers Docker clients expect.
package podspec

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContainerID derives the 64-hex Docker container ID for a Pod located at
// namespace/name. Stable across the pod's lifetime; no daemon state.
func ContainerID(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return hex.EncodeToString(sum[:])
}

// ShortID returns the first 12 chars of a container ID, matching how the
// docker CLI displays them.
func ShortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
