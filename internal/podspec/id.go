// Package podspec builds Pod specs and derives Docker IDs for them.
package podspec

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContainerID = sha256(namespace/name) in 64 hex chars.
func ContainerID(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return hex.EncodeToString(sum[:])
}

// ShortID returns the first 12 chars (Docker CLI display format).
func ShortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
