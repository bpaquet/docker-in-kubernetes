package podspec_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

func TestContainerIDIsDeterministicAnd64Hex(t *testing.T) {
	id1 := podspec.ContainerID("ns", "redis-abc")
	id2 := podspec.ContainerID("ns", "redis-abc")
	assert.Equal(t, id1, id2)
	assert.Len(t, id1, 64)
	for _, c := range id1 {
		assert.True(t, (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'), "non-hex char %q", c)
	}
}

func TestContainerIDDiffersByNamespaceAndName(t *testing.T) {
	assert.NotEqual(t,
		podspec.ContainerID("ns-a", "redis"),
		podspec.ContainerID("ns-b", "redis"),
	)
	assert.NotEqual(t,
		podspec.ContainerID("ns", "redis-a"),
		podspec.ContainerID("ns", "redis-b"),
	)
}

func TestShortID(t *testing.T) {
	full := podspec.ContainerID("ns", "name")
	assert.Equal(t, full[:12], podspec.ShortID(full))
	assert.Equal(t, "abc", podspec.ShortID("abc"))
	assert.Equal(t, "", podspec.ShortID(""))
}
